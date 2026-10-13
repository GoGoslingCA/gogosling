import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Project, Session } from "../api/types";
import { readUiPreferences } from "../lib/uiPreferences";
import { queryKeys } from "../state/queryKeys";
import { SidebarProjects } from "./SidebarProjects";

const { listProjects, getProject, listSessions, createProject, listAgents, version, toast } = vi.hoisted(() => ({
  listProjects: vi.fn(), getProject: vi.fn(), listSessions: vi.fn(), createProject: vi.fn(), listAgents: vi.fn(), version: vi.fn(), toast: vi.fn(),
}));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ system: { version }, agents: { list: listAgents }, sessions: { list: listSessions }, workbench: { listProjects, getProject, createProject } }) }));
vi.mock("../components/Toast", () => ({ useToast: () => ({ show: toast }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, values?: { name?: string; title?: string }) => values?.name ? `${key}: ${values.name}` : values?.title ? `${key}: ${values.title}` : key }) }));

const PROJECT_A = "01ARZ3NDEKTSV4RRFFQ69G5FAA";
const PROJECT_B = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
const SESSION_ID = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
const DATE = "2026-10-13T00:00:00Z";
const STORAGE_KEY = "gogosling.ui.v1";
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let taskMenu = vi.fn();
let media: MediaQueryList;
let mediaChange: ((event: MediaQueryListEvent) => void) | undefined;
const dialogMethods = { showModal: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal"), close: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "close") };

const projectA: Project = { id: PROJECT_A, name: "需求整理", agent_id: "agent-a", status: "active", version: 1, created_at: DATE, updated_at: DATE };
const projectB: Project = { ...projectA, id: PROJECT_B, name: "代码协作" };
const session: Session = { id: SESSION_ID, project_id: PROJECT_A, agent_id: "agent-a", key: "local-task", title: "需求初稿", status: "active", origin: "desktop", created_at: DATE, updated_at: DATE };

function LocationProbe() { const location = useLocation(); return <output aria-label="测试路由">{location.pathname}{location.search}</output>; }
async function render(activeProjectId?: string) {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={["/chat"]}><SidebarProjects activeProjectId={activeProjectId} onTaskMenu={taskMenu} /><LocationProbe /></MemoryRouter></QueryClientProvider>));
  await settle();
}
async function settle() { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 25)); }); }
async function ready(assertion: () => void) { await vi.waitFor(async () => { await settle(); assertion(); }, { interval: 10, timeout: 2000 }); }
function button(label: string, scope: ParentNode = host): HTMLButtonElement {
  const found = Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find((item) => item.getAttribute("aria-label") === label || item.textContent === label);
  if (!found) throw new Error(`未找到按钮 ${label}`);
  return found;
}
async function toggle(project: Project) { await act(async () => button(`projects.toggleTasks: ${project.name}`).click()); await settle(); }
function projectLink(id: string): HTMLAnchorElement | null { return host.querySelector(`a[href='/projects/${id}']`); }
function taskLink(): HTMLAnchorElement | null { return host.querySelector(`a[href='/chat/${SESSION_ID}']`); }
function setWidth(width: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
  Object.defineProperty(media, "matches", { configurable: true, value: width <= 760 });
  mediaChange?.({ matches: width <= 760 } as MediaQueryListEvent);
  window.dispatchEvent(new Event("resize"));
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  mediaChange = undefined;
  media = { matches: false, media: "(max-width: 760px)", onchange: null, addEventListener: vi.fn((_event, callback) => { mediaChange = callback as typeof mediaChange; }), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn() } as unknown as MediaQueryList;
  vi.stubGlobal("matchMedia", vi.fn(() => media));
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1280 });
  version.mockReset().mockResolvedValue({ version: "test", capabilities: { "workbench.projects": 1 } });
  listProjects.mockReset().mockResolvedValue([projectA, projectB]);
  getProject.mockReset().mockResolvedValue(projectB);
  listSessions.mockReset().mockImplementation(async (query: { project_id?: string }) => query.project_id === PROJECT_A ? [session] : []);
  createProject.mockReset(); listAgents.mockReset().mockResolvedValue([]); toast.mockReset(); taskMenu = vi.fn();
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value(this: HTMLDialogElement) { this.open = true; } });
  Object.defineProperty(HTMLDialogElement.prototype, "close", { configurable: true, value(this: HTMLDialogElement) { this.open = false; } });
});

