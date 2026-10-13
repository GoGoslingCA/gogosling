import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, Artifact, ArtifactInput, Message, Session, VersionInfo } from "../../api/types";
import { ChatPage } from "./ChatPage";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => api }));
vi.mock("../../state/EventStreamProvider", () => ({ useLiveRunsDispatch: () => dispatch, useSessionLiveRuns: () => ({ runs: emptyRuns, active: null }) }));
vi.mock("./SavedArtifactEditor", () => ({ SavedArtifactEditor: ({ artifactId }: { artifactId: string }) => <output aria-label="打开的成果">{artifactId}</output> }));

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
    workbench: {
      listArtifacts: vi.fn(async (scope: { session_id?: string }) => records.filter((record) => record.session_id === scope.session_id)),
      createArtifact: vi.fn(async (_sessionId: string, _input: ArtifactInput, _key?: string) => { records = [...records, savedA]; return savedA; }),
      getArtifact: vi.fn(), listDrafts: vi.fn(), createDraft: vi.fn(),
    },
  };
}
async function ready(assertion: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { interval: 10, timeout: 2500 }); }
async function mount(initialPath = "/chat/s1") {
  router = createMemoryRouter([{ path: "/chat/:sessionId", element: <ChatPage /> }], { initialEntries: [initialPath] });
  await act(async () => root.render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>));
  await ready(() => expect(host.querySelectorAll(".result-open")).toHaveLength(2));
}
async function openFirstReply() { await act(async () => host.querySelector<HTMLButtonElement>(".result-open")!.click()); }
function openedResult() { return host.querySelector("output[aria-label='打开的成果']")?.textContent; }

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  records = [savedB]; api = fixture(); dispatch.mockClear();
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: false, media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  vi.spyOn(window, "confirm").mockReturnValue(false);
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); router?.dispose(); client.clear(); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("ChatPage 成果入口兼容与打开目标", () => {
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
