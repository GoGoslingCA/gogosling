"""OpenAI Chat Completions 及其兼容接口（``POST {base_url}/chat/completions``，``stream=true``）。

覆盖 OpenAI 本身、绝大多数国内模型服务、vLLM / LM Studio 等本地运行时，以及 Ollama 的 ``/v1``。

健壮性设计：

- SSE 由 :mod:`gogosling_worker.providers.sse` 解析，非 JSON 的 ``data`` 片段跳过而不是中断；
  流内的 ``{"error": …}`` 对象作为供应商错误报告；
- 有些兼容服务忽略 ``stream=true`` 直接返回 JSON：按 Content-Type 识别后当作一次性结果处理；
- 有些严格实现不认识 ``stream_options`` 或要求 ``max_completion_tokens``：按 4xx 响应体里的
  参数名做**一次**参数回退重试；
- 错误信息脱敏：密钥替换为 ``***``，响应体只保留压缩后的前 200 字符；
- 思考内容：``delta.reasoning_content``（DeepSeek、通义、vLLM 等）或 ``delta.reasoning``
  （OpenRouter、Ollama 等）翻译为 :class:`ReasoningDelta`，不进入最终消息。
"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import AsyncIterator, Iterable, Sequence
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
from gogosling_worker.providers.retry import retry_delay
from gogosling_worker.providers.sse import ServerSentEvent, SSEParser, SSEProtocolError
from gogosling_worker.providers.stream_lifetime import stream_request

# 兼容服务放思考内容的字段名，按优先级。
_REASONING_FIELDS = ("reasoning_content", "reasoning")

DEFAULT_TIMEOUT = httpx.Timeout(connect=30.0, read=300.0, write=30.0, pool=30.0)
_ERROR_BODY_LIMIT = 64 * 1024
_JSON_BODY_LIMIT = 16 * 1024 * 1024
_DONE_MARKER = "[DONE]"

_log = logging.getLogger(__name__)


def chat_completions_url(base_url: str) -> str:
    """由 base_url（通常以 ``/v1`` 结尾）得到 chat completions 端点。"""
    return base_url.strip().rstrip("/") + "/chat/completions"


def parse_openai_usage(usage: dict[str, Any]) -> Usage:
    """解析 OpenAI 形状的 usage；``prompt_tokens`` 已含缓存读写部分。

    缓存写入不是 OpenAI 官方字段，兼容服务若报告则填入 ``cache_write_tokens``：
    ``prompt_tokens_details.cache_write_tokens`` / ``cache_creation_tokens``（部分网关），
    或顶层的 ``cache_creation_input_tokens``（转发 Anthropic 的代理，如 LiteLLM）。
    """
    prompt = token_int(usage.get("prompt_tokens")) or token_int(usage.get("input_tokens"))
    completion = token_int(usage.get("completion_tokens")) or token_int(usage.get("output_tokens"))
    details = usage.get("prompt_tokens_details")
    if not isinstance(details, dict):
        details = {}
    cached = token_int(details.get("cached_tokens"))
    # DeepSeek 等把缓存命中放在顶层。
    cached = cached or token_int(usage.get("prompt_cache_hit_tokens"))
    written = (
        token_int(details.get("cache_write_tokens"))
        or token_int(details.get("cache_creation_tokens"))
        or token_int(usage.get("cache_creation_input_tokens"))
    )
    if prompt:
        cached = min(cached, prompt)
        written = min(written, prompt)
    return Usage(
        input_tokens=prompt,
        output_tokens=completion,
        cache_read_tokens=cached,
        cache_write_tokens=written,
    )


def parse_stream_object(
    obj: Any, *, locale: str = DEFAULT_LOCALE, secrets: Iterable[str] = ()
) -> tuple[list[Chunk], str | None]:
    """把一个流式 chunk 对象翻译成分片；返回 ``(chunks, finish_reason)``。"""
    if not isinstance(obj, dict):
        return [], None
    error = obj.get("error")
    if error:
        detail = error if isinstance(error, str) else json.dumps(error, ensure_ascii=False)
        raise ProviderError(
            tr("provider.stream_error", locale, detail=excerpt(redact(detail, secrets)))
        )
    chunks: list[Chunk] = []
    finish: str | None = None
    choices = obj.get("choices")
    if isinstance(choices, list):
        for choice in choices:
            if not isinstance(choice, dict):
                continue
            delta: dict[str, Any] = {}
            raw_delta = choice.get("delta")
            raw_message = choice.get("message")
            if isinstance(raw_delta, dict):
                delta = raw_delta
            elif isinstance(raw_message, dict):
                # 非流式响应：整条 message 当作唯一的 delta。
                delta = raw_message
            for name in _REASONING_FIELDS:
                reasoning = delta.get(name)
                if isinstance(reasoning, str) and reasoning:
                    chunks.append(ReasoningDelta(reasoning))
                    break
            content = delta.get("content")
            if isinstance(content, str) and content:
                chunks.append(TextDelta(content))
            refusal = delta.get("refusal")
            if isinstance(refusal, str) and refusal:
                chunks.append(TextDelta(refusal))
            calls = delta.get("tool_calls")
            if isinstance(calls, list):
                for position, raw in enumerate(calls):
                    if not isinstance(raw, dict):
                        continue
                    chunks.append(_tool_call_delta(raw, position))
            reason = choice.get("finish_reason")
            if isinstance(reason, str) and reason.strip():
                finish = reason.strip()
    usage = obj.get("usage")
    if isinstance(usage, dict):
        chunks.append(UsageChunk(parse_openai_usage(usage)))
    return chunks, finish


def _tool_call_delta(raw: dict[str, Any], position: int) -> ToolCallDelta:
    index = raw.get("index")
    if isinstance(index, bool) or not isinstance(index, int):
        index = position
    function = raw.get("function")
    if not isinstance(function, dict):
        function = {}
    call_id = raw.get("id")
    name = function.get("name")
    arguments = function.get("arguments")
    return ToolCallDelta(
        index=index,
        id=call_id if isinstance(call_id, str) and call_id else None,
        name=name if isinstance(name, str) and name else None,
        arguments=arguments if isinstance(arguments, str) else "",
    )


def _is_message_choice(choice: Any) -> bool:
    if not isinstance(choice, dict):
        return False
    message = choice.get("message", choice.get("delta"))
    if not isinstance(message, dict):
        return False
    return (
        (
            "content" in message
            and (message["content"] is None or isinstance(message["content"], str))
        )
        or isinstance(message.get("tool_calls"), list)
        or isinstance(message.get("refusal"), str)
    )


def parameter_fallback(status: int, detail: str, body: dict[str, Any]) -> dict[str, Any] | None:
    """按 4xx 响应体里的参数名决定一次性的请求参数回退；不适用时返回 None。"""
    if status not in (400, 422):
        return None
    lowered = detail.lower()
    if "stream_options" in body and "stream_options" in lowered:
        out = dict(body)
        del out["stream_options"]
        return out
    if "max_tokens" in body and "max_completion_tokens" in lowered:
        out = dict(body)
        out["max_completion_tokens"] = out.pop("max_tokens")
        return out
    return None


async def read_limited(response: httpx.Response, limit: int) -> str:
    """读取响应体，最多 ``limit`` 字节，解码为文本。"""
    buf = bytearray()
    async for chunk in response.aiter_bytes():
        buf += chunk
        if len(buf) >= limit:
            break
    return bytes(buf[:limit]).decode("utf-8", errors="replace")


class OpenAICompatibleProvider:
    """OpenAI 兼容接口的流式客户端。"""

    def __init__(
        self,
        credentials: ProviderCredentials,
        *,
        locale: str = DEFAULT_LOCALE,
        client: httpx.AsyncClient | None = None,
        logger: logging.Logger | None = None,
    ) -> None:
        if not credentials.base_url:
            raise ProviderError(tr("provider.missing_base_url", locale, kind=credentials.kind))
        self._creds = credentials
        self._locale = locale
        self._log = logger or _log
        self._client = client or httpx.AsyncClient(timeout=DEFAULT_TIMEOUT)
        self._owns_client = client is None
        self._url = chat_completions_url(credentials.base_url)

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
        from gogosling_worker.providers.messages import to_openai_messages, to_openai_tools

        body: dict[str, Any] = {
            "model": self._creds.model,
            "messages": to_openai_messages(messages, locale=self._locale, logger=self._log),
            "stream": True,
            "stream_options": {"include_usage": True},
        }
        if tools:
            body["tools"] = to_openai_tools(tools)
        if options.temperature is not None:
            body["temperature"] = options.temperature
        if options.max_output_tokens is not None:
            body["max_tokens"] = options.max_output_tokens
        return body

    def _headers(self) -> dict[str, str]:
        headers = {
            "Accept": "text/event-stream",
            "Content-Type": "application/json",
            "User-Agent": f"gogosling-worker/{__version__}",
        }
        if self._creds.api_key:
            headers["Authorization"] = f"Bearer {self._creds.api_key}"
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
        retried = False
        transient_retries = 0
        try:
            while True:
                async with stream_request(
                    self._client, self._url, body=body, headers=self._headers()
                ) as response:
                    if response.status_code >= 400:
                        detail = await read_limited(response, _ERROR_BODY_LIMIT)
                        fallback = None
                        if not retried:
                            fallback = parameter_fallback(response.status_code, detail, body)
                        if fallback is not None:
                            self._log.info(
                                "模型服务拒绝了请求参数，按响应提示回退后重试一次",
                                extra={"status": response.status_code},
                            )
                            body = fallback
                            retried = True
                            continue
                        delay = retry_delay(response, transient_retries)
                        if delay is not None:
                            # 明确的 HTTP 拒绝发生在产出任何分片之前；关闭响应后等待，可随运行取消。
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
            chunks, single_finish = parse_stream_object(
                payload, locale=self._locale, secrets=(self._creds.api_key,)
            )
            choices = payload.get("choices") if isinstance(payload, dict) else None
            if not isinstance(choices, list) or not any(_is_message_choice(c) for c in choices):
                raise ProviderError(
                    tr("provider.bad_response", self._locale, detail=self._clean(raw))
                )
            for chunk in chunks:
                yield chunk
            yield Done(single_finish)
            return

        parser = SSEParser()
        finish: str | None = None
        done = False
        try:
            async for raw_chunk in response.aiter_bytes():
                for event in parser.feed(raw_chunk):
                    if event.data.strip() == _DONE_MARKER:
                        done = True
                        break
                    for chunk in self._chunks_from_event(event):
                        if isinstance(chunk, Done):
                            finish = chunk.finish_reason or finish
                        else:
                            yield chunk
                if done:
                    break
            if not done:
                for event in parser.flush():
                    if event.data.strip() == _DONE_MARKER:
                        done = True
                        break
                    for chunk in self._chunks_from_event(event):
                        if isinstance(chunk, Done):
                            finish = chunk.finish_reason or finish
                        else:
                            yield chunk
        except SSEProtocolError as exc:
            raise ProviderError(
                tr("provider.stream_error", self._locale, detail=self._clean(str(exc)))
            ) from exc
        if not done and finish is None:
            raise ProviderError(tr("provider.incomplete_response", self._locale))
        yield Done(finish)

    def _chunks_from_event(self, event: ServerSentEvent) -> list[Chunk]:
        try:
            obj = json.loads(event.data)
        except ValueError:
            self._log.warning("跳过无法解析的 SSE 片段（%d 字节）", len(event.data))
            return []
        chunks, finish = parse_stream_object(
            obj, locale=self._locale, secrets=(self._creds.api_key,)
        )
        if finish is not None:
            chunks.append(Done(finish))
        return chunks
