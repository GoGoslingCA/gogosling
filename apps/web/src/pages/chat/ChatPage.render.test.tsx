import { act, useCallback, useSyncExternalStore } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, ApiEvent, Message, Session } from "../../api/types";
import { LiveRunStore } from "../../state/liveRunStore";
import { WorkspaceTabsProvider, useWorkspaceTabs } from "../../state/WorkspaceTabsProvider";
import { ChatPage } from "./ChatPage";
import { Markdown } from "./Markdown";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => api, useConnection: () => ({ api }) }));
vi.mock("../../state/EventStreamProvider", () => ({
  useLiveRunsDispatch: () => store.dispatch,
  useSessionLiveRuns: (sessionId: string | null) => {
    const subscribe = useCallback((listener: () => void) => store.subscribe(sessionId, listener), [sessionId]);
    const snapshot = useCallback(() => store.snapshot(sessionId), [sessionId]);
    return useSyncExternalStore(subscribe, snapshot, snapshot);
  },
}));
vi.mock("./Markdown", async (original) => {
  const actual = await original<typeof import("./Markdown")>();
  return { ...actual, Markdown: vi.fn(actual.Markdown) };
});

const at = "2026-10-13T01:00:00Z";
const agents: Agent[] = ["a", "b"].map(id => ({ id, name: id, model: { provider_id: "local", model: "test" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: `workspace-${id}`, enabled: true, created_at: at, updated_at: at }));
const sessions: Session[] = agents.map(agent => ({ id: agent.id, agent_id: agent.id, title: agent.name, key: `desktop:${agent.id}`, status: "active", origin: "desktop", created_at: at, updated_at: at }));
const histories = new Map(agents.map(agent => [agent.id, Array.from({ length: 100 }, (_, index): Message => ({
  id: `${agent.id}-${index}`, session_id: agent.id, seq: index + 1, role: "assistant", created_at: at,
  parts: [{ type: "text", text: `# History ${agent.id}-${index}\n**Unchanged** paragraph.` }, ...(index === 0 ? [{ type: "attachment" as const, attachment: { name: "notes.md", path: "inbound/notes.md", size: 10 } }] : [])],
}))]));
let store: LiveRunStore;
let api: ReturnType<typeof fixture>;
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let router: ReturnType<typeof createMemoryRouter>;
let seq: number;

function fixture() {
  return {
    system: { version: async () => ({ version: "test", capabilities: { "workbench.preview": 1 } }) },
    settings: { get: async () => ({ timezone: "UTC" }) },
    agents: { list: async () => agents },
    sessions: {
      get: async (id: string) => sessions.find(session => session.id === id),
      list: async () => sessions,
      listMessagesAfter: async (id: string) => histories.get(id) ?? [],
      upload: vi.fn(), send: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(),
    },
    runs: { get: vi.fn(), cancel: vi.fn() },
    workbench: {},
  };
}
function OpenFiles() {
  const tabs = useWorkspaceTabs();
  return <output data-open-files>{JSON.stringify(tabs?.tabs)}</output>;
}
async function ready(assertion: () => void) {
  await vi.waitFor(async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); }); assertion(); }, { interval: 10, timeout: 3000 });
}
async function mount() {
  router = createMemoryRouter([{ path: "/chat/:sessionId", element: <ChatPage /> }], { initialEntries: ["/chat/a"] });
  await act(async () => root.render(<QueryClientProvider client={client}><WorkspaceTabsProvider><RouterProvider router={router} /><OpenFiles /></WorkspaceTabsProvider></QueryClientProvider>));
  await ready(() => expect(host.querySelectorAll(".msg")).toHaveLength(100));
}
async function delta(sessionId: string, text: string) {
  const event: ApiEvent = { type: "text.delta", seq: ++seq, at, payload: { type: "text.delta", session_id: sessionId, run_id: `run-${sessionId}`, agent_id: sessionId, at, delta: text } };
  await act(async () => { store.receive(event); store.flush(); });
}
function historyRenderCount() { return vi.mocked(Markdown).mock.calls.filter(([props]) => props.text.startsWith("# History")).length; }

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: false, media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  seq = 0; store = new LiveRunStore(); api = fixture();
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); store.dispatch({ type: "reset" }); router?.dispose(); client.clear(); host.remove(); vi.clearAllMocks(); vi.unstubAllGlobals(); });

describe("聊天页流式更新的历史消息隔离", () => {
  it("100 条历史消息在 20 次真实会话更新时不重新执行 Markdown 渲染", async () => {
    await mount(); vi.mocked(Markdown).mockClear();
    for (let index = 0; index < 20; index++) await delta("a", "stream ");
    expect(historyRenderCount()).toBe(0);
    expect(vi.mocked(Markdown).mock.calls.filter(([props]) => props.text.startsWith("stream "))).toHaveLength(20);
    expect(host.querySelector(".messages")?.textContent).toContain("stream ".repeat(20));
    expect(host.querySelectorAll(".msg")).toHaveLength(101);
  });

  it("切换会话后文件动作使用新 Agent，后台流不重绘或混入当前会话", async () => {
    await mount(); await delta("a", "original stream");
    await act(async () => host.querySelector<HTMLButtonElement>(".preview-attachment-button")!.click());
    expect(host.querySelector("[data-open-files]")?.textContent).toContain('"agentId":"a"');
    await act(async () => { await router.navigate("/chat/b"); });
    await ready(() => expect(host.querySelector(".messages")?.textContent).toContain("History b-0"));
    vi.mocked(Markdown).mockClear(); await delta("a", "background-only");
    expect(historyRenderCount()).toBe(0);
    expect(host.querySelector(".messages")?.textContent).not.toContain("background-only");
    await delta("b", "current stream");
    await act(async () => host.querySelector<HTMLButtonElement>(".preview-attachment-button")!.click());
    const tabs = JSON.parse(host.querySelector("[data-open-files]")!.textContent!) as Array<{ agentId: string; path: string }>;
    expect(tabs.map(tab => [tab.agentId, tab.path])).toEqual([["a", "inbound/notes.md"], ["b", "inbound/notes.md"]]);
  });
});
