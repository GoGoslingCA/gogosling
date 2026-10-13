"""把 ``model.Message``（parts 形式）转换成各供应商的消息格式。

转换分两步：

1. :func:`flatten` 把 parts 展开成与供应商无关的"轮次"序列（system / user / assistant / tool），
   并修复工具调用与结果的配对：
   - 助手消息里 ``tool_call`` 与 ``tool_result`` 交错（``run.completed`` 的最终消息就是这样）时，
     拆成 "assistant(调用) → tool(结果) → assistant(后续文本)" 的合法顺序；
   - 没有对应调用的孤儿结果丢弃（并记录日志）；
   - 有调用却没有结果（例如运行中途被取消）时补一条标记为错误的结果，避免被供应商以 400 拒绝；
   - 空的用户消息用占位文案代替，附件变成模型可读的一行提示；
2. 各供应商的渲染函数只做字面翻译，不再处理顺序问题。
"""

from __future__ import annotations

import json
import logging
from collections.abc import Sequence
from dataclasses import dataclass, field
from typing import Any

from gogosling_worker.i18n import tr
from gogosling_worker.protocol import (
    PART_ATTACHMENT,
    PART_TEXT,
    PART_TOOL_CALL,
    PART_TOOL_RESULT,
    ROLE_ASSISTANT,
    ROLE_SYSTEM,
    ROLE_TOOL,
    ROLE_USER,
    Attachment,
    Message,
    ToolCall,
    ToolResult,
    ToolSpec,
)

_log = logging.getLogger(__name__)


@dataclass(slots=True)
class SystemTurn:
    text: str


@dataclass(slots=True)
class UserTurn:
    text: str


@dataclass(slots=True)
class AssistantTurn:
    text: str = ""
    calls: list[ToolCall] = field(default_factory=list)


@dataclass(slots=True)
class ToolTurn:
    results: list[ToolResult] = field(default_factory=list)


Turn = SystemTurn | UserTurn | AssistantTurn | ToolTurn


def attachment_hint(att: Attachment) -> str:
    """附件在模型上下文中的表示：一行结构化提示，不内联文件内容。"""
    fields = [f'name="{att.name}"', f'path="{att.path}"']
    if att.mime:
        fields.append(f'mime="{att.mime}"')
    fields.append(f"size={att.size}")
    return "[attachment " + " ".join(fields) + "]"


def flatten(
    messages: Sequence[Message], *, locale: str, logger: logging.Logger | None = None
) -> list[Turn]:
    """展开消息并修复工具配对，返回供应商无关的轮次序列。"""
    log = logger or _log
    turns: list[Turn] = []

    def append_results(results: list[ToolResult]) -> None:
        if not results:
            return
        last = turns[-1] if turns else None
        if isinstance(last, ToolTurn):
            last.results.extend(results)
        else:
            turns.append(ToolTurn(list(results)))

    for msg in messages:
        if msg.role == ROLE_SYSTEM:
            text = msg.text()
            if text.strip():
                turns.append(SystemTurn(text))
        elif msg.role == ROLE_USER:
            text_parts: list[str] = []
            hints: list[str] = []
            results: list[ToolResult] = []
            for part in msg.parts:
                if part.type == PART_TEXT:
                    text_parts.append(part.text)
                elif part.type == PART_ATTACHMENT and part.attachment is not None:
                    hints.append(attachment_hint(part.attachment))
                elif part.type == PART_TOOL_RESULT and part.tool_result is not None:
                    results.append(part.tool_result)
            append_results(results)
            content = "".join(text_parts)
            if hints:
                content = (content + "\n" if content else "") + "\n".join(hints)
            if not content.strip() and not results:
                content = tr("message.empty", locale)
            if content.strip():
                turns.append(UserTurn(content))
        elif msg.role == ROLE_ASSISTANT:
            current = AssistantTurn()
            fragments: list[str] = []

            def close(current: AssistantTurn, fragments: list[str]) -> AssistantTurn:
                # 一次拼接一个连续片段组，避免每个 part 都复制不断增长的正文。
                current.text = "".join(fragments)
                fragments.clear()
                if current.text or current.calls:
                    turns.append(current)
                return AssistantTurn()

            for part in msg.parts:
                if part.type == PART_TEXT:
                    if part.text:
                        fragments.append(part.text)
                elif part.type == PART_TOOL_CALL and part.tool_call is not None:
                    current.calls.append(part.tool_call)
                elif part.type == PART_TOOL_RESULT and part.tool_result is not None:
                    current = close(current, fragments)
                    append_results([part.tool_result])
            close(current, fragments)
        elif msg.role == ROLE_TOOL:
            append_results([p.tool_result for p in msg.parts if p.tool_result is not None])
        else:
            log.warning("忽略未知角色的消息", extra={"role": msg.role})

    return _repair_tool_pairing(turns, locale=locale, logger=log)


