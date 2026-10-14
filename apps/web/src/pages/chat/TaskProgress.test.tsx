import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Message, Part } from "../../api/types";
import type { LiveRun } from "../../state/liveRuns";
import { PersistentList } from "../../state/persistentList";
import { TaskProgress } from "./TaskProgress";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

let root: Root;
let container: HTMLDivElement;
let props: Parameters<typeof TaskProgress>[0];
const toolCall = { id: "call-a", name: "read_file", arguments: { path: "requirements.md" } };

function message(id: string, role: Message["role"], parts: Part[]): Message {
  return { id, session_id: "session-a", seq: 1, role, parts, created_at: "2026-10-12T01:00:00Z" };
}
function live(status: LiveRun["status"], toolCalls: LiveRun["toolCalls"] = []): LiveRun {
  return { runId: "run-new", sessionId: "session-a", agentId: "agent-a", status, toolCalls, text: "", reasoning: "", approval: null, usage: null, error: null, truncated: false, lastSeq: 20, startedAt: "2026-10-12T01:00:00Z", endedAt: null };
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  props = { messages: [], activeStatus: undefined, liveRuns: [], onRetry: vi.fn() };
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});
async function render() { await act(async () => root.render(<TaskProgress {...props} />)); }
function status() { return container.querySelector('[role="status"]')!.textContent; }

