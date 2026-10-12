"""Anthropic Messages 接口（``POST {base_url}/v1/messages``，``stream: true``）。

流式事件：``message_start``（输入用量）→ ``content_block_start`` / ``content_block_delta``
（``text_delta``、工具调用的 ``input_json_delta``、思考内容的 ``thinking_delta``）
→ ``message_delta``（输出用量、结束原因）→ ``message_stop``。
工具调用的参数 JSON 按 content block 的 ``index`` 拼接；
``thinking_delta`` 翻译为 :class:`ReasoningDelta`（``reasoning.delta`` 事件，不进入最终消息），
``signature_delta`` 忽略。

用量口径：Anthropic 的 ``input_tokens`` **不含**缓存读写部分，这里统一成与 OpenAI 相同的口径：
``input_tokens`` = 全部输入，``cache_read_tokens`` / ``cache_write_tokens`` 为其中命中缓存与写入缓存
（``cache_creation_input_tokens``）的子集，避免两家数据混算时失真。
"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import AsyncIterator, Sequence
from typing import Any

import httpx

from gogosling_worker import __version__
from gogosling_worker.i18n import DEFAULT_LOCALE, tr
from gogosling_worker.protocol import Message, ProviderCredentials, ToolSpec, Usage
from gogosling_worker.providers.base import (
    ChatOptions,
    Chunk,
    Done,
    ProviderError,
    ReasoningDelta,
    TextDelta,
    ToolCallDelta,
    UsageChunk,
    excerpt,
    redact,
    token_int,
)
from gogosling_worker.providers.openai_compatible import DEFAULT_TIMEOUT, read_limited
from gogosling_worker.providers.retry import retry_delay
from gogosling_worker.providers.sse import SSEParser, SSEProtocolError
from gogosling_worker.providers.stream_lifetime import stream_request

API_VERSION = "2023-06-01"
DEFAULT_BASE_URL = "https://api.anthropic.com"
DEFAULT_MAX_TOKENS = 4096
_ERROR_BODY_LIMIT = 64 * 1024
_JSON_BODY_LIMIT = 16 * 1024 * 1024

_log = logging.getLogger(__name__)


def messages_url(base_url: str) -> str:
    """由 base_url 得到 ``/v1/messages`` 端点；base_url 已含 ``/v1`` 时不重复。"""
    base = (base_url.strip() or DEFAULT_BASE_URL).rstrip("/")
    if base.endswith("/v1"):
        return base + "/messages"
    return base + "/v1/messages"


def parse_anthropic_usage(usage: dict[str, Any]) -> Usage:
    """把 Anthropic 的 usage 统一成本项目口径。"""
    uncached = token_int(usage.get("input_tokens"))
    cache_read = token_int(usage.get("cache_read_input_tokens"))
    cache_write = token_int(usage.get("cache_creation_input_tokens"))
    return Usage(
        input_tokens=uncached + cache_read + cache_write,
        output_tokens=token_int(usage.get("output_tokens")),
        cache_read_tokens=cache_read,
        cache_write_tokens=cache_write,
    )


class _StreamState:
    """一次 ``/v1/messages`` 流的累积状态。"""

    def __init__(self) -> None:
        self.usage = Usage()
        self.stop_reason: str | None = None
        self.finished = False


class AnthropicProvider:
    """Anthropic Messages 接口的流式客户端。"""

    def __init__(
        self,
        credentials: ProviderCredentials,
        *,
        locale: str = DEFAULT_LOCALE,
        client: httpx.AsyncClient | None = None,
        logger: logging.Logger | None = None,
    ) -> None:
        self._creds = credentials
        self._locale = locale
        self._log = logger or _log
        self._client = client or httpx.AsyncClient(timeout=DEFAULT_TIMEOUT)
        self._owns_client = client is None
        self._url = messages_url(credentials.base_url)

    @property
    def url(self) -> str:
        """实际请求的端点。"""
        return self._url

    async def aclose(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    def build_body(
        self,
        messages: Sequence[Message],
        tools: Sequence[ToolSpec],
        options: ChatOptions,
    ) -> dict[str, Any]:
        """构造请求体（公开以便测试）。"""
        from gogosling_worker.providers.messages import to_anthropic_request, to_anthropic_tools

        system, rendered = to_anthropic_request(messages, locale=self._locale, logger=self._log)
        body: dict[str, Any] = {
            "model": self._creds.model,
            "max_tokens": options.max_output_tokens or DEFAULT_MAX_TOKENS,
            "messages": rendered,
            "stream": True,
        }
        if system:
            body["system"] = system
        if tools:
            body["tools"] = to_anthropic_tools(tools)
        if options.temperature is not None:
            body["temperature"] = options.temperature
        return body

    def _headers(self) -> dict[str, str]:
        headers = {
            "Accept": "text/event-stream",
            "Content-Type": "application/json",
            "anthropic-version": API_VERSION,
            "User-Agent": f"gogosling-worker/{__version__}",
        }
        if self._creds.api_key:
            headers["x-api-key"] = self._creds.api_key
        return headers

    def _clean(self, text: str) -> str:
        return excerpt(redact(text, [self._creds.api_key]))

    async def stream_chat(
        self,
        messages: Sequence[Message],
        tools: Sequence[ToolSpec],
        options: ChatOptions,
    ) -> AsyncIterator[Chunk]:
        body = self.build_body(messages, tools, options)
        transient_retries = 0
        try:
            while True:
                async with stream_request(
                    self._client, self._url, body=body, headers=self._headers()
                ) as response:
                    if response.status_code >= 400:
                        detail = await read_limited(response, _ERROR_BODY_LIMIT)
                        delay = retry_delay(response, transient_retries)
                        if delay is not None:
                            await response.aclose()
                            transient_retries += 1
                            self._log.info(
                                "模型服务暂时不可用，等待后重试",
                                extra={"status": response.status_code, "retry": transient_retries},
                            )
                            await asyncio.sleep(delay)
                            continue
                        raise ProviderError(
                            tr(
                                "provider.http_error",
                                self._locale,
                                model=self._creds.model or "—",
                                status=response.status_code,
                                detail=self._clean(detail),
                            ),
                            status=response.status_code,
                        )
                    async for chunk in self._consume(response):
                        yield chunk
                    return
        except httpx.TimeoutException as exc:
            raise ProviderError(tr("provider.timeout", self._locale)) from exc
        except httpx.HTTPError as exc:
            reason = self._clean(f"{type(exc).__name__}: {exc}")
            raise ProviderError(
                tr("provider.transport_error", self._locale, reason=reason)
            ) from exc

    async def _consume(self, response: httpx.Response) -> AsyncIterator[Chunk]:
        content_type = response.headers.get("content-type", "")
        if "text/event-stream" not in content_type:
            raw = await read_limited(response, _JSON_BODY_LIMIT)
            try:
                payload = json.loads(raw)
            except ValueError as exc:
                raise ProviderError(
                    tr("provider.bad_response", self._locale, detail=self._clean(raw))
                ) from exc
            for chunk in self._chunks_from_message(payload):
                yield chunk
            return

        parser = SSEParser()
        state = _StreamState()
        try:
            async for raw_chunk in response.aiter_bytes():
                for event in parser.feed(raw_chunk):
                    for chunk in self._chunks_from_event(event.data, state):
                        yield chunk
                if state.finished:
                    break
            if not state.finished:
                for event in parser.flush():
                    for chunk in self._chunks_from_event(event.data, state):
                        yield chunk
        except SSEProtocolError as exc:
            raise ProviderError(
                tr("provider.stream_error", self._locale, detail=self._clean(str(exc)))
            ) from exc
        if not state.finished:
            if state.stop_reason is None:
                raise ProviderError(tr("provider.incomplete_response", self._locale))
            yield Done(state.stop_reason)

    def _chunks_from_event(self, data: str, state: _StreamState) -> list[Chunk]:
        try:
            obj = json.loads(data)
        except ValueError:
            self._log.warning("跳过无法解析的 SSE 片段（%d 字节）", len(data))
            return []
        if not isinstance(obj, dict):
            return []
        kind = obj.get("type")
        if kind == "error":
            error = obj.get("error")
            detail = json.dumps(error, ensure_ascii=False) if error is not None else data
            raise ProviderError(
                tr("provider.stream_error", self._locale, detail=self._clean(detail))
            )
        if kind == "message_start":
            message = obj.get("message")
            usage = message.get("usage") if isinstance(message, dict) else None
            if isinstance(usage, dict):
                state.usage = state.usage.merge_max(parse_anthropic_usage(usage))
                # 立即交给推理循环；流内错误、断连或取消不应丢掉已经报告的输入用量。
                return [UsageChunk(state.usage)]
            return []
        if kind == "content_block_start":
            return _block_start_chunks(obj)
        if kind == "content_block_delta":
            return _block_delta_chunks(obj)
        if kind == "message_delta":
            delta = obj.get("delta")
            if isinstance(delta, dict):
                reason = delta.get("stop_reason")
                if isinstance(reason, str) and reason.strip():
                    state.stop_reason = reason.strip()
            usage = obj.get("usage")
            if isinstance(usage, dict):
                # message_delta 的用量是累计值（通常只有 output_tokens，新版也会带输入与缓存字段），
                # 逐字段取最大即可；缺席的字段解析为 0，不会覆盖 message_start 的值。
                state.usage = state.usage.merge_max(parse_anthropic_usage(usage))
                return [UsageChunk(state.usage)]
            return []
        if kind == "message_stop":
            state.finished = True
            return [Done(state.stop_reason)]
        # ping、content_block_stop 与未知事件：忽略。
        return []

    def _chunks_from_message(self, payload: Any) -> list[Chunk]:
        """非流式响应（服务端忽略了 stream）：整条消息翻译成分片。"""
        if not isinstance(payload, dict):
            raise ProviderError(tr("provider.bad_response", self._locale, detail=""))
        if payload.get("type") == "error":
            error = payload.get("error")
            detail = json.dumps(error, ensure_ascii=False) if error is not None else ""
            raise ProviderError(
                tr("provider.stream_error", self._locale, detail=self._clean(detail))
            )
        chunks: list[Chunk] = []
        content = payload.get("content")
        if not isinstance(content, list):
            raise ProviderError(
                tr(
                    "provider.bad_response",
                    self._locale,
                    detail=self._clean(json.dumps(payload, ensure_ascii=False)),
                )
            )
        if isinstance(content, list):
            for index, block in enumerate(content):
                if not isinstance(block, dict):
                    continue
                if block.get("type") == "text":
                    text = block.get("text")
                    if isinstance(text, str) and text:
                        chunks.append(TextDelta(text))
                elif block.get("type") == "thinking":
                    thinking = block.get("thinking")
                    if isinstance(thinking, str) and thinking:
                        chunks.append(ReasoningDelta(thinking))
                elif block.get("type") == "tool_use":
                    chunks.append(
                        ToolCallDelta(
                            index=index,
                            id=_opt_str(block.get("id")),
                            name=_opt_str(block.get("name")),
                            arguments=json.dumps(block.get("input") or {}, ensure_ascii=False),
                        )
                    )
        usage = payload.get("usage")
        if isinstance(usage, dict):
            chunks.append(UsageChunk(parse_anthropic_usage(usage)))
        chunks.append(Done(_opt_str(payload.get("stop_reason"))))
        return chunks


def _opt_str(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


def _block_index(obj: dict[str, Any]) -> int:
    index = obj.get("index")
    return index if isinstance(index, int) and not isinstance(index, bool) else 0


def _block_start_chunks(obj: dict[str, Any]) -> list[Chunk]:
    block = obj.get("content_block")
    if not isinstance(block, dict):
        return []
    if block.get("type") == "tool_use":
        return [
            ToolCallDelta(
                index=_block_index(obj),
                id=_opt_str(block.get("id")),
                name=_opt_str(block.get("name")),
            )
        ]
    if block.get("type") == "text":
        text = block.get("text")
        if isinstance(text, str) and text:
            return [TextDelta(text)]
    if block.get("type") == "thinking":
        thinking = block.get("thinking")
        if isinstance(thinking, str) and thinking:
            return [ReasoningDelta(thinking)]
    return []


def _block_delta_chunks(obj: dict[str, Any]) -> list[Chunk]:
    delta = obj.get("delta")
    if not isinstance(delta, dict):
        return []
    kind = delta.get("type")
    if kind == "text_delta":
        text = delta.get("text")
        return [TextDelta(text)] if isinstance(text, str) and text else []
    if kind == "input_json_delta":
        partial = delta.get("partial_json")
        if not isinstance(partial, str) or not partial:
            return []
        return [ToolCallDelta(index=_block_index(obj), arguments=partial)]
    if kind == "thinking_delta":
        thinking = delta.get("thinking")
        return [ReasoningDelta(thinking)] if isinstance(thinking, str) and thinking else []
    # signature_delta 与未知类型：忽略。
    return []


__all__ = [
    "API_VERSION",
    "DEFAULT_BASE_URL",
    "DEFAULT_MAX_TOKENS",
    "AnthropicProvider",
    "messages_url",
    "parse_anthropic_usage",
]