afterEach(async () => {
  await act(async () => root.unmount()); host.remove(); client.clear(); localStorage.clear();
  vi.restoreAllMocks(); vi.unstubAllGlobals();
  for (const method of ["showModal", "close"] as const) { const descriptor = dialogMethods[method]; if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, method, descriptor); else delete HTMLDialogElement.prototype[method]; }
});

describe("侧栏项目按需展开", () => {
  const firstEight = Array.from({ length: 8 }, (_, index): Project => ({ ...projectA, id: `project-${index}`, name: `Project ${index}` }));

  it("只读取八个项目，并单独补入列表之外的当前项目", async () => {
    listProjects.mockResolvedValue(firstEight);
    await render(PROJECT_B);
    await ready(() => expect(projectLink(PROJECT_B)?.textContent).toContain(projectB.name));
    expect(listProjects).toHaveBeenCalledExactlyOnceWith({ status: "active", limit: 8 }, expect.any(AbortSignal));
    expect(getProject).toHaveBeenCalledExactlyOnceWith(PROJECT_B);
    expect(host.querySelectorAll(".nav-project-row")).toHaveLength(9);
    expect(button(`projects.toggleTasks: ${projectB.name}`).getAttribute("aria-expanded")).toBe("true");
    expect(listSessions).toHaveBeenCalledExactlyOnceWith({ status: "active", project_id: PROJECT_B, limit: 6 }, expect.any(AbortSignal));
  });

  it("当前项目复用详情页的元数据缓存，不重新请求或拉取完整项目列表", async () => {
    listProjects.mockResolvedValue(firstEight);
    client.setQueryData(queryKeys.project(PROJECT_B), projectB);
    await render(PROJECT_B);
    await ready(() => expect(projectLink(PROJECT_B)).not.toBeNull());
    expect(getProject).not.toHaveBeenCalled();
    expect(listProjects).toHaveBeenCalledTimes(1);
  });

  it("当前项目与已在途的详情页请求合并，不能重复读取", async () => {
    listProjects.mockResolvedValue(firstEight);
    let complete!: (value: Project) => void;
    getProject.mockImplementation(() => new Promise<Project>((resolve) => { complete = resolve; }));
    const pending = client.fetchQuery({ queryKey: queryKeys.project(PROJECT_B), queryFn: () => getProject(PROJECT_B) as Promise<Project> });
    await render(PROJECT_B);
    expect(getProject).toHaveBeenCalledTimes(1);
    await act(async () => { complete(projectB); await pending; });
    await ready(() => expect(projectLink(PROJECT_B)).not.toBeNull());
    expect(getProject).toHaveBeenCalledTimes(1);
  });

  it.each(["archived", "missing"] as const)("当前项目%s时不把它插入活动项目区", async (state) => {
    listProjects.mockResolvedValue(firstEight);
    if (state === "archived") getProject.mockResolvedValue({ ...projectB, status: "archived" });
    else getProject.mockRejectedValue(new Error("unavailable"));
    await render(PROJECT_B);
    await ready(() => expect(getProject).toHaveBeenCalledOnce());
    expect(projectLink(PROJECT_B)).toBeNull();
    expect(host.querySelectorAll(".nav-project-row")).toHaveLength(8);
    expect(listSessions).not.toHaveBeenCalled();
  });

  it("当前项目已在八条短列表中时不请求详情", async () => {
    await render(PROJECT_A);
    await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    expect(getProject).not.toHaveBeenCalled();
  });

  it.each(["折叠", "缩窄"] as const)("%s后当前项目缓存失效也不会发起额外详情请求", async (action) => {
    listProjects.mockResolvedValue(firstEight);
    await render(PROJECT_B);
    await ready(() => expect(projectLink(PROJECT_B)).not.toBeNull());
    await act(async () => { if (action === "折叠") button("projects.title").click(); else setWidth(640); });
    await act(async () => { await client.invalidateQueries({ queryKey: queryKeys.project(PROJECT_B), exact: true }); });
    expect(getProject).toHaveBeenCalledTimes(1);
    expect(projectLink(PROJECT_B)).toBeNull();
  });

  it.each(["折叠", "缩窄"] as const)("%s时取消尚未完成的项目列表请求，隐藏后不继续刷新", async (action) => {
    let pendingSignal: AbortSignal | undefined;
    listProjects.mockImplementation((_query, signal: AbortSignal) => {
      pendingSignal = signal;
      return new Promise<Project[]>((_resolve, reject) => signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true }));
    });
    await render();
    expect(pendingSignal?.aborted).toBe(false);
    await act(async () => { if (action === "折叠") button("projects.title").click(); else setWidth(640); });
    expect(pendingSignal?.aborted).toBe(true);
    await act(async () => { await client.invalidateQueries({ queryKey: queryKeys.projects }); });
    expect(listProjects).toHaveBeenCalledTimes(1);
    expect(listSessions).not.toHaveBeenCalled();
  });

  it("隐藏侧栏不取消仍有其他侧栏订阅者的共享请求", async () => {
    let pendingSignal: AbortSignal | undefined;
    let complete: ((projects: Project[]) => void) | undefined;
    listProjects.mockImplementation((_query, signal: AbortSignal) => {
      pendingSignal = signal;
      return new Promise<Project[]>((resolve, reject) => {
        complete = resolve;
        signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      });
    });
    await render();
    // 只有同一个短列表共享请求；项目总览使用独立的分页键。
    const observer = new QueryObserver<Project[]>(client, {
      queryKey: [...queryKeys.projects, "sidebar"],
      queryFn: ({ signal }) => listProjects({ status: "active", limit: 8 }, signal) as Promise<Project[]>,
    });
    const notified = vi.fn();
    const unsubscribe = observer.subscribe(notified);
    try {
      await act(async () => button("projects.title").click());
      expect(pendingSignal?.aborted).toBe(false);
      expect(listProjects).toHaveBeenCalledTimes(1);
      await act(async () => complete?.([projectA, projectB]));
      await ready(() => expect(observer.getCurrentResult().data).toEqual([projectA, projectB]));
      expect(notified).toHaveBeenCalled();
      expect(pendingSignal?.aborted).toBe(false);
    } finally { unsubscribe(); }
  });

  it("先显示项目名称，展开时只查询所选项目的有限任务", async () => {
    await render();
    await ready(() => expect(projectLink(PROJECT_B)?.textContent).toContain(projectB.name));
    expect(listProjects).toHaveBeenCalledWith({ status: "active", limit: 8 }, expect.any(AbortSignal));
    expect(getProject).not.toHaveBeenCalled();
    expect(listSessions).not.toHaveBeenCalled();
    await toggle(projectA);
    expect(listSessions).toHaveBeenCalledExactlyOnceWith({ status: "active", project_id: PROJECT_A, limit: 6 }, expect.any(AbortSignal));
    expect(taskLink()?.textContent).toContain(session.title);
    expect(button(`projects.toggleTasks: ${projectA.name}`).getAttribute("aria-expanded")).toBe("true");
    expect(readUiPreferences().expandedProjectId).toBe(PROJECT_A);
    expect(host.querySelector(`a[href='/projects/${PROJECT_A}?view=tasks']`)).not.toBeNull();
    await toggle(projectB);
    expect(button(`projects.toggleTasks: ${projectA.name}`).getAttribute("aria-expanded")).toBe("false");
    expect(taskLink()).toBeNull();
    expect(readUiPreferences().expandedProjectId).toBe(PROJECT_B);
    expect(listSessions).toHaveBeenLastCalledWith({ status: "active", project_id: PROJECT_B, limit: 6 }, expect.any(AbortSignal));
  });

  it("折叠中的任务请求立即取消，后台失效不会继续拉取隐藏任务", async () => {
    let pendingSignal: AbortSignal | undefined;
    listSessions.mockImplementation((_query, signal: AbortSignal) => {
      pendingSignal = signal;
      return new Promise<Session[]>((_resolve, reject) => signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true }));
    });
    await render(); await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    await toggle(projectA);
    expect(pendingSignal?.aborted).toBe(false);
    await toggle(projectA);
    expect(pendingSignal?.aborted).toBe(true);
    expect(readUiPreferences().expandedProjectId).toBeNull();
    await act(async () => { await client.invalidateQueries({ queryKey: queryKeys.sessionsRoot }); });
    expect(listSessions).toHaveBeenCalledTimes(1);
  });

  it("项目名称直接进入既有项目页且不会创建会话或展开其他项目", async () => {
    await render(); await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    await act(async () => projectLink(PROJECT_A)!.click());
    expect(host.querySelector("output")?.textContent).toBe(`/projects/${PROJECT_A}`);
    expect(createProject).not.toHaveBeenCalled();
    expect(listSessions).not.toHaveBeenCalled();
  });

  it("恢复折叠偏好，路由指定的项目可展开且不反复覆盖用户折叠操作", async () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ expandedProjectId: PROJECT_B }));
    await render(PROJECT_A);
    await ready(() => expect(taskLink()).not.toBeNull());
    expect(readUiPreferences().expandedProjectId).toBe(PROJECT_A);
    await toggle(projectA);
    await render(PROJECT_A);
    expect(taskLink()).toBeNull();
    expect(button(`projects.toggleTasks: ${projectA.name}`).getAttribute("aria-expanded")).toBe("false");
  });

  it("项目区折叠独立保存，再展开后恢复项目行", async () => {
    await render(); await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    await act(async () => button("projects.title").click());
    expect(readUiPreferences().projectListVisible).toBe(false);
    expect(projectLink(PROJECT_A)).toBeNull();
    await act(async () => button("projects.title").click());
    await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    expect(readUiPreferences().projectListVisible).toBe(true);
  });

  it("窄窗口只保留项目页入口，不读取项目与任务列表", async () => {
    setWidth(640);
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ expandedProjectId: PROJECT_A }));
    await render(PROJECT_B); await settle();
    expect(host.querySelector("a[href='/projects']")).not.toBeNull();
    expect(projectLink(PROJECT_A)).toBeNull();
    expect(listProjects).not.toHaveBeenCalled();
    expect(getProject).not.toHaveBeenCalled();
    expect(listSessions).not.toHaveBeenCalled();
  });

  it("项目任务复用已有菜单回调，不写入项目关系", async () => {
    await render(); await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    await toggle(projectA);
    await act(async () => button(`phase2.taskMenu: ${session.title}`).click());
    expect(taskMenu).toHaveBeenCalledWith(session, expect.objectContaining({ type: "click" }));
    expect(createProject).not.toHaveBeenCalled();
  });

  it("缩窄窗口取消展开任务的读取，恢复宽窗口仍保留项目偏好", async () => {
    let pendingSignal: AbortSignal | undefined;
    listSessions.mockImplementation((_query, signal: AbortSignal) => {
      pendingSignal = signal;
      return new Promise<Session[]>((_resolve, reject) => signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true }));
    });
    await render(); await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    await toggle(projectA);
    await act(async () => setWidth(640));
    expect(pendingSignal?.aborted).toBe(true);
    expect(host.querySelector("a[href='/projects']")).not.toBeNull();
    expect(projectLink(PROJECT_A)).toBeNull();
    expect(readUiPreferences().expandedProjectId).toBe(PROJECT_A);
    listSessions.mockResolvedValue([session]);
    await act(async () => setWidth(1280));
    await ready(() => expect(taskLink()).not.toBeNull());
  });

  it("点击加号打开共用项目表单，取消不创建项目", async () => {
    await render(); await ready(() => expect(projectLink(PROJECT_A)).not.toBeNull());
    const trigger = button("projects.create");
    trigger.focus();
    await act(async () => trigger.click());
    await ready(() => expect(host.querySelector("dialog[open]")).not.toBeNull());
    await act(async () => button("common.cancel", host.querySelector("dialog")!).click());
    expect(host.querySelector("dialog")).toBeNull();
    expect(createProject).not.toHaveBeenCalled();
  });
});
