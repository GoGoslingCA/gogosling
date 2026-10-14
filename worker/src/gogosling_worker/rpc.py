"""行分隔 JSON-RPC 2.0 的全双工实现（asyncio）。

协议见 ``api/worker-rpc/protocol.md``：每条消息一行、UTF-8、``\\n`` 结尾，单行上限 16 MiB。
两个方向都可以发请求，所以 :class:`Connection` 同时扮演服务端与客户端：

- 读循环只负责切行、解析与分发：正向请求各自作为独立 task 执行，慢处理器
  （例如等待工具结果的 ``run.start``）不会阻塞 ``run.cancel`` 与 ``worker.ping``；
- 写入经由一个队列、由单个协程串行写出并 flush，保证一行一条消息、顺序与发出顺序一致；
- 反向请求（``host.*``）带自增整数 id 并登记 pending future，响应按 id 回填；
  连接关闭时所有 pending 以 :class:`ConnectionClosed` 失败；
- 处理器抛出的任何异常都转成 JSON-RPC 错误对象，不会让进程退出。

传输由 :class:`Transport` 抽象：生产环境是 stdio（见 ``transport.py``），测试用内存管道。
本模块不理解任何业务方法；方法由 :class:`Dispatcher` 注册。
"""

from __future__ import annotations

import asyncio
import inspect
import json
import logging
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any, Protocol

JSONRPC_VERSION = "2.0"

# JSON-RPC 标准错误码。
PARSE_ERROR = -32700
INVALID_REQUEST = -32600
METHOD_NOT_FOUND = -32601
INVALID_PARAMS = -32602
INTERNAL_ERROR = -32603

# 协议 §5 定义的扩展错误码。
INCOMPATIBLE_VERSION = -32001
DUPLICATE_RUN = -32002
RUN_CANCELLED = -32003
PERMISSION_DENIED = -32004
PROVIDER_FAILED = -32005

# 单行上限（协议规定），以及每次从传输读取的块大小。
MAX_LINE_BYTES = 16 * 1024 * 1024
READ_CHUNK_BYTES = 64 * 1024

# 输入结束后给已派发的处理器完成的时间（秒）；正常情况下处理器瞬间完成。
# 与 methods.DEFAULT_SHUTDOWN_GRACE 之和须小于协议 §6 的 5 秒退出上限。
DEFAULT_DRAIN_GRACE = 1.5


class JsonRpcError(Exception):
    """带错误码的协议错误。处理器抛出它即可让对端收到对应的错误对象。"""

    def __init__(self, code: int, message: str, data: Any = None) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.data = data

    def to_json(self) -> dict[str, Any]:
        """序列化为 JSON-RPC 的 ``error`` 对象。"""
        error: dict[str, Any] = {"code": self.code, "message": self.message}
        if self.data is not None:
            error["data"] = self.data
        return error


class ConnectionClosed(Exception):
    """连接已关闭：对端 EOF、写入失败或本端主动关闭。"""


class LineTooLong(Exception):
    """要发送的消息超过单行上限。"""

    def __init__(self, size: int, limit: int) -> None:
        super().__init__(f"message of {size} bytes exceeds the {limit} byte line limit")
        self.size = size
        self.limit = limit


class Transport(Protocol):
    """字节流传输。``read`` 在 EOF 时返回空字节串；``write`` 必须写完并 flush。"""

    async def read(self, max_bytes: int) -> bytes: ...

    async def write(self, data: bytes) -> None: ...

    async def aclose(self) -> None: ...


# 方法处理器：接收 params（任意 JSON 值），返回结果或返回结果的 awaitable。
Handler = Callable[[Any], Any]


class Dispatcher:
    """方法注册表。只保存名字到处理器的映射，不参与消息收发。"""

    def __init__(self) -> None:
        self._handlers: dict[str, Handler] = {}

    def register(self, method: str, handler: Handler) -> None:
        """注册方法；重复注册同名方法是编程错误，直接抛 ValueError。"""
        if method in self._handlers:
            raise ValueError(f"方法已注册: {method}")
        self._handlers[method] = handler

    def get(self, method: str) -> Handler | None:
        """按名字查找处理器。"""
        return self._handlers.get(method)

    def methods(self) -> list[str]:
        """已注册的方法名，按注册顺序。"""
        return list(self._handlers)


@dataclass(frozen=True, slots=True)
class OverlongLine:
    """一行超过上限时的标记；``size`` 是被丢弃的字节数。"""

    size: int


