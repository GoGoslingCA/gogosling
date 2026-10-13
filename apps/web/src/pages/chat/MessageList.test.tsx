import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Message, Part } from "../../api/types";
import { MessageList } from "./MessageList";

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
});
