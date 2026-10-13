import { describe, expect, it } from "vitest";
import type { Message, Run, RunStatus } from "../../api/types";
import type { LiveRun } from "../../state/liveRuns";
import { latestTaskRunId, resolveTaskFailure } from "./taskFailure";

const date = "2026-10-13T00:00:00Z";
function message(runId: string, seq = 1, sessionId = "task-a", at = date): Message {
  return { id: `message-${seq}`, session_id: sessionId, run_id: runId, seq, role: "user", parts: [], created_at: at };
}
function live(runId: string, status: RunStatus = "failed", sessionId = "task-a", at = date): LiveRun {
  return { runId, sessionId, agentId: "agent-a", status, startedAt: at, endedAt: at, error: "Synthetic failure", text: "", reasoning: "", toolCalls: [], approval: null, usage: null, truncated: false, lastSeq: 1 };
}
function persisted(id = "run-a", status: RunStatus = "failed", sessionId = "task-a"): Run {
  return { id, session_id: sessionId, agent_id: "agent-a", status, trigger: "user", error: "Stored failure", usage: {}, created_at: date, ended_at: date };
}
describe("当前任务失败选择", () => {
  it("重新进入任务时从持久化的最后运行恢复失败，且核对任务与运行", () => {
    const id = latestTaskRunId("task-a", [message("run-a")], []);
    expect(id).toBe("run-a");
    expect(resolveTaskFailure("task-a", id, [], persisted())).toMatchObject({ runId: "run-a", sessionId: "task-a", detail: "Stored failure", occurredAt: date });
    expect(resolveTaskFailure("task-a", id, [], persisted("run-b"))).toBeNull();
    expect(resolveTaskFailure("task-a", id, [], persisted("run-a", "failed", "task-b"))).toBeNull();
    expect(resolveTaskFailure("task-a", id, [], undefined)).toBeNull();
  });
  it.each(["running", "queued", "completed", "cancelled", "awaiting_approval"] as const)("后来的 %s 运行替代旧失败", status => {
    const old = live("old"); const current = live("new", status, "task-a", "2026-10-13T00:01:00Z");
    const id = latestTaskRunId("task-a", [message("old")], [old, current]);
    expect(id).toBe("new"); expect(resolveTaskFailure("task-a", id, [old, current], persisted("old"))).toBeNull();
  });
  it("较新的持久化成功替代尚未清理的旧失败流，不受数组排序影响", () => {
    const messages = [message("new", 2, "task-a", "2026-10-13T00:02:00Z"), message("old", 1)];
    const id = latestTaskRunId("task-a", messages, [live("old")]);
    expect(id).toBe("new"); expect(resolveTaskFailure("task-a", id, [live("old")], persisted("new", "completed"))).toBeNull();
  });
  it("流先于消息保存时使用新失败；同轮的旧 queued 查询结果不能覆盖终态", () => {
    const runs = [live("new", "failed", "task-a", "2026-10-13T00:01:00Z")];
    const id = latestTaskRunId("task-a", [message("old")], runs);
    expect(resolveTaskFailure("task-a", id, runs, persisted("new", "queued"))).toMatchObject({ runId: "new" });
  });
  it("活动运行恢复中不展示旧失败；其他会话事件完全忽略", () => {
    expect(latestTaskRunId("task-a", [message("foreign", 3, "task-b")], [live("foreign", "failed", "task-b")])).toBeUndefined();
    expect(resolveTaskFailure("task-a", "run-a", [live("run-a")], undefined, "new-running")).toBeNull();
    expect(resolveTaskFailure("task-a", "run-a", [live("run-a", "failed", "task-b")])).toBeNull();
  });
  it("详情有长度上限，不从错误字符串伪造请求或追踪标识", () => {
    const failure = resolveTaskFailure("task-a", "run-a", [{ ...live("run-a"), error: "x".repeat(6000) }]);
    expect(failure?.detail).toHaveLength(2048);
    expect(Object.keys(failure!).sort()).toEqual(["detail", "occurredAt", "runId", "sessionId"]);
  });
  it("同毫秒的新成功流抑制旧失败，不复用旧任务报告", () => {
    const runs = [live("old"), { ...live("new", "completed"), lastSeq: 8 }];
    const id = latestTaskRunId("task-a", [message("old")], runs);
    expect(id).toBe("new"); expect(resolveTaskFailure("task-a", id, runs, persisted("old"))).toBeNull();
  });
  it("已知较旧失败流时间异常也不能覆盖新成功消息", () => {
    const runs = [live("old", "failed", "task-a", "2026-10-13T12:00:00Z")];
    const id = latestTaskRunId("task-a", [message("old", 1), message("new", 3)], runs);
    expect(id).toBe("new"); expect(resolveTaskFailure("task-a", id, runs, persisted("new", "completed"))).toBeNull();
  });
  it("同毫秒且未落库的失败顺序不能确认时，等待消息而非显示旧失败", () => {
    expect(latestTaskRunId("task-a", [message("old")], [{ ...live("new"), lastSeq: 9 }])).toBeUndefined();
  });
});
