from __future__ import annotations

import pytest

from gogosling_worker.protocol import (
    PART_ATTACHMENT,
    Attachment,
    Message,
    Part,
    ToolCall,
    ToolResult,
    ToolSpec,
)
from gogosling_worker.providers.messages import (
    AssistantTurn,
    SystemTurn,
    ToolTurn,
    UserTurn,
    flatten,
    to_anthropic_request,
    to_anthropic_tools,
    to_openai_messages,
    to_openai_tools,
)


def _user(text: str) -> Message:
    return Message("user", (Part.of_text(text),))


def _interleaved_assistant() -> Message:
    """run.completed 产出的最终消息：文本 → 调用 → 结果 → 文本。"""
    call = ToolCall("call_1", "read_file", {"path": "a.md"})
    return Message(
        "assistant",
        (
            Part.of_text("我看一下。"),
            Part.of_tool_call(call),
            Part.of_tool_result(ToolResult("call_1", "内容")),
            Part.of_text("看完了。"),
        ),
    )


def test_flatten_splits_interleaved_assistant_message_into_valid_turns() -> None:
    turns = flatten([_user("请读 a.md"), _interleaved_assistant()], locale="zh")
    assert turns == [
        UserTurn("请读 a.md"),
        AssistantTurn("我看一下。", [ToolCall("call_1", "read_file", {"path": "a.md"})]),
        ToolTurn([ToolResult("call_1", "内容")]),
        AssistantTurn("看完了。", []),
    ]


def test_flatten_drops_orphan_results_and_synthesizes_missing_ones() -> None:
    call = ToolCall("call_1", "read_file", {})
    history = [
        _user("开始"),
        Message("assistant", (Part.of_tool_call(call),)),
        Message("tool", (Part.of_tool_result(ToolResult("ghost", "孤儿")),)),
        _user("继续"),
    ]
    turns = flatten(history, locale="zh")
    assert turns[1] == AssistantTurn("", [call])
    tool_turn = turns[2]
    assert isinstance(tool_turn, ToolTurn)
    assert [r.call_id for r in tool_turn.results] == ["call_1"]
    assert tool_turn.results[0].is_error is True
    assert turns[3] == UserTurn("继续")


def test_flatten_synthesizes_results_for_trailing_cancelled_call() -> None:
    call = ToolCall("call_9", "run_cmd", {"cmd": "ls"})
    turns = flatten([_user("跑"), Message("assistant", (Part.of_tool_call(call),))], locale="en")
    assert isinstance(turns[-1], ToolTurn)
    assert turns[-1].results[0].call_id == "call_9"
    assert "no result" in turns[-1].results[0].content


def test_flatten_empty_user_message_gets_placeholder_and_attachments_become_hints() -> None:
    empty = Message("user", (Part.of_text("   "),))
    with_attachment = Message(
        "user",
        (
            Part.of_text("看看这个"),
            Part(PART_ATTACHMENT, attachment=Attachment("a.png", "inbound/a.png", "image/png", 12)),
        ),
    )
    turns = flatten([empty, with_attachment], locale="zh")
    assert turns[0] == UserTurn("（空消息）")
    assert isinstance(turns[1], UserTurn)
    assert (
        turns[1].text
        == '看看这个\n[attachment name="a.png" path="inbound/a.png" mime="image/png" size=12]'
    )


def test_flatten_skips_blank_system_and_empty_assistant() -> None:
    turns = flatten(
        [Message("system", (Part.of_text("  "),)), Message("assistant", ()), _user("hi")],
        locale="en",
    )
    assert turns == [UserTurn("hi")]


def test_openai_rendering_orders_tool_messages_after_calls() -> None:
    system = Message("system", (Part.of_text("你是助理"),))
    rendered = to_openai_messages(
        [system, _user("请读 a.md"), _interleaved_assistant()], locale="zh"
    )
    assert rendered == [
        {"role": "system", "content": "你是助理"},
        {"role": "user", "content": "请读 a.md"},
        {
            "role": "assistant",
            "content": "我看一下。",
            "tool_calls": [
                {
                    "id": "call_1",
                    "type": "function",
                    "function": {"name": "read_file", "arguments": '{"path": "a.md"}'},
                }
            ],
        },
        {"role": "tool", "tool_call_id": "call_1", "content": "内容"},
        {"role": "assistant", "content": "看完了。"},
    ]


