from __future__ import annotations

import asyncio

import pytest

from gogosling_worker.i18n import tr
from gogosling_worker.protocol import (
    Message,
    Part,
    ToolCall,
    ToolResult,
    ToolSpec,
    Usage,
    parse_run_request,
    run_event,
)
from gogosling_worker.providers.base import (
    Chunk,
    Done,
    ProviderError,
    ReasoningDelta,
    TextDelta,
    ToolCallDelta,
    UsageChunk,
)
from gogosling_worker.runtime import DefaultEngine
from gogosling_worker.runtime.engine import RunCancelled
from gogosling_worker.runtime.loop import INVALID_ARGUMENTS_KEY, ToolCallAccumulator
from tests.harness import WAIT, FakeHost, FakeProvider, fixed_clock, make_request, run_start_params

READ_FILE = ToolSpec(name="read_file", description="读文件", input_schema={"type": "object"})
USER = Message("user", (Part.of_text("请读 notes.md"),))


def _tool_step() -> list[TextDelta | ToolCallDelta | UsageChunk | Done]:
    return [
        TextDelta("我来"),
        TextDelta("看看。"),
        ToolCallDelta(index=0, id="call_1", name="read_file", arguments='{"pa'),
        ToolCallDelta(index=0, arguments='th": "notes.md"}'),
        UsageChunk(Usage(input_tokens=10, output_tokens=5)),
        Done("tool_calls"),
    ]


def _final_step() -> list[TextDelta | UsageChunk | Done]:
    return [
        TextDelta("内容是：ok:read_file"),
        UsageChunk(Usage(input_tokens=20, output_tokens=7, cache_read_tokens=4)),
        Done("stop"),
    ]


def _identical_read(call_id: str) -> list[ToolCallDelta | Done]:
    return [
        ToolCallDelta(index=0, id=call_id, name="read_file", arguments='{"path": "notes.md"}'),
        Done("tool_calls"),
    ]


async def test_identical_tool_calls_stop_before_the_third_execution() -> None:
    provider = FakeProvider(
        [_identical_read("call_a"), _identical_read("call_b"), _identical_read("call_c")]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)

    assert [call.name for call in host.tool_calls] == ["read_file", "read_file"]
    assert len(provider.calls) == 3
    assert host.events[-1]["type"] == "run.failed"
    assert "read_file" in str(host.events[-1]["error"])


async def test_plain_completion_event_order_and_message() -> None:
    provider = FakeProvider(
        [[TextDelta("你好"), TextDelta("，世界"), UsageChunk(Usage(3, 2)), Done("stop")]]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)

    assert host.types() == ["run.started", "text.delta", "text.delta", "usage", "run.completed"]
    completed = host.events[-1]
    assert completed["message"] == Message("assistant", (Part.of_text("你好，世界"),))
    assert completed["usage"] == Usage(3, 2)
    # 系统提示在历史之前。
    sent_messages = provider.calls[0][0]
    assert sent_messages[0].role == "system" and sent_messages[0].text() == "你是助理"
    assert sent_messages[1] == USER


@pytest.mark.parametrize("locale", ["zh", "en", "fr"])
@pytest.mark.parametrize(
    "chunks",
    [
        [],
        [Done(None)],
        [UsageChunk(Usage(8, 2)), Done("stop")],
        [ReasoningDelta("thinking without an answer"), Done("stop")],
        [TextDelta(" \n\t\u3000"), Done("stop")],
        [TextDelta(" \n"), Done("length")],
    ],
    ids=["no-chunks", "done-only", "usage-only", "reasoning-only", "whitespace", "blank-limit"],
)
async def test_empty_response_fails_without_retry(chunks: list[Chunk], locale: str) -> None:
    provider = FakeProvider([chunks])
    host = FakeHost(locale=locale)
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)

    assert host.events[-1] == {
        "type": "run.failed",
        "error": tr("provider.empty_response", locale),
    }
    assert host.types().count("run.failed") == 1
    assert "run.completed" not in host.types()
    assert len(provider.calls) == 1
    assert host.tool_calls == []
    expected_usage = [chunk.usage for chunk in chunks if isinstance(chunk, UsageChunk)]
    assert [event["usage"] for event in host.events if event["type"] == "usage"] == expected_usage


