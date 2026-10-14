import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceTabsProvider, useWorkspaceTabs } from "../state/WorkspaceTabsProvider";
import { WorkspacePane } from "./WorkspacePane";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import type { PreviewDocument } from "../content/types";
import type { WorkspaceBrowserBridge, WorkspaceBrowserState } from "../api/workspaceBrowser";
const mocks = vi.hoisted(() => ({ read: vi.fn(), content: vi.fn(), onResize: vi.fn(), onResizeEnd: vi.fn(), api: {} as object | null }));
vi.mock("../state/ConnectionContext", () => ({ useConnection: () => ({ api: mocks.api }), useApi: () => ({ preview: { read: mocks.read, content: mocks.content } }) }));
vi.mock("../state/useWorkbenchCapabilities", () => ({ useWorkbenchCapabilities: () => ({ preview: true, previewLayout: false }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
let root: Root; let host: HTMLDivElement; let client: QueryClient;
let controls: NonNullable<ReturnType<typeof useWorkspaceTabs>>;
let listeners: ((state: WorkspaceBrowserState) => void)[];
let resize: (() => void)[];
const documentResult: PreviewDocument = { name: "notes.md", kind: "markdown", format: "markdown", size: 12, mime: "text/markdown", version: "1", text: "# Notes\nHello" };
function Fixture() { const tabs = useWorkspaceTabs()!; controls = tabs; return <><WorkspaceSidebar /><output>{tabs.activeId ?? ""}</output>{tabs.visible ? <WorkspacePane width={500} onResize={mocks.onResize} onResizeEnd={mocks.onResizeEnd} /> : null}</>; }
async function mount() { await act(async () => root.render(<QueryClientProvider client={client}><WorkspaceTabsProvider><Fixture /></WorkspaceTabsProvider></QueryClientProvider>)); }
async function ready(check: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); }); check(); }, { timeout: 2500, interval: 10 }); }
function nativeBridge() {
  const state = (tabId: string, url: string): WorkspaceBrowserState => ({ tabId, url, title: new URL(url).hostname, loading: false, canGoBack: false, canGoForward: false });
  const bridge = {
    show: vi.fn(async (input: { tabId: string; url: string }) => state(input.tabId, input.url)), hide: vi.fn(async () => undefined), close: vi.fn(async (): Promise<WorkspaceBrowserState | null> => null),
    navigate: vi.fn(async (input: { tabId: string; url: string }) => state(input.tabId, input.url)), action: vi.fn(async () => state(controls.activeId!, "https://example.com/")),
    onState: vi.fn((callback: (value: WorkspaceBrowserState) => void) => { listeners.push(callback); return () => { listeners = listeners.filter(value => value !== callback); }; }),
  } satisfies WorkspaceBrowserBridge;
  window.gogosling = { workspaceBrowser: bridge }; return bridge;
}
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  mocks.api = {}; mocks.read.mockReset(); mocks.content.mockReset(); mocks.onResize.mockReset(); mocks.onResizeEnd.mockReset(); mocks.read.mockResolvedValue(documentResult); listeners = []; resize = [];
  host = document.createElement("div"); document.body.append(host); root = createRoot(host); client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.stubGlobal("ResizeObserver", class { constructor(callback: () => void) { resize.push(callback); } observe() {} disconnect() {} });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ x: 200, y: 90, width: 500, height: 560, top: 90, left: 200, right: 700, bottom: 650, toJSON() {} });
  vi.spyOn(HTMLElement.prototype, "getClientRects").mockImplementation(function(this: HTMLElement) { return (this.hasAttribute("data-visible") ? [this.getBoundingClientRect()] : []) as unknown as DOMRectList; });
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); delete window.gogosling; vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("共享阅读区", () => {
  it("松手先于下一帧时保存最终宽度，并取消旧拖动帧", async () => {
    await mount(); await act(async () => { controls.store.openFile({ agentId: "a", path: "notes.md" }); });
    await ready(() => expect(host.querySelector(".content-preview")).not.toBeNull());
    const callbacks = new Map<number, FrameRequestCallback>();
    vi.stubGlobal("requestAnimationFrame", vi.fn((callback: FrameRequestCallback) => { callbacks.set(1, callback); return 1; }));
    vi.stubGlobal("cancelAnimationFrame", vi.fn((id: number) => { callbacks.delete(id); }));
    const separator = host.querySelector<HTMLDivElement>('[role="separator"]')!;
    let captured = false; separator.setPointerCapture = () => { captured = true; }; separator.hasPointerCapture = () => captured; separator.releasePointerCapture = () => { captured = false; };
    await act(async () => {
      for (const [type, clientX] of [["pointerdown", 800], ["pointermove", 750], ["pointermove", 720], ["pointerup", 700]] as const) {
        const event = new MouseEvent(type, { bubbles: true, clientX, button: 0 }); Object.defineProperty(event, "pointerId", { value: 1 }); separator.dispatchEvent(event);
      }
    });
    expect(mocks.onResize.mock.calls).toEqual([[600]]); expect(mocks.onResizeEnd.mock.calls).toEqual([[600]]); expect(callbacks.size).toBe(0); expect(captured).toBe(false);
  });
  it("只挂载活动文件，切换会取消旧读取，迟到正文不能覆盖当前文件", async () => {
    let finish!: (value: PreviewDocument) => void;
    mocks.read.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await mount(); await act(async () => { controls.store.openFile({ agentId: "a", path: "one.md" }); });
    await ready(() => expect(mocks.read).toHaveBeenCalledOnce()); const signal = mocks.read.mock.calls[0]![2] as AbortSignal;
    await act(async () => { controls.store.openFile({ agentId: "b", path: "two.docx" }); });
    await ready(() => expect(mocks.read).toHaveBeenCalledTimes(2)); expect(signal.aborted).toBe(true);
    await act(async () => finish({ ...documentResult, text: "STALE" }));
    await ready(() => expect(host.textContent).toContain("Hello")); expect(host.textContent).not.toContain("STALE");
    expect(host.querySelectorAll(".content-preview")).toHaveLength(1); expect(controls.tabs).toHaveLength(2); expect(mocks.read.mock.calls[1]?.slice(0, 2)).toEqual(["b", "two.docx"]);
    await act(async () => controls.store.hide()); expect(host.querySelector(".content-preview")).toBeNull(); expect(controls.tabs).toHaveLength(2);
  });
  it("文件解析失败可切换其他文件；关闭当前文件保留相邻位置", async () => {
    mocks.read.mockRejectedValueOnce(new Error("synthetic failure")); await mount();
    await act(async () => { controls.store.openFile({ agentId: "a", path: "broken.docx" }); });
    await ready(() => expect(host.textContent).toContain("synthetic failure"));
    await act(async () => { controls.store.openFile({ agentId: "a", path: "good.md" }); });
    await ready(() => expect(host.textContent).toContain("Hello"));
    await act(async () => controls.store.close(controls.activeId!)); expect(controls.tabs[0]?.kind).toBe("file"); expect(controls.tabs).toHaveLength(1);
  });
  it("标签及网页地址只留内存，切换连接清除文件并隐藏网页，保留可返回的网页入口", async () => {
    const storage = vi.spyOn(Storage.prototype, "setItem"); const bridge = nativeBridge(); await mount();
    await act(async () => { controls.store.openFile({ agentId: "a", path: "private.md" }); controls.store.openWeb("https://example.com/private?ref=sample"); });
    await ready(() => expect(bridge.show).toHaveBeenCalled()); const webId = controls.activeId;
    mocks.api = {}; await mount(); expect(controls.tabs).toHaveLength(1); expect(controls.tabs[0]?.kind).toBe("web"); expect(controls.visible).toBe(false);
    expect(host.querySelector(".workspace-pane")).toBeNull(); expect(bridge.hide).toHaveBeenCalled(); expect(bridge.close).not.toHaveBeenCalled();
    await act(async () => controls.store.activate(webId!)); await ready(() => expect(host.querySelector(".workspace-web-view")).not.toBeNull());
    mocks.api = null; await mount(); expect(controls.visible).toBe(false); expect(controls.tabs[0]?.id).toBe(webId); expect(storage).not.toHaveBeenCalled();
  });
  it("旧宿主不嵌入网页或调用接口，明确提供独立浏览器入口", async () => {
    await mount(); await act(async () => { controls.store.openWeb("https://example.com/docs"); });
    await ready(() => expect(host.querySelector(".workspace-web-fallback a")?.getAttribute("href")).toBe("https://example.com/docs"));
    expect(host.querySelector("iframe")).toBeNull(); expect(host.querySelector("webview")).toBeNull(); expect(mocks.read).not.toHaveBeenCalled();
  });
  it("网页取消切换时恢复原标签，不把原网页显示成新标题", async () => {
    const bridge = nativeBridge(); await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); });
    await ready(() => expect(bridge.show).toHaveBeenCalledOnce()); const original = controls.activeId!;
    bridge.show.mockResolvedValueOnce({ tabId: original, url: "https://example.com/one", title: "Original", loading: false, canGoBack: false, canGoForward: false, error: "cancelled" });
    await act(async () => { controls.store.openWeb("https://example.com/two"); });
    await ready(() => expect(controls.activeId).toBe(original)); expect(controls.tabs).toHaveLength(2);
  });
  it("拒绝关闭网页时保留标签和表单入口", async () => {
    const bridge = nativeBridge(); await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); });
    await ready(() => expect(bridge.show).toHaveBeenCalled()); const original = controls.activeId!;
    bridge.close.mockResolvedValueOnce({ tabId: original, url: "https://example.com/one", title: "Original", loading: false, canGoBack: false, canGoForward: false, error: "cancelled" });
    await act(async () => host.querySelector<HTMLButtonElement>(".workspace-close-tab")!.click());
    expect(controls.tabs).toHaveLength(1); expect(controls.activeId).toBe(original);
    await act(async () => host.querySelector<HTMLButtonElement>(".workspace-close-tab")!.click());
    expect(controls.tabs).toHaveLength(0); expect(host.querySelector(".workspace-pane")).toBeNull();
  });
  it("展开普通任务详情不挡网页；真实弹窗打开时隐藏，关闭后恢复", async () => {
    const details = document.createElement("details"); details.className = "task-failure-details"; details.open = true; details.setAttribute("data-visible", ""); document.body.append(details);
    const bridge = nativeBridge(); await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); });
    await ready(() => expect(bridge.show).toHaveBeenCalledOnce()); const before = bridge.hide.mock.calls.length;
    const dialog = document.createElement("dialog"); dialog.open = true; dialog.setAttribute("data-visible", "");
    await act(async () => document.body.append(dialog)); await ready(() => expect(bridge.hide.mock.calls.length).toBeGreaterThan(before));
    await act(async () => dialog.remove()); await ready(() => expect(bridge.show).toHaveBeenCalledTimes(2)); details.remove();
  });
  it("聊天DOM更新及相同尺寸通知不会反复调用原生布局", async () => {
    const bridge = nativeBridge(); await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); }); await ready(() => expect(bridge.show).toHaveBeenCalledOnce());
    const paragraph = document.createElement("p"); document.body.append(paragraph);
    await act(async () => { for (let i = 0; i < 20; i++) { paragraph.textContent = String(i); resize.forEach(callback => callback()); } await new Promise(resolve => setTimeout(resolve, 60)); });
    expect(bridge.show).toHaveBeenCalledOnce(); paragraph.remove();
  });
  it("文件已选中后忽略迟到网页结果和状态", async () => {
    const bridge = nativeBridge(); let finish!: (value: WorkspaceBrowserState) => void; bridge.show.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); }); await ready(() => expect(bridge.show).toHaveBeenCalledOnce()); const webId = controls.activeId!; const callback = listeners[0]!;
    await act(async () => { controls.store.openFile({ agentId: "a", path: "notes.md" }); }); const fileId = controls.activeId;
    const late = { tabId: webId, url: "https://example.com/late", title: "LATE", loading: false, canGoBack: false, canGoForward: false };
    await act(async () => { finish(late); callback(late); }); expect(controls.activeId).toBe(fileId); expect(controls.tabs.find(tab => tab.id === webId)?.title).toBe("example.com"); expect(listeners).toHaveLength(0);
  });
});

