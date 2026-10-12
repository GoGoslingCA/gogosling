from __future__ import annotations

import json
from collections.abc import Callable

import httpx
import pytest

from gogosling_worker.protocol import Message, Part, ProviderCredentials, ToolSpec, Usage
from gogosling_worker.providers import build_provider, ollama_credentials
from gogosling_worker.providers.base import (
    ChatOptions,
    Chunk,
    Done,
    ProviderError,
    ReasoningDelta,
    TextDelta,
    ToolCallDelta,
    UsageChunk,
    redact,
)
from gogosling_worker.providers.openai_compatible import (
    OpenAICompatibleProvider,
    parameter_fallback,
    parse_openai_usage,
)

KEY = "sk-secret-key-1234567890"
CREDS = ProviderCredentials(
    kind="openai_compatible", model="gpt-test", base_url="https://api.test/v1/", api_key=KEY
)
USER = Message("user", (Part.of_text("你好"),))
TOOL = ToolSpec(name="read_file", description="读", input_schema={"type": "object"})


def _sse(*objects: object) -> bytes:
    lines = []
    for obj in objects:
        payload = obj if isinstance(obj, str) else json.dumps(obj, ensure_ascii=False)
        lines.append(f"data: {payload}\n\n")
    return "".join(lines).encode("utf-8")