class LineSplitter:
    """把字节流切成不含换行的行。

    超过上限的行会被整体丢弃并以 :class:`OverlongLine` 标记，绝不把半行当成一整行交给上层
    （否则后半截会被当作下一条消息，破坏分帧）。未完成行的缓冲不超过上限；
    调用方传入的块和已完成行的返回列表不计入该缓冲上限。
    """

    def __init__(self, max_line: int = MAX_LINE_BYTES) -> None:
        self._max = max_line
        self._buf = bytearray()
        # 大于 0 表示正处于"丢弃一条超长行"的状态，记录已丢弃的字节数。
        self._discarding = 0

    def feed(self, data: bytes) -> list[bytes | OverlongLine]:
        """喂入一块数据，返回其中完整的行（已去掉 ``\\n`` 与可选的 ``\\r``）。"""
        out: list[bytes | OverlongLine] = []
        start = 0
        # 只搜索新到达的字节，避免长行被细碎分块时反复扫描整个已有前缀。
        while start < len(data):
            newline = data.find(b"\n", start)
            end = newline if newline >= 0 else len(data)
            size = self._discarding + len(self._buf) + end - start
            if self._discarding or size > self._max:
                # 超限前先计数，不复制将被丢弃的内容；下一条完整消息仍能正常解析。
                self._buf.clear()
                self._discarding = size
                if newline >= 0:
                    out.append(OverlongLine(size))
                    self._discarding = 0
            elif newline < 0:
                self._buf.extend(memoryview(data)[start:end])
            else:
                if self._buf:
                    self._buf.extend(memoryview(data)[start:end])
                    line = bytes(self._buf)
                    self._buf.clear()
                else:
                    line = data[start:end]
                out.append(line.removesuffix(b"\r"))
            start = end + 1
        return out

    def flush(self) -> bytes | OverlongLine | None:
        """流结束时取出最后一个没有换行结尾的行；没有则返回 None。"""
        if self._discarding:
            size = self._discarding + len(self._buf)
            self._discarding = 0
            self._buf.clear()
            return OverlongLine(size)
        if not self._buf:
            return None
        line = bytes(self._buf)
        self._buf.clear()
        if len(line) > self._max:
            return OverlongLine(len(line))
        return line.removesuffix(b"\r")


