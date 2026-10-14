import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, Artifact, ArtifactInput, Message, Session, VersionInfo } from "../../api/types";
import { ChatPage } from "./ChatPage";
import { queryKeys } from "../../state/queryKeys";
import { WorkspaceTabsProvider, useWorkspaceTabs } from "../../state/WorkspaceTabsProvider";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => api, useConnection: () => ({ api }) }));
vi.mock("../../state/EventStreamProvider", () => ({ useLiveRunsDispatch: () => dispatch, useSessionLiveRuns: () => ({ runs: emptyRuns, active: null }) }));
vi.mock("./SavedArtifactEditor", () => ({ SavedArtifactEditor: ({ artifactId, onDirtyChange }: { artifactId: string; onDirtyChange: (dirty: boolean) => void }) => <><output aria-label="打开的成果">{artifactId}</output><button data-dirty-editor onClick={() => onDirtyChange(true)}>draft</button></> }));

const dispatch = vi.fn();
const emptyRuns: [] = [];
const date = "2026-10-12T00:00:00Z";
const agent: Agent = { id: "agent-a", name: "助手", model: { provider_id: "provider-a", model: "local-test" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "workspace-a", enabled: true, created_at: date, updated_at: date };
const session: Session = { id: "s1", agent_id: agent.id, title: "当前任务", key: "desktop:s1", status: "active", origin: "desktop", created_at: date, updated_at: date };
const messages: Message[] = [
  { id: "message-a", session_id: "s1", seq: 2, role: "assistant", parts: [{ type: "text", text: "# 历史回复A\n需要编辑的原稿" }], created_at: date },
  { id: "message-b", session_id: "s1", seq: 4, role: "assistant", parts: [{ type: "text", text: "# 较新回复B\n另一份内容" }], created_at: date },
];
const savedB: Artifact = { id: "saved-b", agent_id: agent.id, session_id: "s1", title: "已保存B", kind: "document", filename: "b.md", media_type: "text/markdown", latest_revision_id: "b-r1", version: 1, source: { message_id: "message-b", message_seq: 4, fragment: "document", source_sha256: "hash", partial: false }, created_at: date, updated_at: date };
const savedA: Artifact = { ...savedB, id: "saved-a", title: "历史回复A", filename: "a.md", latest_revision_id: "a-r1", source: { ...savedB.source, message_id: "message-a", message_seq: 2 } };
let api: ReturnType<typeof fixture>;
let records: Artifact[];
let host: HTMLDivElement;
let root: Root;
let router: ReturnType<typeof createMemoryRouter>;
let client: QueryClient;
const dialogMethods = { showModal: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal"), close: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "close") };

function fixture() {
  return {
    system: { version: vi.fn(async (): Promise<VersionInfo> => ({ version: "dev", capabilities: { "workbench.artifacts": 1 } })) },
    settings: { get: vi.fn(async () => ({ timezone: "UTC" })) },
    agents: { list: vi.fn(async () => [agent]) },
    sessions: {
      list: vi.fn(async () => [session]), get: vi.fn(async (id: string) => ({ ...session, id })), listMessagesAfter: vi.fn(async (id: string) => id === "s1" ? messages : []),
      upload: vi.fn(async (_id: string, file: File) => ({ name: file.name, path: `inbound/${file.name}`, size: file.size })),
      send: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(),
    },
    runs: { get: vi.fn(), cancel: vi.fn() },
    preview: { list: vi.fn(async (_agentId: string, folder: string) => ({ items: folder ? [{ name: "design-check.md", path: "inbound/design-check.md", size: 12, is_dir: false }] : [{ name: "inbound", path: "inbound", size: 0, is_dir: true }], truncated: false })) },
    workbench: {
      listArtifacts: vi.fn(async (scope: { session_id?: string }) => records.filter((record) => record.session_id === scope.session_id)),
      createArtifact: vi.fn(async (_sessionId: string, _input: ArtifactInput, _key?: string) => { records = [...records, savedA]; return savedA; }),
      getArtifact: vi.fn(), listDrafts: vi.fn(), createDraft: vi.fn(),
    },
  };
}
async function ready(assertion: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { interval: 10, timeout: 2500 }); }
function ReadingControls() {
  const tabs = useWorkspaceTabs();
  return <><output data-reading-visible>{String(tabs?.visible)}</output><button data-return-task onClick={() => tabs?.store.hide()}>return</button></>;
}
async function mount(initialPath = "/chat/s1", reading = false) {
  router = createMemoryRouter([{ path: "/chat/:sessionId", element: <ChatPage /> }], { initialEntries: [initialPath] });
  await act(async () => root.render(<QueryClientProvider client={client}>{reading ? <WorkspaceTabsProvider><RouterProvider router={router} /><ReadingControls /></WorkspaceTabsProvider> : <RouterProvider router={router} />}</QueryClientProvider>));
  await ready(() => expect(host.querySelectorAll(".result-open")).toHaveLength(2));
}
async function openFirstReply() { await act(async () => host.querySelector<HTMLButtonElement>(".result-open")!.click()); }
function openedResult() { return host.querySelector("output[aria-label='打开的成果']")?.textContent; }

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  records = [savedB]; api = fixture(); dispatch.mockClear();
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: false, media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  vi.spyOn(window, "confirm").mockReturnValue(false);
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value(this: HTMLDialogElement) { this.open = true; } });
  Object.defineProperty(HTMLDialogElement.prototype, "close", { configurable: true, value(this: HTMLDialogElement) { this.open = false; } });
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount()); router?.dispose(); client.clear(); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals();
  for (const method of ["showModal", "close"] as const) { const descriptor = dialogMethods[method]; if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, method, descriptor); else Reflect.deleteProperty(HTMLDialogElement.prototype, method); }
});

