from __future__ import annotations

import json
from collections.abc import Callable

import httpx
import pytest

from gogosling_worker.protocol import Message, Part, ProviderCredentials, ToolSpec, Usage
from gogosling_worker.providers import build_provider
from gogosling_worker.providers.anthropic import (
    AnthropicProvider,
    messages_url,
    parse_anthropic_usage,
)
from gogosling_worker.providers.base import (
    ChatOptions,
    Chunk,
    Done,
    ProviderError,
    ReasoningDelta,
    TextDelta,
    ToolCallDelta,
    UsageChunk,
)

KEY = "sk-ant-secret-1234567890"
CREDS = ProviderCredentials(kind="anthropic", model="claude-test", api_key=KEY)
SYSTEM = Message("system", (Part.of_text("你是助理"),))
USER = Message("user", (Part.of_text("你好"),))
TOOL = ToolSpec(name="read_file", description="读", input_schema={"type": "object"})


def _events(*objects: dict[str, object]) -> bytes:
    out = []
    for obj in objects:
        out.append(f"event: {obj['type']}\ndata: {json.dumps(obj, ensure_ascii=False)}\n\n")
    return "".join(out).encode("utf-8")


def _provider(handler: Callable[[httpx.Request], httpx.Response]) -> AnthropicProvider:
    client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    return AnthropicProvider(CREDS, locale="zh", client=client)


async def _collect(provider: AnthropicProvider, tools: list[ToolSpec] | None = None) -> list[Chunk]:
    return [c async for c in provider.stream_chat([SYSTEM, USER], tools or [], ChatOptions())]


def _stream_fixture() -> bytes:
    return _events(
        {
            "type": "message_start",
            "message": {
                "id": "m",
                "usage": {
                    "input_tokens": 10,
                    "cache_read_input_tokens": 6,
                    "cache_creation_input_tokens": 2,
                    "output_tokens": 1,
                },
            },
        },
        {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
        {"type": "ping"},
        {
            "type": "content_block_delta",
            "index": 0,
            "delta": {"type": "text_delta", "text": "我来"},
        },
        {
            "type": "content_block_delta",
            "index": 0,
            "delta": {"type": "text_delta", "text": "看看"},
        },
        {"type": "content_block_stop", "index": 0},
        {
            "type": "content_block_start",
            "index": 1,
            "content_block": {
                "type": "tool_use",
                "id": "toolu_1",
                "name": "read_file",
                "input": {},
            },
        },
        {
            "type": "content_block_delta",
            "index": 1,
            "delta": {"type": "input_json_delta", "partial_json": ""},
        },
        {
            "type": "content_block_delta",
            "index": 1,
            "delta": {"type": "input_json_delta", "partial_json": '{"path":'},
        },
        {
            "type": "content_block_delta",
            "index": 1,
            "delta": {"type": "input_json_delta", "partial_json": ' "a.md"}'},
        },
        {"type": "content_block_stop", "index": 1},
        {
            "type": "message_delta",
            "delta": {"stop_reason": "tool_use"},
            "usage": {"output_tokens": 9},
        },
        {"type": "message_stop"},
    )


async def test_streaming_parses_text_tool_use_and_usage() -> None:
    seen: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        return httpx.Response(
            200, content=_stream_fixture(), headers={"content-type": "text/event-stream"}
        )

    chunks = await _collect(_provider(handler), [TOOL])
    assert chunks == [
        UsageChunk(
            Usage(input_tokens=18, output_tokens=1, cache_read_tokens=6, cache_write_tokens=2)
        ),
        TextDelta("我来"),
        TextDelta("看看"),
        ToolCallDelta(index=1, id="toolu_1", name="read_file"),
        ToolCallDelta(index=1, arguments='{"path":'),
        ToolCallDelta(index=1, arguments=' "a.md"}'),
        UsageChunk(
            Usage(input_tokens=18, output_tokens=9, cache_read_tokens=6, cache_write_tokens=2)
        ),
        Done("tool_use"),
    ]
    request = seen[0]
    assert str(request.url) == "https://api.anthropic.com/v1/messages"
    assert request.headers["x-api-key"] == KEY
    assert request.headers["anthropic-version"] == "2023-06-01"
    body = json.loads(request.content)
    assert body["system"] == "你是助理"
    assert body["messages"] == [{"role": "user", "content": [{"type": "text", "text": "你好"}]}]
    assert body["max_tokens"] == 4096 and body["stream"] is True
    assert body["tools"] == [
        {"name": "read_file", "description": "读", "input_schema": {"type": "object"}}
    ]


async def test_options_and_url_variants() -> None:
    captured: list[dict[str, object]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        captured.append(json.loads(request.content))
        return httpx.Response(
            200,
            content=_events({"type": "message_stop"}),
            headers={"content-type": "text/event-stream"},
        )

    provider = _provider(handler)
    chunks = [
        c
        async for c in provider.stream_chat(
            [USER], [], ChatOptions(temperature=0.5, max_output_tokens=99)
        )
    ]
    assert chunks == [Done(None)]
    assert captured[0]["temperature"] == 0.5 and captured[0]["max_tokens"] == 99
    assert messages_url("https://proxy.test/v1/") == "https://proxy.test/v1/messages"
    assert messages_url("https://proxy.test") == "https://proxy.test/v1/messages"
    assert messages_url("") == "https://api.anthropic.com/v1/messages"


async def test_stream_ending_without_message_stop_still_reports_usage() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        body = _events(
            {"type": "message_start", "message": {"usage": {"input_tokens": 3}}},
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "text_delta", "text": "x"},
            },
            {
                "type": "message_delta",
                "delta": {"stop_reason": "end_turn"},
                "usage": {"output_tokens": 2},
            },
        )
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    assert await _collect(_provider(handler)) == [
        UsageChunk(Usage(3, 0)),
        TextDelta("x"),
        UsageChunk(Usage(3, 2)),
        Done("end_turn"),
    ]