class Connection:
    """一条全双工 JSON-RPC 连接。

    用法：构造后 ``await serve()``，它在对端 EOF 或 :meth:`close` 之后返回。
    处理器通过 :meth:`notify` 发通知、通过 :meth:`request` 发反向请求。
    不是线程安全的；所有方法都必须在同一个事件循环里调用。
    """

    def __init__(
        self,
        transport: Transport,
        dispatcher: Dispatcher,
        *,
        logger: logging.Logger | None = None,
        max_line_bytes: int = MAX_LINE_BYTES,
        drain_grace: float = DEFAULT_DRAIN_GRACE,
    ) -> None:
        self._transport = transport
        self._dispatcher = dispatcher
        self._log = logger or logging.getLogger(__name__)
        self._max_line = max_line_bytes
        self._drain_grace = drain_grace
        self._outgoing: asyncio.Queue[bytes | None] = asyncio.Queue()
        self._pending: dict[int, asyncio.Future[Any]] = {}
        self._next_id = 0
        self._tasks: set[asyncio.Task[None]] = set()
        # _stop：不再有输入（EOF / close()），serve() 开始收尾；_closed：输出也已关闭。
        self._stop = asyncio.Event()
        self._closed = asyncio.Event()
        self._close_reason = ""

    @property
    def closed(self) -> bool:
        """连接是否已经关闭（关闭后 notify 被丢弃、request 立即失败）。"""
        return self._closed.is_set()

    @property
    def stopping(self) -> bool:
        """是否已经没有输入（EOF 或 close()），正在等待处理器收尾。"""
        return self._stop.is_set()

    @property
    def close_reason(self) -> str:
        """关闭原因：``eof`` / ``closed`` / ``read_error`` / ``write_error``；未关闭时为空。"""
        return self._close_reason

    async def serve(self) -> None:
        """运行读循环与写循环，直到对端 EOF 或本端 close()。

        收尾顺序：让所有 pending 的反向请求失败（不会再有响应进来）→ 给已派发的处理器
        ``drain_grace`` 秒完成（EOF 之前收到的请求仍能得到响应，例如 ``printf … | worker``），
        超时的取消 → 把已排队的输出写完 → 关闭传输。
        """
        writer = asyncio.create_task(self._write_loop(), name="rpc-writer")
        reader = asyncio.create_task(self._read_loop(), name="rpc-reader")
        try:
            await self._stop.wait()
        finally:
            self._request_stop("closed")
            reader.cancel()
            await asyncio.gather(reader, return_exceptions=True)
            self._fail_pending(ConnectionClosed(f"connection closed ({self._close_reason})"))
            await self._drain_handlers()
            self._closed.set()
            self._outgoing.put_nowait(None)
            await asyncio.gather(writer, return_exceptions=True)
            try:
                await self._transport.aclose()
            except Exception:  # noqa: BLE001 - 关闭传输失败只记录，不影响退出
                self._log.debug("关闭传输失败", exc_info=True)

    def close(self) -> None:
        """主动关闭连接；可重复调用。serve() 随后开始收尾并返回。"""
        self._request_stop("closed")

    def notify(self, method: str, params: Any = None) -> None:
        """发送通知（无 id，不等待响应）。连接已关闭时静默丢弃；超长时抛 LineTooLong。"""
        payload: dict[str, Any] = {"jsonrpc": JSONRPC_VERSION, "method": method}
        if params is not None:
            payload["params"] = params
        try:
            self._enqueue(payload)
        except ConnectionClosed:
            self._log.debug("连接已关闭，通知被丢弃", extra={"method": method})

    async def request(self, method: str, params: Any = None) -> Any:
        """发送反向请求并等待结果。

        对端返回错误对象时抛 :class:`JsonRpcError`；连接关闭抛 :class:`ConnectionClosed`；
        调用方的 task 被取消时撤销登记，迟到的响应会被忽略。没有内置超时：
        ``host.tool.call`` 可能等待用户审批很久，由调用方决定何时取消。
        """
        if self.stopping or self.closed:
            raise ConnectionClosed(f"connection closed ({self._close_reason})")
        self._next_id += 1
        request_id = self._next_id
        future: asyncio.Future[Any] = asyncio.get_running_loop().create_future()
        self._pending[request_id] = future
        payload: dict[str, Any] = {"jsonrpc": JSONRPC_VERSION, "id": request_id, "method": method}
        if params is not None:
            payload["params"] = params
        try:
            self._enqueue(payload)
            return await future
        finally:
            self._pending.pop(request_id, None)

    # ---- 内部：读 ----

    async def _read_loop(self) -> None:
        splitter = LineSplitter(self._max_line)
        reason = "eof"
        try:
            while True:
                data = await self._transport.read(READ_CHUNK_BYTES)
                if not data:
                    break
                for item in splitter.feed(data):
                    self._on_line(item)
                    # 解析后的请求交给处理器持有；空闲等待时不再保留原始完整行。
                    del item
                del data
            tail = splitter.flush()
            if tail is not None:
                self._on_line(tail)
        except asyncio.CancelledError:
            raise
        except Exception:  # noqa: BLE001 - 读失败等同于对端消失
            self._log.exception("读取输入流失败")
            reason = "read_error"
        finally:
            self._request_stop(reason)

    def _on_line(self, item: bytes | OverlongLine) -> None:
        if isinstance(item, OverlongLine):
            self._log.warning("收到超过 %d 字节的行，已丢弃（%d 字节）", self._max_line, item.size)
            self._send_error(None, PARSE_ERROR, "Parse error: line too long")
            return
        if not item.strip():
            return
        try:
            message = json.loads(item)
        except ValueError:
            self._send_error(None, PARSE_ERROR, "Parse error")
            return
        if not isinstance(message, dict):
            self._send_error(None, INVALID_REQUEST, "Invalid Request")
            return
        if "method" in message:
            method = message["method"]
            if not isinstance(method, str) or not method:
                if "id" in message:
                    self._send_error(message.get("id"), INVALID_REQUEST, "Invalid Request")
                return
            self._spawn(self._serve_request(message))
        elif "id" in message:
            self._deliver_response(message)
        else:
            self._send_error(None, INVALID_REQUEST, "Invalid Request")

    def _spawn(self, coro: Any) -> None:
        task: asyncio.Task[None] = asyncio.create_task(coro)
        self._tasks.add(task)
        task.add_done_callback(self._tasks.discard)

    async def _serve_request(self, message: dict[str, Any]) -> None:
        method: str = message["method"]
        is_notification = "id" not in message
        request_id = message.get("id")
        handler = self._dispatcher.get(method)
        if handler is None:
            if not is_notification:
                self._send_error(request_id, METHOD_NOT_FOUND, f"Method not found: {method}")
            return
        try:
            result = handler(message.get("params"))
            if inspect.isawaitable(result):
                result = await result
        except JsonRpcError as exc:
            if is_notification:
                self._log.warning("通知 %s 处理失败: %s", method, exc.message)
            else:
                self._send_error(request_id, exc.code, exc.message, exc.data)
        except asyncio.CancelledError:
            raise
        except Exception as exc:  # noqa: BLE001 - 协议边界：任何异常都不能让进程退出
            self._log.exception("处理 %s 时发生未捕获的异常", method)
            if not is_notification:
                self._send_error(
                    request_id, INTERNAL_ERROR, f"Internal error: {type(exc).__name__}"
                )
        else:
            if not is_notification:
                self._send_result(request_id, result)

    def _deliver_response(self, message: dict[str, Any]) -> None:
        raw_id = message.get("id")
        # bool 是 int 的子类，但不是 JSON-RPC 的数字 ID；True 不得命中请求 1。
        future = self._pending.pop(raw_id, None) if type(raw_id) is int else None
        if future is None or future.done():
            self._log.warning("收到未知或已撤销请求的响应，已忽略", extra={"id": raw_id})
            return
        error = message.get("error")
        if isinstance(error, dict):
            code = error.get("code")
            future.set_exception(
                JsonRpcError(
                    code if type(code) is int else INTERNAL_ERROR,
                    str(error.get("message") or "unknown error"),
                    error.get("data"),
                )
            )
        else:
            future.set_result(message.get("result"))

    # ---- 内部：写 ----

    def _send_result(self, request_id: Any, result: Any) -> None:
        payload = {"jsonrpc": JSONRPC_VERSION, "id": request_id, "result": result}
        try:
            self._enqueue(payload)
        except LineTooLong as exc:
            self._log.warning("响应超过单行上限（%d 字节），改为返回错误", exc.size)
            self._send_error(request_id, INTERNAL_ERROR, "Internal error: response too large")
        except (TypeError, ValueError):
            self._log.exception("响应无法序列化为 JSON")
            self._send_error(request_id, INTERNAL_ERROR, "Internal error: result not serializable")
        except ConnectionClosed:
            self._log.debug("连接已关闭，响应被丢弃")

    def _send_error(self, request_id: Any, code: int, message: str, data: Any = None) -> None:
        payload = {
            "jsonrpc": JSONRPC_VERSION,
            "id": request_id,
            "error": JsonRpcError(code, message, data).to_json(),
        }
        try:
            self._enqueue(payload)
        except LineTooLong:
            # data 过大：去掉 data 再发一次，尽量让对端收到错误码；仍超长只能记录。
            payload["error"] = JsonRpcError(code, message).to_json()
            try:
                self._enqueue(payload)
            except LineTooLong as exc:
                self._log.error("错误响应本身超过单行上限（%d 字节），已丢弃", exc.size)
            except ConnectionClosed:
                self._log.debug("连接已关闭，错误响应被丢弃")
        except ConnectionClosed:
            self._log.debug("连接已关闭，错误响应被丢弃")

    def _enqueue(self, payload: dict[str, Any]) -> None:
        if self.closed:
            raise ConnectionClosed(f"connection closed ({self._close_reason})")
        data = json.dumps(payload, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
        encoded = data.encode("utf-8", errors="replace")
        if len(encoded) > self._max_line:
            raise LineTooLong(len(encoded), self._max_line)
        self._outgoing.put_nowait(encoded + b"\n")

    async def _write_loop(self) -> None:
        try:
            while True:
                item = await self._outgoing.get()
                if item is None:
                    return
                await self._transport.write(item)
                # 大型终态消息已写出，不能一直留到下一次通知才释放。
                del item
        except asyncio.CancelledError:
            raise
        except Exception:  # noqa: BLE001 - 写失败等同于对端消失
            self._log.exception("写入输出流失败")
            self._mark_closed("write_error")

    # ---- 内部：关闭 ----

    def _request_stop(self, reason: str) -> None:
        if not self._stop.is_set():
            self._close_reason = reason
            self._stop.set()

    def _mark_closed(self, reason: str) -> None:
        """输出通道不可用：立即关闭，不再等待处理器。"""
        self._request_stop(reason)
        self._closed.set()

    async def _drain_handlers(self) -> None:
        """给已派发的处理器 drain_grace 秒完成，超时的取消。"""
        pending = set(self._tasks)
        if not pending:
            return
        _done, remaining = await asyncio.wait(pending, timeout=self._drain_grace)
        if remaining:
            self._log.warning("%d 个处理器在收尾宽限期内未完成，已取消", len(remaining))
            for task in remaining:
                task.cancel()
            await asyncio.gather(*remaining, return_exceptions=True)

    def _fail_pending(self, exc: Exception) -> None:
        for future in list(self._pending.values()):
            if not future.done():
                future.set_exception(exc)
        self._pending.clear()
