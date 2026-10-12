import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session, SessionPatchInput } from "../api/types";
import { readUiPreferences } from "../lib/uiPreferences";
import { queryKeys } from "../state/queryKeys";
import { NavigationSidebar } from "./NavigationSidebar";

const { list, get, update, toast } = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), update: vi.fn(), toast: vi.fn() }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ sessions: { list, get, update } }) }));
vi.mock("../components/Toast", () => ({ useToast: () => ({ show: toast }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, values?: { title?: string }) => values?.title ? `${key}: ${values.title}` : key }) }));

const SESSION_ID = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
const OTHER_ID = "01ARZ3NDEKTSV4RRFFQ69G5FAW";
const STORAGE_KEY = "gogosling.ui.v1";
const dialogMethods = { showModal: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal"), close: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "close") };
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let sessions: Map<string, Session>;
let openSearch = vi.fn<() => void>();

function session(id = SESSION_ID, title = "准备产品需求文档"): Session {
  return { id, title, agent_id: "agent-a", key: `session:${id}`, origin: "desktop", status: "active", created_at: "2026-10-12T01:00:00Z", updated_at: "2026-10-12T01:00:00Z" };
}

function LocationProbe() {
  const location = useLocation();
  return <output aria-label="测试当前路由">{location.pathname}</output>;
}

async function render(instance = 0) {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/chat/${SESSION_ID}`]}><NavigationSidebar key={instance} openSearch={openSearch} modifier="⌘" /><LocationProbe /></MemoryRouter></QueryClientProvider>));
  await settle();
}

async function settle() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 25)); });
}

function button(text: string, scope: ParentNode = document): HTMLButtonElement {
  const found = [...scope.querySelectorAll("button")].find((element) => element.textContent === text);
  if (!found) throw new Error(`未找到按钮：${text}`);
  return found;
}

function taskLink(id = SESSION_ID): HTMLAnchorElement | null {
  return host.querySelector<HTMLAnchorElement>(`a[href='/chat/${id}']`);
}

async function openMenu(id = SESSION_ID) {
  const trigger = taskLink(id)?.parentElement?.querySelector<HTMLButtonElement>("button");
  if (!trigger) throw new Error("未找到任务菜单入口");
  await act(async () => trigger.click());
  return trigger;
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  sessions = new Map([[SESSION_ID, session()], [OTHER_ID, session(OTHER_ID, "检查代码修改")]]);
  list.mockReset().mockImplementation(async () => [...sessions.values()].filter((value) => value.status === "active"));
  get.mockReset().mockImplementation(async (id: string) => sessions.get(id));
  update.mockReset().mockImplementation(async (id: string, patch: SessionPatchInput) => {
    const current = sessions.get(id);
    if (!current) throw new Error("任务不存在");
    const saved: Session = { ...current, ...(patch.title === undefined ? {} : { title: patch.title }), ...(patch.status === undefined ? {} : { status: patch.status }) };
    sessions.set(id, saved);
    return saved;
  });
  toast.mockReset();
  openSearch = vi.fn();
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value(this: HTMLDialogElement) { this.open = true; } });
  Object.defineProperty(HTMLDialogElement.prototype, "close", { configurable: true, value(this: HTMLDialogElement) { this.open = false; } });
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  client.clear();
  localStorage.clear();
  for (const method of ["showModal", "close"] as const) {
    const descriptor = dialogMethods[method];
    if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, method, descriptor);
    else delete HTMLDialogElement.prototype[method];
  }
  vi.restoreAllMocks();
});

describe("NavigationSidebar 兼容原入口与设备偏好", () => {
  it("主导航聚焦任务，管理能力统一收进设置，旧偏好可读取", async () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ appearance: "dark", artifactWidth: 540, toolsExpanded: true }));
    await render();
    expect(taskLink()?.textContent).toContain("准备产品需求文档");
    expect(host.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded")).toBe("true");
    for (const path of ["/agents", "/providers", "/extensions", "/channels", "/audit"]) expect(host.querySelector(`a[href='${path}']`)).toBeNull();
    for (const path of ["/jobs", "/settings"]) expect(host.querySelector(`a[href='${path}']`)).not.toBeNull();
    expect(host.querySelector(".nav-tools")).toBeNull();
    await act(async () => host.querySelector<HTMLAnchorElement>("a[href='/settings']")!.click());
    expect(host.querySelector("output")?.textContent).toBe("/settings");
    await act(async () => button("phase2.all").click()); expect(openSearch).toHaveBeenCalledOnce();
  });

  it("固定仅写会话 ID，不持久化标题、正文或工作区材料", async () => {
    await render();
    await openMenu();
    await act(async () => button("phase2.pin").click());
    await settle();
    expect(readUiPreferences().pinnedSessionIds).toEqual([SESSION_ID]);
    const storedText = localStorage.getItem(STORAGE_KEY)!;
    const stored = JSON.parse(storedText) as Record<string, unknown>;
    expect(Object.keys(stored).sort()).toEqual(["appearance", "artifactWidth", "pinnedSessionIds", "taskListVisible", "toolsExpanded", "lastSessionId", "lastArtifactId"].sort());
    expect(storedText).not.toContain("准备产品需求文档");
    expect(storedText).not.toContain(`session:${SESSION_ID}`);
    expect(get).toHaveBeenCalledWith(SESSION_ID);
    expect(update).not.toHaveBeenCalled();
    await render(1);
    expect(taskLink()).not.toBeNull();
    await openMenu();
    await act(async () => button("phase2.unpin").click());
    expect(readUiPreferences().pinnedSessionIds).toEqual([]);
    expect(taskLink()).not.toBeNull();
  });

  it("最近任务的折叠偏好继续恢复，旧工具折叠值不影响设置入口", async () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ toolsExpanded: false }));
    await render();
    await act(async () => host.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
    expect(readUiPreferences()).toMatchObject({ taskListVisible: false, toolsExpanded: false });
    await render(1); expect(taskLink()).toBeNull();
    expect(host.querySelector("a[href='/settings']")).not.toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
    expect(taskLink()).not.toBeNull(); expect(readUiPreferences().taskListVisible).toBe(true);
  });

});

describe("NavigationSidebar 任务操作", () => {
  it("菜单重命名更新同一任务并刷新名称，不改变会话状态", async () => {
    await render();
    await openMenu();
    await act(async () => button("chat.rename").click());
    const input = host.querySelector<HTMLInputElement>("#sidebar-task-name")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "  最新需求计划  ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => button("common.save").click());
    await settle();
    expect(update).toHaveBeenCalledWith(SESSION_ID, expect.objectContaining({ title: "最新需求计划" }));
    expect(sessions.get(SESSION_ID)?.status).toBe("active");
    expect(taskLink()?.textContent).toContain("最新需求计划");
    expect(client.getQueryData<Session>(queryKeys.session(SESSION_ID))?.title).toBe("最新需求计划");
    expect(host.querySelector("dialog")).toBeNull();
  });

  it("归档更新任务状态并从最近列表移除，不执行删除", async () => {
    await render();
    await openMenu();
    await act(async () => button("chat.archive").click());
    await settle();
    expect(update).toHaveBeenCalledWith(SESSION_ID, expect.objectContaining({ status: "archived" }));
    expect(sessions.get(SESSION_ID)?.title).toBe("准备产品需求文档");
    expect(taskLink()).toBeNull();
    expect(taskLink(OTHER_ID)).not.toBeNull();
    expect(client.getQueryData<Session>(queryKeys.session(SESSION_ID))?.status).toBe("archived");
  });

  it("正在运行的任务不能通过菜单归档", async () => {
    sessions.set(SESSION_ID, { ...session(), active_run_id: "running-operation" });
    await render();
    await openMenu();
    expect(button("chat.archive").disabled).toBe(true);
    await act(async () => button("chat.archive").click());
    expect(update).not.toHaveBeenCalled();
  });

  it("固定的归档任务仍可打开和恢复，恢复不解除固定", async () => {
    sessions.set(SESSION_ID, { ...session(), status: "archived" });
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ pinnedSessionIds: [SESSION_ID] }));
    await render();
    expect(taskLink()?.textContent).toContain("phase2.archived");
    await openMenu();
    await act(async () => button("chat.restore").click());
    await settle();
    expect(update).toHaveBeenCalledWith(SESSION_ID, expect.objectContaining({ status: "active" }));
    expect(taskLink()?.textContent).not.toContain("phase2.archived");
    expect(readUiPreferences().pinnedSessionIds).toEqual([SESSION_ID]);
  });

  it("菜单支持方向键定位和 Escape 返回触发控件", async () => {
    await render();
    const trigger = await openMenu();
    const menu = document.querySelector<HTMLElement>("[role=menu]")!;
    expect(document.activeElement).toBe(button("phase2.openTask", menu));
    await act(async () => { document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true })); });
    expect(document.activeElement).toBe(button("chat.archive", menu));
    await act(async () => { document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(document.querySelector("[role=menu]")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});