def test_openai_assistant_with_only_calls_has_null_content() -> None:
    call = ToolCall("c", "t", {})
    rendered = to_openai_messages(
        [_user("x"), Message("assistant", (Part.of_tool_call(call),))], locale="zh"
    )
    assert rendered[1]["content"] is None
    assert rendered[2] == {
        "role": "tool",
        "tool_call_id": "c",
        "content": "（该工具调用没有可用的结果）",
    }


def test_anthropic_rendering_merges_roles_and_groups_tool_results() -> None:
    system = Message("system", (Part.of_text("你是助理"),))
    sys_text, rendered = to_anthropic_request(
        [system, _user("a"), _user("b"), _interleaved_assistant()], locale="zh"
    )
    assert sys_text == "你是助理"
    assert rendered == [
        {"role": "user", "content": [{"type": "text", "text": "a"}, {"type": "text", "text": "b"}]},
        {
            "role": "assistant",
            "content": [
                {"type": "text", "text": "我看一下。"},
                {
                    "type": "tool_use",
                    "id": "call_1",
                    "name": "read_file",
                    "input": {"path": "a.md"},
                },
            ],
        },
        {
            "role": "user",
            "content": [{"type": "tool_result", "tool_use_id": "call_1", "content": "内容"}],
        },
        {"role": "assistant", "content": [{"type": "text", "text": "看完了。"}]},
    ]


def test_anthropic_rendering_prepends_user_when_history_starts_with_assistant() -> None:
    _, rendered = to_anthropic_request([Message("assistant", (Part.of_text("hi"),))], locale="en")
    assert rendered[0] == {"role": "user", "content": [{"type": "text", "text": "(empty message)"}]}
    assert rendered[1]["role"] == "assistant"


def test_anthropic_error_results_and_non_object_arguments() -> None:
    call = ToolCall("c1", "t", [1, 2])
    history = [
        _user("x"),
        Message("assistant", (Part.of_tool_call(call),)),
        Message("tool", (Part.of_tool_result(ToolResult("c1", "boom", True)),)),
    ]
    _, rendered = to_anthropic_request(history, locale="zh")
    assert rendered[1]["content"][0]["input"] == {"value": [1, 2]}
    assert rendered[2]["content"][0]["is_error"] is True


def test_tool_rendering_defaults_empty_schema() -> None:
    spec = ToolSpec(name="noop", description="什么都不做")
    assert to_openai_tools([spec]) == [
        {
            "type": "function",
            "function": {
                "name": "noop",
                "description": "什么都不做",
                "parameters": {"type": "object", "properties": {}},
            },
        }
    ]
    assert to_anthropic_tools([spec]) == [
        {
            "name": "noop",
            "description": "什么都不做",
            "input_schema": {"type": "object", "properties": {}},
        }
    ]


def test_flatten_keeps_system_turn_type() -> None:
    turns = flatten([Message("system", (Part.of_text("S"),))], locale="zh")
    assert turns == [SystemTurn("S")]


@pytest.mark.parametrize("format_name", ["openai", "anthropic"])
def test_fragmented_text_preserves_tool_boundaries_and_exact_contents(format_name: str) -> None:
    render = to_openai_messages if format_name == "openai" else to_anthropic_request
    first = ToolCall("c1", "read_file", {"path": "a.md"})
    second = ToolCall("c2", "read_file", {"path": "b.md"})
    history = [
        _user("begin"),
        Message(
            "assistant",
            (
                Part.of_text("  first\n你好🙂"),
                Part.of_tool_call(first),
                Part.of_tool_result(ToolResult("c1", "result one")),
                Part.of_text("\nsecond é\t"),
                Part.of_tool_call(second),
                Part.of_tool_result(ToolResult("c2", "result two")),
                Part.of_text("tail "),
            ),
        ),
        Message("assistant", (Part.of_text("next"),)),
    ]
    fragmented = []
    for message in history:
        parts = []
        for part in message.parts:
            if part.type == "text":
                parts.append(Part.of_text(""))
                parts.extend(Part.of_text(character) for character in part.text)
                parts.append(Part.of_text(""))
            else:
                parts.append(part)
        fragmented.append(Message(message.role, tuple(parts)))
    expected = render(history, locale="en")
    assert render(fragmented, locale="en") == expected
    assert render(history, locale="en") == expected


def test_large_fragmented_assistant_text_is_not_truncated_or_retained_across_messages() -> None:
    parts = tuple(Part.of_text(f"{index}:你好🙂\n") for index in range(4096))
    turns = flatten([Message("assistant", parts), Message("assistant", ())], locale="en")
    assert turns == [AssistantTurn("".join(part.text for part in parts))]
