"""模型供应商适配。

- :mod:`base`：推理循环依赖的协议与分片类型；
- :mod:`messages`：``model.Message`` → 各供应商消息格式；
- :mod:`sse`：Server-Sent Events 解析；
- :mod:`openai_compatible`：OpenAI 及兼容接口（Ollama 的 ``/v1`` 也走它）；
- :mod:`anthropic`：Anthropic Messages。

:func:`build_provider` 按 ``ProviderCredentials.kind`` 选择实现；未知类型抛 :class:`ProviderError`。

待实现：
- 提示缓存友好的请求构造（Anthropic 的 ``cache_control`` 标记）；
- 多模态输入的转发（协议尚无对应片段类型）；
- 开启 Anthropic 扩展思考时在工具回路中回传带签名的 thinking 块（当前请求不开启思考，
  供应商主动返回的思考内容只作为 ``reasoning.delta`` 事件转发，不回填给模型）。

边界：供应商配置与密钥由守护进程随 ``run.start`` 下发，本包不读取配置文件，也不持久化密钥。
"""

from __future__ import annotations

import logging
from dataclasses import replace

import httpx

from gogosling_worker.i18n import DEFAULT_LOCALE, tr
from gogosling_worker.protocol import (
    PROVIDER_ANTHROPIC,
    PROVIDER_OLLAMA,
    PROVIDER_OPENAI_COMPATIBLE,
    ProviderCredentials,
)
from gogosling_worker.providers.anthropic import AnthropicProvider
from gogosling_worker.providers.base import (
    ChatOptions,
    Chunk,
    Done,
    Provider,
    ProviderError,
    ReasoningDelta,
    TextDelta,
    ToolCallDelta,
    UsageChunk,
)
from gogosling_worker.providers.openai_compatible import OpenAICompatibleProvider

OLLAMA_DEFAULT_BASE_URL = "http://127.0.0.1:11434"


def ollama_credentials(credentials: ProviderCredentials) -> ProviderCredentials:
    """把 Ollama 的根地址（``http://host:11434``）映射到其 OpenAI 兼容前缀 ``/v1``。"""
    base = (credentials.base_url or OLLAMA_DEFAULT_BASE_URL).rstrip("/")
    if not base.endswith("/v1"):
        base += "/v1"
    return replace(credentials, base_url=base)


def build_provider(
    credentials: ProviderCredentials,
    *,
    locale: str = DEFAULT_LOCALE,
    client: httpx.AsyncClient | None = None,
    logger: logging.Logger | None = None,
) -> Provider:
    """按凭据类型构造供应商客户端。"""
    if credentials.kind == PROVIDER_OPENAI_COMPATIBLE:
        return OpenAICompatibleProvider(credentials, locale=locale, client=client, logger=logger)
    if credentials.kind == PROVIDER_ANTHROPIC:
        return AnthropicProvider(credentials, locale=locale, client=client, logger=logger)
    if credentials.kind == PROVIDER_OLLAMA:
        return OpenAICompatibleProvider(
            ollama_credentials(credentials), locale=locale, client=client, logger=logger
        )
    raise ProviderError(tr("provider.unsupported_kind", locale, kind=credentials.kind))


__all__ = [
    "OLLAMA_DEFAULT_BASE_URL",
    "AnthropicProvider",
    "ChatOptions",
    "Chunk",
    "Done",
    "OpenAICompatibleProvider",
    "Provider",
    "ProviderError",
    "ReasoningDelta",
    "TextDelta",
    "ToolCallDelta",
    "UsageChunk",
    "build_provider",
    "ollama_credentials",
]
