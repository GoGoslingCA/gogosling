"""Server-Sent Events 的增量解析器。

按 WHATWG 规范实现最小子集：``data:`` 行以 ``\\n`` 拼接、空行派发、``:`` 开头的行是注释、
``event:`` 记录事件名、其余字段忽略；兼容 ``\\r\\n``。

两点刻意的健壮性设计：

- 只在**完整的一行**形成后才解码 UTF-8，网络分块切在多字节字符中间不会出错；
- 没有换行的超长行会抛 :class:`SSEProtocolError`，而不是无限占用内存。

数据本身是否是 JSON、是否是 ``[DONE]``，由各供应商适配器判断。
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from dataclasses import dataclass

DEFAULT_MAX_LINE_BYTES = 16 * 1024 * 1024


class SSEProtocolError(ValueError):
    """流违反了基本分帧约束（例如一行超过上限）。"""


@dataclass(frozen=True, slots=True)
class ServerSentEvent:
    """一条已派发的事件。``data`` 是多行 ``data:`` 以 ``\\n`` 拼接的结果。"""

    data: str
    event: str | None = None


class SSEParser:
    """喂字节、吐事件的增量解析器。"""

    def __init__(self, *, max_line_bytes: int = DEFAULT_MAX_LINE_BYTES) -> None:
        self._max_line = max_line_bytes
        self._buf = bytearray()
        self._data: list[str] = []
        self._event: str | None = None

    def feed(self, chunk: bytes) -> list[ServerSentEvent]:
        """喂入一块数据，返回其中完整派发的事件。"""
        self._buf += chunk
        out: list[ServerSentEvent] = []
        while True:
            newline = self._buf.find(b"\n")
            if newline < 0:
                if len(self._buf) > self._max_line:
                    raise SSEProtocolError(f"SSE line exceeds {self._max_line} bytes")
                return out
            line = bytes(self._buf[:newline])
            del self._buf[: newline + 1]
            event = self._process_line(line.removesuffix(b"\r"))
            if event is not None:
                out.append(event)

    def flush(self) -> list[ServerSentEvent]:
        """流结束：处理没有换行结尾的最后一行，并派发尚未以空行结束的事件。"""
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
        field = name.decode("utf-8", errors="replace")
        text = value.decode("utf-8", errors="replace")
        if field == "data":
            self._data.append(text)
        elif field == "event":
            self._event = text
        # id / retry 与未知字段：忽略。
        return None

    def _dispatch(self) -> ServerSentEvent | None:
        event_name = self._event
        self._event = None
        if not self._data:
            return None
        data = "\n".join(self._data)
        self._data = []
        return ServerSentEvent(data=data, event=event_name)


async def iter_sse(
    stream: AsyncIterator[bytes], *, max_line_bytes: int = DEFAULT_MAX_LINE_BYTES
) -> AsyncIterator[ServerSentEvent]:
    """把字节流变成事件流。"""
    parser = SSEParser(max_line_bytes=max_line_bytes)
    async for chunk in stream:
        for event in parser.feed(chunk):
            yield event
    for event in parser.flush():
        yield event
