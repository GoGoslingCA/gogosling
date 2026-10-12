import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Message } from "../../api/types";
import { ArtifactPanel, type ArtifactPanelProps } from "./ArtifactPanel";

const { upload, toast } = vi.hoisted(() => ({ upload: vi.fn(), toast: vi.fn() }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => ({ sessions: { upload } }) }));
vi.mock("../../components/Toast", () => ({ useToast: () => ({ show: toast }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, values?: Record<string, unknown>) => key === "artifact.namedOption" ? `${values?.name} · ${values?.seq}` : key === "artifact.codeName" ? `${values?.language} ${values?.index}` : key }) }));

const messages: Message[] = [{ id: "m1", session_id: "s1", seq: 2, role: "assistant", parts: [{ type: "text", text: "初稿内容" }], created_at: "2026-10-12T00:00:00Z" }];
let host: HTMLDivElement;
let root: Root;
let queryClient: QueryClient;

function button(label: string): HTMLButtonElement {
  const found = [...host.querySelectorAll("button")].find((element) => element.textContent === label);
  if (!found) throw new Error(`缺少按钮 ${label}`);
  return found;
}

async function edit(text: string) {
  await act(async () => {
    const field = host.querySelector("textarea");
    if (!field) throw new Error("缺少编辑器");
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function render(onDirtyChange = vi.fn(), props: Partial<ArtifactPanelProps> = {}) {
  const sessionId = props.sessionId ?? "s1";
  await act(async () => root.render(<QueryClientProvider client={queryClient}><ArtifactPanel key={sessionId} sessionId={sessionId} messages={messages} onClose={vi.fn()} onDirtyChange={onDirtyChange} {...props} /></QueryClientProvider>));
}

async function settle() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
}

async function uploadedText(index = 0): Promise<string> {
  const file = upload.mock.calls[index][1] as File;
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });
}