async def test_empty_final_response_keeps_tool_results_and_reported_usage() -> None:
    provider = FakeProvider(
        [
            [
                ToolCallDelta(index=0, id="call_1", name="read_file", arguments="{}"),
                UsageChunk(Usage(4, 1)),
                Done("tool_calls"),
            ],
            [
                ReasoningDelta("no final answer"),
                UsageChunk(Usage(8, 1, cache_read_tokens=2)),
                UsageChunk(Usage(8, 3, cache_write_tokens=1)),
                Done("stop"),
            ],
        ]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)

    assert len(provider.calls) == 2
    assert host.tool_calls == [ToolCall("call_1", "read_file", {})]
    assert [
        event["tool_result"] for event in host.events if event["type"] == "tool.call.completed"
    ] == [ToolResult("call_1", "ok:read_file")]
    assert [event["usage"] for event in host.events if event["type"] == "usage"] == [
        Usage(4, 1),
        Usage(8, 3, cache_read_tokens=2, cache_write_tokens=1),
    ]
    assert host.types()[-2:] == ["usage", "run.failed"]
    assert host.events[-1]["error"] == tr("provider.empty_response")
    assert "run.completed" not in host.types()


async def test_refusal_text_remains_a_normal_model_answer() -> None:
    answer = "I cannot comply with that request."
    provider = FakeProvider([[TextDelta(answer), Done("stop")]])
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)

    assert host.types() == ["run.started", "text.delta", "run.completed"]
    assert host.events[-1]["message"].text() == answer


async def test_history_system_messages_are_ignored() -> None:
    # 协议规定系统提示只经 agent.system_prompt 传递；历史里混入的 system 消息不得重复注入。
    provider = FakeProvider([[TextDelta("好"), Done("stop")]])
    stray = Message("system", (Part.of_text("历史里的系统消息"),))
    await DefaultEngine().run(make_request(messages=[stray, USER]), FakeHost(), provider)

    sent_messages = provider.calls[0][0]
    assert [m.role for m in sent_messages] == ["system", "user"]
    assert sent_messages[0].text() == "你是助理"


async def test_tool_loop_round_trip() -> None:
    provider = FakeProvider([_tool_step(), _final_step()])
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)

    assert host.types() == [
        "run.started",
        "text.delta",
        "text.delta",
        "usage",
        "tool.call.started",
        "tool.call.completed",
        "text.delta",
        "usage",
        "run.completed",
    ]
    call = ToolCall("call_1", "read_file", {"path": "notes.md"})
    assert host.tool_calls == [call]
    assert host.events[4]["tool_call"] == call
    assert host.events[5]["tool_result"] == ToolResult("call_1", "ok:read_file")

    completed = host.events[-1]
    assert completed["usage"] == Usage(input_tokens=30, output_tokens=12, cache_read_tokens=4)
    assert completed["message"].parts == (
        Part.of_text("我来看看。"),
        Part.of_tool_call(call),
        Part.of_tool_result(ToolResult("call_1", "ok:read_file")),
        Part.of_text("内容是：ok:read_file"),
    )
    # 第二次模型调用的历史：assistant(文本+调用) → tool(结果)。
    second_history = provider.calls[1][0]
    assert second_history[-2].role == "assistant"
    assert second_history[-2].parts == (Part.of_text("我来看看。"), Part.of_tool_call(call))
    assert second_history[-1].role == "tool"
    assert second_history[-1].parts[0].tool_result == ToolResult("call_1", "ok:read_file")


async def test_max_steps_exhaustion_fails_run() -> None:
    provider = FakeProvider([_tool_step(), _tool_step(), _tool_step()])
    host = FakeHost()
    await DefaultEngine().run(
        make_request(messages=[USER], tools=[READ_FILE], max_steps=2), host, provider
    )

    assert host.types()[-1] == "run.failed"
    assert "2" in host.events[-1]["error"]
    assert len(host.tool_calls) == 2
    assert len(provider.calls) == 2


async def test_invalid_tool_arguments_are_not_sent_to_host() -> None:
    provider = FakeProvider(
        [
            [
                ToolCallDelta(index=0, id="c1", name="read_file", arguments='{"path": '),
                Done("tool_calls"),
            ],
            [TextDelta("好的"), Done("stop")],
        ]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)

    assert host.tool_calls == []
    started = host.events[1]
    assert started["type"] == "tool.call.started"
    assert started["tool_call"].arguments == {INVALID_ARGUMENTS_KEY: '{"path":'}
    completed = host.events[2]
    assert completed["tool_result"].is_error is True
    assert "JSON" in completed["tool_result"].content
    assert host.types()[-1] == "run.completed"


