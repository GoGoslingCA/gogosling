from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator
from datetime import UTC, datetime, timedelta
from email.utils import format_datetime
from typing import Any

import httpx
import pytest

from gogosling_worker.protocol import Message, Part, ProviderCredentials
from gogosling_worker.providers import build_provider
from gogosling_worker.providers.base import (
    ChatOptions,
    Chunk,
    Done,
    ProviderError,
    TextDelta,
    UsageChunk,
)
from gogosling_worker.providers.retry import retry_delay
from gogosling_worker.providers.sse import SSEParser
from tests.harness import WAIT

KINDS = ["openai_compatible", "anthropic"]
SECRET = "vendor-private-credential-unique"
USER = Message("user", (Part.of_text("hi"),))


def _credentials(kind: str, secret: str = SECRET) -> ProviderCredentials:
    return ProviderCredentials(kind, "test-model", "https://model.test/v1", secret)


def _success(kind: str) -> httpx.Response:
    if kind == "anthropic":
        return httpx.Response(
            200,
            json={"type": "message", "content": [{"type": "text", "text": "ok"}]},
        )
    return httpx.Response(200, json={"choices": [{"message": {"content": "ok"}}]})


def _sse(kind: str, *objects: dict[str, Any]) -> httpx.Response:
    data = "".join("data: " + json.dumps(obj) + "\n\n" for obj in objects)
    return httpx.Response(200, content=data, headers={"content-type": "text/event-stream"})


@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("status", [429, 503, 529])
async def test_transient_http_failure_retries_without_replaying_output(
    kind: str, status: int
) -> None:
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        if len(requests) == 1:
            return httpx.Response(status, text="busy", headers={"retry-after": "0"})
        return _success(kind)

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = build_provider(_credentials(kind), client=client)
        chunks = [c async for c in provider.stream_chat([USER], [], ChatOptions())]
    assert [c for c in chunks if isinstance(c, TextDelta)] == [TextDelta("ok")]
    assert isinstance(chunks[-1], Done)
    assert len(requests) == 2
    assert requests[0].content == requests[1].content


@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("status,attempts", [(401, 1), (501, 1), (505, 1), (429, 3), (503, 3)])
async def test_http_retry_is_bounded_and_preserves_final_error(
    kind: str, status: int, attempts: int
) -> None:
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(status, text=f"error: {SECRET}", headers={"retry-after": "0"})

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = build_provider(_credentials(kind), client=client)
        with pytest.raises(ProviderError) as info:
            [c async for c in provider.stream_chat([USER], [], ChatOptions())]
    assert len(requests) == attempts
    assert info.value.status == status
    assert SECRET not in info.value.message and "***" in info.value.message


@pytest.mark.parametrize("kind", KINDS)
async def test_retry_wait_can_be_cancelled_before_next_http_request(kind: str) -> None:
    received = asyncio.Event()
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        received.set()
        return httpx.Response(429, text="busy", headers={"retry-after": "30"})

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = build_provider(_credentials(kind), client=client)

        async def collect() -> list[Chunk]:
            return [c async for c in provider.stream_chat([USER], [], ChatOptions())]

        task = asyncio.create_task(collect())
        await asyncio.wait_for(received.wait(), WAIT)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await asyncio.wait_for(task, WAIT)
    assert len(requests) == 1


def test_retry_after_seconds_dates_and_wait_budget() -> None:
    assert retry_delay(httpx.Response(503), 0) == 0.5
    assert retry_delay(httpx.Response(503), 1) == 1.0
    assert retry_delay(httpx.Response(503), 2) is None
    assert retry_delay(httpx.Response(429, headers={"retry-after": "4"}), 0) == 4
    assert retry_delay(httpx.Response(503, headers={"retry-after": "31"}), 0) is None
    assert retry_delay(httpx.Response(503, headers={"retry-after": "oops"}), 0) == 0.5
    at = datetime.now(UTC) + timedelta(seconds=10)
    delay = retry_delay(httpx.Response(429, headers={"retry-after": format_datetime(at)}), 0)
    assert delay is not None and 8 <= delay <= 10


@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("stream", [False, True])
@pytest.mark.parametrize("secret", [SECRET, "abc", 'vendor-credential-"quoted\\slash'])
async def test_nonstandard_configured_secret_is_redacted_from_provider_error(
    kind: str, stream: bool, secret: str
) -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        error = {"type": "error", "error": {"message": f"invalid credential {secret}"}}
        return _sse(kind, error) if stream else httpx.Response(200, json=error)

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = build_provider(_credentials(kind, secret), client=client)
        with pytest.raises(ProviderError) as info:
            [c async for c in provider.stream_chat([USER], [], ChatOptions())]
    assert secret not in info.value.message and "***" in info.value.message


@pytest.mark.parametrize("kind", KINDS)
async def test_unrecognised_json_response_is_not_an_empty_success(kind: str) -> None:
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(lambda request: httpx.Response(200, json={"detail": SECRET}))
    ) as client:
        provider = build_provider(_credentials(kind), client=client)
        with pytest.raises(ProviderError) as info:
            [c async for c in provider.stream_chat([USER], [], ChatOptions())]
    assert SECRET not in info.value.message