async def test_error_event_and_http_error_are_sanitized() -> None:
    def stream_error(request: httpx.Request) -> httpx.Response:
        body = _events(
            {"type": "error", "error": {"type": "overloaded_error", "message": f"key {KEY}"}}
        )
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    with pytest.raises(ProviderError) as info:
        await _collect(_provider(stream_error))
    assert KEY not in info.value.message and "overloaded_error" in info.value.message

    def http_error(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            529, json={"type": "error", "error": {"type": "overloaded", "message": KEY}}
        )

    with pytest.raises(ProviderError) as info:
        await _collect(_provider(http_error))
    assert info.value.status == 529 and KEY not in info.value.message


async def test_garbage_fragments_are_skipped_and_thinking_becomes_reasoning() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        body = b"event: junk\ndata: {oops\n\n" + _events(
            {
                "type": "content_block_start",
                "index": 0,
                "content_block": {"type": "thinking", "thinking": ""},
            },
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "thinking_delta", "thinking": "hmm"},
            },
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "signature_delta", "signature": "sig"},
            },
            {
                "type": "content_block_delta",
                "index": 1,
                "delta": {"type": "text_delta", "text": "ok"},
            },
            {"type": "message_stop"},
        )
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    assert await _collect(_provider(handler)) == [
        ReasoningDelta("hmm"),
        TextDelta("ok"),
        Done(None),
    ]


async def test_message_delta_usage_keeps_input_and_cache_fields() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        body = _events(
            {
                "type": "message_start",
                "message": {
                    "usage": {
                        "input_tokens": 3,
                        "cache_read_input_tokens": 4,
                        "cache_creation_input_tokens": 5,
                        "output_tokens": 1,
                    }
                },
            },
            {
                "type": "message_delta",
                "delta": {"stop_reason": "end_turn"},
                "usage": {"output_tokens": 7},
            },
            {"type": "message_stop"},
        )
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    chunks = await _collect(_provider(handler))
    assert (
        UsageChunk(
            Usage(input_tokens=12, output_tokens=7, cache_read_tokens=4, cache_write_tokens=5)
        )
        in chunks
    )


async def test_non_streaming_json_message_is_accepted() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        payload = {
            "type": "message",
            "content": [
                {"type": "thinking", "thinking": "先想想", "signature": "sig"},
                {"type": "text", "text": "一次性"},
                {"type": "tool_use", "id": "toolu_9", "name": "read_file", "input": {"path": "x"}},
            ],
            "stop_reason": "tool_use",
            "usage": {"input_tokens": 4, "output_tokens": 6},
        }
        return httpx.Response(200, json=payload)

    assert await _collect(_provider(handler)) == [
        ReasoningDelta("先想想"),
        TextDelta("一次性"),
        ToolCallDelta(index=2, id="toolu_9", name="read_file", arguments='{"path": "x"}'),
        UsageChunk(Usage(4, 6)),
        Done("tool_use"),
    ]


async def test_transport_error_maps_to_provider_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("down", request=request)

    with pytest.raises(ProviderError) as info:
        await _collect(_provider(handler))
    assert "ConnectError" in info.value.message


def test_usage_normalisation_includes_cache_in_input() -> None:
    usage = parse_anthropic_usage(
        {
            "input_tokens": 10,
            "cache_read_input_tokens": 5,
            "cache_creation_input_tokens": 1,
            "output_tokens": 2,
        }
    )
    assert usage == Usage(
        input_tokens=16, output_tokens=2, cache_read_tokens=5, cache_write_tokens=1
    )
    assert usage.to_json() == {
        "input_tokens": 16,
        "output_tokens": 2,
        "cache_read_tokens": 5,
        "cache_write_tokens": 1,
    }


def test_factory_builds_anthropic_provider() -> None:
    provider = build_provider(CREDS)
    assert isinstance(provider, AnthropicProvider)
    assert provider.url == "https://api.anthropic.com/v1/messages"