describe("任务进度使用真实运行证据", () => {
  it("有用户目标或助手正文但没有运行状态时，不声称任务已完成", async () => {
    props.messages = [message("m1", "user", [{ type: "text", text: "整理需求文档" }]), message("m2", "assistant", [{ type: "text", text: "已有部分草稿" }])];
    await render();
    expect(status()).toContain("work.state_ready");
    expect(status()).not.toContain("work.state_completed");
    expect(container.textContent).toContain("整理需求文档");
    expect(container.querySelectorAll("li")).toHaveLength(0);
  });

  it("有在途运行 ID 而状态尚未恢复时，显示运行中而不是旧轮次完成", async () => {
    props.activeStatus = null;
    props.latestStatus = "completed";
    await render();
    expect(status()).toContain("work.state_running");
    expect(status()).not.toContain("work.state_completed");
  });

  it("等待审批不显示完成，并保留来自真实调用的执行步骤", async () => {
    props.activeStatus = "awaiting_approval";
    props.activeRunId = "run-new";
    props.liveRuns = [live("awaiting_approval", [{ call: toolCall, startedAt: "2026-10-12T01:00:00Z" }])];
    await render();
    expect(status()).toContain("work.state_awaiting_approval");
    expect(container.querySelectorAll("li")).toHaveLength(1);
    expect(container.querySelector("li")?.textContent).toContain("read_file");
  });

  it.each(["failed", "cancelled"] as const)("新一轮实时状态为 %s 时不被旧轮次完成状态遮住", async (nextStatus) => {
    props.latestStatus = "completed";
    props.liveRuns = [live(nextStatus)];
    await render();
    expect(status()).toContain(`work.state_${nextStatus}`);
    expect(status()).not.toContain("work.state_completed");
    await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    expect(props.onRetry).toHaveBeenCalledOnce();
  });

  it("实时调用缺少结果时，仍保留消息中已经持久化的成功结果", async () => {
    props.activeStatus = "running";
    props.messages = [
      message("m1", "assistant", [{ type: "tool_call", tool_call: toolCall }]),
      message("m2", "tool", [{ type: "tool_result", tool_result: { call_id: toolCall.id, content: "已读取" } }]),
    ];
    props.liveRuns = [live("running", [{ call: toolCall, startedAt: "2026-10-12T01:00:00Z" }])];
    await render();
    expect(container.querySelectorAll("li")).toHaveLength(1);
    expect(container.querySelector("li")?.textContent).toContain("chat.toolDone");
    expect(container.querySelector("li")?.textContent).not.toContain("chat.toolRunning");
  });

  it("终态运行中没有回传结果的工具调用保持未知，不补造成功", async () => {
    props.latestStatus = "completed";
    props.messages = [message("m1", "assistant", [{ type: "tool_call", tool_call: toolCall }])];
    await render();
    expect(status()).toContain("work.state_completed");
    expect(container.querySelector("li")?.textContent).toContain("work.noStepResult");
    expect(container.querySelector("li")?.textContent).not.toContain("chat.toolDone");
  });

  it("运行重用工具 ID 时保留两步，不让旧轮次成功覆盖当前审批", async () => {
    props.messages = [
      { ...message("old-call", "assistant", [{ type: "tool_call", tool_call: toolCall }]), run_id: "old" },
      { ...message("old-result", "tool", [{ type: "tool_result", tool_result: { call_id: toolCall.id, content: "old" } }]), run_id: "old" },
    ];
    props.activeRunId = "run-new"; props.activeStatus = "awaiting_approval";
    props.liveRuns = [live("awaiting_approval", [{ call: toolCall, startedAt: "2026-10-12T01:00:00Z" }])];
    await render();
    const steps = container.querySelectorAll("li");
    expect(steps).toHaveLength(2);
    expect(steps[0]!.getAttribute("data-state")).toBe("done");
    expect(steps[1]!.getAttribute("data-state")).toBe("approval");
  });

  it("旧轮次未清理的实时失败状态不能覆盖后来已落库的新轮次完成", async () => {
    props.latestRunId = "run-later";
    props.latestStatus = "completed";
    props.liveRuns = [live("failed")];
    props.messages = [{ ...message("m-later", "assistant", [{ type: "text", text: "新一轮结果" }]), run_id: "run-later", created_at: "2026-10-12T01:05:00Z" }];
    await render();
    expect(status()).toContain("work.state_completed");
    expect(container.querySelector("button")).toBeNull();
  });

  it("新轮次尚无工具调用时，不把上一轮无结果的步骤显示为当前进行中", async () => {
    props.activeRunId = "run-new";
    props.activeStatus = "running";
    props.messages = [{ ...message("m-old", "assistant", [{ type: "tool_call", tool_call: toolCall }]), run_id: "run-old" }];
    await render();
    expect(container.querySelector("summary")?.textContent).toBe("compact.goalAndHistory");
    expect(container.querySelector("li")?.textContent).toContain("work.noStepResult");
    expect(container.querySelector("li")?.textContent).not.toContain("chat.toolRunning");
    props.liveRuns = [live("running", [{ call: { ...toolCall, id: "call-new", name: "search_files" }, startedAt: "2026-10-12T01:00:00Z" }])];
    await render();
    expect(container.querySelector(".execution-trail")?.textContent).toContain("search_files");
    expect(container.querySelector(".execution-trail")?.textContent).toContain("chat.toolRunning");
    expect(container.querySelectorAll("li")[0]?.textContent).toContain("work.noStepResult");
    expect(container.querySelectorAll("li")[1]?.textContent).toContain("chat.toolRunning");
  });

  it("轨迹只展示真实的最近四步，较早步骤与完整目标折叠保留，不生成百分比进度", async () => {
    props.messages = [message("goal", "user", [{ type: "text", text: "完整目标\n以及必须保留的约束" }])];
    props.liveRuns = [live("completed", Array.from({ length: 6 }, (_, index) => ({ call: { ...toolCall, id: `call-${index}`, name: `tool-${index}` }, result: { call_id: `call-${index}`, content: "真实结果" }, startedAt: "2026-10-12T01:00:00Z" })))];
    await render();
    expect(container.querySelectorAll(".execution-trail li")).toHaveLength(4);
    expect(container.querySelectorAll(".execution-history li")).toHaveLength(0);
    expect(container.querySelector(".execution-trail")?.textContent).not.toContain("tool-0");
    expect(container.querySelector(".execution-trail")?.textContent).toContain("tool-5");
    expect(container.querySelector<HTMLDetailsElement>(".progress-details")!.open).toBe(false);
    expect(container.querySelector(".task-goal")?.textContent).toContain("完整目标\n以及必须保留的约束");
    expect(container.querySelector('[role="progressbar"]')).toBeNull();
    expect(container.textContent).not.toContain("%");
    const history = container.querySelector<HTMLDetailsElement>(".progress-details")!;
    await act(async () => { history.open = true; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".execution-history li")).toHaveLength(2);
    expect(container.querySelector(".execution-history")?.textContent).toContain("tool-0");
  });

  it("长历史关闭时只挂载四步，展开按页可访问全部记录，关闭后释放历史节点", async () => {
    props.liveRuns = [live("completed", PersistentList.from(Array.from({ length: 125 }, (_, index) => ({ call: { ...toolCall, id: `call-${index}`, name: `tool-${index}` }, result: { call_id: `call-${index}`, content: "result" }, startedAt: "2026-10-12T01:00:00Z" }))))];
    await render();
    expect(container.querySelectorAll("li")).toHaveLength(4);
    const history = container.querySelector<HTMLDetailsElement>(".progress-details")!;
    await act(async () => { history.open = true; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".execution-history li")).toHaveLength(40);
    expect(container.querySelector(".execution-history")?.textContent).toContain("tool-0");
    for (let page = 0; page < 3; page++) await act(async () => Array.from(container.querySelectorAll<HTMLButtonElement>(".tool-history-pages button")).find(button => button.textContent === "compact.nextSteps")!.click());
    expect(container.querySelectorAll(".execution-history li")).toHaveLength(1);
    expect(container.querySelector(".execution-history")?.textContent).toContain("tool-120");
    await act(async () => { history.open = false; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll("li")).toHaveLength(4);
  });

  it("纯文本增量不重读工具历史，审批变化即时显示且重试使用最新动作", async () => {
    let toolReads = 0;
    const entry = { call: { ...toolCall, get name() { toolReads++; return "read_file"; } }, startedAt: "2026-10-12T01:00:00Z" };
    props.activeRunId = "run-new"; props.activeStatus = "running";
    props.liveRuns = [live("running", [entry])];
    await render(); toolReads = 0;
    const retry = vi.fn();
    for (let index = 0; index < 20; index++) {
      props.liveRuns = [{ ...props.liveRuns[0]!, text: `${index}`, lastSeq: index + 21 }];
      props.onRetry = retry;
      await render();
    }
    expect(toolReads).toBe(0);
    props.activeStatus = "awaiting_approval";
    await render();
    expect(status()).toContain("work.state_awaiting_approval");
    expect(container.querySelector("li")?.getAttribute("data-state")).toBe("approval");
    props.activeStatus = "failed";
    await render();
    await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    expect(retry).toHaveBeenCalledOnce();
  });
});