async function selectResult(id: string) {
  await act(async () => {
    const select = host.querySelector("select") as HTMLSelectElement;
    select.value = id;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

beforeEach(() => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  upload.mockReset();
  toast.mockReset();
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  queryClient.clear();
  vi.restoreAllMocks();
});

describe("ArtifactPanel 保存", () => {
  it("保存失败保留编辑、给出可重试状态且不宣称成功", async () => {
    upload.mockRejectedValue(new Error("offline"));
    const onDirtyChange = vi.fn();
    await render(onDirtyChange);
    await edit("已经修改的内容");
    await act(async () => button("artifact.saveCopy").click());
    await settle();
    expect(upload).toHaveBeenCalledWith("s1", expect.any(File));
    expect(host.querySelector("textarea")?.value).toBe("已经修改的内容");
    expect(host.querySelector("[role=alert]")?.textContent).toContain("artifact.saveFailed");
    expect(button("artifact.saveCopy").disabled).toBe(false);
    expect(toast).not.toHaveBeenCalled();
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });

  it("保存上传时的版本，晚到响应不会把继续编辑的内容标记已保存", async () => {
    let resolveUpload!: (value: { name: string; path: string; size: number }) => void;
    upload.mockReturnValue(new Promise((resolve) => { resolveUpload = resolve; }));
    await render();
    await edit("第一版");
    await act(async () => button("artifact.saveCopy").click());
    await edit("保存时继续修改的第二版");
    await act(async () => resolveUpload({ name: "result.md", path: "inbound/copy/result.md", size: 9 }));
    await settle();
    expect(host.querySelector("textarea")?.value).toBe("保存时继续修改的第二版");
    expect(host.querySelector("[role=status]")?.textContent).toContain("artifact.unsaved");
    expect(host.querySelector(".artifact-saved-path")?.textContent).toContain("inbound/copy/result.md");
  });

  it("保存快捷键调用同一个副本上传入口", async () => {
    upload.mockResolvedValue({ name: "result.md", path: "inbound/result.md", size: 12 });
    await render();
    await act(async () => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", ctrlKey: true, cancelable: true })); });
    await settle();
    expect(upload).toHaveBeenCalledTimes(1);
    expect(host.querySelector(".artifact-saved-path")?.textContent).toContain("inbound/result.md");
  });

  it("隐藏面板保留编辑，离开窗口仍拦截未保存正文", async () => {
    const onClose = vi.fn();
    const onDirtyChange = vi.fn();
    await render(onDirtyChange, { onClose });
    await edit("不能丢失的修改");
    await act(async () => (host.querySelector("button[aria-label='artifact.close']") as HTMLButtonElement).click());
    expect(onClose).toHaveBeenCalledTimes(1);
    await render(onDirtyChange, { visible: false });
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    expect(host.querySelector("textarea")?.value).toBe("不能丢失的修改");
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    await render(onDirtyChange, { visible: true });
    expect(host.querySelector("textarea")?.value).toBe("不能丢失的修改");
  });

  it("面板隐藏或打开对话框时不拦截保存快捷键", async () => {
    await render(vi.fn(), { visible: false });
    const hiddenSave = new KeyboardEvent("keydown", { key: "s", ctrlKey: true, cancelable: true });
    await act(async () => { window.dispatchEvent(hiddenSave); });
    expect(hiddenSave.defaultPrevented).toBe(false);
    expect(upload).not.toHaveBeenCalled();
    await render(vi.fn(), { visible: true });
    const dialog = document.createElement("dialog");
    dialog.setAttribute("open", "");
    document.body.append(dialog);
    try {
      const modalSave = new KeyboardEvent("keydown", { key: "s", metaKey: true, cancelable: true });
      await act(async () => { window.dispatchEvent(modalSave); });
      expect(modalSave.defaultPrevented).toBe(false);
      expect(upload).not.toHaveBeenCalled();
    } finally { dialog.remove(); }
  });

  it("切换会话后旧保存响应不会污染新会话的正文或成功提示", async () => {
    let resolveUpload!: (value: { name: string; path: string; size: number }) => void;
    upload.mockReturnValue(new Promise((resolve) => { resolveUpload = resolve; }));
    await render();
    await edit("旧任务的草稿");
    await act(async () => button("artifact.saveCopy").click());
    await render(vi.fn(), { sessionId: "s2", messages: [{ ...messages[0], id: "m2", session_id: "s2", parts: [{ type: "text", text: "新任务的正文" }] }] });
    await act(async () => resolveUpload({ name: "old.md", path: "inbound/old.md", size: 12 }));
    await settle();
    expect(upload).toHaveBeenCalledWith("s1", expect.any(File));
    expect(host.querySelector("textarea")?.value).toBe("新任务的正文");
    expect(host.querySelector(".artifact-saved-path")).toBeNull();
    expect(toast).not.toHaveBeenCalled();
  });

  it("打开指定历史回复并保留其他成果编辑，重复请求和普通刷新可区分", async () => {
    const history: Message[] = [messages[0], { ...messages[0], id: "m2", seq: 4, parts: [{ type: "text", text: "最新回复" }] }];
    await render(vi.fn(), { messages: history });
    await edit("最新回复的修改");
    await render(vi.fn(), { messages: history, requestedMessageId: "m1", requestedVersion: 1 });
    expect(host.querySelector("textarea")?.value).toBe("初稿内容");
    await edit("历史回复的修改");
    await act(async () => {
      const select = host.querySelector("select") as HTMLSelectElement;
      select.value = "m2:document";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(host.querySelector("textarea")?.value).toBe("最新回复的修改");
    await render(vi.fn(), { messages: [...history], requestedMessageId: "m1", requestedVersion: 1 });
    expect(host.querySelector("textarea")?.value).toBe("最新回复的修改");
    await render(vi.fn(), { messages: history, requestedMessageId: "m1", requestedVersion: 2 });
    expect(host.querySelector("textarea")?.value).toBe("历史回复的修改");
  });

  it("保存后继续编辑时禁止把旧副本加入下次提问", async () => {
    upload.mockResolvedValue({ name: "result.md", path: "inbound/result.md", size: 12 });
    const onUseAsContext = vi.fn();
    await render(vi.fn(), { onUseAsContext });
    await act(async () => button("artifact.saveCopy").click());
    await settle();
    expect(button("artifact.useAsContext").disabled).toBe(false);
    await edit("有尚未保存的新内容");
    expect(button("artifact.useAsContext").disabled).toBe(true);
    await act(async () => button("artifact.useAsContext").click());
    expect(onUseAsContext).not.toHaveBeenCalled();
  });

  it("混合回复默认打开文档，用原稿标题和回复编号区分成果", async () => {
    const mixed = { ...messages[0], parts: [{ type: "text" as const, text: "# 发布计划\n\n```ts\nconst count = 1;\n```" }] };
    await render(vi.fn(), { messages: [mixed] });
    expect(host.querySelector("select")?.value).toBe("m1:document");
    expect(host.querySelector("option")?.textContent).toContain("发布计划 · 2");
    expect(host.querySelector("#artifact-filename")?.getAttribute("value")).toContain("发布计划");
    expect(host.querySelector("textarea")?.value).toContain("# 发布计划");
  });

  it("新增回复与同消息刷新都保留已打开原稿，恢复操作仍回到原先版本", async () => {
    const original = { ...messages[0], parts: [{ type: "text" as const, text: "# 第一版\n原始正文" }] };
    await render(vi.fn(), { messages: [original] });
    await edit("# 第一版\n正在编辑的内容");
    const revised = { ...original, parts: [{ type: "text" as const, text: "# 服务器新标题\n服务器变更" }] };
    const next = { ...original, id: "m2", seq: 4, parts: [{ type: "text" as const, text: "# 下一份\n新回复" }] };
    await render(vi.fn(), { messages: [revised, next] });
    expect(host.querySelector("select")?.value).toBe("m1:document");
    expect(host.querySelector("textarea")?.value).toBe("# 第一版\n正在编辑的内容");
    expect(host.querySelector("option[value='m1:document']")?.textContent).toContain("第一版");
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await act(async () => button("artifact.undo").click());
    expect(host.querySelector("textarea")?.value).toBe("# 第一版\n原始正文");
  });

  it("快捷键保存当前检查版本且阻止连按生成重复副本，隐藏重开仍恢复保存状态", async () => {
    upload.mockResolvedValue({ name: "result.md", path: "inbound/saved-copy.md", size: 12 });
    await render();
    await edit("当前正在检查的修改");
    const next = { ...messages[0], id: "m2", seq: 4, parts: [{ type: "text" as const, text: "新的助手回复" }] };
    await render(vi.fn(), { messages: [...messages, next] });
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", ctrlKey: true, cancelable: true }));
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", metaKey: true, cancelable: true }));
    });
    await settle();
    expect(upload).toHaveBeenCalledTimes(1);
    expect(await uploadedText()).toBe("当前正在检查的修改");
    expect(button("artifact.savedAction").disabled).toBe(true);
    await render(vi.fn(), { messages: [...messages, next], visible: false });
    await render(vi.fn(), { messages: [...messages, next], visible: true });
    expect(host.querySelector("textarea")?.value).toBe("当前正在检查的修改");
    expect(host.querySelector(".artifact-saved-path")?.textContent).toContain("inbound/saved-copy.md");
    await act(async () => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", ctrlKey: true, cancelable: true })); });
    expect(upload).toHaveBeenCalledTimes(1);
    await selectResult("m2:document");
    expect(button("artifact.saveCopy").disabled).toBe(false);
    await selectResult("m1:document");
    expect(button("artifact.savedAction").disabled).toBe(true);
  });

  it("保存失败后释放提交锁，重试可保存同一编辑版本", async () => {
    upload.mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ name: "result.md", path: "inbound/retried.md", size: 12 });
    await render();
    await edit("保留到重试的内容");
    await act(async () => button("artifact.saveCopy").click());
    await settle();
    await act(async () => button("artifact.saveCopy").click());
    await settle();
    expect(upload).toHaveBeenCalledTimes(2);
    expect(await uploadedText(1)).toBe("保留到重试的内容");
    expect(button("artifact.savedAction").disabled).toBe(true);
    expect(host.querySelector("[role=alert]")).toBeNull();
  });

  it("输入法合成中的保存组合键不提交成果", async () => {
    await render();
    const event = new KeyboardEvent("keydown", { key: "s", ctrlKey: true, isComposing: true, cancelable: true });
    await act(async () => { window.dispatchEvent(event); });
    expect(event.defaultPrevented).toBe(false);
    expect(upload).not.toHaveBeenCalled();
  });
});
