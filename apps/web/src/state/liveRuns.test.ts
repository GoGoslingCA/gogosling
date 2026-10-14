import { describe, expect, it } from "vitest";
import type { ApiEvent, RunEvent, RunEventType } from "../api/types";
import { addUsage, initialLiveRuns, liveRunsReducer, selectActiveRun, selectSessionRuns, type LiveRunsState } from "./liveRuns";

let seqCounter = 0;

function ev(type: RunEventType, runId: string, extra: Partial<RunEvent> = {}, seq?: number): ApiEvent {
  const s = seq ?? ++seqCounter;
  const payload: RunEvent = { type, run_id: runId, session_id: extra.session_id ?? "s1", agent_id: "a1", at: `2026-10-10T00:00:${String(s).padStart(2, "0")}Z`, ...extra };
  return { seq: s, type, at: payload.at, run_id: runId, session_id: payload.session_id, payload };
}

function reduce(events: ApiEvent[], start: LiveRunsState = initialLiveRuns): LiveRunsState {
  return events.reduce((state, e) => liveRunsReducer(state, { type: "event", event: e }), start);
}

describe("liveRunsReducer", () => {
  it("按 run_id 聚合 text.delta，交错的两个运行互不影响", () => {
    const state = reduce([
      ev("run.started", "r1"),
      ev("run.started", "r2", { session_id: "s2" }),
      ev("text.delta", "r1", { delta: "你好" }),
      ev("text.delta", "r2", { delta: "hi", session_id: "s2" }),
      ev("text.delta", "r1", { delta: "，世界" }),
    ]);
    expect(state.runs.r1?.text).toBe("你好，世界");
    expect(state.runs.r2?.text).toBe("hi");
    expect(selectSessionRuns(state, "s1").map((r) => r.runId)).toEqual(["r1"]);
    expect(selectSessionRuns(state, "s2").map((r) => r.runId)).toEqual(["r2"]);
    expect(selectActiveRun(state, "s1")?.runId).toBe("r1");
  });

  it("重复或乱序的事件（seq 不大于已处理的）被忽略", () => {
    const first = ev("text.delta", "r1", { delta: "a" }, 10);
    const state = reduce([ev("run.started", "r1", {}, 9), first, first, ev("text.delta", "r1", { delta: "b" }, 5)]);
    expect(state.runs.r1?.text).toBe("a");
    expect(state.runs.r1?.lastSeq).toBe(10);
  });

  it("工具调用：started 建卡片，completed 回填结果，缺失 started 时补占位", () => {
    const state = reduce([
      ev("run.started", "r1"),
      ev("tool.call.started", "r1", { tool_call: { id: "c1", name: "read_file", arguments: { path: "a.md" } } }),
      ev("tool.call.completed", "r1", { tool_result: { call_id: "c1", content: "ok" } }),
      ev("tool.call.completed", "r1", { tool_result: { call_id: "c2", content: "orphan", is_error: true } }),
    ]);
    const calls = state.runs.r1?.toolCalls ?? [];
    expect(calls).toHaveLength(2);
    expect(calls.at(0)?.call.name).toBe("read_file");
    expect(calls.at(0)?.result?.content).toBe("ok");
    expect(calls.at(0)?.completedAt).toBeTruthy();
    expect(calls.at(1)?.call.id).toBe("c2");
    expect(calls.at(1)?.result?.is_error).toBe(true);
  });

  it("索引更新保留旧快照；旧状态分叉、重复开始和占位回填不串联未来工具", () => {
    const base = reduce([ev("tool.call.started", "r1", { tool_call: { id: "a", name: "read_file", arguments: {} } })]);
    const initialCall = base.runs.r1!.toolCalls.at(0)!;
    const branch = reduce([ev("tool.call.completed", "r1", { tool_result: { call_id: "b", content: "branch" } })], base);
    const other = reduce([ev("tool.call.started", "r1", { tool_call: { id: "c", name: "list_files", arguments: {} } })], base);
    const done = reduce([
      ev("tool.call.completed", "r1", { tool_result: { call_id: "a", content: "base result" } }),
      ev("tool.call.started", "r1", { tool_call: { id: "b", name: "known_tool", arguments: { filled: true } } }),
    ], branch);
    expect(base.runs.r1!.toolCalls).toEqual([initialCall]);
    expect(initialCall.result).toBeUndefined();
    expect(other.runs.r1!.toolCalls.slice().map(entry => entry.call.id)).toEqual(["a", "c"]);
    expect(branch.runs.r1!.toolCalls.at(0)).toBe(initialCall);
    expect(done.runs.r1!.toolCalls.slice().map(entry => [entry.call.id, entry.result?.content])).toEqual([["a", "base result"], ["b", "branch"]]);
    expect(done.runs.r1!.toolCalls.at(1)!.call.name).toBe("known_tool");
    expect(done.runs.r1!.toolCalls.at(1)!.startedAt).toBe(branch.runs.r1!.toolCalls.at(1)!.startedAt);
    const revisit = reduce([ev("tool.call.completed", "r1", { tool_result: { call_id: "c", content: "other result" } })], other);
    expect(revisit.runs.r1!.toolCalls.slice().map(entry => [entry.call.id, entry.result?.content])).toEqual([["a", undefined], ["c", "other result"]]);
  });

  it("外部旧状态含重复工具 ID 时仍更新第一条，与原查找规则一致", () => {
    const base = reduce([ev("tool.call.started", "r1", { tool_call: { id: "same", name: "first", arguments: {} } })]);
    const first = base.runs.r1!.toolCalls.at(0)!;
    const state = { ...base, runs: { ...base.runs, r1: { ...base.runs.r1!, toolCalls: [first, { ...first, call: { ...first.call, name: "second" } }] } } };
    const next = reduce([ev("tool.call.completed", "r1", { tool_result: { call_id: "same", content: "ok" } })], state);
    expect(next.runs.r1!.toolCalls.slice().map(entry => [entry.call.name, entry.result?.content])).toEqual([["first", "ok"], ["second", undefined]]);
  });

  it("随机乱序完成、重复开始和旧快照分叉与线性查找语义一致", () => {
    const snapshots = [reduce([ev("run.started", "r1")])];
    let seed = 731;
    const random = () => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed; };
    for (let index = 0; index < 1200; index++) {
      const base = snapshots[index % 17 === 0 ? random() % snapshots.length : snapshots.length - 1]!;
      const id = `call-${random() % 80}`;
      const start = random() % 3 === 0;
      const call = { id, name: `tool-${index}`, arguments: { index } };
      const result = { call_id: id, content: `result-${index}` };
      const event = ev(start ? "tool.call.started" : "tool.call.completed", "r1", start ? { tool_call: call } : { tool_result: result });
      const before = base.runs.r1!.toolCalls;
      const expected = before.slice();
      const found = before.slice().findIndex(entry => entry.call.id === id);
      if (found < 0) expected.push(start ? { call, startedAt: event.at } : { call: { id, name: "", arguments: {} }, result, startedAt: event.at, completedAt: event.at });
      else expected[found] = start ? { ...before.at(found)!, call } : { ...before.at(found)!, result, completedAt: event.at };
      const next = reduce([event], base);
      expect(next.runs.r1!.toolCalls.slice()).toEqual(expected);
      expect(base.runs.r1!.toolCalls).toBe(before);
      for (let position = 0; position < before.length; position++) if (position !== found) expect(next.runs.r1!.toolCalls.at(position)).toBe(before.at(position));
      snapshots.push(next);
    }
  });

  it("长工具循环中保留全部调用和结果，审批与终态不等待列表刷新", () => {
    const events: ApiEvent[] = [];
    for (let index = 0; index < 1500; index++) {
      events.push(ev("tool.call.started", "r1", { tool_call: { id: `c-${index}`, name: "read_file", arguments: { index } } }));
      events.push(ev("tool.call.completed", "r1", { tool_result: { call_id: `c-${index}`, content: `result-${index}` } }));
    }
    const state = reduce(events);
    expect(state.runs.r1!.toolCalls).toHaveLength(1500);
    expect(state.runs.r1!.toolCalls.slice().every((entry, index) => entry.call.id === `c-${index}` && entry.result?.content === `result-${index}`)).toBe(true);
    const waiting = reduce([ev("approval.requested", "r1")], state);
    expect(waiting.runs.r1!.status).toBe("awaiting_approval");
    expect(waiting.runs.r1!.toolCalls).toBe(state.runs.r1!.toolCalls);
    expect(reduce([ev("run.completed", "r1")], waiting).runs.r1!.status).toBe("completed");
  });

  it("审批：requested 切到 awaiting_approval 并保存请求；resolved 清空并恢复 running", () => {
    const approval = {
      id: "ap1",
      run_id: "r1",
      session_id: "s1",
      agent_id: "a1",
      tool_call: { id: "c1", name: "shell", arguments: { cmd: "rm -rf x" } },
      risk: "destructive" as const,
      summary: "删除目录 x",
      status: "pending" as const,
      created_at: "2026-10-10T00:00:00Z",
      expires_at: "2026-10-10T00:10:00Z",
    };
    let state = reduce([ev("run.started", "r1"), ev("approval.requested", "r1", { approval })]);
    expect(state.runs.r1?.status).toBe("awaiting_approval");
    expect(state.runs.r1?.approval?.id).toBe("ap1");
    state = reduce([ev("approval.resolved", "r1", { approval: { ...approval, status: "approved" } })], state);
    expect(state.runs.r1?.status).toBe("running");
    expect(state.runs.r1?.approval).toBeNull();
  });

  it("终态：completed 用最终消息补全文本并记录用量；之后的事件被忽略", () => {
    let state = reduce([ev("run.started", "r1"), ev("text.delta", "r1", { delta: "部分" })]);
    state = reduce(
      [
        ev("run.completed", "r1", {
          usage: { input_tokens: 10, output_tokens: 5 },
          message: { id: "m1", session_id: "s1", seq: 2, role: "assistant", run_id: "r1", parts: [{ type: "text", text: "完整回复" }], created_at: "2026-10-10T00:00:05Z" },
        }),
        ev("text.delta", "r1", { delta: "late" }),
      ],
      state,
    );
    expect(state.runs.r1?.status).toBe("completed");
    expect(state.runs.r1?.text).toBe("完整回复");
    expect(state.runs.r1?.usage).toEqual({ input_tokens: 10, output_tokens: 5 });
    expect(state.runs.r1?.endedAt).toBeTruthy();
    expect(selectActiveRun(state, "s1")).toBeNull();
  });

  it("failed 记录错误；cancelled 清空审批", () => {
    const failed = reduce([ev("run.started", "r1"), ev("run.failed", "r1", { error: "provider down" })]);
    expect(failed.runs.r1).toMatchObject({ status: "failed", error: "provider down" });
    const cancelled = reduce([ev("run.started", "r2"), ev("approval.requested", "r2"), ev("run.cancelled", "r2")]);
    expect(cancelled.runs.r2).toMatchObject({ status: "cancelled", approval: null });
  });

  it("gap 把未结束的运行标记为 truncated，终态的不动", () => {
    let state = reduce([ev("run.started", "r1"), ev("run.started", "r2"), ev("run.completed", "r2")]);
    state = liveRunsReducer(state, { type: "gap" });
    expect(state.runs.r1?.truncated).toBe(true);
    expect(state.runs.r2?.truncated).toBe(false);
    // 再次 gap 不产生新对象
    expect(liveRunsReducer(state, { type: "gap" })).toBe(state);
  });

  it("中途加入（第一条就是 text.delta）视为可能缺失前文", () => {
    const state = reduce([ev("text.delta", "r1", { delta: "x" })]);
    expect(state.runs.r1?.truncated).toBe(true);
    expect(state.runs.r1?.text).toBe("x");
  });

  it("forget 移除运行并清理会话索引；reset 清空全部", () => {
    let state = reduce([ev("run.started", "r1"), ev("run.started", "r2", { session_id: "s2" })]);
    state = liveRunsReducer(state, { type: "forget", runId: "r1" });
    expect(state.runs.r1).toBeUndefined();
    expect(state.bySession.s1).toBeUndefined();
    expect(state.bySession.s2).toEqual(["r2"]);
    expect(liveRunsReducer(state, { type: "reset" })).toBe(initialLiveRuns);
  });

  it("非运行事件不改变状态", () => {
    const sessionEvent: ApiEvent = { seq: 1, type: "session.created", at: "2026-10-10T00:00:00Z", payload: { id: "s", agent_id: "a", origin: "desktop", key: "k", status: "active", created_at: "", updated_at: "" } };
    expect(liveRunsReducer(initialLiveRuns, { type: "event", event: sessionEvent })).toBe(initialLiveRuns);
  });
});

