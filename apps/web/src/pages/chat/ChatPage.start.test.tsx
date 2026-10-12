import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import type { Agent, Attachment, Message, Run, SendMessageInput, Session, SessionCreateInput } from "../../api/types";
import { queryKeys } from "../../state/queryKeys";
import type { LiveRun } from "../../state/liveRuns";
import { ChatPage } from "./ChatPage";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => api }));
vi.mock("../../state/EventStreamProvider", () => ({
  useLiveRunsDispatch: () => dispatch,
  useSessionLiveRuns: () => ({ runs: liveRuns, active: null }),
}));

const agent: Agent = { id: "agent-a", name: "写作助手", model: { provider_id: "provider-a", model: "test-model" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "/workspace/agent-a", enabled: true, created_at: "2026-10-12T01:00:00Z", updated_at: "2026-10-12T01:00:00Z" };
const session: Session = { id: "session-new", agent_id: agent.id, title: "新任务", origin: "desktop", key: "desktop:agent-a", status: "active", created_at: "2026-10-12T01:00:00Z", updated_at: "2026-10-12T01:00:00Z" };
const run: Run = { id: "run-new", session_id: session.id, agent_id: agent.id, status: "queued", trigger: "user", usage: {}, created_at: "2026-10-12T01:00:01Z" };
const liveRuns: LiveRun[] = [];
const dispatch = vi.fn();
let root: Root;
let container: HTMLDivElement;
let router: ReturnType<typeof createMemoryRouter>;
let client: QueryClient;
let api: ReturnType<typeof fixture>;
let operations: string[];

function fixture() {
  let storedSession: Session | null = null;
  let storedMessages: Message[] = [];
  return {
    agents: { list: vi.fn(async () => [agent]) },
    settings: { get: vi.fn(async () => ({ timezone: "UTC", locale: "zh" })) },
    sessions: {
      list: vi.fn(async () => storedSession ? [storedSession] : []),
      get: vi.fn(async (_id: string) => storedSession ?? session),
      listMessagesAfter: vi.fn(async (_id: string, _after: number) => storedMessages),
      create: vi.fn(async (_input: SessionCreateInput) => { operations.push("create"); storedSession = { ...session }; return { ...session }; }),
      upload: vi.fn(async (_id: string, file: File): Promise<Attachment> => { operations.push(`upload:${file.name}`); return { name: file.name, path: `inbound/${file.name}`, size: file.size }; }),
      send: vi.fn(async (_id: string, input: SendMessageInput) => {
        operations.push("send");
        storedSession = { ...session, active_run_id: run.id };
        storedMessages = [{ id: "user-goal", session_id: session.id, run_id: run.id, seq: 1, role: "user", parts: [{ type: "text", text: input.text }], created_at: run.created_at }];
        return run;
      }),
      update: vi.fn(async () => session),
      remove: vi.fn(async () => undefined),
    },
    runs: { get: vi.fn(async (_id: string) => run), cancel: vi.fn(async (_id: string) => undefined) },
  };
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  operations = [];
  api = fixture();
  dispatch.mockReset();
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: false, media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  vi.spyOn(window, "confirm").mockReturnValue(false);
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  router?.dispose();
  client.clear();
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function ready(assertion: () => void) {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    assertion();
  }, { timeout: 2000, interval: 10 });
}
async function mount() {
  router = createMemoryRouter([{ path: "/chat/:sessionId?", element: <ChatPage /> }, { path: "/settings", element: <p>settings-page</p> }], { initialEntries: ["/chat"] });
  await act(async () => root.render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>));
  await ready(() => expect(container.querySelector<HTMLSelectElement>("#start-agent")?.value).toBe(agent.id));
}
function homeInput() { return container.querySelector<HTMLTextAreaElement>(".start-composer textarea")!; }
async function typeGoal(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(homeInput(), value);
    homeInput().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function chooseFiles(files: File[]) {
  await act(async () => {
    const input = container.querySelector('.start-composer input[type="file"]')!;
    Object.defineProperty(input, "files", { configurable: true, value: files });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
async function submit() {
  await act(async () => container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.click());
}

describe("首页一步开始任务的真实页面编排", () => {
  it("输入正文和文件后依次创建、上传、发送，成功导航不触发自己的草稿离开确认", async () => {
    // 会话详情请求仍在等待时，发送响应已经足以恢复当前运行和取消入口。
    api.sessions.get.mockImplementation(() => new Promise<Session>(() => undefined));
    await mount();
    const file = new File(["需求正文"], "requirements.md");
    await typeGoal("  整理需求并生成第一稿  ");
    await chooseFiles([file]);
    await submit();
    await ready(() => expect(router.state.location.pathname).toBe(`/chat/${session.id}`));
    expect(operations).toEqual(["create", "upload:requirements.md", "send"]);
    expect(api.sessions.create).toHaveBeenCalledOnce();
    expect(api.sessions.upload).toHaveBeenCalledWith(session.id, file);
    expect(api.sessions.send).toHaveBeenCalledWith(session.id, { text: "整理需求并生成第一稿", attachments: [{ name: file.name, path: `inbound/${file.name}`, size: file.size }] });
    expect(window.confirm).not.toHaveBeenCalled();
    expect(client.getQueryData<Session>(queryKeys.session(session.id))?.active_run_id).toBe(run.id);
    expect(client.getQueryData<Run>(queryKeys.run(run.id))).toEqual(run);
    await ready(() => expect(container.querySelector(".composer")?.textContent).toContain("chat.cancel"));
    expect(container.querySelector<HTMLButtonElement>('.composer button[type="submit"]')?.disabled).toBe(true);
  });

  it("第二份资料上传失败后保留首页内容，重试不重复建任务或上传第一份成功资料", async () => {
    let uploads = 0;
    api.sessions.upload.mockImplementation(async (_id, file) => {
      operations.push(`upload:${file.name}`);
      uploads += 1;
      if (uploads === 2) throw new ApiError("network", "upload failed");
      return { name: file.name, path: `inbound/${file.name}`, size: file.size };
    });
    await mount();
    const files = [new File(["a"], "a.md"), new File(["b"], "b.md")];
    await typeGoal("整理两份资料");
    await chooseFiles(files);
    await submit();
    await ready(() => expect(container.querySelector(".start-feedback")).not.toBeNull());
    expect(router.state.location.pathname).toBe("/chat");
    expect(homeInput().value).toBe("整理两份资料");
    expect(container.querySelectorAll(".start-composer .attachment-chip")).toHaveLength(2);
    expect(container.querySelector<HTMLSelectElement>("#start-agent")?.disabled).toBe(true);
    expect(api.sessions.send).not.toHaveBeenCalled();
    await submit();
    await ready(() => expect(router.state.location.pathname).toBe(`/chat/${session.id}`));
    expect(api.sessions.create).toHaveBeenCalledOnce();
    expect(api.sessions.upload).toHaveBeenCalledTimes(3);
    expect(api.sessions.send).toHaveBeenCalledOnce();
    expect(operations).toEqual(["create", "upload:a.md", "upload:b.md", "upload:b.md", "send"]);
    expect(window.confirm).not.toHaveBeenCalled();
  });

  it("新建空任务只创建并导航，不调用上传或发送", async () => {
    await mount();
    const empty = Array.from(container.querySelectorAll<HTMLButtonElement>(".start-composer button")).find((button) => button.textContent === "phase2.emptyTask")!;
    await act(async () => empty.click());
    await ready(() => expect(router.state.location.pathname).toBe(`/chat/${session.id}`));
    expect(api.sessions.create).toHaveBeenCalledWith({ agent_id: agent.id, title: undefined });
    expect(api.sessions.create).toHaveBeenCalledOnce();
    expect(api.sessions.upload).not.toHaveBeenCalled();
    expect(api.sessions.send).not.toHaveBeenCalled();
    expect(window.confirm).not.toHaveBeenCalled();
  });

  it("开始请求未返回时用户已离开首页，迟到响应继续原任务但不能抢回导航", async () => {
    let finishCreate!: (value: Session) => void;
    api.sessions.create.mockImplementation(() => new Promise<Session>((resolve) => { operations.push("create"); finishCreate = resolve; }));
    vi.mocked(window.confirm).mockReturnValue(true);
    await mount();
    await typeGoal("留给原任务的要求");
    const file = new File(["需求"], "original.md");
    await chooseFiles([file]);
    await submit();
    expect(api.sessions.create).toHaveBeenCalledOnce();
    await act(async () => { await router.navigate("/settings"); });
    await ready(() => expect(router.state.location.pathname).toBe("/settings"));
    expect(window.confirm).toHaveBeenCalled();
    await act(async () => finishCreate(session));
    await ready(() => expect(api.sessions.send).toHaveBeenCalledOnce());
    expect(api.sessions.upload).toHaveBeenCalledWith(session.id, file);
    expect(api.sessions.send).toHaveBeenCalledWith(session.id, expect.objectContaining({ text: "留给原任务的要求" }));
    expect(router.state.location.pathname).toBe("/settings");
    expect(container.textContent).toBe("settings-page");
  });

  it.each(["create", "send"] as const)("%s 的结果未知时保留输入，禁止快捷键或重复提交自动重发", async (phase) => {
    if (phase === "create") api.sessions.create.mockRejectedValue(new ApiError("network", "connection lost"));
    else api.sessions.send.mockRejectedValue(new ApiError("timeout", "request timed out"));
    await mount();
    await typeGoal("生成第一稿");
    await submit();
    await ready(() => expect(container.querySelector(".start-feedback")?.textContent).toContain("phase2.startUnknown"));
    expect(router.state.location.pathname).toBe("/chat");
    expect(homeInput().value).toBe("生成第一稿");
    // 未知结果只阻止重发，仍允许编辑和保留草稿。
    expect(homeInput().disabled).toBe(false);
    expect(container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.disabled).toBe(true);
    await act(async () => {
      homeInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
      container.querySelector(".start-composer")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(api.sessions.create).toHaveBeenCalledOnce();
    expect(api.sessions.send).toHaveBeenCalledTimes(phase === "create" ? 0 : 1);
  });
});
