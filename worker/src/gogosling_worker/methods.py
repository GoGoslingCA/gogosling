"""协议方法的实现：``worker.*`` 与 ``run.*``。

:class:`WorkerService` 持有运行表（``run_id → task``），每个 ``run.start`` 在独立 task 里执行引擎，
``run.start`` 本身立即返回 ``accepted``。取消通过 ``task.cancel()`` 传播：正在等待的供应商流
与反向请求都会被中断；引擎负责发 ``run.cancelled``，服务层兜底保证"恰好一个终态事件"。

``worker.shutdown`` 是通知：取消全部在途运行、等待它们收尾、关闭连接，进程随后以 0 退出。
协议 §6 要求收到 ``worker.shutdown`` 或 stdin EOF 后 5 秒内退出：运行收尾的等待上限
（:data:`DEFAULT_SHUTDOWN_GRACE`）加上连接的处理器收尾上限（``rpc.DEFAULT_DRAIN_GRACE``）小于 5 秒，
且同一次退出只等待一次。
"""

from __future__ import annotations

import asyncio
import functools
import logging
import os
import platform
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from gogosling_worker import __version__
from gogosling_worker.i18n import DEFAULT_LOCALE, normalize_locale, tr
from gogosling_worker.protocol import (
    EVENT_RUN_CANCELLED,
    EVENT_RUN_FAILED,
    EVENT_RUN_STARTED,
    PROTOCOL_VERSION,
    Clock,
    ProviderCredentials,
    RunRequest,
    now_utc,
    parse_run_request,
)
from gogosling_worker.providers.base import Provider, ProviderError
from gogosling_worker.rpc import (
    DUPLICATE_RUN,
    INCOMPATIBLE_VERSION,
    INTERNAL_ERROR,
    INVALID_PARAMS,
    Connection,
    ConnectionClosed,
    Dispatcher,
    JsonRpcError,
)
from gogosling_worker.runtime.context import RunContext
from gogosling_worker.runtime.engine import Engine, RunCancelled

# 供应商工厂：(凭据, locale) → 客户端。构造失败抛 ProviderError。
ProviderFactory = Callable[[ProviderCredentials, str], Provider]

CAPABILITIES = (
    "run",
    "cancel",
    "providers.openai_compatible",
    "providers.anthropic",
    "providers.ollama",
)

# 退出时等待在途运行收尾的上限（秒）；运行对取消的响应通常是即时的，这只是兜底。
DEFAULT_SHUTDOWN_GRACE = 3.0

_log = logging.getLogger(__name__)


@dataclass(frozen=True, slots=True)
class _RunEntry:
    """运行表里的一项：执行 task 与它的宿主句柄。"""

    task: asyncio.Task[None]
    ctx: RunContext