describe("liveRunsReducer：思考内容与用量口径", () => {
  it("reasoning.delta 单独累积，不混入正文；终态消息只补全正文", () => {
    const state = reduce([
      ev("run.started", "r9"),
      ev("reasoning.delta", "r9", { delta: "先想" }),
      ev("reasoning.delta", "r9", { delta: "一下" }),
      ev("text.delta", "r9", { delta: "答案" }),
    ]);
    expect(state.runs.r9?.reasoning).toBe("先想一下");
    expect(state.runs.r9?.text).toBe("答案");
    const done = reduce(
      [ev("run.completed", "r9", { message: { id: "m", session_id: "s1", seq: 1, role: "assistant", parts: [{ type: "text", text: "答案。" }], created_at: "x" } })],
      state,
    );
    expect(done.runs.r9?.reasoning).toBe("先想一下");
    expect(done.runs.r9?.text).toBe("答案。");
  });

  it("中途加入时第一条是 reasoning.delta 也视为可能缺失前文", () => {
    const state = reduce([ev("reasoning.delta", "r10", { delta: "x" })]);
    expect(state.runs.r10?.truncated).toBe(true);
  });

  it("usage 事件是增量：多步工具回路逐次累加；run.completed 的累计值直接采用", () => {
    const state = reduce([
      ev("run.started", "r11"),
      ev("usage", "r11", { usage: { input_tokens: 100, output_tokens: 10, cache_read_tokens: 5 } }),
      ev("usage", "r11", { usage: { input_tokens: 150, output_tokens: 20, cache_write_tokens: 7 } }),
    ]);
    expect(state.runs.r11?.usage).toEqual({ input_tokens: 250, output_tokens: 30, cache_read_tokens: 5, cache_write_tokens: 7 });
    const done = reduce([ev("run.completed", "r11", { usage: { input_tokens: 250, output_tokens: 31 } })], state);
    expect(done.runs.r11?.usage).toEqual({ input_tokens: 250, output_tokens: 31 });
  });

  it("addUsage：缺失增量原样返回，忽略非数字字段", () => {
    expect(addUsage(null, undefined)).toBeNull();
    expect(addUsage(null, { input_tokens: 3 })).toEqual({ input_tokens: 3 });
    expect(addUsage({ output_tokens: 1 }, { output_tokens: 2, input_tokens: Number.NaN })).toEqual({ output_tokens: 3 });
  });
});