def _chunk(delta: dict[str, object], finish: str | None = None) -> dict[str, object]:
    return {"id": "x", "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}


def _provider(
    handler: Callable[[httpx.Request], httpx.Response], creds: ProviderCredentials = CREDS
) -> OpenAICompatibleProvider:
    client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    return OpenAICompatibleProvider(creds, locale="zh", client=client)


async def _collect(
    provider: OpenAICompatibleProvider, tools: list[ToolSpec] | None = None
) -> list[Chunk]:
    return [c async for c in provider.stream_chat([USER], tools or [], ChatOptions())]


async def test_streaming_text_tool_calls_and_usage() -> None:
    seen: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        body = _sse(
            _chunk({"role": "assistant", "content": ""}),
            _chunk({"content": "我来"}),
            _chunk({"content": "看看"}),
            _chunk(
                {
                    "tool_calls": [
                        {
                            "index": 0,
                            "id": "call_1",
                            "type": "function",
                            "function": {"name": "read_file", "arguments": ""},
                        }
                    ]
                }
            ),
            _chunk({"tool_calls": [{"index": 0, "function": {"arguments": '{"path":'}}]}),
            _chunk({"tool_calls": [{"index": 0, "function": {"arguments": ' "a.md"}'}}]}),
            _chunk({}, "tool_calls"),
            {
                "id": "x",
                "choices": [],
                "usage": {
                    "prompt_tokens": 12,
                    "completion_tokens": 7,
                    "prompt_tokens_details": {"cached_tokens": 4},
                },
            },
            "[DONE]",
        )
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    chunks = await _collect(_provider(handler), [TOOL])
    assert chunks == [
        TextDelta("我来"),
        TextDelta("看看"),
        ToolCallDelta(index=0, id="call_1", name="read_file", arguments=""),
        ToolCallDelta(index=0, arguments='{"path":'),
        ToolCallDelta(index=0, arguments=' "a.md"}'),
        UsageChunk(Usage(input_tokens=12, output_tokens=7, cache_read_tokens=4)),
        Done("tool_calls"),
    ]
    request = seen[0]
    assert str(request.url) == "https://api.test/v1/chat/completions"
    assert request.headers["authorization"] == f"Bearer {KEY}"
    assert request.headers["accept"] == "text/event-stream"
    body = json.loads(request.content)
    assert body["model"] == "gpt-test"
    assert body["stream"] is True and body["stream_options"] == {"include_usage": True}
    assert body["messages"] == [{"role": "user", "content": "你好"}]
    assert body["tools"][0]["function"]["name"] == "read_file"
    assert "temperature" not in body and "max_tokens" not in body


async def test_options_are_sent_when_present() -> None:
    captured: list[dict[str, object]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        captured.append(json.loads(request.content))
        return httpx.Response(
            200,
            content=_sse(_chunk({"content": "x"}, "stop"), "[DONE]"),
            headers={"content-type": "text/event-stream"},
        )

    provider = _provider(handler)
    chunks = [
        c
        async for c in provider.stream_chat(
            [USER], [], ChatOptions(temperature=0.3, max_output_tokens=256)
        )
    ]
    assert chunks == [TextDelta("x"), Done("stop")]
    assert captured[0]["temperature"] == 0.3 and captured[0]["max_tokens"] == 256


async def test_garbage_sse_fragments_are_skipped_and_crlf_handled() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        body = (
            b"data: {not json}\r\n\r\n: comment\r\n"
            + _sse(_chunk({"content": "ok"}, "stop")).replace(b"\n", b"\r\n")
            + b"data: [DONE]\r\n\r\n"
        )
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    assert await _collect(_provider(handler)) == [TextDelta("ok"), Done("stop")]


async def test_stream_without_done_marker_still_finishes() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        body = b'data: {"choices":[{"delta":{"content":"tail"},"finish_reason":"stop"}]}'
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    assert await _collect(_provider(handler)) == [TextDelta("tail"), Done("stop")]


async def test_in_stream_error_object_raises_sanitized_provider_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        body = _sse({"error": {"message": f"bad key {KEY}", "type": "auth"}})
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    with pytest.raises(ProviderError) as info:
        await _collect(_provider(handler))
    assert KEY not in info.value.message
    assert "***" in info.value.message


async def test_http_error_is_redacted_and_truncated() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(401, text=f"Unauthorized: Bearer {KEY} " + "x" * 1000)

    with pytest.raises(ProviderError) as info:
        await _collect(_provider(handler))
    message = info.value.message
    assert info.value.status == 401
    assert "HTTP 401" in message
    assert KEY not in message
    assert len(message) < 300


async def test_transport_error_maps_to_provider_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("boom", request=request)

    with pytest.raises(ProviderError) as info:
        await _collect(_provider(handler))
    assert "ConnectError" in info.value.message


async def test_timeout_maps_to_provider_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("slow", request=request)

    with pytest.raises(ProviderError) as info:
        await _collect(_provider(handler))
    assert "超时" in info.value.message


async def test_non_streaming_json_response_is_accepted() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        payload = {
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": "一次性",
                        "tool_calls": [
                            {
                                "id": "c9",
                                "type": "function",
                                "function": {"name": "read_file", "arguments": "{}"},
                            }
                        ],
                    },
                    "finish_reason": "stop",
                }
            ],
            "usage": {"prompt_tokens": 1, "completion_tokens": 2},
        }
        return httpx.Response(200, json=payload)

    assert await _collect(_provider(handler)) == [
        TextDelta("一次性"),
        ToolCallDelta(index=0, id="c9", name="read_file", arguments="{}"),
        UsageChunk(Usage(1, 2)),
        Done("stop"),
    ]