describe("ChatPage 成果入口兼容与打开目标", () => {
  it("当前失败在输入区上方显示；切换任务清除提示且不重复请求运行状态", async () => {
    api.sessions.listMessagesAfter.mockResolvedValue([...messages, { id: "failure-user", session_id: "s1", run_id: "failed-run", seq: 5, role: "user", parts: [{ type: "text", text: "Synthetic task" }], created_at: date }]);
    api.runs.get.mockResolvedValue({ id: "failed-run", session_id: "s1", agent_id: agent.id, status: "failed", trigger: "user", usage: {}, created_at: date, ended_at: date, error: "Synthetic task failure" });
    await mount();
    await ready(() => expect(host.querySelector(".workspace-composer > .task-failure")?.textContent).toContain("taskFailure.message"));
    expect(host.querySelector(".workspace-composer")?.firstElementChild?.className).toBe("task-failure");
    expect(api.runs.get).toHaveBeenCalledExactlyOnceWith("failed-run");
    expect(host.textContent).not.toContain("taskFailure.report");
    await act(async () => { await router.navigate("/chat/s2"); });
    await ready(() => expect(host.querySelector(".task-failure")).toBeNull());
  });

  it("旧daemon缺能力时使用原副本编辑区，不探测或调用新成果草稿接口", async () => {
    api.system.version.mockResolvedValue({ version: "old-daemon" });
    const writes = vi.spyOn(Storage.prototype, "setItem");
    await mount(); await openFirstReply();
    await ready(() => expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label='artifact.editor']")?.value).toBe("# 历史回复A\n需要编辑的原稿"));
    expect(host.querySelector<HTMLElement>(".artifact-container")?.hidden).toBe(false);
    expect(api.workbench.listArtifacts).not.toHaveBeenCalled(); expect(api.workbench.createArtifact).not.toHaveBeenCalled();
    expect(api.workbench.listDrafts).not.toHaveBeenCalled(); expect(api.workbench.createDraft).not.toHaveBeenCalled();
    expect(openedResult()).toBeUndefined();
    // 最后打开位置是UI偏好，允许只存资源ID；正文不能因此落入浏览器存储。
    for (const [key, value] of writes.mock.calls) {
      expect(key).toBe("gogosling.ui.v1");
      expect(Object.keys(JSON.parse(String(value))).sort()).toEqual(["appearance", "artifactWidth", "pinnedSessionIds", "taskListVisible", "projectListVisible", "expandedProjectId", "toolsExpanded", "lastSessionId", "lastArtifactId"].sort());
      expect(String(value)).not.toContain("历史回复A"); expect(String(value)).not.toContain("需要编辑的原稿");
    }
  });

  it("明确点击历史回复才保存其原稿并打开，不使用最新回复正文", async () => {
    await mount(); expect(api.workbench.createArtifact).not.toHaveBeenCalled(); await openFirstReply();
    await ready(() => expect(openedResult()).toBe("saved-a"));
    expect(api.workbench.createArtifact).toHaveBeenCalledWith("s1", expect.objectContaining({ source_message_id: "message-a", fragment: "document", content: "# 历史回复A\n需要编辑的原稿" }), "source:message-a:document");
  });

  it("创建成果响应晚于会话切换时不把旧成果带到新会话", async () => {
    let finish!: (artifact: Artifact) => void;
    api.workbench.createArtifact.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    await mount(); await openFirstReply(); await ready(() => expect(api.workbench.createArtifact).toHaveBeenCalled());
    await act(async () => { await router.navigate("/chat/s2"); });
    await act(async () => finish(savedA));
    await ready(() => expect(api.sessions.get).toHaveBeenCalledWith("s2"));
    expect(router.state.location.pathname).toBe("/chat/s2"); expect(openedResult()).toBeUndefined();
  });

  it("用户在同会话打开B后，先前A的创建响应不能抢回当前成果", async () => {
    let finish!: (artifact: Artifact) => void;
    api.workbench.createArtifact.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    await mount(); await openFirstReply(); await ready(() => expect(api.workbench.createArtifact).toHaveBeenCalled());
    await act(async () => host.querySelector<HTMLButtonElement>(".saved-results-bar button")!.click());
    expect(openedResult()).toBe("saved-b");
    await act(async () => finish(savedA));
    await ready(() => expect(api.workbench.listArtifacts.mock.calls.length).toBeGreaterThan(2));
    expect(openedResult()).toBe("saved-b");
    expect(router.state.location.search).toContain("artifact=saved-b");
  });

  it("从历史任务列表切换会话时清除旧成果定位，保留有效列表过滤条件", async () => {
    api.sessions.list.mockResolvedValue([session, { ...session, id: "s2", title: "第二个任务" }]);
    await mount("/chat/s1?artifact=saved-b&agent=agent-a");
    await ready(() => expect(openedResult()).toBe("saved-b"));
    await act(async () => host.querySelector<HTMLButtonElement>(".chat-head button[title='work.searchShortcut']")!.click());
    await ready(() => expect(host.querySelectorAll(".history-drawer .session-item").length).toBeGreaterThan(1));
    const nextTask = [...host.querySelectorAll<HTMLButtonElement>(".history-drawer .session-item")].find((item) => item.textContent?.includes("第二个任务"))!;
    await act(async () => nextTask.click());
    await ready(() => expect(router.state.location.pathname).toBe("/chat/s2"));
    expect(router.state.location.search).not.toContain("artifact="); expect(router.state.location.search).toContain("agent=agent-a");
    expect(openedResult()).toBeUndefined();
  });
});