async def test_provider_error_becomes_run_failed() -> None:
    provider = FakeProvider([[TextDelta("开始"), ProviderError("模型服务返回 HTTP 500：boom")]])
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)
    assert host.types() == ["run.started", "text.delta", "run.failed"]
    assert host.events[-1]["error"] == "模型服务返回 HTTP 500：boom"


async def test_failed_call_reports_merged_usage_once_before_terminal() -> None:
    provider = FakeProvider(
        [
            _tool_step(),
            [
                TextDelta("正在生成"),
                UsageChunk(Usage(20, 1, cache_read_tokens=2)),
                UsageChunk(Usage(20, 4, cache_write_tokens=3)),
                ProviderError("stream interrupted"),
            ],
        ]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)
    assert [e["usage"] for e in host.events if e["type"] == "usage"] == [
        Usage(10, 5),
        Usage(20, 4, cache_read_tokens=2, cache_write_tokens=3),
    ]
    assert host.types()[-2:] == ["usage", "run.failed"]
    assert host.types().count("run.failed") == 1
    assert "run.completed" not in host.types()


async def test_cancelled_stream_preserves_reported_usage_once() -> None:
    reported = Usage(8, 3, cache_read_tokens=2)
    provider = FakeProvider([[UsageChunk(Usage(8, 1)), UsageChunk(reported)]])
    provider.block = asyncio.Event()
    host = FakeHost()
    task = asyncio.create_task(DefaultEngine().run(make_request(messages=[USER]), host, provider))
    await asyncio.wait_for(provider.started.wait(), WAIT)
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    assert host.types() == ["run.started", "usage", "run.cancelled"]
    assert host.events[1]["usage"] == reported
    assert provider.interrupted


async def test_call_failure_without_usage_does_not_invent_an_increment() -> None:
    provider = FakeProvider([[ProviderError("failed before usage")]])
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)
    assert host.types() == ["run.started", "run.failed"]


async def test_cancel_during_stream_stops_immediately() -> None:
    provider = FakeProvider([[TextDelta("一")]])
    provider.block = asyncio.Event()
    host = FakeHost()
    task = asyncio.create_task(DefaultEngine().run(make_request(messages=[USER]), host, provider))
    await asyncio.wait_for(provider.started.wait(), WAIT)
    await asyncio.sleep(0.01)
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    assert provider.interrupted
    assert host.types() == ["run.started", "text.delta", "run.cancelled"]


async def test_cancel_while_waiting_for_tool_result() -> None:
    provider = FakeProvider([_tool_step()])
    host = FakeHost()
    host.tool_block = asyncio.Event()
    task = asyncio.create_task(
        DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)
    )
    await asyncio.wait_for(host.tool_started.wait(), WAIT)
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    assert host.tool_cancelled
    assert host.types()[-1] == "run.cancelled"
    assert host.types().count("run.cancelled") == 1


async def test_host_reports_run_cancelled_via_tool_call() -> None:
    provider = FakeProvider([_tool_step()])
    host = FakeHost({"read_file": RunCancelled("cancelled by daemon")})
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)
    assert host.types()[-1] == "run.cancelled"
    assert "tool.call.completed" not in host.types()


async def test_tool_error_result_is_fed_back_and_loop_continues() -> None:
    provider = FakeProvider([_tool_step(), _final_step()])
    host = FakeHost({"read_file": ToolResult("x", "权限被拒绝", True)})
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)
    assert host.events[5]["tool_result"] == ToolResult("call_1", "权限被拒绝", True)
    assert host.types()[-1] == "run.completed"


async def test_multiple_tool_calls_in_one_step_run_in_order() -> None:
    provider = FakeProvider(
        [
            [
                ToolCallDelta(index=1, id="b", name="read_file", arguments='{"path": "b"}'),
                ToolCallDelta(index=0, id="a", name="read_file", arguments='{"path": "a"}'),
                Done("tool_calls"),
            ],
            [TextDelta("完成"), Done("stop")],
        ]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)
    assert [c.id for c in host.tool_calls] == ["b", "a"]
    assert host.types()[-1] == "run.completed"
    assert host.events[-1]["message"].parts[0].tool_call is not None


def test_accumulator_synthesizes_ids_and_handles_empty_arguments() -> None:
    acc = ToolCallAccumulator()
    acc.feed(ToolCallDelta(index=0, name="noop"))
    acc.feed(ToolCallDelta(index=0, name="noop"))  # 重复的 name 不会拼接
    acc.feed(ToolCallDelta(index=1, id="x", name="read_file", arguments=""))
    built = acc.build(3, "zh")
    assert built.calls[0][0] == ToolCall("call_3_0", "noop", {})
    assert built.calls[0][1] is None
    assert built.calls[1][0] == ToolCall("x", "read_file", {})
    assert built.nameless == []


