import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Agent, Project, ProjectInput, ProjectMaterial, ProjectPatch, Session, SessionProjectInput, VersionInfo } from "../api/types";
import { queryKeys } from "../state/queryKeys";
import { ProjectsPage } from "./ProjectsPage";
import { SessionProjectControl } from "./chat/SessionProjectControl";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => api }));
vi.mock("../components/Toast", () => ({ useToast: () => ({ show: toast }) }));

const date = "2026-10-12T00:00:00Z";
const agentA: Agent = { id: "agent-a", name: "资料助手", model: { provider_id: "p", model: "test" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "workspace-a", enabled: true, created_at: date, updated_at: date };
const agentB: Agent = { ...agentA, id: "agent-b", name: "写作助手", workspace_dir: "workspace-b" };
const initialProject: Project = { id: "project-a", agent_id: agentA.id, name: "已有项目", status: "active", version: 1, created_at: date, updated_at: date };
const originalSession: Session = { id: "session-existing", agent_id: agentA.id, title: "已有任务和历史", key: "agent-a:desktop:stable", origin: "desktop", status: "active", created_at: date, updated_at: date };
const toast = vi.fn();
const dialogMethods = { showModal: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal"), close: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "close") };
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let projects: Map<string, Project>;
let materials: ProjectMaterial[];
let storedSession: Session;
let api: ReturnType<typeof fixture>;

function material(id: string, name: string): ProjectMaterial {
  return { id, project_id: initialProject.id, agent_id: agentA.id, name, size: 16, sha256: "sample-digest", attachment: { name, path: `inbound/${name}`, size: 16 }, created_at: date };
}
function fixture() {
  return {
    system: { version: vi.fn(async (): Promise<VersionInfo> => ({ version: "dev", capabilities: { "workbench.projects": 1, "workbench.materials": 1, "workbench.artifacts": 1 } })) },
    agents: { list: vi.fn(async () => [agentA, agentB]) },
    sessions: {
      list: vi.fn(async (query?: { project_id?: string }) => !query?.project_id || storedSession.project_id === query.project_id ? [{ ...storedSession }] : []),
      get: vi.fn(async (_id: string) => ({ ...storedSession })), create: vi.fn(), update: vi.fn(), remove: vi.fn(), send: vi.fn(),
    },
    workbench: {
      listProjects: vi.fn(async (query?: { status?: string; agent_id?: string }) => [...projects.values()].filter((project) => (!query?.status || project.status === query.status) && (!query?.agent_id || project.agent_id === query.agent_id))),
      getProject: vi.fn(async (id: string) => ({ ...projects.get(id)! })),
      createProject: vi.fn(async (input: ProjectInput): Promise<Project> => { const created: Project = { ...input, id: "project-new", status: "active", version: 1, created_at: date, updated_at: date }; projects.set(created.id, created); return { ...created }; }),
      updateProject: vi.fn(async (id: string, patch: ProjectPatch): Promise<Project> => { const current = projects.get(id)!; if (current.version !== patch.expected_version) throw new ApiError("conflict", "版本已变化", { status: 409 }); const saved = { ...current, ...patch, version: current.version + 1 }; projects.set(id, saved); return { ...saved }; }),
      listMaterials: vi.fn(async (id: string) => materials.filter((item) => item.project_id === id)),
      uploadMaterial: vi.fn(async (projectId: string, file: File): Promise<ProjectMaterial> => { const item: ProjectMaterial = { ...material("material-uploaded", file.name), project_id: projectId, agent_id: projects.get(projectId)!.agent_id, size: file.size, attachment: { name: file.name, path: `inbound/${file.name}`, size: file.size } }; materials = [...materials, item]; return item; }),
      removeMaterial: vi.fn(async (projectId: string, id: string) => { materials = materials.filter((item) => item.id !== id || item.project_id !== projectId); }),
      listArtifacts: vi.fn(async () => []),
      setSessionProject: vi.fn(async (id: string, input: SessionProjectInput): Promise<Session> => { if (id !== storedSession.id || input.expected_project_id !== (storedSession.project_id ?? null)) throw new ApiError("conflict", "归属已变化", { status: 409 }); storedSession = { ...storedSession, project_id: input.project_id ?? undefined }; return { ...storedSession }; }),
    },
  };
}

function LocationProbe() { const location = useLocation(); return <output aria-label="测试路由">{location.pathname}{location.search}</output>; }
function SessionControlFixture() {
  const current = useQuery({ queryKey: queryKeys.session(originalSession.id), queryFn: () => api.sessions.get(originalSession.id) });
  return current.data ? <><SessionProjectControl session={current.data} running={false} /><output aria-label="当前会话">{JSON.stringify(current.data)}</output></> : null;
}
async function ready(assertion: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { interval: 10, timeout: 2000 }); }
async function mount(path = "/projects/project-a", control = false) {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}>{control ? <SessionControlFixture /> : <Routes><Route path="/projects/:projectId?" element={<ProjectsPage />} /><Route path="/chat" element={<p>next-task-page</p>} /></Routes>}<LocationProbe /></MemoryRouter></QueryClientProvider>));
}
function button(label: string, scope: ParentNode = host) { const found = [...scope.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.getAttribute("aria-label") === label || item.textContent === label); if (!found) throw new Error(`未找到按钮 ${label}`); return found; }
async function setInput(input: HTMLInputElement, value: string) { await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value); input.dispatchEvent(new Event("input", { bubbles: true })); }); }
async function setSelect(select: HTMLSelectElement, value: string) { await act(async () => { select.value = value; select.dispatchEvent(new Event("change", { bubbles: true })); }); }
function materialCheckboxes() { return [...host.querySelectorAll<HTMLInputElement>(".project-material-list input[type=checkbox]")]; }
function currentRoute() { return host.querySelector("output[aria-label='测试路由']")?.textContent ?? ""; }

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  projects = new Map([[initialProject.id, { ...initialProject }]]);
  materials = [material("material-1", "需求.md"), material("material-2", "补充.csv")]; storedSession = { ...originalSession, project_id: initialProject.id };
  api = fixture(); toast.mockReset();
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  vi.spyOn(window, "confirm").mockReturnValue(true);
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value(this: HTMLDialogElement) { this.open = true; } });
  Object.defineProperty(HTMLDialogElement.prototype, "close", { configurable: true, value(this: HTMLDialogElement) { this.open = false; } });
});
afterEach(async () => {
  await act(async () => root.unmount()); client.clear(); host.remove(); vi.restoreAllMocks();
  for (const method of ["showModal", "close"] as const) { const descriptor = dialogMethods[method]; if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, method, descriptor); else delete HTMLDialogElement.prototype[method]; }
});

