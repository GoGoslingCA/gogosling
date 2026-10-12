"""默认推理循环。

一步 = 一次模型调用：系统提示 + 历史 → 流式调用供应商 → 文本增量即时转发为 ``text.delta``，
思考内容增量转发为 ``reasoning.delta``（不进入最终消息，也不回填给模型）；
若模型发起工具调用，则按顺序对每个调用发 ``tool.call.started`` → 反向请求 ``host.tool.call``
（不设超时，可被取消）→ ``tool.call.completed``，把结果作为 tool 消息回填，进入下一步；
直到某一步没有工具调用（``run.completed``）、同参连续调用达到上限，或步数耗尽（``run.failed``）。

事件顺序严格按协议 §3；``run.completed`` 携带的助手消息包含本次运行全部步骤的文本、
工具调用与结果片段（按发生顺序交错）。用量口径：每步一个 ``usage`` 事件，是**本次模型调用的增量**；
``run.completed.usage`` 是各步**累计**（守护进程按增量累加，两者一致）。

模型给出的工具调用缺少名称时（部分网关或本地模型的流式输出不带 ``function.name``），不产生
``tool_call`` 片段——否则 ``run.completed.message`` 会被下一次 ``run.start`` 的解析器拒绝，整个会话
从此无法再发消息——只在最终消息里留一条带 ``call_id`` 的错误结果，并以一条说明把问题告诉模型。

工具调用与结果按 ``call_id`` 配对；参数 JSON 不合法时
不把半截文本交给宿主，而是直接回填一条错误结果让模型重试；用量按"同一调用取最大、跨调用累加"
的口径统计，不会因为供应商重复上报而翻倍。
"""

from __future__ import annotations

import asyncio
import json
import logging
from dataclasses import dataclass, field
from typing import Any

from gogosling_worker.i18n import tr
from gogosling_worker.protocol import (
    EVENT_REASONING_DELTA,
    EVENT_RUN_CANCELLED,
    EVENT_RUN_COMPLETED,
    EVENT_RUN_FAILED,
    EVENT_RUN_STARTED,
    EVENT_TEXT_DELTA,
    EVENT_TOOL_CALL_COMPLETED,
    EVENT_TOOL_CALL_STARTED,
    EVENT_USAGE,
    ROLE_ASSISTANT,
    ROLE_SYSTEM,
    ROLE_TOOL,
    ROLE_USER,
    Message,
    Part,
    RunRequest,
    ToolCall,
    ToolResult,
    Usage,
)
from gogosling_worker.providers.base import (
    ChatOptions,
    Done,
    Provider,
    ProviderError,
    ReasoningDelta,
    TextDelta,
    ToolCallDelta,
    UsageChunk,
    excerpt,
)
from gogosling_worker.runtime.engine import Engine, Host, RunCancelled

INVALID_ARGUMENTS_KEY = "_invalid_json"
MAX_ANSWER_CONTINUATIONS = 2
# 同一工具、同一参数连续出现这么多次就停止，避免失败后原样重试把步数耗尽。
MAX_IDENTICAL_TOOL_CALLS = 3
_OUTPUT_LIMIT_REASONS = frozenset({"length", "max_tokens"})


def _tool_signature(call: ToolCall) -> str:
    """同一工具加同一参数视为同一次调用；参数顺序不同但内容相同算作重复。"""
    try:
        arguments = json.dumps(
            call.arguments, sort_keys=True, ensure_ascii=False, separators=(",", ":")
        )
    except TypeError:
        arguments = repr(call.arguments)
    return f"{call.name}\n{arguments}"


_log = logging.getLogger(__name__)


@dataclass(slots=True)
class _PartialCall:
    id: str | None = None
    name: str | None = None
    fragments: list[str] = field(default_factory=list)


@dataclass(frozen=True, slots=True)
class BuiltCalls:
    """:meth:`ToolCallAccumulator.build` 的结果。

    - ``calls``：有名称的调用，按出现顺序；参数不是合法 JSON 时附带预置的错误结果（不交给宿主）；
    - ``nameless``：缺少名称的调用，只有一条带 ``call_id`` 的错误结果，不产生 ``tool_call`` 片段。
    """

    calls: list[tuple[ToolCall, ToolResult | None]] = field(default_factory=list)
    nameless: list[ToolResult] = field(default_factory=list)

    def __bool__(self) -> bool:
        return bool(self.calls or self.nameless)


