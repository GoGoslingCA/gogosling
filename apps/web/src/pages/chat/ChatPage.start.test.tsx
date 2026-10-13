import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import type { Agent, Attachment, Draft, DraftInput, DraftUpdateInput, Message, PageQuery, Project, ProjectMaterial, Run, SendMessageInput, Session, SessionCreateInput, VersionInfo } from "../../api/types";
import { queryKeys } from "../../state/queryKeys";
import type { LiveRun } from "../../state/liveRuns";
import { ChatPage } from "./ChatPage";
import { DraftContext, DraftRegistry } from "../../state/DraftProvider";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => api }));
vi.mock("../../state/EventStreamProvider", () => ({
  useLiveRunsDispatch: () => dispatch,
  useSessionLiveRuns: () => ({ runs: liveRuns, active: null }),
}));

const agent: Agent = { id: "agent-a", name: "写作助手", model: { provider_id: "provider-a", model: "test-model" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "/workspace/agent-a", enabled: true, created_at: "2026-10-12T01:00:00Z", updated_at: "2026-10-12T01:00:00Z" };
const session: Session = { id: "session-new", agent_id: agent.id, title: "新任务", origin: "desktop", key: "desktop:agent-a", status: "active", created_at: "2026-10-12T01:00:00Z", updated_at: "2026-10-12T01:00:00Z" };
const run: Run = { id: "run-new", session_id: session.id, agent_id: agent.id, status: "queued", trigger: "user", usage: {}, created_at: "2026-10-12T01:00:01Z" };
const projectA: Project = { id: "project-a", agent_id: agent.id, name: "Project A", status: "active", version: 1, created_at: session.created_at, updated_at: session.updated_at };
const projectB: Project = { ...projectA, id: "project-b", name: "Project B" };
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
    system: { version: vi.fn(async (): Promise<VersionInfo> => ({ version: "dev", capabilities: {} })) },
    agents: { list: vi.fn(async () => [agent]) },
    settings: { get: vi.fn(async () => ({ timezone: "UTC", locale: "zh" })) },
    sessions: {
      list: vi.fn(async () => storedSession ? [storedSession] : []),
      get: vi.fn(async (_id: string) => storedSession ?? session),
      listMessagesAfter: vi.fn(async (_id: string, _after: number) => storedMessages),
      create: vi.fn(async (input: SessionCreateInput) => { operations.push("create"); storedSession = { ...session, ...(input.project_id ? { project_id: input.project_id } : {}) }; return { ...storedSession }; }),
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
    workbench: {
      getProject: vi.fn(async (id: string) => id === projectB.id ? projectB : projectA),
      listProjects: vi.fn(async () => [projectA, projectB]),
      listMaterials: vi.fn(async (_projectId: string, _query?: PageQuery, _signal?: AbortSignal): Promise<ProjectMaterial[]> => []),
      uploadMaterial: vi.fn(async (projectId: string, file: File): Promise<ProjectMaterial> => ({ id: `material-${projectId}-${file.name}`, project_id: projectId, agent_id: agent.id, name: file.name, size: file.size, sha256: "test-digest", attachment: { name: file.name, path: `inbound/${file.name}`, size: file.size }, created_at: session.created_at })),
      createDraft: vi.fn(async (input: DraftInput): Promise<Draft> => ({ ...input, id: `draft-${input.session_id}`, owner_id: "test-owner", version: 1, created_at: session.created_at, updated_at: session.updated_at })),
      updateDraft: vi.fn(async (id: string, input: DraftUpdateInput): Promise<Draft> => ({ ...input, id, owner_id: "test-owner", version: input.expected_version + 1, created_at: session.created_at, updated_at: session.updated_at })),
      deleteDraft: vi.fn(async (_id: string, _version: number) => undefined),
    },
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
async function mount(path = "/chat", drafts: DraftRegistry | null = null) {
  router = createMemoryRouter([{ path: "/chat/:sessionId?", element: <ChatPage /> }, { path: "/settings", element: <p>settings-page</p> }], { initialEntries: [path] });
  await act(async () => root.render(<QueryClientProvider client={client}><DraftContext.Provider value={drafts}><RouterProvider router={router} /></DraftContext.Provider></QueryClientProvider>));
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
function enableProjects(drafts = false) {
  api.system.version.mockResolvedValue({ version: "dev", capabilities: { "workbench.projects": 1, "workbench.materials": 1, ...(drafts ? { "workbench.drafts": 1 } : {}) } });
}
async function switchProject(projectId: string) {
  await act(async () => { await router.navigate(`/chat?project=${projectId}`); });
}
async function projectReady(projectId: string) {
  await ready(() => {
    expect(router.state.location.search).toBe(`?project=${projectId}`);
    expect(container.querySelector<HTMLSelectElement>("#start-agent")?.value).toBe(agent.id);
    expect(container.querySelector(".project-context")?.textContent).toContain(projectId === projectB.id ? projectB.name : projectA.name);
  });
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

describe("项目开始页的作用域隔离", () => {
  function projectMaterial(index: number): ProjectMaterial { return { id: `m${index}`, project_id: projectA.id, agent_id: agent.id, name: `Document ${index}.md`, size: 1, sha256: "digest", attachment: { name: `Document ${index}.md`, path: `inbound/${index}.md`, size: 1 }, created_at: session.created_at }; }

  it.each(["route", "draft"] as const)("从%s恢复第201份资料后可以实际发送，候选清单默认折叠", async (source) => {
    enableProjects();
    api.workbench.listMaterials.mockImplementation(async (_projectId, query) => Array.from({ length: Math.min(200, 400 - (query?.offset ?? 0)) }, (_, index) => projectMaterial(index + (query?.offset ?? 0) + 1)));
    const draft: Draft = { id: "start-draft", kind: "composer", agent_id: agent.id, project_id: projectA.id, text: "Review selected material", material_ids: ["m201"], owner_id: "owner", version: 1, created_at: session.created_at, updated_at: session.updated_at };
    const registry = source === "draft" ? new DraftRegistry() : null;
    registry?.configure({ ...api.workbench, listDrafts: async (query) => query?.project_id === projectA.id ? [draft] : [], getDraft: async () => draft }, true, false);
    try {
      await mount(`/chat?project=${projectA.id}${source === "route" ? "&materials=m201" : ""}`, registry);
      if (source === "route") await typeGoal("Review selected material");
      await ready(() => {
        expect(container.querySelector('.project-context input[type="checkbox"]:checked')?.parentElement?.textContent).toBe("Document 201.md");
        expect(container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.disabled).toBe(false);
      });
      expect(container.querySelector<HTMLDetailsElement>(".project-context details")!.open).toBe(false);
      expect(container.querySelectorAll('.project-context input[type="checkbox"]')).toHaveLength(201);
      expect(container.querySelector(".project-context")!.textContent).not.toContain("Document 202.md");
      await submit(); await ready(() => expect(api.sessions.send).toHaveBeenCalledOnce());
      expect(api.sessions.send).toHaveBeenCalledWith(session.id, expect.objectContaining({ material_ids: ["m201"] }));
    } finally { registry?.dispose(); }
  });

  it("真正缺失的资料保留为可移除引用，移除前禁止提交", async () => {
    enableProjects();
    await mount(`/chat?project=${projectA.id}&materials=missing`);
    await typeGoal("Review material");
    await ready(() => expect(container.querySelector(".project-context")?.textContent).toContain("projects.invalidSelection"));
    expect(container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.disabled).toBe(true);
    await act(async () => [...container.querySelectorAll<HTMLButtonElement>(".project-context button")].find((item) => item.textContent === "projects.removeUnavailable")!.click());
    await ready(() => expect(container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.disabled).toBe(false));
    await submit(); await ready(() => expect(api.sessions.send).toHaveBeenCalledOnce());
    expect(api.sessions.send.mock.calls[0]![1]).not.toHaveProperty("material_ids");
  });

  it("项目切换取消未完成的资料扫描，旧响应不污染新项目", async () => {
    enableProjects(); vi.mocked(window.confirm).mockReturnValue(true);
    let signalA: AbortSignal | undefined;
    api.workbench.listMaterials.mockImplementation((id, _query, signal) => id === projectA.id ? new Promise<ProjectMaterial[]>((_resolve, reject) => {
      signalA = signal; signal?.addEventListener("abort", () => reject(new DOMException("cancelled", "AbortError")), { once: true });
    }) : Promise.resolve([]));
    await mount(`/chat?project=${projectA.id}&materials=m201`);
    await ready(() => expect(signalA).toBeDefined());
    await switchProject(projectB.id); await projectReady(projectB.id);
    expect(signalA!.aborted).toBe(true);
    expect(container.querySelector(".project-context")?.textContent).not.toContain("projects.invalidSelection");
    await typeGoal("Project B task");
    await ready(() => expect(container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.disabled).toBe(false));
    expect(api.sessions.send).not.toHaveBeenCalled();
  });

  it("旧内核只有项目能力时不等待不存在的资料接口", async () => {
    api.system.version.mockResolvedValue({ version: "old", capabilities: { "workbench.projects": 1 } });
    await mount(`/chat?project=${projectA.id}`); await typeGoal("Start without materials");
    expect(api.workbench.listMaterials).not.toHaveBeenCalled();
    expect(container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.disabled).toBe(false);
    await submit(); await ready(() => expect(api.sessions.send).toHaveBeenCalledOnce());
  });

  it("取消同路径项目切换时保留原项目的正文和文件，不上传或新建任务", async () => {
    enableProjects();
    await mount(`/chat?project=${projectA.id}`);
    await typeGoal("Only for project A");
    await chooseFiles([new File(["A contents"], "a.md")]);
    await switchProject(projectB.id);
    expect(window.confirm).toHaveBeenCalledWith("work.leaveDraft");
    expect(router.state.location.search).toBe(`?project=${projectA.id}`);
    expect(homeInput().value).toBe("Only for project A");
    expect(container.querySelectorAll(".start-composer .attachment-chip")).toHaveLength(1);
    expect(container.querySelector(".start-composer .attachment-chip")?.textContent).toContain("a.md");
    expect(api.sessions.create).not.toHaveBeenCalled();
    expect(api.workbench.uploadMaterial).not.toHaveBeenCalled();
  });

  it("确认切换项目后清空本地输入及文件，后续发送只属于目标项目", async () => {
    enableProjects();
    vi.mocked(window.confirm).mockReturnValue(true);
    await mount(`/chat?project=${projectA.id}`);
    const originalFile = new File(["A contents"], "a.md");
    await typeGoal("Only for project A");
    await chooseFiles([originalFile]);
    await switchProject(projectB.id);
    await projectReady(projectB.id);
    expect(window.confirm).toHaveBeenCalledWith("work.leaveDraft");
    expect(homeInput().value).toBe("");
    expect(container.querySelectorAll(".start-composer .attachment-chip")).toHaveLength(0);
    const nextFile = new File(["B contents"], "b.md");
    await typeGoal("Only for project B");
    await chooseFiles([nextFile]);
    await submit();
    await ready(() => expect(api.sessions.send).toHaveBeenCalledOnce());
    expect(api.sessions.create).toHaveBeenCalledWith({ agent_id: agent.id, title: "Only for project B", project_id: projectB.id });
    expect(api.workbench.uploadMaterial).toHaveBeenCalledOnce();
    expect(api.workbench.uploadMaterial).toHaveBeenCalledWith(projectB.id, nextFile);
    expect(api.sessions.upload).not.toHaveBeenCalled();
    expect(api.sessions.send).toHaveBeenCalledWith(session.id, { text: "Only for project B", attachments: undefined, material_ids: [`material-${projectB.id}-b.md`] });
  });

  it("原项目请求结果未知时，确认切换会重置错误和重发锁，不把错误继承到新项目", async () => {
    enableProjects();
    vi.mocked(window.confirm).mockReturnValue(true);
    api.sessions.create.mockRejectedValueOnce(new ApiError("network", "connection lost"));
    await mount(`/chat?project=${projectA.id}`);
    await typeGoal("Unknown project A task");
    await submit();
    await ready(() => expect(container.querySelector(".start-feedback")?.textContent).toContain("phase2.startUnknown"));
    await switchProject(projectB.id);
    await projectReady(projectB.id);
    expect(container.querySelector(".start-feedback")).toBeNull();
    expect(homeInput().value).toBe("");
    await typeGoal("New project B task");
    expect(container.querySelector<HTMLButtonElement>('.start-composer button[type="submit"]')!.disabled).toBe(false);
    await submit();
    await ready(() => expect(api.sessions.send).toHaveBeenCalledOnce());
    expect(api.sessions.create).toHaveBeenCalledTimes(2);
    expect(api.sessions.create).toHaveBeenLastCalledWith({ agent_id: agent.id, title: "New project B task", project_id: projectB.id });
  });

  it("原项目上传失败后的检查点不被新项目复用或删除", async () => {
    enableProjects(true);
    vi.mocked(window.confirm).mockReturnValue(true);
    const sessionA = { ...session, id: "session-a", project_id: projectA.id };
    const sessionB = { ...session, id: "session-b", project_id: projectB.id };
    api.sessions.create.mockImplementation(async (input) => input.project_id === projectB.id ? sessionB : sessionA);
    api.sessions.get.mockImplementation(async (id) => id === sessionB.id ? sessionB : sessionA);
    api.sessions.send.mockImplementation(async (id) => ({ ...run, id: `run-${id}`, session_id: id }));
    api.workbench.uploadMaterial.mockRejectedValueOnce(new ApiError("network", "upload failed"));
    await mount(`/chat?project=${projectA.id}`);
    await typeGoal("Keep project A checkpoint");
    await chooseFiles([new File(["A contents"], "a.md")]);
    await submit();
    await ready(() => expect(container.querySelector(".start-feedback")?.textContent).toContain("errors.network"));
    expect(api.workbench.createDraft).toHaveBeenCalledWith(expect.objectContaining({ session_id: sessionA.id, project_id: projectA.id, text: "Keep project A checkpoint" }));
    await switchProject(projectB.id);
    await projectReady(projectB.id);
    expect(container.querySelector(".start-feedback")).toBeNull();
    await typeGoal("Independent project B checkpoint");
    await submit();
    await ready(() => expect(router.state.location.pathname).toBe(`/chat/${sessionB.id}`));
    expect(api.workbench.createDraft).toHaveBeenCalledTimes(2);
    expect(api.workbench.createDraft).toHaveBeenLastCalledWith(expect.objectContaining({ session_id: sessionB.id, project_id: projectB.id, text: "Independent project B checkpoint" }));
    expect(api.workbench.updateDraft).not.toHaveBeenCalled();
    expect(api.workbench.deleteDraft).toHaveBeenCalledOnce();
    expect(api.workbench.deleteDraft).toHaveBeenCalledWith(`draft-${sessionB.id}`, 1);
    expect(api.workbench.uploadMaterial).toHaveBeenCalledOnce();
    expect(api.sessions.send).toHaveBeenCalledWith(sessionB.id, { text: "Independent project B checkpoint", attachments: undefined });
  });

  it("原项目创建响应迟到时两次启动独立完成，旧响应不抢走新项目导航", async () => {
    enableProjects();
    vi.mocked(window.confirm).mockReturnValue(true);
    const sessionA = { ...session, id: "session-a", project_id: projectA.id };
    const sessionB = { ...session, id: "session-b", project_id: projectB.id };
    let finishCreate!: (created: Session) => void;
    api.sessions.create.mockImplementation((input) => input.project_id === projectA.id ? new Promise<Session>((resolve) => { finishCreate = resolve; }) : Promise.resolve(sessionB));
    api.sessions.get.mockImplementation(async (id) => id === sessionB.id ? sessionB : sessionA);
    api.sessions.send.mockImplementation(async (id) => ({ ...run, id: `run-${id}`, session_id: id }));
    await mount(`/chat?project=${projectA.id}`);
    const fileA = new File(["A contents"], "a.md");
    const fileB = new File(["B contents"], "b.md");
    await typeGoal("Task A");
    await chooseFiles([fileA]);
    await submit();
    expect(api.sessions.create).toHaveBeenCalledOnce();
    await switchProject(projectB.id);
    await projectReady(projectB.id);
    expect(homeInput().value).toBe("");
    expect(homeInput().disabled).toBe(false);
    await typeGoal("Task B");
    await chooseFiles([fileB]);
    await submit();
    await ready(() => expect(router.state.location.pathname).toBe(`/chat/${sessionB.id}`));
    expect(api.sessions.create).toHaveBeenCalledTimes(2);
    await act(async () => finishCreate(sessionA));
    await ready(() => expect(api.sessions.send).toHaveBeenCalledTimes(2));
    expect(api.workbench.uploadMaterial).toHaveBeenNthCalledWith(1, projectB.id, fileB);
    expect(api.workbench.uploadMaterial).toHaveBeenNthCalledWith(2, projectA.id, fileA);
    expect(api.sessions.send).toHaveBeenNthCalledWith(1, sessionB.id, { text: "Task B", attachments: undefined, material_ids: [`material-${projectB.id}-b.md`] });
    expect(api.sessions.send).toHaveBeenNthCalledWith(2, sessionA.id, { text: "Task A", attachments: undefined, material_ids: [`material-${projectA.id}-a.md`] });
    expect(router.state.location.pathname).toBe(`/chat/${sessionB.id}`);
    expect(window.confirm).toHaveBeenCalledOnce();
  });
});