async def test_unsupported_stream_options_triggers_single_retry() -> None:
    bodies: list[dict[str, object]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        body = json.loads(request.content)
        bodies.append(body)
        if "stream_options" in body:
            return httpx.Response(422, json={"error": "unknown field stream_options"})
        return httpx.Response(
            200,
            content=_sse(_chunk({"content": "ok"}, "stop"), "[DONE]"),
            headers={"content-type": "text/event-stream"},
        )

    assert await _collect(_provider(handler)) == [TextDelta("ok"), Done("stop")]
    assert len(bodies) == 2 and "stream_options" not in bodies[1]


async def test_retry_happens_at_most_once() -> None:
    calls = 0

    def handler(request: httpx.Request) -> httpx.Response:
        nonlocal calls
        calls += 1
        return httpx.Response(
            400, json={"error": "stream_options unsupported; also max_completion_tokens"}
        )

    provider = _provider(handler)
    with pytest.raises(ProviderError):
        [c async for c in provider.stream_chat([USER], [], ChatOptions(max_output_tokens=10))]
    assert calls == 2


def test_parameter_fallback_rules() -> None:
    body = {"max_tokens": 5, "stream_options": {}}
    assert parameter_fallback(500, "stream_options", body) is None
    dropped = parameter_fallback(400, "stream_options is not allowed", body)
    assert dropped is not None and "stream_options" not in dropped
    renamed = parameter_fallback(400, "use max_completion_tokens", {"max_tokens": 5})
    assert renamed == {"max_completion_tokens": 5}
    assert parameter_fallback(400, "something else", body) is None


def test_parse_openai_usage_is_tolerant() -> None:
    assert parse_openai_usage({"prompt_tokens": "7", "completion_tokens": None}) == Usage(7, 0)
    assert parse_openai_usage({"prompt_tokens": True, "completion_tokens": 3.0}) == Usage(0, 3)
    assert parse_openai_usage({"prompt_tokens": 5, "prompt_cache_hit_tokens": 9}) == Usage(5, 0, 5)


def test_redact_handles_bearer_and_key_shapes() -> None:
    text = f"Authorization: Bearer {KEY}; also sk-abcdefghijklmnop and token_abcdefghijklmnop"
    cleaned = redact(text, [KEY])
    assert KEY not in cleaned and "sk-abcdefghijklmnop" not in cleaned
    assert "token_abcdefghijklmnop" not in cleaned


def test_missing_base_url_is_rejected_for_openai_compatible() -> None:
    with pytest.raises(ProviderError):
        build_provider(ProviderCredentials(kind="openai_compatible", model="m"))


def test_ollama_maps_to_openai_compatible_v1() -> None:
    assert (
        ollama_credentials(ProviderCredentials(kind="ollama", model="m")).base_url
        == "http://127.0.0.1:11434/v1"
    )
    assert (
        ollama_credentials(
            ProviderCredentials(kind="ollama", model="m", base_url="http://h:1/")
        ).base_url
        == "http://h:1/v1"
    )
    assert (
        ollama_credentials(
            ProviderCredentials(kind="ollama", model="m", base_url="http://h:1/v1")
        ).base_url
        == "http://h:1/v1"
    )
    provider = build_provider(ProviderCredentials(kind="ollama", model="m"))
    assert isinstance(provider, OpenAICompatibleProvider)
    assert provider.url == "http://127.0.0.1:11434/v1/chat/completions"


async def test_provider_without_key_sends_no_authorization_header() -> None:
    seen: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        return httpx.Response(
            200, content=_sse("[DONE]"), headers={"content-type": "text/event-stream"}
        )

    creds = ProviderCredentials(
        kind="openai_compatible", model="m", base_url="http://localhost:1234/v1"
    )
    chunks = await _collect(_provider(handler, creds))
    assert chunks == [Done(None)]
    assert "authorization" not in seen[0].headers


def test_parse_openai_usage_reports_cache_writes_when_present() -> None:
    nested = {"prompt_tokens": 100, "prompt_tokens_details": {"cache_write_tokens": 30}}
    assert parse_openai_usage(nested) == Usage(100, 0, 0, 30)
    litellm = {
        "prompt_tokens": 50,
        "completion_tokens": 2,
        "cache_creation_input_tokens": 20,
        "prompt_tokens_details": {"cached_tokens": 10},
    }
    assert parse_openai_usage(litellm) == Usage(50, 2, 10, 20)
    # 畸形值不会超过总输入。
    assert parse_openai_usage({"prompt_tokens": 5, "cache_creation_input_tokens": 9}) == Usage(
        5, 0, 0, 5
    )


async def test_reasoning_content_becomes_reasoning_delta() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        body = _sse(
            _chunk({"role": "assistant", "reasoning_content": "先想"}),
            _chunk({"reasoning_content": "一想", "content": None}),
            _chunk({"reasoning": "OpenRouter 风格"}),
            _chunk({"content": "答案"}, "stop"),
            "[DONE]",
        )
        return httpx.Response(200, content=body, headers={"content-type": "text/event-stream"})

    chunks = await _collect(_provider(handler))
    assert chunks == [
        ReasoningDelta("先想"),
        ReasoningDelta("一想"),
        ReasoningDelta("OpenRouter 风格"),
        TextDelta("答案"),
        Done("stop"),
    ]