@pytest.mark.parametrize("message", [{}, {"content": {"wrong": "shape"}}])
async def test_invalid_openai_message_shape_is_rejected(message: dict[str, Any]) -> None:
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(
            lambda request: httpx.Response(200, json={"choices": [{"message": message}]})
        )
    ) as client:
        provider = build_provider(_credentials("openai_compatible"), client=client)
        with pytest.raises(ProviderError):
            [c async for c in provider.stream_chat([USER], [], ChatOptions())]


async def test_openai_refusal_is_presented_as_model_text() -> None:
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(
            lambda request: httpx.Response(
                200, json={"choices": [{"message": {"content": None, "refusal": "cannot comply"}}]}
            )
        )
    ) as client:
        provider = build_provider(_credentials("openai_compatible"), client=client)
        chunks = [c async for c in provider.stream_chat([USER], [], ChatOptions())]
    assert chunks == [TextDelta("cannot comply"), Done(None)]


@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("with_error", [False, True])
async def test_interrupted_stream_is_failed_and_never_replayed(kind: str, with_error: bool) -> None:
    requests: list[httpx.Request] = []
    if kind == "anthropic":
        delta = {"type": "content_block_delta", "delta": {"type": "text_delta", "text": "part"}}
    else:
        delta = {"choices": [{"delta": {"content": "part"}}]}
    events = [delta]
    if with_error:
        events.append({"type": "error", "error": {"message": "overloaded", "status": 503}})

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return _sse(kind, *events)

    seen: list[Chunk] = []
    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = build_provider(_credentials(kind), client=client)
        with pytest.raises(ProviderError):
            async for chunk in provider.stream_chat([USER], [], ChatOptions()):
                seen.append(chunk)
    assert [c for c in seen if isinstance(c, TextDelta)] == [TextDelta("part")]
    assert not any(isinstance(c, Done) for c in seen)
    assert not any(isinstance(c, UsageChunk) for c in seen)
    assert len(requests) == 1


class _ObservedStream(httpx.AsyncByteStream):
    def __init__(self, chunks: list[bytes], *, pause: bool = False) -> None:
        self.chunks = chunks
        self.pause = pause
        self.waiting = asyncio.Event()
        self.closed = False

    async def __aiter__(self) -> AsyncIterator[bytes]:
        for chunk in self.chunks:
            yield chunk
        if self.pause:
            self.waiting.set()
            await asyncio.Event().wait()

    async def aclose(self) -> None:
        self.closed = True


def _text_frame(kind: str) -> bytes:
    if kind == "anthropic":
        delta = {"type": "content_block_delta", "delta": {"type": "text_delta", "text": "part"}}
    else:
        delta = {"choices": [{"delta": {"content": "part"}}]}
    return ("data: " + json.dumps(delta) + "\n\n").encode()


@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("oversized", ["line", "event", "eof"])
async def test_oversized_sse_closes_response_without_retry_or_success(
    kind: str, oversized: str, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(
        f"gogosling_worker.providers.{kind}.SSEParser",
        lambda: SSEParser(max_line_bytes=256, max_event_bytes=512),
    )
    if oversized == "line":
        bad_chunks = [b"data: " + b"x" * 257 + b"\n\n"]
    else:
        bad_chunks = [b"data: " + b"x" * 100 + b"\n"] * 5
        bad_chunks.append(b"data: " + b"x" * 20 + (b"" if oversized == "eof" else b"\n"))
    stream = _ObservedStream([_text_frame(kind), *bad_chunks])
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(200, stream=stream, headers={"content-type": "text/event-stream"})

    seen: list[Chunk] = []
    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = build_provider(_credentials(kind), client=client)
        with pytest.raises(ProviderError, match="SSE (line|event) exceeds"):
            async for chunk in provider.stream_chat([USER], [], ChatOptions()):
                seen.append(chunk)
        assert stream.closed
    assert [c for c in seen if isinstance(c, TextDelta)] == [TextDelta("part")]
    assert not any(isinstance(c, Done) for c in seen)
    assert len(requests) == 1


@pytest.mark.parametrize("kind", KINDS)
async def test_cancel_during_incomplete_sse_event_closes_response(kind: str) -> None:
    stream = _ObservedStream([_text_frame(kind), b"data: {\n"], pause=True)
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(200, stream=stream, headers={"content-type": "text/event-stream"})

    seen: list[Chunk] = []
    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        provider = build_provider(_credentials(kind), client=client)

        async def collect() -> None:
            async for chunk in provider.stream_chat([USER], [], ChatOptions()):
                seen.append(chunk)

        task = asyncio.create_task(collect())
        await asyncio.wait_for(stream.waiting.wait(), WAIT)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await asyncio.wait_for(task, WAIT)
        assert stream.closed
    assert [c for c in seen if isinstance(c, TextDelta)] == [TextDelta("part")]
    assert not any(isinstance(c, Done) for c in seen)
    assert len(requests) == 1
