import { describe, expect, it } from "vitest";
import { ApiError } from "../api/client";
import type { Run } from "../api/types";
import type { LiveRun } from "../state/liveRuns";
import { fallbackRunId, resolveActiveRun } from "./activeRun";

function live(runId: string, status: LiveRun["status"] = "running"): LiveRun {
  return {
    runId,
    sessionId: "s1",
    agentId: "a1",
    status,
    text: "",
    reasoning: "",
    toolCalls: [],
    approval: null,
    usage: null,
    error: null,
    truncated: false,
    lastSeq: 1,
    startedAt: "2026-10-10T00:00:00Z",
    endedAt: null,
  };
}

function run(id: string, status: Run["status"]): Run {
  return { id, session_id: "s1", agent_id: "a1", status, trigger: "user", usage: {}, created_at: "2026-10-10T00:00:00Z" };
}

const none = { data: undefined, error: null };

describe("fallbackRunId", () => {
  it("实时视图有在途运行时不回退", () => {
    expect(fallbackRunId({ live: live("r1"), liveRunIds: ["r1"], sessionActiveRunId: "r1" })).toBeNull();
  });

  it("页面刷新或 gap 之后实时视图为空：回退到 Session.active_run_id", () => {
    expect(fallbackRunId({ live: null, liveRunIds: [], sessionActiveRunId: "r2" })).toBe("r2");
  });

  it("实时视图已见过该运行（例如刚终态、会话列表尚未刷新）时以实时视图为准", () => {
    expect(fallbackRunId({ live: null, liveRunIds: ["r2"], sessionActiveRunId: "r2" })).toBeNull();
  });

  it("会话没有 active_run_id 时不回退", () => {
    expect(fallbackRunId({ live: null, liveRunIds: [], sessionActiveRunId: undefined })).toBeNull();
  });
});

describe("resolveActiveRun", () => {
  it("实时视图优先", () => {
    expect(resolveActiveRun({ live: live("r1", "awaiting_approval"), liveRunIds: ["r1"], sessionActiveRunId: "r9" }, none)).toEqual({
      runId: "r1",
      status: "awaiting_approval",
      source: "live",
    });
  });

  it("回退路径：查询中按在途处理（Composer 显示取消按钮，而不是让发送得到 409）", () => {
    expect(resolveActiveRun({ live: null, liveRunIds: [], sessionActiveRunId: "r2" }, none)).toEqual({ runId: "r2", status: null, source: "session" });
  });

  it("回退路径：GET /runs/{id} 给出等待审批等非终态状态", () => {
    expect(resolveActiveRun({ live: null, liveRunIds: [], sessionActiveRunId: "r2" }, { data: run("r2", "awaiting_approval"), error: null })).toEqual({
      runId: "r2",
      status: "awaiting_approval",
      source: "session",
    });
  });

  it("回退路径：已终态或 not_found 视为没有在途运行", () => {
    expect(resolveActiveRun({ live: null, liveRunIds: [], sessionActiveRunId: "r2" }, { data: run("r2", "completed"), error: null })).toBeNull();
    expect(
      resolveActiveRun({ live: null, liveRunIds: [], sessionActiveRunId: "r2" }, { data: undefined, error: new ApiError("not_found", "no", { status: 404 }) }),
    ).toBeNull();
  });

  it("查询结果属于另一个运行（会话切换的残留）时不采用其状态", () => {
    expect(resolveActiveRun({ live: null, liveRunIds: [], sessionActiveRunId: "r3" }, { data: run("r2", "completed"), error: null })).toEqual({
      runId: "r3",
      status: null,
      source: "session",
    });
  });
});