it("同任务切换成果和地址定位都保护尚未持久化的编辑", async () => {
  records = [savedB, savedA]; await mount("/chat/s1?artifact=saved-b"); await ready(() => expect(openedResult()).toBe("saved-b"));
  await act(async () => host.querySelector<HTMLButtonElement>("button[data-dirty-editor]")!.click());
  await act(async () => host.querySelectorAll<HTMLButtonElement>(".saved-results-bar button")[1]!.click());
  expect(openedResult()).toBe("saved-b"); expect(window.confirm).toHaveBeenCalledOnce();
  await act(async () => { await router.navigate("/chat/s1?artifact=saved-a"); });
  expect(openedResult()).toBe("saved-b"); expect(router.state.location.search).toBe("?artifact=saved-b"); expect(window.confirm).toHaveBeenCalledTimes(2);
  vi.mocked(window.confirm).mockReturnValue(true);
  await act(async () => host.querySelectorAll<HTMLButtonElement>(".saved-results-bar button")[1]!.click());
  await ready(() => expect(openedResult()).toBe("saved-a")); expect(window.confirm).toHaveBeenCalledTimes(3);
});

describe("历史与任务菜单的打开关闭", () => {
  const historyButton = () => host.querySelector<HTMLButtonElement>(".chat-head button[title='work.searchShortcut']")!;
  const menuTrigger = () => host.querySelector<HTMLElement>(".task-menu > summary")!;
  async function openHistory() { await act(async () => { historyButton().focus(); historyButton().click(); }); await ready(() => expect(host.querySelector(".history-drawer .session-list")).not.toBeNull()); }

  it("历史关闭即卸载列表，重新打开保留搜索、缓存和滚动，Esc返回原入口", async () => {
    api.sessions.list.mockResolvedValue([session, { ...session, id: "s2", title: "第二个任务" }]);
    await mount(); expect(host.querySelector(".history-drawer .session-list")).toBeNull();
    await openHistory();
    const input = host.querySelector<HTMLInputElement>(".history-filters input")!;
    expect(document.activeElement).toBe(input);
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "任务"); input.dispatchEvent(new Event("input", { bubbles: true })); });
    const list = host.querySelector<HTMLElement>(".history-drawer .session-list")!;
    list.scrollTop = 144;
    await act(async () => list.dispatchEvent(new Event("scroll")));
    const cached = client.getQueryData([...queryKeys.sessions(undefined), "list", "active"]);
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    expect(host.querySelector(".history-drawer .session-list")).toBeNull();
    expect(document.activeElement).toBe(historyButton());
    expect(client.getQueryData([...queryKeys.sessions(undefined), "list", "active"])).toBe(cached);
    await openHistory();
    expect(host.querySelector<HTMLInputElement>(".history-filters input")?.value).toBe("任务");
    expect(host.querySelector<HTMLElement>(".history-drawer .session-list")?.scrollTop).toBe(144);
  });

  it("从URL打开历史后选任务会清除history参数并保留筛选，避免自动重新展开", async () => {
    api.sessions.list.mockResolvedValue([session, { ...session, id: "s2", title: "第二个任务" }]);
    await mount("/chat/s1?history=1&agent=agent-a");
    await ready(() => expect(host.querySelectorAll(".history-drawer .session-item")).toHaveLength(2));
    const target = Array.from(host.querySelectorAll<HTMLButtonElement>(".history-drawer .session-item")).find(item => item.textContent?.includes("第二个任务"))!;
    await act(async () => target.click());
    await ready(() => expect(router.state.location.pathname).toBe("/chat/s2"));
    expect(router.state.location.search).not.toContain("history=");
    expect(router.state.location.search).toContain("agent=agent-a");
    expect(host.querySelector(".history-drawer .session-list")).toBeNull();
  });

  it("从全局导航切换任务会收起历史，不抢导航焦点且保留搜索与未发送草稿", async () => {
    api.sessions.list.mockResolvedValue([session, { ...session, id: "s2", title: "第二个任务" }]);
    await mount();
    const composer = host.querySelector<HTMLTextAreaElement>(".workspace-composer textarea")!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer, "来源任务的未发送内容"); composer.dispatchEvent(new Event("input", { bubbles: true })); });
    await openHistory();
    const searchInput = host.querySelector<HTMLInputElement>(".history-filters input")!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(searchInput, "任务"); searchInput.dispatchEvent(new Event("input", { bubbles: true })); });
    const navigation = document.createElement("button"); document.body.append(navigation);
    try {
      await act(async () => { navigation.focus(); await router.navigate("/chat/s2"); });
      await ready(() => expect(router.state.location.pathname).toBe("/chat/s2"));
      expect(host.querySelector(".history-drawer .session-list")).toBeNull();
      expect(document.activeElement).toBe(navigation);
      await openHistory();
      expect(host.querySelector<HTMLInputElement>(".history-filters input")?.value).toBe("任务");
      await act(async () => { await router.navigate("/chat/s1"); });
      await ready(() => expect(host.querySelector<HTMLTextAreaElement>(".workspace-composer textarea")?.value).toBe("来源任务的未发送内容"));
      expect(host.querySelector(".history-drawer .session-list")).toBeNull();
      expect(api.sessions.send).not.toHaveBeenCalled();
    } finally { navigation.remove(); }
  });

  it("跨任务的明确history请求仍保持或重新打开抽屉", async () => {
    api.sessions.list.mockResolvedValue([session, { ...session, id: "s2", title: "第二个任务" }]);
    await mount("/chat/s1?history=1");
    await ready(() => expect(host.querySelectorAll(".history-drawer .session-item")).toHaveLength(2));
    await act(async () => { await router.navigate("/chat/s2?history=1&agent=agent-a"); });
    await ready(() => expect(host.querySelectorAll(".history-drawer .session-item")).toHaveLength(2));
    expect(host.querySelector<HTMLSelectElement>(".history-filters select")?.value).toBe("agent-a");
    await act(async () => { await router.navigate("/chat/s1"); });
    expect(host.querySelector(".history-drawer .session-list")).toBeNull();
    await act(async () => { await router.navigate("/chat/s2?history=1"); });
    await ready(() => expect(host.querySelectorAll(".history-drawer .session-item")).toHaveLength(2));
    expect(document.activeElement).toBe(host.querySelector(".history-filters input"));
  });

  it("审批对话框存在时Esc和后台抽屉关闭都不夺走其焦点", async () => {
    await mount(); await openHistory();
    const dialog = document.createElement("dialog"); dialog.open = true;
    const approval = document.createElement("button"); dialog.append(approval); document.body.append(dialog);
    try {
      await act(async () => approval.focus());
      const escape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
      await act(async () => approval.dispatchEvent(escape));
      expect(escape.defaultPrevented).toBe(false);
      expect(host.querySelector(".history-drawer .session-list")).not.toBeNull();
      expect(document.activeElement).toBe(approval);
      await act(async () => host.querySelector<HTMLButtonElement>(".history-scrim")!.click());
      expect(host.querySelector(".history-drawer .session-list")).toBeNull();
      expect(document.activeElement).toBe(approval);
      expect(dialog.open).toBe(true);
    } finally { dialog.remove(); }
  });

  it("任务菜单外部点击、Esc与归档选择都会卸载菜单且不重复执行", async () => {
    api.sessions.update.mockResolvedValue({ ...session, status: "archived" });
    await mount();
    await act(async () => menuTrigger().click());
    expect(host.querySelector(".task-menu-items")).not.toBeNull();
    await act(async () => host.querySelector(".messages")!.dispatchEvent(new Event("pointerdown", { bubbles: true })));
    expect(host.querySelector(".task-menu-items")).toBeNull();
    await act(async () => menuTrigger().click());
    const first = host.querySelector<HTMLButtonElement>(".task-menu-items button")!;
    await act(async () => { first.focus(); first.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })); });
    expect(host.querySelector(".task-menu-items")).toBeNull(); expect(document.activeElement).toBe(menuTrigger());
    await act(async () => menuTrigger().click());
    await act(async () => Array.from(host.querySelectorAll<HTMLButtonElement>(".task-menu-items button")).find(button => button.textContent === "chat.archive")!.click());
    expect(host.querySelector(".task-menu-items")).toBeNull();
    expect(api.sessions.update).toHaveBeenCalledExactlyOnceWith("s1", { status: "archived" });
  });

  it("重命名开启原对话框前收起菜单，取消后可以重新打开", async () => {
    await mount(); await act(async () => menuTrigger().click());
    await act(async () => host.querySelector<HTMLButtonElement>(".task-menu-items button")!.click());
    expect(host.querySelector(".task-menu-items")).toBeNull();
    expect(host.querySelector<HTMLDialogElement>("dialog")?.open).toBe(true);
    await act(async () => host.querySelector<HTMLButtonElement>("dialog .dialog-head button")!.click());
    expect(host.querySelector("dialog")).toBeNull();
    await act(async () => menuTrigger().click());
    expect(host.querySelector(".task-menu-items")).not.toBeNull();
  });

  it("成果面板键盘调宽保存最新值，关闭后不会响应隐藏分隔条", async () => {
    await mount(); await openFirstReply(); await ready(() => expect(openedResult()).toBe("saved-a"));
    const separator = host.querySelector<HTMLElement>(".artifact-container [role='separator']")!;
    await act(async () => separator.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true })));
    expect(separator.getAttribute("aria-valuenow")).toBe("640");
    expect(JSON.parse(localStorage.getItem("gogosling.ui.v1")!).artifactWidth).toBe(640);
    await act(async () => separator.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true })));
    expect(separator.getAttribute("aria-valuenow")).toBe("620");
    expect(JSON.parse(localStorage.getItem("gogosling.ui.v1")!).artifactWidth).toBe(620);
    await act(async () => { await router.navigate("/chat/s2"); });
    await ready(() => expect(host.querySelector<HTMLElement>(".artifact-container")?.hidden).toBe(true));
    await act(async () => separator.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true })));
    expect(separator.getAttribute("aria-valuenow")).toBe("620");
  });
});

