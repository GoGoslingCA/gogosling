import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Agent, Project, ProjectInput, VersionInfo } from "../api/types";
import { queryKeys } from "../state/queryKeys";
import { CreateProjectDialog, ProjectForm } from "./ProjectForm";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => api }));
vi.mock("./Toast", () => ({ useToast: () => ({ show: toast }) }));

const date = "2026-10-13T00:00:00Z";
const agent: Agent = { id: "agent-enabled", name: "Enabled agent", model: { provider_id: "provider", model: "test" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "workspace", enabled: true, created_at: date, updated_at: date };
const disabledAgent: Agent = { ...agent, id: "agent-disabled", enabled: false };
const project: Project = { id: "project-new", agent_id: agent.id, name: "New project", status: "active", version: 1, created_at: date, updated_at: date };
const toast = vi.fn();
const onCreated = vi.fn();
const onClose = vi.fn();
const dialogMethods = { showModal: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal"), close: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "close") };
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let api: ReturnType<typeof fixture>;

function fixture() {
  return {
    system: { version: vi.fn(async (): Promise<VersionInfo> => ({ version: "dev", capabilities: { "workbench.projects": 1 } })) },
    agents: { list: vi.fn(async () => [disabledAgent, agent]) },
    workbench: { createProject: vi.fn(async (input: ProjectInput): Promise<Project> => ({ ...project, ...input })) },
  };
}
function button(label: string) {
  const found = [...host.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === label);
  if (!found) throw new Error(`未找到按钮 ${label}`);
  return found;
}
async function ready(assertion: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { interval: 10, timeout: 2000 }); }
async function setName(value: string) {
  await act(async () => {
    const input = host.querySelector<HTMLInputElement>("input")!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function mount() { await act(async () => root.render(<QueryClientProvider client={client}><CreateProjectDialog onClose={onClose} onCreated={onCreated} /></QueryClientProvider>)); }

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  api = fixture(); toast.mockReset(); onCreated.mockReset(); onClose.mockReset();
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value(this: HTMLDialogElement) { this.open = true; } });
  Object.defineProperty(HTMLDialogElement.prototype, "close", { configurable: true, value(this: HTMLDialogElement) { this.open = false; } });
});
afterEach(async () => {
  await act(async () => root.unmount()); client.clear(); host.remove(); vi.restoreAllMocks();
  for (const method of ["showModal", "close"] as const) { const descriptor = dialogMethods[method]; if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, method, descriptor); else delete HTMLDialogElement.prototype[method]; }
});

describe("共享项目创建入口", () => {
  it("助手延迟返回后自动选择首个启用助手，创建成功先更新项目缓存", async () => {
    let resolveAgents!: (agents: Agent[]) => void;
    api.agents.list.mockImplementation(() => new Promise((resolve) => { resolveAgents = resolve; }));
    client.setQueryData([...queryKeys.projects, "sidebar"], []);
    await mount(); await ready(() => expect(api.agents.list).toHaveBeenCalledOnce());
    await setName("  A writing project  ");
    expect(button("common.save").disabled).toBe(true);
    expect(api.workbench.createProject).not.toHaveBeenCalled();
    await act(async () => resolveAgents([disabledAgent, agent]));
    await ready(() => expect(button("common.save").disabled).toBe(false));
    expect(host.querySelector<HTMLSelectElement>("select")?.value).toBe(agent.id);
    expect(host.querySelector(`option[value='${disabledAgent.id}']`)).toBeNull();
    onCreated.mockImplementation((saved: Project) => {
      expect(client.getQueryData(queryKeys.project(saved.id))).toEqual(saved);
      expect(client.getQueryState([...queryKeys.projects, "sidebar"])?.isInvalidated).toBe(true);
    });
    await act(async () => button("common.save").click());
    await ready(() => expect(onCreated).toHaveBeenCalledOnce());
    expect(api.workbench.createProject).toHaveBeenCalledWith({ agent_id: agent.id, name: "A writing project", description: "" });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("助手加载失败可重试且保留已输入名称，恢复后才能提交", async () => {
    api.agents.list.mockRejectedValueOnce(new ApiError("network", "offline"));
    await mount(); await ready(() => expect(host.querySelector('[role="alert"]')?.textContent).toBe("errors.network"));
    await setName("Draft project");
    expect(button("common.save").disabled).toBe(true);
    await act(async () => button("projects.retryAgents").click());
    await ready(() => expect(button("common.save").disabled).toBe(false));
    expect(host.querySelector<HTMLInputElement>("input")?.value).toBe("Draft project");
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(api.agents.list).toHaveBeenCalledTimes(2);
    expect(api.workbench.createProject).not.toHaveBeenCalled();
  });

  it("没有启用助手时给出设置提示，不发送无效创建请求", async () => {
    api.agents.list.mockResolvedValue([disabledAgent]);
    await mount(); await ready(() => expect(api.agents.list).toHaveBeenCalledOnce());
    await setName("A project");
    await ready(() => expect(host.textContent).toContain("projects.noEnabledAgent"));
    expect(button("common.save").disabled).toBe(true);
    await act(async () => button("common.save").click());
    expect(api.workbench.createProject).not.toHaveBeenCalled();
  });

  it("旧守护进程不会探测项目写接口或请求助手列表", async () => {
    api.system.version.mockResolvedValue({ version: "legacy" });
    await mount(); await ready(() => expect(host.textContent).toContain("projects.unavailable"));
    await setName("A project");
    expect(button("common.save").disabled).toBe(true);
    expect(api.agents.list).not.toHaveBeenCalled();
    expect(api.workbench.createProject).not.toHaveBeenCalled();
    await act(async () => button("common.cancel").click());
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("编辑项目在助手列表不可用时仍保留既有归属，只提交名称与描述", async () => {
    const submit = vi.fn();
    await act(async () => root.render(<ProjectForm project={project} agents={[]} busy={false} agentError={new ApiError("network", "offline")} onClose={onClose} onSubmit={submit} />));
    const select = host.querySelector<HTMLSelectElement>("select")!;
    expect(select.disabled).toBe(true); expect(select.value).toBe(agent.id);
    await setName("Renamed project");
    expect(button("common.save").disabled).toBe(false);
    await act(async () => button("common.save").click());
    expect(submit).toHaveBeenCalledWith(agent.id, "Renamed project", "");
  });

  it("新建项目收起可选说明，展开编辑再收起后仍提交完整说明", async () => {
    await mount(); await ready(() => expect(api.agents.list).toHaveBeenCalledOnce());
    const details = host.querySelector<HTMLDetailsElement>(".project-form-description")!;
    expect(details.open).toBe(false);
    await setName("A project with notes");
    await act(async () => details.querySelector("summary")!.click());
    expect(details.open).toBe(true);
    await act(async () => {
      const input = details.querySelector<HTMLTextAreaElement>("textarea")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "Keep the brief with this project.");
      input.dispatchEvent(new Event("input", { bubbles: true }));
      details.querySelector("summary")!.click();
    });
    expect(details.open).toBe(false);
    await act(async () => button("common.save").click());
    await ready(() => expect(onCreated).toHaveBeenCalledOnce());
    expect(api.workbench.createProject).toHaveBeenCalledWith({ agent_id: agent.id, name: "A project with notes", description: "Keep the brief with this project." });
  });
});
