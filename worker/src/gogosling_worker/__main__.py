"""工作进程入口：在标准输入输出上提供 JSON-RPC 服务。

stdout 只用于协议消息，日志一律写 stderr，否则会破坏分帧。
对端 EOF 或 ``worker.shutdown`` 之后退出码为 0（协议 §6）。

退出路径不走解释器的正常收尾：stdio 读线程可能仍阻塞在 stdin 的读取里（守护进程发出
``worker.shutdown`` 后并不关闭管道），解释器收尾时与守护线程争用标准流的锁会以退出码 134 中止。
:func:`main` 在服务结束后先 flush stdout / stderr 与日志，再以 ``os._exit`` 结束进程。
"""

from __future__ import annotations

import asyncio
import logging
import os
import sys
from typing import BinaryIO, NoReturn

from gogosling_worker.i18n import normalize_locale
from gogosling_worker.methods import WorkerService
from gogosling_worker.protocol import ProviderCredentials
from gogosling_worker.providers import build_provider
from gogosling_worker.providers.base import Provider
from gogosling_worker.rpc import Connection, Dispatcher
from gogosling_worker.runtime import DefaultEngine
from gogosling_worker.transport import StdioTransport

LOG_LEVEL_ENV = "GOGOSLING_WORKER_LOG_LEVEL"
LOCALE_ENV = "GOGOSLING_LOCALE"


def _make_provider(credentials: ProviderCredentials, locale: str) -> Provider:
    return build_provider(credentials, locale=locale)


async def run_worker(stdin: BinaryIO, stdout: BinaryIO) -> int:
    """在给定的字节流上服务，直到对端 EOF 或收到 worker.shutdown。"""
    logger = logging.getLogger("gogosling_worker")
    transport = StdioTransport(stdin, stdout)
    dispatcher = Dispatcher()
    conn = Connection(transport, dispatcher, logger=logger)
    service = WorkerService(
        conn,
        engine=DefaultEngine(logger=logger),
        provider_factory=_make_provider,
        locale=normalize_locale(os.environ.get(LOCALE_ENV)),
        logger=logger,
    )
    service.register(dispatcher)
    try:
        await conn.serve()
    finally:
        await service.aclose()
    return 0


def main() -> NoReturn:
    """命令行入口：服务结束后以确定的退出码立即结束进程（见模块说明）。"""
    logging.basicConfig(
        stream=sys.stderr,
        level=os.environ.get(LOG_LEVEL_ENV, "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    code = 1
    try:
        code = asyncio.run(run_worker(sys.stdin.buffer, sys.stdout.buffer))
    except KeyboardInterrupt:
        code = 0
    except Exception:  # noqa: BLE001 - 入口兜底：记录后以非零码退出，而不是带着守护线程走收尾
        logging.getLogger("gogosling_worker").exception("工作进程异常退出")
        code = 1
    hard_exit(code)


def hard_exit(code: int) -> NoReturn:
    """flush 标准输出、标准错误与日志处理器后，用 ``os._exit`` 结束进程，跳过解释器收尾。

    调用前协议输出必须已经写完：``rpc.Connection.serve`` 在返回前等待写循环把队列写空，
    这里的 flush 只是兜底（例如 ``print`` 之类绕过传输的残留）。
    """
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.flush()
        except Exception:  # noqa: BLE001 - 管道已断开等情况：没有更多可做的
            pass
    logging.shutdown()
    os._exit(code)


if __name__ == "__main__":
    main()