it("窄窗口阅读标签返回任务会显示对话，显式成果入口仍打开原未保存编辑器", async () => {
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: query === "(max-width: 1250px)", media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  api.system.version.mockResolvedValue({ version: "dev", capabilities: { "workbench.artifacts": 1, "workbench.preview": 1 } });
  await mount("/chat/s1?artifact=saved-b", true);
  await ready(() => expect(openedResult()).toBe("saved-b"));
  const retainedEditor = host.querySelector("output[aria-label='打开的成果']")!;
  await act(async () => host.querySelector<HTMLButtonElement>("[data-dirty-editor]")!.click());
  const input = host.querySelector<HTMLTextAreaElement>(".workspace-composer textarea")!;
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "继续讨论的未发送内容"); input.dispatchEvent(new Event("input", { bubbles: true })); });
  const filesButton = () => host.querySelector<HTMLButtonElement>(".chat-head button[aria-label='preview.files']")!;
  const artifactButton = () => host.querySelector<HTMLButtonElement>(".chat-head button[aria-label='work.openArtifact']")!;
  const openReadingFile = async () => {
    await act(async () => filesButton().click());
    await ready(() => expect(host.querySelector(".preview-entry")).not.toBeNull());
    const folder = Array.from(host.querySelectorAll<HTMLButtonElement>(".preview-entry")).find(button => button.textContent?.includes("inbound"));
    if (folder) await act(async () => folder.click());
    await ready(() => expect(host.querySelector(".preview-entry")?.textContent).toContain("design-check.md"));
    await act(async () => host.querySelector<HTMLButtonElement>(".preview-entry")!.click());
    await ready(() => expect(host.querySelector("[data-reading-visible]")?.textContent).toBe("true"));
  };
  await openReadingFile();
  await act(async () => host.querySelector<HTMLButtonElement>("[data-return-task]")!.click());
  expect(host.querySelector(".workbench")?.classList.contains("showing-conversation")).toBe(true);
  expect(host.querySelector("#conversation-tab")?.getAttribute("aria-selected")).toBe("true");
  expect(input.value).toBe("继续讨论的未发送内容");
  expect(host.querySelector("output[aria-label='打开的成果']")).toBe(retainedEditor);
  await act(async () => artifactButton().click());
  expect(host.querySelector(".workbench")?.classList.contains("showing-artifact")).toBe(true);
  expect(openedResult()).toBe("saved-b");
  await openReadingFile();
  await act(async () => artifactButton().click());
  expect(host.querySelector("[data-reading-visible]")?.textContent).toBe("false");
  expect(host.querySelector(".workbench")?.classList.contains("showing-artifact")).toBe(true);
  expect(host.querySelector("output[aria-label='打开的成果']")).toBe(retainedEditor);
  expect(window.confirm).not.toHaveBeenCalled();
  expect(api.sessions.send).not.toHaveBeenCalled();
});