def test_accumulator_never_builds_nameless_tool_calls() -> None:
    acc = ToolCallAccumulator()
    acc.feed(ToolCallDelta(index=0, id="x", arguments="{}"))
    acc.feed(ToolCallDelta(index=1, arguments='{"a": 1}'))
    acc.feed(ToolCallDelta(index=2, id="ok", name="read_file", arguments="{}"))
    built = acc.build(1, "en")
    # 只有带名称的调用会成为 ToolCall；缺名称的只留下一条带 call_id 的错误结果。
    assert [call.name for call, _ in built.calls] == ["read_file"]
    assert [r.call_id for r in built.nameless] == ["x", "call_1_1"]
    assert all(r.is_error and "without a name" in r.content for r in built.nameless)


def _completed_message_json(host: FakeHost) -> dict[str, object]:
    """按工作进程实际发出的形状序列化 run.completed.message（守护进程原样落库）。"""
    completed = host.events[-1]
    assert completed["type"] == "run.completed"
    event = run_event(
        "run.completed",
        run_id="run_1",
        session_id="sess_1",
        agent_id="agent_1",
        at=fixed_clock(),
        message=completed["message"],
        usage=completed["usage"],
    )
    message = event["message"]
    assert isinstance(message, dict)
    return message


async def test_nameless_tool_call_is_not_emitted_and_model_is_told() -> None:
    provider = FakeProvider(
        [
            [
                TextDelta("我来调用"),
                ToolCallDelta(index=0, id="call_x", arguments='{"a": 1}'),
                Done("tool_calls"),
            ],
            [TextDelta("好的，直接回答。"), Done("stop")],
        ]
    )
    host = FakeHost(locale="zh")
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)

    assert host.tool_calls == []
    assert "tool.call.started" not in host.types()
    assert host.types()[-1] == "run.completed"
    parts = host.events[-1]["message"].parts
    assert all(p.tool_call is None for p in parts)
    results = [p.tool_result for p in parts if p.tool_result is not None]
    assert len(results) == 1 and results[0].call_id == "call_x" and results[0].is_error
    assert "缺少名称" in results[0].content
    # 第二次模型调用能看到说明（以用户消息的形式，因为没有对应调用的 tool 结果会被供应商拒绝）。
    second = provider.calls[1][0]
    assert second[-1].role == "user" and "缺少名称" in second[-1].text()


async def test_completed_message_round_trips_through_run_start_parser() -> None:
    """协议 §3：工作进程发出的 run.completed.message 必须能被自己的 run.start 解析器接受。"""
    scenarios = [
        # 无名工具调用（审查 C1 的触发条件）
        [
            [ToolCallDelta(index=0, id="call_x", arguments="{}"), Done("tool_calls")],
            [TextDelta("完成"), Done("stop")],
        ],
        # 正常工具回路
        [_tool_step(), _final_step()],
        # 参数不是合法 JSON
        [
            [
                ToolCallDelta(index=0, id="c1", name="read_file", arguments='{"x'),
                Done("tool_calls"),
            ],
            [TextDelta("好"), Done("stop")],
        ],
        # 有名与无名混在同一步
        [
            [
                ToolCallDelta(index=0, name="read_file", arguments='{"path": "a"}'),
                ToolCallDelta(index=1, arguments="{}"),
                Done("tool_calls"),
            ],
            [TextDelta("完成"), Done("stop")],
        ],
    ]
    for script in scenarios:
        host = FakeHost()
        await DefaultEngine().run(
            make_request(messages=[USER], tools=[READ_FILE]), host, FakeProvider(script)
        )
        message = _completed_message_json(host)
        user = {"role": "user", "parts": [{"type": "text", "text": "下一条"}]}
        params = run_start_params(
            messages=[{"role": "user", "parts": [{"type": "text", "text": "你好"}]}, message, user]
        )
        request = parse_run_request(params)
        assert len(request.messages) == 3