class WorkerService:
    """全部协议方法。实例与一条连接绑定。"""

    def __init__(
        self,
        conn: Connection,
        *,
        engine: Engine,
        provider_factory: ProviderFactory,
        locale: str = DEFAULT_LOCALE,
        logger: logging.Logger | None = None,
        clock: Clock = now_utc,
        shutdown_grace: float = DEFAULT_SHUTDOWN_GRACE,
    ) -> None:
        self._conn = conn
        self._engine = engine
        self._provider_factory = provider_factory
        self._locale = normalize_locale(locale)
        self._log = logger or _log
        self._clock = clock
        self._shutdown_grace = shutdown_grace
        self._runs: dict[str, _RunEntry] = {}
        self._shutting_down = False
        # 已经为退出等待过一次在途运行：之后的 aclose 不再重复等待，保证总退出时间有上限。
        self._drained = False

    def register(self, dispatcher: Dispatcher) -> None:
        """把全部方法注册到分发器。"""
        dispatcher.register("worker.hello", self.hello)
        dispatcher.register("worker.ping", self.ping)
        dispatcher.register("worker.info", self.info)
        dispatcher.register("worker.shutdown", self.shutdown)
        dispatcher.register("run.start", self.run_start)
        dispatcher.register("run.cancel", self.run_cancel)

    @property
    def locale(self) -> str:
        """当前文案语言。"""
        return self._locale

    @property
    def active_runs(self) -> int:
        """尚未结束的运行数。"""
        return sum(1 for entry in self._runs.values() if not entry.task.done())

    # ---- worker.* ----

    def hello(self, params: object) -> dict[str, Any]:
        """协商协议版本；主版本不同返回 -32001。"""
        data = params if isinstance(params, dict) else {}
        raw = data.get("protocol_version")
        version = str(raw).strip() if raw is not None else ""
        major = version.split(".", 1)[0]
        if major != PROTOCOL_VERSION:
            raise JsonRpcError(
                INCOMPATIBLE_VERSION,
                f"unsupported protocol version {version!r}",
                {"supported": [PROTOCOL_VERSION]},
            )
        if "locale" in data:
            self._locale = normalize_locale(data.get("locale"))
        return {
            "protocol_version": PROTOCOL_VERSION,
            "worker_version": __version__,
            "python": platform.python_version(),
            "capabilities": list(CAPABILITIES),
        }

    def ping(self, params: object) -> str:
        return "pong"

    def info(self, params: object) -> dict[str, object]:
        return {
            "version": __version__,
            "python": platform.python_version(),
            "pid": os.getpid(),
        }

    async def shutdown(self, params: object) -> None:
        """通知：取消在途运行并等待其收尾，然后关闭连接。"""
        self._shutting_down = True
        await self._cancel_all(timeout=self._shutdown_grace)
        self._drained = True
        self._conn.close()

    # ---- run.* ----

    async def run_start(self, params: object) -> dict[str, bool]:
        """校验参数、登记运行并在后台 task 里执行；重复 run_id 返回 -32002。"""
        if self._shutting_down:
            raise JsonRpcError(INTERNAL_ERROR, "worker is shutting down")
        request = parse_run_request(params)
        existing = self._runs.get(request.run_id)
        if existing is not None and not existing.task.done():
            raise JsonRpcError(DUPLICATE_RUN, f"duplicate run_id {request.run_id!r}")
        try:
            provider = self._provider_factory(request.provider, self._locale)
        except ProviderError as exc:
            raise exc.to_rpc_error() from exc
        except ValueError as exc:
            raise JsonRpcError(INVALID_PARAMS, f"Invalid params: provider: {exc}") from exc
        ctx = RunContext(
            self._conn, request, locale=self._locale, clock=self._clock, logger=self._log
        )
        task = asyncio.create_task(
            self._execute(request, provider, ctx), name=f"run-{request.run_id}"
        )
        self._runs[request.run_id] = _RunEntry(task, ctx)
        task.add_done_callback(functools.partial(self._forget, request.run_id))
        return {"accepted": True}

    async def run_cancel(self, params: object) -> dict[str, bool]:
        """取消在途运行；未知或已结束的 run_id 返回 cancelled=false。"""
        data = params if isinstance(params, dict) else {}
        run_id = data.get("run_id")
        entry = self._runs.get(run_id) if isinstance(run_id, str) else None
        if entry is None or entry.task.done():
            return {"cancelled": False}
        if not entry.task.cancelling():
            entry.task.cancel()
        return {"cancelled": True}

    async def aclose(self) -> None:
        """取消并等待全部运行结束（进程退出路径）。

        ``worker.shutdown`` 已经等待过时只取消、不再等待，保证总退出时间有上限。
        """
        self._shutting_down = True
        await self._cancel_all(timeout=0.0 if self._drained else self._shutdown_grace)
        self._drained = True

    # ---- 内部 ----

    def _forget(self, run_id: str, task: asyncio.Task[None]) -> None:
        entry = self._runs.get(run_id)
        if entry is None or entry.task is not task:
            return
        del self._runs[run_id]
        if task.cancelled() and not entry.ctx.terminal_sent:
            # 运行 task 在迈出第一步之前就被取消（例如 run.start 之后紧接着 run.cancel 或
            # worker.shutdown），引擎与 _execute 的兜底都没机会执行；这里补齐协议规定的序列。
            if not entry.ctx.started_sent:
                entry.ctx.emit(EVENT_RUN_STARTED)
            entry.ctx.emit(EVENT_RUN_CANCELLED)

    async def _cancel_all(self, *, timeout: float) -> None:
        tasks = [entry.task for entry in self._runs.values() if not entry.task.done()]
        for task in tasks:
            if not task.cancelling():
                task.cancel()
        if not tasks:
            return
        if timeout <= 0:
            self._log.warning("退出时仍有运行未结束，不再等待", extra={"pending": len(tasks)})
            return
        try:
            await asyncio.wait_for(asyncio.gather(*tasks, return_exceptions=True), timeout)
        except TimeoutError:
            self._log.warning("等待在途运行结束超时", extra={"pending": len(tasks)})

    async def _execute(self, request: RunRequest, provider: Provider, ctx: RunContext) -> None:
        try:
            await self._engine.run(request, ctx, provider)
        except asyncio.CancelledError:
            ctx.emit(EVENT_RUN_CANCELLED)
            raise
        except RunCancelled:
            ctx.emit(EVENT_RUN_CANCELLED)
        except ProviderError as exc:
            ctx.emit(EVENT_RUN_FAILED, error=exc.message)
        except ConnectionClosed:
            self._log.debug("连接已关闭，运行 %s 的结果无处投递", request.run_id)
        except Exception as exc:  # noqa: BLE001 - 引擎的任何异常都要变成终态事件
            self._log.exception("运行 %s 因未捕获的异常失败", request.run_id)
            ctx.emit(
                EVENT_RUN_FAILED,
                error=tr("run.internal_error", self._locale, error=type(exc).__name__),
            )
        else:
            if not ctx.terminal_sent:
                self._log.warning(
                    "引擎结束时没有终态事件，补发 run.failed", extra={"run_id": request.run_id}
                )
                ctx.emit(EVENT_RUN_FAILED, error=tr("run.no_terminal", self._locale))
        finally:
            try:
                await provider.aclose()
            except Exception:  # noqa: BLE001 - 关闭客户端失败不影响运行结果
                self._log.debug("关闭供应商客户端失败", exc_info=True)