describe("ProjectsPage 项目资料数据流程", () => {
  it("创建提交所选助手，已有项目编辑锁定助手且仅发送版本条件与名称描述", async () => {
    await mount("/projects"); await ready(() => expect(host.querySelector(".project-grid")).not.toBeNull());
    await act(async () => button("projects.create").click());
    const dialog = host.querySelector("dialog")!;
    await setInput(dialog.querySelector<HTMLInputElement>("input")!, "  新写作项目  "); await setSelect(dialog.querySelector<HTMLSelectElement>("select")!, agentB.id);
    await act(async () => button("common.save", dialog).click());
    await ready(() => expect(currentRoute()).toBe("/projects/project-new"));
    expect(api.workbench.createProject).toHaveBeenCalledWith({ agent_id: agentB.id, name: "新写作项目", description: "" });
    await ready(() => expect(host.querySelector(".project-details-head")).not.toBeNull());
    await act(async () => button("common.edit").click());
    const edit = host.querySelector("dialog")!; const agentSelect = edit.querySelector<HTMLSelectElement>("select")!;
    expect(agentSelect.value).toBe(agentB.id); expect(agentSelect.disabled).toBe(true);
    await setInput(edit.querySelector<HTMLInputElement>("input")!, "更新后的项目名"); await act(async () => button("common.save", edit).click());
    await ready(() => expect(api.workbench.updateProject).toHaveBeenCalled());
    expect(api.workbench.updateProject).toHaveBeenCalledWith("project-new", { expected_version: 1, name: "更新后的项目名", description: "" });
    expect(projects.get("project-new")?.agent_id).toBe(agentB.id);
  });

  it("上传资料只加入项目索引，默认不选择也不带入新任务", async () => {
    await mount(); await ready(() => expect(materialCheckboxes()).toHaveLength(2));
    const file = new File(["新需求正文"], "新需求.md");
    await act(async () => { const picker = host.querySelector<HTMLInputElement>("input[type=file]")!; Object.defineProperty(picker, "files", { configurable: true, value: [file] }); picker.dispatchEvent(new Event("change", { bubbles: true })); });
    await ready(() => expect(materialCheckboxes()).toHaveLength(3));
    expect(api.workbench.uploadMaterial).toHaveBeenCalledWith(initialProject.id, file); expect(materialCheckboxes().every((item) => !item.checked)).toBe(true);
    await act(async () => button("projects.startTask").click());
    const route = new URL(currentRoute(), "http://local.test"); expect(route.searchParams.get("project")).toBe(initialProject.id); expect(route.searchParams.has("materials")).toBe(false);
    expect(api.sessions.send).not.toHaveBeenCalled(); expect(api.sessions.create).not.toHaveBeenCalled();
  });

  it("只有显式勾选的材料 ID 被带到开始页，未选材料不自动加入", async () => {
    await mount(); await ready(() => expect(materialCheckboxes()).toHaveLength(2));
    await act(async () => materialCheckboxes()[0].click()); expect(materialCheckboxes()[1].checked).toBe(false);
    await act(async () => button("projects.startTask").click());
    const route = new URL(currentRoute(), "http://local.test"); expect(route.pathname).toBe("/chat"); expect(route.searchParams.get("materials")).toBe("material-1");
    expect(currentRoute()).not.toContain("material-2"); expect(currentRoute()).not.toContain("需求.md");
  });

  it("移除引用失败时保留该材料及已选状态，并显示错误", async () => {
    api.workbench.removeMaterial.mockRejectedValue(new ApiError("network", "移除失败"));
    await mount(); await ready(() => expect(materialCheckboxes()).toHaveLength(2)); await act(async () => materialCheckboxes()[0].click());
    await act(async () => button("projects.removeReference", host.querySelector(".project-material-row")!).click());
    await ready(() => expect(toast).toHaveBeenCalledWith("errors.network", "error"));
    expect(api.workbench.removeMaterial).toHaveBeenCalledWith(initialProject.id, "material-1"); expect(materialCheckboxes()).toHaveLength(2); expect(materialCheckboxes()[0].checked).toBe(true);
    expect(host.querySelector(".project-material-list")?.textContent).toContain("需求.md");
  });

  it("归档项目只更新项目状态，不归档或删除已有会话", async () => {
    await mount(); await ready(() => expect(host.querySelector(".project-details-head")).not.toBeNull());
    await act(async () => button("chat.archive").click()); await ready(() => expect(projects.get(initialProject.id)?.status).toBe("archived"));
    expect(api.workbench.updateProject).toHaveBeenCalledWith(initialProject.id, { expected_version: 1, status: "archived" });
    expect(api.sessions.update).not.toHaveBeenCalled(); expect(api.sessions.remove).not.toHaveBeenCalled(); expect(storedSession.status).toBe("active");
    expect(button("projects.startTask").disabled).toBe(true);
    await act(async () => button("projects.tasks").click()); expect(host.querySelector(`a[href='/chat/${originalSession.id}']`)).not.toBeNull();
  });

  it("旧daemon缺少capabilities时不探测工作台接口，保留返回旧工作台入口", async () => {
    api.system.version.mockResolvedValue({ version: "old" });
    await mount(); await ready(() => expect(host.textContent).toContain("projects.unavailable"));
    for (const endpoint of Object.values(api.workbench)) expect(endpoint).not.toHaveBeenCalled();
    expect(api.sessions.list).not.toHaveBeenCalled(); expect(host.querySelector("a[href='/chat']")).not.toBeNull();
  });

  it("资料任务成果为可键盘切换的标签页，权限说明默认折叠但仍可查阅", async () => {
    await mount(); await ready(() => expect(host.querySelector('[role="tablist"]')).not.toBeNull());
    const materialsTab = button("projects.materials");
    const tasksTab = button("projects.tasks");
    const artifactsTab = button("projects.artifacts");
    expect(materialsTab.getAttribute("aria-selected")).toBe("true");
    expect(tasksTab.tabIndex).toBe(-1);
    await act(async () => materialsTab.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true })));
    expect(tasksTab.getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(tasksTab);
    expect(host.querySelector('[role="tabpanel"]')?.getAttribute("aria-labelledby")).toBe(tasksTab.id);
    await act(async () => tasksTab.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true })));
    expect(document.activeElement).toBe(artifactsTab);
    expect(artifactsTab.getAttribute("aria-selected")).toBe("true");
    const scopeDetails = host.querySelector<HTMLDetailsElement>(".project-overview")!;
    expect(scopeDetails.open).toBe(false);
    expect(scopeDetails.textContent).toContain("projects.scopeHint");
    expect(scopeDetails.textContent).toContain(agentA.workspace_dir);
  });
});

