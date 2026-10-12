"""供应商适配的公共类型：流式分片、推理选项、错误与脱敏工具。

推理循环只依赖这里的 :class:`Provider` 协议与五种 :data:`Chunk`，不知道具体供应商；
每个适配器把自家的流式格式翻译成这五种分片。
"""

from __future__ import annotations

import json
import re
from collections.abc import AsyncIterator, Iterable, Sequence
from dataclasses import dataclass
from typing import Protocol

from gogosling_worker.protocol import Message, ToolSpec, Usage
from gogosling_worker.rpc import PROVIDER_FAILED, JsonRpcError


@dataclass(frozen=True, slots=True)
class TextDelta:
    """一段增量文本。"""

    text: str


@dataclass(frozen=True, slots=True)
class ReasoningDelta:
    """一段模型思考内容。

    来源：OpenAI 兼容服务的 ``reasoning_content`` / ``reasoning``、Anthropic 的 ``thinking_delta``。

    推理循环把它原样作为 ``reasoning.delta`` 事件发出，不进入最终助手消息，也不回填给模型。
    """

    text: str


@dataclass(frozen=True, slots=True)
class ToolCallDelta:
    """工具调用的一个增量片段。

    同一次调用的片段共享 ``index``；``id`` 与 ``name`` 通常只在第一个片段出现，
    ``arguments`` 是参数 JSON 文本的一个片段，由推理循环按 ``index`` 拼接。
    """

    index: int
    id: str | None = None
    name: str | None = None
    arguments: str = ""


@dataclass(frozen=True, slots=True)
class UsageChunk:
    """一次模型调用的用量；同一调用内可能上报多次，按字段取最大值合并。"""

    usage: Usage


@dataclass(frozen=True, slots=True)
class Done:
    """流结束；``finish_reason`` 是供应商给出的结束原因（stop / tool_calls / length …）。"""

    finish_reason: str | None = None


Chunk = TextDelta | ReasoningDelta | ToolCallDelta | UsageChunk | Done


@dataclass(frozen=True, slots=True)
class ChatOptions:
    """一次模型调用的采样参数；``None`` 表示不发送该参数。"""

    temperature: float | None = None
    max_output_tokens: int | None = None


class ProviderError(Exception):
    """供应商调用失败。

    ``message`` 已脱敏（不含密钥、不含完整响应体），可直接作为 ``run.failed.error``
    与 ``-32005`` 的 ``data.provider_error``。
    """

    def __init__(self, message: str, *, status: int | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.status = status

    def to_rpc_error(self) -> JsonRpcError:
        """映射为协议 §5 的 ``-32005``。"""
        return JsonRpcError(PROVIDER_FAILED, self.message, {"provider_error": self.message})


class Provider(Protocol):
    """模型供应商客户端。实现必须把取消（``CancelledError``）传播到网络层，立即关闭流。"""

    def stream_chat(
        self,
        messages: Sequence[Message],
        tools: Sequence[ToolSpec],
        options: ChatOptions,
    ) -> AsyncIterator[Chunk]: ...

    async def aclose(self) -> None: ...


def token_int(value: object) -> int:
    """把供应商上报的 token 数转成非负整数；畸形值（None、bool、字符串垃圾）一律按 0。"""
    if value is None or isinstance(value, bool):
        return 0
    if isinstance(value, int):
        return value if value > 0 else 0
    if isinstance(value, float) and value.is_integer():
        return int(value) if value > 0 else 0
    if isinstance(value, str) and value.strip().isdigit():
        return int(value.strip())
    return 0


_BEARER_RE = re.compile(r"(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}")
_KEY_LIKE_RE = re.compile(r"\b(?:sk|key|token)[-_][A-Za-z0-9._-]{12,}")
_WS_RE = re.compile(r"\s+")


def redact(text: str, secrets: Iterable[str] = ()) -> str:
    """把密钥替换为 ``***``：已知密钥按原文替换，再兜底处理 Bearer 头与常见密钥形状。"""
    for secret in secrets:
        if secret:
            # 服务错误可能以 JSON 形式回显密钥；同时清除原文和转义形式，且先脱敏再截断。
            forms = {secret, json.dumps(secret)[1:-1], json.dumps(secret, ensure_ascii=False)[1:-1]}
            for form in sorted(forms, key=len, reverse=True):
                text = text.replace(form, "***")
    text = _BEARER_RE.sub("Bearer ***", text)
    return _KEY_LIKE_RE.sub("***", text)


def excerpt(text: str, limit: int = 200) -> str:
    """压缩空白并截断，用于错误摘要。"""
    collapsed = _WS_RE.sub(" ", text).strip()
    if len(collapsed) <= limit:
        return collapsed
    return collapsed[: limit - 1] + "…"
