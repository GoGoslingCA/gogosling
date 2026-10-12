import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Attachment } from "../../api/types";
import { Composer } from "./Composer";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (value: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

const attachment: Attachment = { name: "notes.txt", path: "inbound/notes.txt", size: 10 };
let root: Root;
let container: HTMLDivElement;
let props: Parameters<typeof Composer>[0];

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  props = { draftKey: "session-a", disabled: false, busy: false, activeRunId: null, onSend: vi.fn(async () => undefined), onCancel: vi.fn(), upload: vi.fn(async () => attachment) };
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});
async function render() { await act(async () => root.render(<Composer {...props} />)); }
function textarea() { return container.querySelector("textarea")!; }
function sendButton() { return Array.from(container.querySelectorAll("button")).find((button) => ["chat.send", "chat.sending"].includes(button.textContent ?? ""))!; }
async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea(), value);
    textarea().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function chooseFile() {
  await act(async () => {
    const input = container.querySelector('input[type="file"]')!;
    Object.defineProperty(input, "files", { configurable: true, value: [new File(["hello"], "notes.txt")] });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
function fileDragEvent(name: string) {
  const event = new Event(name, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: { types: ["Files"], files: [new File(["hello"], "notes.txt")], dropEffect: "none" },
  });
  return event;
}

describe("Composer 的异步草稿与附件", () => {
  it("发送失败保留原始草稿和已上传附件，可重试", async () => {
    const request = deferred<void>();
    props.onSend = vi.fn(() => request.promise);
    await render();
    await type("  keep draft  ");
    await chooseFile();
    await act(async () => sendButton().click());
    expect(props.onSend).toHaveBeenCalledWith("keep draft", [attachment]);
    expect(textarea().value).toBe("  keep draft  ");
    await act(async () => request.reject(new Error("offline")));
    expect(textarea().value).toBe("  keep draft  ");
    expect(container.querySelector(".attachment-chip")?.textContent).toContain("notes.txt");
    expect(sendButton().disabled).toBe(false);
  });

  it("请求成功只清除已发送内容，保留等待期间的新编辑", async () => {
    const request = deferred<void>();
    props.onSend = vi.fn(() => request.promise);
    await render();
    await type("first");
    await act(async () => sendButton().click());
    await type("next draft");
    await act(async () => request.resolve());
    expect(textarea().value).toBe("next draft");
    await act(async () => sendButton().click());
    expect(props.onSend).toHaveBeenLastCalledWith("next draft", []);
  });

  it("没有新编辑时成功清空正文及已发送附件", async () => {
    await render();
    await type("sent");
    await chooseFile();
    await act(async () => sendButton().click());
    expect(textarea().value).toBe("");
    expect(container.querySelector(".attachment-chip")).toBeNull();
  });

  it("上传期间禁用发送，Enter 也不能提前发送", async () => {
    const upload = deferred<Attachment>();
    props.upload = vi.fn(() => upload.promise);
    await render();
    await type("with file");
    await chooseFile();
    expect(sendButton().disabled).toBe(true);
    await act(async () => textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(props.onSend).not.toHaveBeenCalled();
    await act(async () => upload.resolve(attachment));
    expect(sendButton().disabled).toBe(false);
  });

  it("切换会话不混用草稿，迟到的附件回到上传发起会话", async () => {
    const upload = deferred<Attachment>();
    props.upload = vi.fn(() => upload.promise);
    await render();
    await type("draft a");
    await chooseFile();
    props = { ...props, draftKey: "session-b" };
    await render();
    expect(textarea().value).toBe("");
    await type("draft b");
    await act(async () => upload.resolve(attachment));
    expect(container.querySelector(".attachment-chip")).toBeNull();
    props = { ...props, draftKey: "session-a" };
    await render();
    expect(textarea().value).toBe("draft a");
    expect(container.querySelector(".attachment-chip")?.textContent).toContain("notes.txt");
    props = { ...props, draftKey: "session-b" };
    await render();
    expect(textarea().value).toBe("draft b");
  });

  it("旧会话发送完成不清除新会话编辑，同一轮重复点击只发一次", async () => {
    const request = deferred<void>();
    props.onSend = vi.fn(() => request.promise);
    await render();
    await type("draft a");
    await act(async () => { sendButton().click(); sendButton().click(); });
    expect(props.onSend).toHaveBeenCalledTimes(1);
    props = { ...props, draftKey: "session-b" };
    await render();
    await type("draft b");
    await act(async () => request.resolve());
    expect(textarea().value).toBe("draft b");
    props = { ...props, draftKey: "session-a" };
    await render();
    expect(textarea().value).toBe("");
  });

  it("输入法合成和 Shift+Enter 不触发发送", async () => {
    await render();
    await type("中文");
    await act(async () => {
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true }));
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 229, bubbles: true }));
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }));
    });
    expect(props.onSend).not.toHaveBeenCalled();
  });

  it("将首页想法带到新任务，只初始化一次且不覆盖已经编辑过的草稿", async () => {
    props.initialText = "整理这份需求";
    await render();
    expect(textarea().value).toBe("整理这份需求");
    await type("我的修改");
    props.initialText = "更新后的首页想法";
    await render();
    expect(textarea().value).toBe("我的修改");

    props = { ...props, draftKey: "session-b", initialText: undefined };
    await render();
    await type("新任务草稿");
    await type("");
    props.initialText = "迟到的首页想法";
    await render();
    expect(textarea().value).toBe("");
  });

  it("已保存成果可以加入本次资料，重复成果不重复添加，移除后可再次选择", async () => {
    props.pendingAttachment = attachment;
    props.onAttachmentConsumed = vi.fn();
    await render();
    expect(container.querySelectorAll(".attachment-chip")).toHaveLength(1);
    expect(props.onAttachmentConsumed).toHaveBeenCalledTimes(1);
    props.pendingAttachment = { ...attachment };
    await render();
    expect(container.querySelectorAll(".attachment-chip")).toHaveLength(1);
    await act(async () => container.querySelector<HTMLButtonElement>(".attachment-chip button")!.click());
    expect(container.querySelector(".attachment-chip")).toBeNull();
    props.pendingAttachment = null;
    await render();
    props.pendingAttachment = attachment;
    await render();
    expect(container.querySelectorAll(".attachment-chip")).toHaveLength(1);
  });

  it("拖入资料后上传并显示可移除的标签，上传期间重复拖入不会重复上传", async () => {
    const upload = deferred<Attachment>();
    props.upload = vi.fn(() => upload.promise);
    await render();
    const composer = container.querySelector(".composer")!;
    await act(async () => composer.dispatchEvent(fileDragEvent("dragenter")));
    expect(composer.classList.contains("is-dragging")).toBe(true);
    const drop = fileDragEvent("drop");
    await act(async () => composer.dispatchEvent(drop));
    expect(drop.defaultPrevented).toBe(true);
    expect(composer.classList.contains("is-dragging")).toBe(false);
    await act(async () => composer.dispatchEvent(fileDragEvent("drop")));
    expect(props.upload).toHaveBeenCalledTimes(1);
    expect(sendButton().disabled).toBe(true);
    await act(async () => upload.resolve(attachment));
    expect(container.querySelector(".attachment-chip")?.textContent).toContain("notes.txt");
    await act(async () => container.querySelector<HTMLButtonElement>(".attachment-chip button")!.click());
    expect(container.querySelector(".attachment-chip")).toBeNull();
    expect(sendButton().disabled).toBe(true);
  });

  it("禁用输入时阻止文件拖入默认打开文件，且不调用上传", async () => {
    props.disabled = true;
    await render();
    const drop = fileDragEvent("drop");
    await act(async () => container.querySelector(".composer")!.dispatchEvent(drop));
    expect(drop.defaultPrevented).toBe(true);
    expect(props.upload).not.toHaveBeenCalled();
  });

  it("运行中可写补充要求并自然换行，但不发送；完成后保留可发送草稿", async () => {
    props.activeRunId = "run-a";
    await render();
    expect(textarea().disabled).toBe(false);
    await type("补充要求");
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    await act(async () => textarea().dispatchEvent(enter));
    expect(enter.defaultPrevented).toBe(false);
    expect(props.onSend).not.toHaveBeenCalled();
    expect(sendButton().disabled).toBe(true);
    props.activeRunId = null;
    await render();
    expect(textarea().value).toBe("补充要求");
    expect(sendButton().disabled).toBe(false);
  });

  it("发送过初始想法后仍可准备重试，追加到现有草稿并聚焦，每个请求只消费一次", async () => {
    props.initialText = "初始想法";
    props.onDraftRequestConsumed = vi.fn();
    await render();
    await act(async () => sendButton().click());
    expect(textarea().value).toBe("");
    await type("保留我的补充");
    props.draftRequest = { id: "retry-1", text: "继续未完成的步骤" };
    await render();
    expect(textarea().value).toBe("保留我的补充\n\n继续未完成的步骤");
    expect(document.activeElement).toBe(textarea());
    expect(props.onDraftRequestConsumed).toHaveBeenCalledOnce();
    props.draftRequest = { id: "retry-1", text: "继续未完成的步骤" };
    await render();
    expect(props.onDraftRequestConsumed).toHaveBeenCalledOnce();
    expect(textarea().value).toBe("保留我的补充\n\n继续未完成的步骤");
    props.draftRequest = { id: "retry-2", text: "改用更短的方案" };
    await render();
    expect(textarea().value).toBe("保留我的补充\n\n继续未完成的步骤\n\n改用更短的方案");
  });

  it("跨会话草稿统一向宿主报告未保存状态，并在刷新时提示；全部发送后解除提示", async () => {
    props.onDraftDirtyChange = vi.fn();
    await render();
    expect(props.onDraftDirtyChange).toHaveBeenLastCalledWith(false);
    await type("会话 A 草稿");
    expect(props.onDraftDirtyChange).toHaveBeenLastCalledWith(true);
    props = { ...props, draftKey: "session-b" };
    await render();
    expect(textarea().value).toBe("");
    expect(props.onDraftDirtyChange).toHaveBeenLastCalledWith(true);
    const guarded = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(guarded);
    expect(guarded.defaultPrevented).toBe(true);
    props = { ...props, draftKey: "session-a" };
    await render();
    await act(async () => sendButton().click());
    expect(props.onDraftDirtyChange).toHaveBeenLastCalledWith(false);
    const unguarded = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unguarded);
    expect(unguarded.defaultPrevented).toBe(false);
  });
});
