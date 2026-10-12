from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator
from typing import Any

import httpx
import pytest

from gogosling_worker.protocol import ProviderCredentials, Usage
from gogosling_worker.providers.anthropic import AnthropicProvider
from gogosling_worker.runtime import DefaultEngine
from tests.harness import WAIT, FakeHost, make_request


def _events(*objects: dict[str, Any]) -> bytes:
    return "".join("data: " + json.dumps(obj) + "\n\n" for obj in objects).encode()


class InterruptedStream(httpx.AsyncByteStream):
    """发送真实 Anthropic 用量后模拟服务器中断，不经 FakeProvider 预先转换。"""

    def __init__(self, mode: str, with_delta_usage: bool) -> None:
        self.mode = mode
        self.with_delta_usage = with_delta_usage
        self.consumed = asyncio.Event()
        self.release = asyncio.Event()
        self.closed = False

    async def __aiter__(self) -> AsyncIterator[bytes]:
        events = [
            {
                "type": "message_start",
                "message": {
                    "usage": {
                        "input_tokens": 10,
                        "cache_read_input_tokens": 2,
                        "cache_creation_input_tokens": 3,
                        "output_tokens": 1,
                    }
                },
            },
            {"type": "content_block_delta", "delta": {"type": "text_delta", "text": "partial"}},
        ]
        if self.with_delta_usage:
            events.append({"type": "message_delta", "usage": {"output_tokens": 5}})
        yield _events(*events)
        self.consumed.set()
        if self.mode == "error":
            yield _events({"type": "error", "error": {"type": "overloaded_error"}})
        elif self.mode == "disconnect":
            raise httpx.ReadError("connection lost")
        elif self.mode == "timeout":
            raise httpx.ReadTimeout("response timed out")
        elif self.mode == "cancel":
            await self.release.wait()
        elif self.mode == "stop":
            yield _events(
                {"type": "message_delta", "delta": {"stop_reason": "end_turn"}},
                {"type": "message_stop"},
            )

    async def aclose(self) -> None:
        self.closed = True


@pytest.mark.parametrize("mode", ["error", "disconnect", "timeout", "eof", "cancel", "stop"])
@pytest.mark.parametrize("with_delta_usage", [False, True])
async def test_real_anthropic_usage_is_preserved_across_every_stream_exit(
    mode: str, with_delta_usage: bool
) -> None:
    stream = InterruptedStream(mode, with_delta_usage)
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(200, stream=stream, headers={"content-type": "text/event-stream"})

    host = FakeHost()
    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = AnthropicProvider(
            ProviderCredentials("anthropic", "claude-test", "https://model.test", "test-secret"),
            client=client,
        )
        task = asyncio.create_task(
            DefaultEngine().run(make_request(kind="anthropic"), host, provider)
        )
        if mode == "cancel":
            await asyncio.wait_for(stream.consumed.wait(), WAIT)
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await asyncio.wait_for(task, WAIT)
        else:
            await asyncio.wait_for(task, WAIT)
    reported = Usage(15, 5 if with_delta_usage else 1, cache_read_tokens=2, cache_write_tokens=3)
    usage_events = [e["usage"] for e in host.events if e["type"] == "usage"]
    assert usage_events == [reported]
    terminal = (
        "run.completed" if mode == "stop" else "run.cancelled" if mode == "cancel" else "run.failed"
    )
    assert host.types() == ["run.started", "text.delta", "usage", terminal]
    if mode == "stop":
        assert host.events[-1]["usage"] == reported
    assert len(requests) == 1
    assert stream.closed