def _repair_tool_pairing(turns: list[Turn], *, locale: str, logger: logging.Logger) -> list[Turn]:
    out: list[Turn] = []
    pending: dict[str, ToolCall] = {}

    def synthesize_missing() -> None:
        if not pending:
            return
        missing = [
            ToolResult(call_id, tr("tool.result_missing", locale), is_error=True)
            for call_id in pending
        ]
        pending.clear()
        last = out[-1] if out else None
        if isinstance(last, ToolTurn):
            last.results.extend(missing)
        else:
            out.append(ToolTurn(missing))

    for turn in turns:
        if isinstance(turn, ToolTurn):
            kept: list[ToolResult] = []
            for result in turn.results:
                if result.call_id in pending:
                    pending.pop(result.call_id)
                    kept.append(result)
                else:
                    logger.warning("丢弃没有对应调用的工具结果", extra={"call_id": result.call_id})
            if kept:
                out.append(ToolTurn(kept))
            synthesize_missing()
            continue
        synthesize_missing()
        if isinstance(turn, AssistantTurn):
            pending = {call.id: call for call in turn.calls}
        out.append(turn)
    synthesize_missing()
    return out


# ---- OpenAI Chat Completions ----


def _arguments_text(call: ToolCall) -> str:
    arguments = call.arguments if call.arguments is not None else {}
    return json.dumps(arguments, ensure_ascii=False)


def to_openai_messages(
    messages: Sequence[Message], *, locale: str, logger: logging.Logger | None = None
) -> list[dict[str, Any]]:
    """渲染为 ``/chat/completions`` 的 ``messages``。"""
    out: list[dict[str, Any]] = []
    for turn in flatten(messages, locale=locale, logger=logger):
        if isinstance(turn, SystemTurn):
            out.append({"role": "system", "content": turn.text})
        elif isinstance(turn, UserTurn):
            out.append({"role": "user", "content": turn.text})
        elif isinstance(turn, AssistantTurn):
            item: dict[str, Any] = {
                "role": "assistant",
                "content": turn.text if turn.text else None,
            }
            if turn.calls:
                item["tool_calls"] = [
                    {
                        "id": call.id,
                        "type": "function",
                        "function": {"name": call.name, "arguments": _arguments_text(call)},
                    }
                    for call in turn.calls
                ]
            out.append(item)
        else:
            for result in turn.results:
                out.append(
                    {"role": "tool", "tool_call_id": result.call_id, "content": result.content}
                )
    return out


def to_openai_tools(tools: Sequence[ToolSpec]) -> list[dict[str, Any]]:
    """渲染为 OpenAI 的 ``tools`` 数组。"""
    return [
        {
            "type": "function",
            "function": {
                "name": tool.name,
                "description": tool.description,
                "parameters": tool.input_schema or {"type": "object", "properties": {}},
            },
        }
        for tool in tools
    ]


# ---- Anthropic Messages ----


def _tool_use_input(call: ToolCall) -> dict[str, Any]:
    # Anthropic 要求 input 是对象；非对象的参数包一层。
    if isinstance(call.arguments, dict):
        return call.arguments
    if call.arguments is None:
        return {}
    return {"value": call.arguments}


def to_anthropic_request(
    messages: Sequence[Message], *, locale: str, logger: logging.Logger | None = None
) -> tuple[str | None, list[dict[str, Any]]]:
    """渲染为 ``/v1/messages`` 的 ``(system, messages)``。

    连续同角色的消息会合并（Anthropic 要求角色交替），工具结果放在紧随其后的 user 消息里。
    """
    system_texts: list[str] = []
    rendered: list[dict[str, Any]] = []

    def push(role: str, blocks: list[dict[str, Any]]) -> None:
        if not blocks:
            return
        if rendered and rendered[-1]["role"] == role:
            rendered[-1]["content"].extend(blocks)
        else:
            rendered.append({"role": role, "content": blocks})

    for turn in flatten(messages, locale=locale, logger=logger):
        if isinstance(turn, SystemTurn):
            system_texts.append(turn.text)
        elif isinstance(turn, UserTurn):
            push("user", [{"type": "text", "text": turn.text}])
        elif isinstance(turn, AssistantTurn):
            blocks: list[dict[str, Any]] = []
            if turn.text.strip():
                blocks.append({"type": "text", "text": turn.text})
            for call in turn.calls:
                blocks.append(
                    {
                        "type": "tool_use",
                        "id": call.id,
                        "name": call.name,
                        "input": _tool_use_input(call),
                    }
                )
            push("assistant", blocks)
        else:
            results: list[dict[str, Any]] = []
            for result in turn.results:
                block: dict[str, Any] = {
                    "type": "tool_result",
                    "tool_use_id": result.call_id,
                    "content": result.content,
                }
                if result.is_error:
                    block["is_error"] = True
                results.append(block)
            push("user", results)

    if rendered and rendered[0]["role"] != "user":
        # Anthropic 要求第一条消息是 user；历史以助手消息开头时补一个占位。
        rendered.insert(
            0, {"role": "user", "content": [{"type": "text", "text": tr("message.empty", locale)}]}
        )
    system = "\n\n".join(system_texts) if system_texts else None
    return system, rendered


def to_anthropic_tools(tools: Sequence[ToolSpec]) -> list[dict[str, Any]]:
    """渲染为 Anthropic 的 ``tools`` 数组。"""
    return [
        {
            "name": tool.name,
            "description": tool.description,
            "input_schema": tool.input_schema or {"type": "object", "properties": {}},
        }
        for tool in tools
    ]