describe("SessionProjectControl 原位关联", () => {
  it("加入和移出项目带原归属CAS，更新同一会话缓存并保持ID、Key和历史入口", async () => {
    storedSession = { ...originalSession };
    await mount("/chat/session-existing", true); await ready(() => expect(host.querySelector<HTMLSelectElement>("select")?.disabled).toBe(false));
    const selected = () => host.querySelector<HTMLSelectElement>("select")!;
    await setSelect(selected(), initialProject.id); await ready(() => expect(selected().value).toBe(initialProject.id));
    expect(api.workbench.setSessionProject).toHaveBeenCalledWith(originalSession.id, { project_id: initialProject.id, expected_project_id: null });
    expect(client.getQueryData<Session>(queryKeys.session(originalSession.id))).toMatchObject({ id: originalSession.id, key: originalSession.key, origin: originalSession.origin, title: originalSession.title, project_id: initialProject.id });
    await setSelect(selected(), ""); await ready(() => expect(selected().value).toBe(""));
    expect(api.workbench.setSessionProject).toHaveBeenLastCalledWith(originalSession.id, { project_id: null, expected_project_id: initialProject.id });
    expect(storedSession.id).toBe(originalSession.id); expect(storedSession.key).toBe(originalSession.key); expect(storedSession.project_id).toBeUndefined();
    expect(api.sessions.create).not.toHaveBeenCalled(); expect(api.sessions.remove).not.toHaveBeenCalled(); expect(api.sessions.send).not.toHaveBeenCalled();
    expect(currentRoute()).toBe("/chat/session-existing");
  });
});