it("网页标题更新不覆盖正在编辑的地址，外部入口指向当前网页", async () => {
  const bridge = nativeBridge(); await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); }); await ready(() => expect(bridge.show).toHaveBeenCalledOnce());
  const input = host.querySelector<HTMLInputElement>(".workspace-web-toolbar input")!;
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "https://example.org/next"); input.dispatchEvent(new Event("input", { bubbles: true })); });
  await act(async () => listeners.forEach(listener => listener({ tabId: controls.activeId!, url: "https://example.com/one", title: "Loaded title", loading: false, canGoBack: false, canGoForward: false })));
  expect(input.value).toBe("https://example.org/next"); expect(host.querySelector(".workspace-web-toolbar a")?.getAttribute("href")).toBe("https://example.com/one");
  await act(async () => host.querySelector(".workspace-web-toolbar")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
  expect(bridge.navigate).toHaveBeenCalledWith({ tabId: controls.activeId, url: "https://example.org/next" });
});
it("键盘关闭标签后焦点落在相邻项，关闭最后一项回到新增入口", async () => {
  await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); controls.store.openWeb("https://example.com/two"); });
  const buttons = host.querySelectorAll<HTMLButtonElement>(".workspace-close-tab");
  await act(async () => { buttons[1]!.focus(); buttons[1]!.click(); });
  await ready(() => expect(document.activeElement).toBe(host.querySelector(".workspace-open-link")));
  await act(async () => host.querySelector<HTMLButtonElement>(".workspace-close-tab")!.click());
  await ready(() => expect(document.activeElement).toBe(host.querySelector('button[aria-label="workspaceTabs.addWeb"]')));
});
it("原生拒绝新网页时显示错误，修改地址仍能提交重试", async () => {
  const bridge = nativeBridge(); bridge.show.mockImplementationOnce(async input => ({ tabId: input.tabId, url: input.url, title: "", loading: false, canGoBack: false, canGoForward: false, error: "blocked_url" }));
  await mount(); await act(async () => { controls.store.openWeb("https://localhost/"); }); await ready(() => expect(host.querySelector('[role="alert"]')?.textContent).toBe("workspaceTabs.blocked_url"));
  const input = host.querySelector<HTMLInputElement>(".workspace-web-toolbar input")!;
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "https://example.com/"); input.dispatchEvent(new Event("input", { bubbles: true })); });
  await act(async () => host.querySelector(".workspace-web-toolbar")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
  expect(bridge.navigate).toHaveBeenCalledWith({ tabId: controls.activeId, url: "https://example.com/" }); await ready(() => expect(host.querySelector('[role="alert"]')).toBeNull());
});
it("重新加载界面恢复原网页入口，但迟到回执不抢走当前文件", async () => {
  const bridge = nativeBridge(); let finish!: (value: WorkspaceBrowserState | null) => void;
  window.gogosling!.workspaceBrowser = { ...bridge, current: vi.fn(() => new Promise<WorkspaceBrowserState | null>(resolve => { finish = resolve; })) };
  await mount(); await act(async () => { controls.store.openFile({ agentId: "a", path: "notes.md" }); }); const selected = controls.activeId;
  await act(async () => finish({ tabId: "native-surviving-page", url: "https://example.com/form", title: "Draft form", loading: false, canGoBack: false, canGoForward: false }));
  expect(controls.activeId).toBe(selected); expect(controls.visible).toBe(true); expect(controls.tabs).toHaveLength(2); expect(bridge.show).not.toHaveBeenCalled();
  await act(async () => controls.store.activate("native-surviving-page")); await ready(() => expect(bridge.show).toHaveBeenCalledWith(expect.objectContaining({ tabId: "native-surviving-page", url: "https://example.com/form" })));
});
it("换页未完成时调整窗口，较早的取消回执仍恢复原网页", async () => {
  const bridge = nativeBridge(); await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); }); await ready(() => expect(bridge.show).toHaveBeenCalledOnce()); const original = controls.activeId!;
  let finish!: (state: WorkspaceBrowserState) => void; bridge.show.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  await act(async () => { controls.store.openWeb("https://example.org/two"); }); await ready(() => expect(bridge.show).toHaveBeenCalledTimes(2)); const requested = controls.activeId!;
  bridge.show.mockResolvedValueOnce({ tabId: requested, url: "https://example.org/two", title: "", loading: false, canGoBack: false, canGoForward: false, error: "busy" });
  vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue({ x: 200, y: 90, width: 510, height: 560, top: 90, left: 200, right: 710, bottom: 650, toJSON() {} });
  await act(async () => resize.forEach(callback => callback())); await ready(() => expect(bridge.show).toHaveBeenCalledTimes(3));
  const cancelled: WorkspaceBrowserState = { tabId: original, url: "https://example.com/one", title: "Original", loading: false, canGoBack: false, canGoForward: false, error: "cancelled" };
  await act(async () => { for (const callback of [...listeners]) callback(cancelled); finish(cancelled); });
  await ready(() => expect(controls.activeId).toBe(original)); expect(controls.tabs).toHaveLength(2);
});
it("关闭后台网页的取消回执不抢走后来选中的文件", async () => {
  const bridge = nativeBridge(); await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); }); await ready(() => expect(bridge.show).toHaveBeenCalledOnce()); const original = controls.activeId!;
  let finish!: (state: WorkspaceBrowserState | null) => void; bridge.close.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  await act(async () => host.querySelector<HTMLButtonElement>(".workspace-close-tab")!.click());
  await act(async () => { controls.store.openFile({ agentId: "a", path: "notes.md" }); }); const activeFile = controls.activeId;
  await act(async () => finish({ tabId: original, url: "https://example.com/one", title: "Original", loading: false, canGoBack: false, canGoForward: false, error: "cancelled" }));
  expect(controls.activeId).toBe(activeFile); expect(controls.tabs).toHaveLength(2);
});
it("快速换页遇到原生busy会有界重试，恢复后展示最新目标", async () => {
  const bridge = nativeBridge(); bridge.show.mockImplementationOnce(async input => ({ tabId: input.tabId, url: input.url, title: "", loading: false, canGoBack: false, canGoForward: false, error: "busy" }));
  await mount(); await act(async () => { controls.store.openWeb("https://example.com/latest"); });
  await ready(() => expect(bridge.show).toHaveBeenCalledTimes(2)); expect(bridge.show.mock.calls[1]?.[0].url).toBe("https://example.com/latest");
  await ready(() => expect(host.querySelector('[role="alert"]')).toBeNull());
});
it("切到文件会取消等待中的网页busy重试，不保留后台轮询", async () => {
  const bridge = nativeBridge(); bridge.show.mockImplementation(async input => ({ tabId: input.tabId, url: input.url, title: "", loading: false, canGoBack: false, canGoForward: false, error: "busy" }));
  await mount(); await act(async () => { controls.store.openWeb("https://example.com/one"); }); await ready(() => expect(bridge.show).toHaveBeenCalledOnce());
  await act(async () => { controls.store.openFile({ agentId: "a", path: "notes.md" }); });
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 350)); }); expect(bridge.show).toHaveBeenCalledOnce();
});
