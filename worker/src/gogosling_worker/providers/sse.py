"""Server-Sent Events 的增量解析器。

按 WHATWG 规范实现最小子集：``data:`` 行以 ``\\n`` 拼接、空行派发、``:`` 开头的行是注释、
``event:`` 记录事件名、其余字段忽略；兼容 ``\\r\\n``。

两点刻意的健壮性设计：

- 只在**完整的一行**形成后才解码 UTF-8，网络分块切在多字节字符中间不会出错；
- 完整行、未完成行和单个事件均有字节上限，错误后清空缓存并拒绝继续解析。

数据本身是否是 JSON、是否是 ``[DONE]``，由各供应商适配器判断。
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from dataclasses import dataclass

DEFAULT_MAX_LINE_BYTES = 16 * 1024 * 1024
DEFAULT_MAX_EVENT_BYTES = 16 * 1024 * 1024


class SSEProtocolError(ValueError):
    """流违反了基本分帧约束（例如一行超过上限）。"""


@dataclass(frozen=True, slots=True)
class ServerSentEvent:
    """一条已派发的事件。``data`` 是多行 ``data:`` 以 ``\\n`` 拼接的结果。"""

    data: str
    event: str | None = None


class SSEParser:
    """喂字节、吐事件的增量解析器。"""

    def __init__(
        self,
        *,
        max_line_bytes: int = DEFAULT_MAX_LINE_BYTES,
        max_event_bytes: int = DEFAULT_MAX_EVENT_BYTES,
    ) -> None:
        if max_line_bytes <= 0 or max_event_bytes <= 0:
            raise ValueError("SSE byte limits must be positive")
        self._max_line = max_line_bytes
        self._max_event = max_event_bytes
        self._buf = bytearray()
        # 每条 data 行在缓冲中带一个换行，空 data 行也占预算；避免大量空字符串的列表开销。
        self._data = bytearray()
        self._event: bytes | None = None
        self._failed = False

    def feed(self, chunk: bytes) -> list[ServerSentEvent]:
        """喂入一块数据，返回其中完整派发的事件。"""
        self._require_open()
        out: list[ServerSentEvent] = []
        start = 0
        while start < len(chunk):
            newline = chunk.find(b"\n", start)
            end = newline if newline >= 0 else len(chunk)
            # 先检查长度再复制，整行随换行一起到达也不能绕过限制。
            size = len(self._buf) + end - start
            trailing_cr = chunk[end - 1 : end] == b"\r" if end > start else self._buf[-1:] == b"\r"
            if size - int(trailing_cr) > self._max_line:
                self._fail(f"SSE line exceeds {self._max_line} bytes")
            self._buf.extend(memoryview(chunk)[start:end])
            if newline < 0:
                break
            line = bytes(self._buf).removesuffix(b"\r")
            self._buf.clear()
            event = self._process_line(line)
            if event is not None:
                out.append(event)
            start = newline + 1
        return out

    def flush(self) -> list[ServerSentEvent]:
        """流结束：处理没有换行结尾的最后一行，并派发尚未以空行结束的事件。"""
        self._require_open()
        out: list[ServerSentEvent] = []
        if self._buf:
            line = bytes(self._buf).removesuffix(b"\r")
            self._buf.clear()
            event = self._process_line(line)
            if event is not None:
                out.append(event)
        event = self._dispatch()
        if event is not None:
            out.append(event)
        return out

    def _process_line(self, line: bytes) -> ServerSentEvent | None:
        if not line:
            return self._dispatch()
        if line.startswith(b":"):
            return None
        name, sep, value = line.partition(b":")
        if sep and value.startswith(b" "):
            value = value[1:]
        if name == b"data":
            if len(self._data) + len(value) + 1 + len(self._event or b"") > self._max_event:
                self._fail(f"SSE event exceeds {self._max_event} bytes")
            self._data.extend(value)
            self._data.append(10)
        elif name == b"event":
            if len(self._data) + len(value) > self._max_event:
                self._fail(f"SSE event exceeds {self._max_event} bytes")
            self._event = value
        # id / retry 与未知字段：忽略。
        return None

    def _dispatch(self) -> ServerSentEvent | None:
        event_name = (
            self._event.decode("utf-8", errors="replace") if self._event is not None else None
        )
        self._event = None
        if not self._data:
            return None
        # 去掉最后一行的分隔符，其余换行保留；按字节限额，UTF-8 解码只发生一次。
        self._data.pop()
        data = self._data.decode("utf-8", errors="replace")
        self._data.clear()
        return ServerSentEvent(data=data, event=event_name)

    def _require_open(self) -> None:
        if self._failed:
            raise SSEProtocolError("SSE parser is closed after a framing error")

    def _fail(self, message: str) -> None:
        self._buf.clear()
        self._data.clear()
        self._event = None
        self._failed = True
        raise SSEProtocolError(message)


async def iter_sse(
    stream: AsyncIterator[bytes],
    *,
    max_line_bytes: int = DEFAULT_MAX_LINE_BYTES,
    max_event_bytes: int = DEFAULT_MAX_EVENT_BYTES,
) -> AsyncIterator[ServerSentEvent]:
    """把字节流变成事件流。"""
    parser = SSEParser(max_line_bytes=max_line_bytes, max_event_bytes=max_event_bytes)
    async for chunk in stream:
        for event in parser.feed(chunk):
            yield event
    for event in parser.flush():
        yield event
