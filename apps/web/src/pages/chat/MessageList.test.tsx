import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Message, Part } from "../../api/types";
import { MessageList } from "./MessageList";
import type { LiveRun, LiveToolCall } from "../../state/liveRuns";
import { PersistentList } from "../../state/persistentList";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

let root: Root;
let container: HTMLDivElement;
const toolCall = { id: "call-a", name: "read_file", arguments: { path: "requirements.md" } };

function message(id: string, role: Message["role"], parts: Part[]): Message {
  return { id, session_id: "session-a", seq: 1, role, parts, created_at: "2026-10-12T01:00:00Z" };
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});
async function render(messages: Message[], onOpenArtifact?: (id: string) => void) {
  await act(async () => root.render(<MessageList messages={messages} liveRuns={[]} loading={false} timeZone="UTC" onOpenArtifact={onOpenArtifact} />));
}

describe("持久化消息中的工具状态与成果定位", () => {
  it.each([false, true])("跨消息匹配工具结果，is_error=%s 时显示真实终态而不是调用中", async (isError) => {
    await render([
      message("call-message", "assistant", [{ type: "tool_call", tool_call: toolCall }]),
      message("result-message", "tool", [{ type: "tool_result", tool_result: { call_id: toolCall.id, content: "实际执行结果", is_error: isError } }]),
    ]);
    const callCard = container.querySelector(".tool-card")!;
    expect(callCard.querySelector("summary")?.textContent).toContain(isError ? "chat.toolError" : "chat.toolDone");
    expect(callCard.querySelector("pre")).toBeNull();
    await act(async () => { (callCard as HTMLDetailsElement).open = true; callCard.dispatchEvent(new Event("toggle")); });
    expect(callCard.textContent).toContain("实际执行结果");
    expect(callCard.textContent).not.toContain("chat.toolRunning");
  });

  it("历史调用没有对应结果时显示未知，不借用其他调用的结果或声称仍在运行", async () => {
    await render([
      message("call-message", "assistant", [{ type: "tool_call", tool_call: toolCall }]),
      message("unrelated-result", "tool", [{ type: "tool_result", tool_result: { call_id: "call-other", content: "其他调用已完成" } }]),
    ]);
    const callCard = container.querySelector(".tool-card")!;
    expect(callCard.querySelector("summary")?.textContent).toContain("work.noStepResult");
    expect(callCard.textContent).not.toContain("chat.toolRunning");
    expect(callCard.textContent).not.toContain("chat.toolDone");
    expect(callCard.textContent).not.toContain("其他调用已完成");
  });

  it("不同运行复用工具 ID 时，各卡片保留自身结果与状态", async () => {
    await render([
      { ...message("old-call", "assistant", [{ type: "tool_call", tool_call: toolCall }]), run_id: "old" },
      { ...message("old-result", "tool", [{ type: "tool_result", tool_result: { call_id: toolCall.id, content: "old-success" } }]), run_id: "old" },
      { ...message("new-call", "assistant", [{ type: "tool_call", tool_call: toolCall }]), run_id: "new" },
      { ...message("new-result", "tool", [{ type: "tool_result", tool_result: { call_id: toolCall.id, content: "new-error", is_error: true } }]), run_id: "new" },
    ]);
    const cards = container.querySelectorAll<HTMLDetailsElement>(".msg.assistant .tool-card");
    expect(cards[0]!.textContent).toContain("chat.toolDone");
    expect(cards[1]!.textContent).toContain("chat.toolError");
    await act(async () => { for (const card of cards) { card.open = true; card.dispatchEvent(new Event("toggle")); } });
    expect(cards[0]!.textContent).toContain("old-success");
    expect(cards[0]!.textContent).not.toContain("new-error");
    expect(cards[1]!.textContent).toContain("new-error");
  });

  it("分别打开历史和最新成果时传递所点击的消息 ID，用户消息不生成成果入口", async () => {
    const open = vi.fn();
    await render([
      message("user-goal", "user", [{ type: "text", text: "请修改草稿" }]),
      message("older-result", "assistant", [{ type: "text", text: "第一稿" }]),
      message("newer-result", "assistant", [{ type: "text", text: "修改稿" }]),
    ], open);
    const buttons = container.querySelectorAll<HTMLButtonElement>(".result-open");
    expect(buttons).toHaveLength(2);
    expect(container.querySelector(".msg.user .result-open")).toBeNull();
    await act(async () => buttons[0]!.click());
    expect(open).toHaveBeenNthCalledWith(1, "older-result");
    await act(async () => buttons[1]!.click());
    expect(open).toHaveBeenNthCalledWith(2, "newer-result");
  });

  it("保持消息不变时使用最新文件动作，取消预览能力后移除可点击入口", async () => {
    const attachment = { path: "inbound/notes.md", name: "notes.md", size: 10 };
    const messages = [message("file", "user", [{ type: "attachment", attachment }])];
    const oldOpen = vi.fn(), currentOpen = vi.fn();
    const show = (onOpenFile?: typeof oldOpen) => act(async () => root.render(<MessageList messages={messages} liveRuns={[]} loading={false} timeZone="UTC" onOpenFile={onOpenFile} />));
    await show(oldOpen); await show(currentOpen);
    await act(async () => container.querySelector<HTMLButtonElement>(".preview-attachment-button")!.click());
    expect(oldOpen).not.toHaveBeenCalled(); expect(currentOpen).toHaveBeenCalledExactlyOnceWith(attachment);
    await show();
    expect(container.querySelector(".preview-attachment-button")).toBeNull();
    expect(container.querySelector(".attachment-chip")?.textContent).toContain("notes.md");
  });

  it("终态消息中的1200条工具记录按页可达，不在持久化后重新挂载全部卡片", async () => {
    const parts: Part[] = [];
    for (let index = 0; index < 600; index++) {
      parts.push({ type: "tool_call", tool_call: { id: `persisted-${index}`, name: `tool-${index}`, arguments: { path: `${index}.md` } } });
      parts.push({ type: "tool_result", tool_result: { call_id: `persisted-${index}`, content: `persisted-result-${index}` } });
    }
    parts.push({ type: "text", text: "任务完整完成" });
    await render([{ ...message("long-completed", "assistant", parts), run_id: "completed-run" }]);
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
    expect(container.textContent).toContain("任务完整完成");
    const history = container.querySelector<HTMLDetailsElement>(".tool-history")!;
    await act(async () => { history.open = true; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".tool-card")).toHaveLength(60);
    const first = history.querySelector<HTMLDetailsElement>(".tool-card")!;
    await act(async () => { first.open = true; first.dispatchEvent(new Event("toggle")); });
    expect(first.textContent).toContain("persisted-result-0");
    const ids = new Set<string>();
    while (true) {
      for (const code of history.querySelectorAll(".tool-card summary code")) if (code.textContent?.startsWith("persisted-")) ids.add(code.textContent);
      const next = Array.from(history.querySelectorAll<HTMLButtonElement>(".tool-history-pages button")).find(button => button.textContent === "compact.nextSteps")!;
      if (next.disabled) break;
      await act(async () => next.click());
    }
    expect(ids.size).toBe(590);
    expect(ids.has("persisted-0")).toBe(true);
    expect(ids.has("persisted-589")).toBe(true);
    await act(async () => { history.open = false; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
    expect(parts).toHaveLength(1201);
  });

  it("三十批工具之间夹杂进度正文时，整条消息仍只常显二十条工具记录", async () => {
    const parts: Part[] = [];
    for (let batch = 0; batch < 30; batch++) {
      for (let offset = 0; offset < 20; offset++) {
        const index = batch * 20 + offset;
        parts.push({ type: "tool_call", tool_call: { id: `read-${index}`, name: `tool-${index}`, arguments: {} } });
        parts.push({ type: "tool_result", tool_result: { call_id: `read-${index}`, content: `result-${index}` } });
      }
      parts.push({ type: "text", text: `progress-batch-${batch}` });
    }
    await render([{ ...message("batched-completed", "assistant", parts), run_id: "batched-run" }]);
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
    expect(container.querySelectorAll(".tool-history")).toHaveLength(30);
    expect(container.querySelector(".tool-card")?.textContent).toContain("tool-590");
    const paragraphs = Array.from(container.querySelectorAll(".msg p")).map(node => node.textContent).filter(text => text?.startsWith("progress-batch-"));
    expect(paragraphs).toEqual(Array.from({ length: 30 }, (_, index) => `progress-batch-${index}`));
    const first = container.querySelector<HTMLDetailsElement>(".tool-history")!;
    await act(async () => { first.open = true; first.dispatchEvent(new Event("toggle")); });
    expect(first.querySelectorAll(".tool-card")).toHaveLength(40);
    expect(container.querySelectorAll(".tool-card")).toHaveLength(60);
    expect(first.textContent).toContain("tool-0");
    expect(first.textContent).toContain("tool-19");
    await act(async () => { first.open = false; first.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
  });

  it("共享常显预算仍不能隐藏早期批次中的未完成工具", async () => {
    const parts: Part[] = [{ type: "tool_call", tool_call: { ...toolCall, id: "pending-early", name: "pending-early" } }, { type: "text", text: "later progress" }];
    for (let index = 0; index < 30; index++) parts.push({ type: "tool_result", tool_result: { call_id: `later-${index}`, content: "result" } });
    await render([message("pending-history", "assistant", parts)]);
    expect(container.querySelectorAll(".tool-card")).toHaveLength(21);
    expect(container.querySelector(".tool-card")?.textContent).toContain("pending-early");
    expect(container.querySelector(".tool-card")?.textContent).toContain("work.noStepResult");
  });

  it("工具历史分组不跨正文和附件，孤立结果仍可读取，无结果调用保留未知状态", async () => {
    const resultParts: Part[] = Array.from({ length: 25 }, (_, index) => ({ type: "tool_result", tool_result: { call_id: `orphan-${index}`, content: `orphan-content-${index}` } }));
    await render([message("mixed", "assistant", [
      { type: "text", text: "正文之前" }, ...resultParts,
      { type: "attachment", attachment: { path: "out.md", name: "out.md", size: 10 } },
      { type: "text", text: "正文之后" },
      { type: "tool_call", tool_call: { ...toolCall, name: "unresolved" } }, ...resultParts,
    ])]);
    const content = container.querySelector(".msg")!;
    const before = content.firstElementChild!;
    expect(before.textContent).toContain("正文之前");
    const history = content.querySelector<HTMLDetailsElement>(".tool-history")!;
    const attachment = content.querySelector(".attachment-chip")!;
    expect(before.compareDocumentPosition(history) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(history.compareDocumentPosition(attachment) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(container.querySelectorAll(".tool-history")).toHaveLength(1);
    expect(container.textContent).toContain("unresolved");
    expect(container.textContent).toContain("work.noStepResult");
    await act(async () => { history.open = true; history.dispatchEvent(new Event("toggle")); });
    const first = history.querySelector<HTMLDetailsElement>(".tool-card")!;
    await act(async () => { first.open = true; first.dispatchEvent(new Event("toggle")); });
    expect(first.textContent).toContain("orphan-content-0");
  });
});

describe("长实时工具列表", () => {
  function live(calls: LiveRun["toolCalls"]): LiveRun {
    return { runId: "run-long", sessionId: "session-a", agentId: "agent-a", status: "running", toolCalls: calls, text: "", reasoning: "", approval: null, usage: null, error: null, truncated: false, lastSeq: 1, startedAt: "2026-10-12T01:00:00Z", endedAt: null };
  }
  function calls(count: number): LiveToolCall[] {
    return Array.from({ length: count }, (_, index) => ({ call: { id: `c-${index}`, name: `tool-${index}`, arguments: { path: `${index}.md` } }, result: { call_id: `c-${index}`, content: `complete-result-${index}` }, startedAt: "2026-10-12T01:00:00Z" }));
  }
  async function show(run: LiveRun) { await act(async () => root.render(<MessageList messages={[]} liveRuns={[run]} loading={false} timeZone="UTC" />)); }

  it("仅挂载最近二十项，早期记录分页仍能读取完整参数和结果", async () => {
    const original = calls(101);
    const run = live(original);
    await show(run);
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
    const history = container.querySelector<HTMLDetailsElement>(".tool-history")!;
    await act(async () => { history.open = true; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".tool-card")).toHaveLength(60);
    const first = history.querySelector<HTMLDetailsElement>(".tool-card")!;
    await act(async () => { first.open = true; first.dispatchEvent(new Event("toggle")); });
    expect(first.textContent).toContain("complete-result-0");
    expect(first.textContent).toContain('"path": "0.md"');
    for (let page = 0; page < 2; page++) await act(async () => Array.from(history.querySelectorAll<HTMLButtonElement>("button")).find(button => button.textContent === "compact.nextSteps")!.click());
    expect(history.querySelectorAll(".tool-card")).toHaveLength(1);
    expect(history.textContent).toContain("tool-80");
    await act(async () => { history.open = false; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
    expect(run.toolCalls).toBe(original);
    expect(run.toolCalls).toHaveLength(101);
  });

  it("持久化分块集合分页只读取当前范围，末页不会重复最新常显工具", async () => {
    const tools = PersistentList.from(calls(2061));
    const reads = vi.spyOn(tools, "slice");
    await show(live(tools));
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
    expect(reads.mock.calls).toEqual([[2041]]);
    const history = container.querySelector<HTMLDetailsElement>(".tool-history")!;
    reads.mockClear();
    await act(async () => { history.open = true; history.dispatchEvent(new Event("toggle")); });
    expect(container.querySelectorAll(".tool-card")).toHaveLength(60);
    expect(reads.mock.calls.every(([start = 0, end = tools.length]) => end - start <= 40)).toBe(true);
    for (let page = 0; page < 51; page++) await act(async () => Array.from(history.querySelectorAll<HTMLButtonElement>(".tool-history-pages button")).find(button => button.textContent === "compact.nextSteps")!.click());
    expect(history.querySelectorAll(".tool-card")).toHaveLength(1);
    expect(history.textContent).toContain("tool-2040");
    expect(history.textContent).not.toContain("tool-2041");
    expect(container.querySelectorAll(".tool-card")).toHaveLength(21);
    expect(tools.length).toBe(2061);
  });

  it("即使有较新的已完成调用，较早未完成工具仍显示且完成后可折叠", async () => {
    const tools = calls(60);
    tools[2] = { ...tools[2]!, result: undefined };
    const run = live(tools);
    await show({ ...run, status: "awaiting_approval" });
    expect(container.querySelector(".tool-history + .tool-card")?.textContent).toContain("tool-2");
    expect(container.textContent).toContain("chat.awaitingApproval");
    const completed = tools.slice(); completed[2] = { ...completed[2]!, result: { call_id: "c-2", content: "approved result" } };
    await show({ ...run, toolCalls: completed });
    expect(container.querySelectorAll(".tool-card")).toHaveLength(20);
  });

  it("长参数在折叠时不扫描后续字段，展开保留正文，工具完成无需重做摘要", async () => {
    let reads = 0;
    const argumentsValue = { text: "x".repeat(100_000), get tail() { reads++; return "保留末尾"; } };
    const tool: LiveToolCall = { call: { id: "long", name: "write_file", arguments: argumentsValue }, startedAt: "2026-10-12T01:00:00Z" };
    const run = live([tool]); await show(run);
    expect(reads).toBe(0);
    await show({ ...run, toolCalls: [{ ...tool, result: { call_id: "long", content: "done" } }] });
    expect(reads).toBe(0);
    const card = container.querySelector<HTMLDetailsElement>(".tool-card")!;
    await act(async () => { card.open = true; card.dispatchEvent(new Event("toggle")); });
    expect(card.textContent).toContain("保留末尾");
    expect(reads).toBe(1);
  });
});