def test_history_with_nameless_tool_call_is_parsed_leniently() -> None:
    """旧版本写进库里的无名工具调用不得让后续 run.start 以 -32602 失败。"""
    broken = {
        "role": "assistant",
        "parts": [
            {"type": "text", "text": "调用"},
            {"type": "tool_call", "tool_call": {"id": "call_x", "name": "", "arguments": {}}},
            {"type": "tool_call", "tool_call": {"id": "", "name": "read_file"}},
            {"type": "tool_result", "tool_result": {"call_id": "call_x", "content": "err"}},
            {"type": "tool_result", "tool_result": {"content": "no id"}},
        ],
    }
    request = parse_run_request(run_start_params(messages=[broken]))
    parts = request.messages[0].parts
    assert [p.type for p in parts] == ["text", "tool_result"]
    assert parts[1].tool_result == ToolResult("call_x", "err")


async def test_usage_events_are_per_call_increments_and_completed_is_cumulative() -> None:
    step1 = Usage(input_tokens=10, output_tokens=5, cache_read_tokens=2, cache_write_tokens=3)
    step2 = Usage(input_tokens=20, output_tokens=7, cache_read_tokens=4)
    provider = FakeProvider(
        [
            [
                ToolCallDelta(index=0, id="c", name="read_file", arguments="{}"),
                # 同一调用内多次上报（累计值）按字段取最大，不重复计数。
                UsageChunk(Usage(input_tokens=10, output_tokens=1)),
                UsageChunk(step1),
                Done("tool_calls"),
            ],
            [TextDelta("好"), UsageChunk(step2), Done("stop")],
        ]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)

    usage_events = [e["usage"] for e in host.events if e["type"] == "usage"]
    assert usage_events == [step1, step2]
    total = host.events[-1]["usage"]
    assert total == step1.add(step2)
    assert total.cache_write_tokens == 3
    assert total.to_json()["cache_write_tokens"] == 3


async def test_reasoning_is_streamed_but_not_part_of_final_message() -> None:
    provider = FakeProvider(
        [[ReasoningDelta("先想"), ReasoningDelta("一想"), TextDelta("答案"), Done("stop")]]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)

    assert host.types() == [
        "run.started",
        "reasoning.delta",
        "reasoning.delta",
        "text.delta",
        "run.completed",
    ]
    assert [e["delta"] for e in host.events if e["type"] == "reasoning.delta"] == ["先想", "一想"]
    assert host.events[-1]["message"] == Message("assistant", (Part.of_text("答案"),))


@pytest.mark.parametrize("reason", ["length", "max_tokens"])
async def test_output_limit_continues_text_and_accumulates_usage(reason: str) -> None:
    provider = FakeProvider(
        [
            [TextDelta("前半段"), UsageChunk(Usage(3, 2)), Done(reason)],
            [TextDelta("后半段"), UsageChunk(Usage(6, 2)), Done("stop")],
        ]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)
    assert host.types().count("run.completed") == 1
    assert host.events[-1]["message"].text() == "前半段后半段"
    assert host.events[-1]["usage"] == Usage(9, 4)
    assert provider.calls[1][0][-2] == Message("assistant", (Part.of_text("前半段"),))
    assert provider.calls[1][0][-1].role == "user"


async def test_repeated_output_limit_is_bounded_and_fails_without_false_completion() -> None:
    provider = FakeProvider([[TextDelta("一段"), Done("length")]] * 4)
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER]), host, provider)
    assert host.types()[-1] == "run.failed"
    assert len(provider.calls) == 3
    assert "run.completed" not in host.types()


@pytest.mark.parametrize("arguments", ['{"path":', '{"path":"x"}'])
async def test_length_limited_tool_calls_are_not_executed(arguments: str) -> None:
    provider = FakeProvider(
        [[ToolCallDelta(index=0, id="c", name="read_file", arguments=arguments), Done("length")]]
    )
    host = FakeHost()
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)
    assert host.tool_calls == []
    assert host.types() == ["run.started", "run.failed"]


async def test_truncated_tool_result_survives_events_completion_and_history_round_trip() -> None:
    result = ToolResult("call_1", "partial output", truncated=True)
    host = FakeHost({"read_file": result})
    provider = FakeProvider([_tool_step(), _final_step()])
    await DefaultEngine().run(make_request(messages=[USER], tools=[READ_FILE]), host, provider)
    assert host.events[5]["tool_result"] == result
    assert provider.calls[1][0][-1].parts[0].tool_result == result
    message = _completed_message_json(host)
    request = parse_run_request(run_start_params(messages=[message]))
    results = [p.tool_result for p in request.messages[0].parts if p.tool_result is not None]
    assert results == [result]
    assert results[0].to_json()["truncated"] is True
