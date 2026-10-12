"""补齐 TLS 握手取消时的连接回收；只使用 HTTPX 的公开 trace 扩展。

httpcore 1.0.9 的 AnyIO start_tls 只捕获 Exception，任务取消属于 BaseException。
握手未完成时连接尚未交给连接池，关闭客户端不足以收回该 TCP 句柄。
每次请求记录已建立的流，异常退出时有界关闭；不记录地址、头部或正文。
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any, Protocol, cast

import httpx


class _Stream(Protocol):
    async def aclose(self) -> None: ...


@asynccontextmanager
async def stream_request(
    client: httpx.AsyncClient,
    url: str,
    *,
    body: dict[str, Any],
    headers: dict[str, str],
) -> AsyncIterator[httpx.Response]:
    streams: list[_Stream] = []

    async def trace(event: str, info: dict[str, Any]) -> None:
        if event in (
            "connection.connect_tcp.complete",
            "connection.connect_unix_socket.complete",
            "connection.start_tls.complete",
        ):
            stream = info.get("return_value")
            if stream is not None and callable(getattr(stream, "aclose", None)):
                streams.append(cast(_Stream, stream))

    try:
        async with client.stream(
            "POST", url, json=body, headers=headers, extensions={"trace": trace}
        ) as response:
            yield response
    except BaseException:
        # 从 TLS 包装向原始 TCP 逆序关闭；重复关闭是幂等操作。
        for stream in reversed(streams):
            try:
                await asyncio.wait_for(stream.aclose(), timeout=0.5)
            except (Exception, asyncio.CancelledError):
                continue
        raise
