from __future__ import annotations

import asyncio
from contextlib import suppress

import httpx
import pytest

from gogosling_worker.providers.stream_lifetime import stream_request


@pytest.mark.parametrize("tls", [False, True])
async def test_cancel_closes_socket_during_headers_or_tls_handshake(tls: bool) -> None:
    """本地真实 socket：取消等待响应头或 TLS 握手时，对端必须及时收到 EOF。"""
    connected = asyncio.Event()
    closed = asyncio.Event()
    handlers: set[asyncio.Task[None]] = set()

    async def accept(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        task = asyncio.current_task()
        assert task is not None
        handlers.add(task)
        connected.set()
        try:
            while await reader.read(4096):
                pass
        finally:
            closed.set()
            writer.close()
            with suppress(ConnectionError):
                await writer.wait_closed()
            handlers.discard(task)

    server = await asyncio.start_server(accept, "127.0.0.1", 0)
    port = server.sockets[0].getsockname()[1]
    scheme = "https" if tls else "http"
    async with server, httpx.AsyncClient(trust_env=False, timeout=10) as client:

        async def request() -> None:
            async with stream_request(client, f"{scheme}://127.0.0.1:{port}", body={}, headers={}):
                pytest.fail("假服务器不应返回响应")

        task = asyncio.create_task(request())
        try:
            await asyncio.wait_for(connected.wait(), 2)
            await asyncio.sleep(0.05)
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await task
            await asyncio.wait_for(closed.wait(), 1)
        finally:
            task.cancel()
            await asyncio.gather(task, return_exceptions=True)
            for handler in list(handlers):
                handler.cancel()
            await asyncio.gather(*handlers, return_exceptions=True)