class ToolCallAccumulator:
    """按 ``index`` 拼接工具调用的流式片段。"""

    def __init__(self) -> None:
        self._calls: dict[int, _PartialCall] = {}

    def feed(self, delta: ToolCallDelta) -> None:
        entry = self._calls.setdefault(delta.index, _PartialCall())
        # id 与 name 以第一次出现为准：部分网关会在每个片段里重复完整的 name。
        if delta.id and not entry.id:
            entry.id = delta.id
        if delta.name and not entry.name:
            entry.name = delta.name
        if delta.arguments:
            entry.fragments.append(delta.arguments)

    def __len__(self) -> int:
        return len(self._calls)

    def build(self, step: int, locale: str) -> BuiltCalls:
        """生成本步的工具调用。

        参数不是合法 JSON 时给出预置错误结果，调用不会交给宿主；缺少名称的调用不生成 ``ToolCall``
        （协议要求 ``tool_call.name`` 非空），只给出一条带 ``call_id`` 的错误结果。
        """
        out = BuiltCalls()
        for index, entry in self._calls.items():
            call_id = entry.id or f"call_{step}_{index}"
            if not entry.name:
                out.nameless.append(
                    ToolResult(call_id, tr("tool.missing_name", locale, call_id=call_id), True)
                )
                continue
            name = entry.name
            text = "".join(entry.fragments).strip()
            arguments: Any
            error: ToolResult | None = None
            if not text:
                arguments = {}
            else:
                try:
                    arguments = json.loads(text)
                except ValueError:
                    arguments = {INVALID_ARGUMENTS_KEY: excerpt(text, 500)}
                    error = ToolResult(
                        call_id,
                        tr("tool.invalid_arguments", locale, excerpt=excerpt(text, 200)),
                        is_error=True,
                    )
            out.calls.append((ToolCall(id=call_id, name=name, arguments=arguments), error))
        return out


@dataclass(slots=True)
class _RunState:
    parts: list[Part] = field(default_factory=list)
    usage: Usage = Usage()
    continuations: int = 0
    last_tool_signature: str | None = None
    identical_tool_calls: int = 0


class DefaultEngine(Engine):
    """默认实现：工具回路 + 步数上限 + 可取消。"""

    def __init__(self, *, logger: logging.Logger | None = None) -> None:
        self._log = logger or _log

    async def run(self, request: RunRequest, host: Host, provider: Provider) -> None:
        host.emit(EVENT_RUN_STARTED)
        state = _RunState()
        try:
            await self._loop(request, host, provider, state)
        except RunCancelled:
            host.emit(EVENT_RUN_CANCELLED)
        except asyncio.CancelledError:
            host.emit(EVENT_RUN_CANCELLED)
            raise
        except ProviderError as exc:
            host.emit(EVENT_RUN_FAILED, error=exc.message)

    async def _loop(
        self, request: RunRequest, host: Host, provider: Provider, state: _RunState
    ) -> None:
        messages: list[Message] = []
        if request.agent.system_prompt.strip():
            messages.append(Message(ROLE_SYSTEM, (Part.of_text(request.agent.system_prompt),)))
        # 协议规定系统提示只经 agent.system_prompt 传递；
        # 历史里若混入 system 消息则忽略，避免重复注入。
        messages.extend(m for m in request.messages if m.role != ROLE_SYSTEM)
        options = ChatOptions(
            temperature=request.options.temperature,
            max_output_tokens=request.options.max_output_tokens,
        )
        for step in range(1, request.options.max_steps + 1):
            if await self._step(step, messages, request, options, host, provider, state):
                return
        host.emit(
            EVENT_RUN_FAILED,
            error=tr("run.max_steps", host.locale, max_steps=request.options.max_steps),
        )

    async def _step(
        self,
        step: int,
        messages: list[Message],
        request: RunRequest,
        options: ChatOptions,
        host: Host,
        provider: Provider,
        state: _RunState,
    ) -> bool:
        """执行一步；返回 True 表示运行已以 run.completed 结束。"""
        text_parts: list[str] = []
        calls = ToolCallAccumulator()
        step_usage: Usage | None = None
        finish_reason: str | None = None
        try:
            async for chunk in provider.stream_chat(messages, request.tools, options):
                if isinstance(chunk, TextDelta):
                    if chunk.text:
                        host.emit(EVENT_TEXT_DELTA, delta=chunk.text)
                        text_parts.append(chunk.text)
                elif isinstance(chunk, ReasoningDelta):
                    # 思考内容只用于展示：不进入最终消息，也不回填给下一步的模型调用。
                    if chunk.text:
                        host.emit(EVENT_REASONING_DELTA, delta=chunk.text)
                elif isinstance(chunk, ToolCallDelta):
                    calls.feed(chunk)
                elif isinstance(chunk, UsageChunk):
                    step_usage = (
                        chunk.usage if step_usage is None else step_usage.merge_max(chunk.usage)
                    )
                elif isinstance(chunk, Done):
                    finish_reason = chunk.finish_reason or finish_reason
                    self._log.debug(
                        "模型调用结束", extra={"step": step, "finish_reason": chunk.finish_reason}
                    )
        finally:
            if step_usage is not None:
                # 成功、流中断和取消均保留已报告的用量；每次调用只发一条增量，不推测未报告的数值。
                state.usage = state.usage.add(step_usage)
                host.emit(EVENT_USAGE, usage=step_usage)

        text = "".join(text_parts)
        assistant_parts: list[Part] = []
        if text:
            part = Part.of_text(text)
            assistant_parts.append(part)
            state.parts.append(part)

        if finish_reason in _OUTPUT_LIMIT_REASONS:
            # 长度截断可能落在工具参数中间；即使某个参数碰巧是完整 JSON，也不执行这一组调用。
            if len(calls):
                raise ProviderError(tr("run.tool_call_truncated", host.locale))
            if not text or state.continuations >= MAX_ANSWER_CONTINUATIONS:
                raise ProviderError(tr("run.output_truncated", host.locale))
            state.continuations += 1
            messages.append(Message(ROLE_ASSISTANT, tuple(assistant_parts)))
            messages.append(
                Message(ROLE_USER, (Part.of_text(tr("run.continue_answer", host.locale)),))
            )
            return False

        built = calls.build(step, host.locale)
        if not built:
            host.emit(
                EVENT_RUN_COMPLETED,
                message=Message(ROLE_ASSISTANT, tuple(state.parts)),
                usage=state.usage,
            )
            return True

        for call, _ in built.calls:
            assistant_parts.append(Part.of_tool_call(call))
        if assistant_parts:
            messages.append(Message(ROLE_ASSISTANT, tuple(assistant_parts)))

        for call, preset_error in built.calls:
            signature = _tool_signature(call)
            if signature == state.last_tool_signature:
                state.identical_tool_calls += 1
            else:
                state.last_tool_signature = signature
                state.identical_tool_calls = 1
            if state.identical_tool_calls >= MAX_IDENTICAL_TOOL_CALLS:
                raise ProviderError(
                    tr(
                        "run.tool_repeat",
                        host.locale,
                        name=call.name or "tool",
                        count=MAX_IDENTICAL_TOOL_CALLS,
                    )
                )
            state.parts.append(Part.of_tool_call(call))
            host.emit(EVENT_TOOL_CALL_STARTED, tool_call=call)
            result = preset_error if preset_error is not None else await host.call_tool(call)
            host.emit(EVENT_TOOL_CALL_COMPLETED, tool_result=result)
            result_part = Part.of_tool_result(result)
            state.parts.append(result_part)
            messages.append(Message(ROLE_TOOL, (result_part,)))

        if built.nameless:
            for result in built.nameless:
                self._log.warning(
                    "模型给出的工具调用缺少名称，未执行",
                    extra={"step": step, "call_id": result.call_id},
                )
                state.parts.append(Part.of_tool_result(result))
            # 没有对应调用的 tool 结果会被供应商拒绝（消息转换也会把它当孤儿丢弃），
            # 因此用一条用户消息把说明交给模型，让它带上工具名重试或直接作答。
            messages.append(
                Message(ROLE_USER, tuple(Part.of_text(r.content) for r in built.nameless))
            )
        return False
