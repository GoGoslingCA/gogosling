import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { agentTools, type Agent, type Extension, type Provider, type ToolSpec } from "../api/types";
import { queryKeys } from "../state/queryKeys";
import { AgentsPage, buildAgentInput, builtinToolNames, selectableExtensionIds, type AgentFormState } from "./AgentsPage";

const mocks = vi.hoisted(() => ({
  api: {
    agents: { list: vi.fn(), update: vi.fn(), create: vi.fn(), remove: vi.fn(), tools: vi.fn() },
    providers: { list: vi.fn() },
    extensions: { list: vi.fn() },
    system: { version: vi.fn() },
  },
}));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => mocks.api }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

const base: AgentFormState = {
  name: " a ",
  description: "",
  system_prompt: "",
  provider_id: "p1",
  model: " m ",
  autonomy: "ask_risky",
  enabled: true,
  tools: null,
  extensions: [],
};

describe("buildAgentInput：tools 的三态语义", () => {
  it("创建时 null = 使用默认工具集：不发 tools 字段", () => {
    const input = buildAgentInput(base, "create");
    expect("tools" in input).toBe(false);
    expect(input).toMatchObject({ name: "a", model: { provider_id: "p1", model: "m" } });
  });

  it("编辑时 null = 恢复默认工具集：显式发送 tools: null", () => {
    const input = buildAgentInput(base, "edit");
    expect(input.tools).toBeNull();
    expect(JSON.stringify(input)).toContain('"tools":null');
  });

  it("[] = 不启用任何内置工具，原样发送", () => {
    expect(buildAgentInput({ ...base, tools: [] }, "edit").tools).toEqual([]);
    expect(buildAgentInput({ ...base, tools: [] }, "create").tools).toEqual([]);
  });

  it("自定义列表原样发送", () => {
    expect(buildAgentInput({ ...base, tools: ["read_file"] }, "edit").tools).toEqual(["read_file"]);
  });

  it("扩展绑定显式发送，空列表可以解绑全部扩展", () => {
    expect(buildAgentInput({ ...base, extensions: ["skill", "mcp"] }, "create").extensions).toEqual(["skill", "mcp"]);
    expect(buildAgentInput(base, "edit").extensions).toEqual([]);
  });
  it("模板创建使用后端默认提示和工具，不给编辑请求重新套用模板", () => {
    const input = buildAgentInput({ ...base, template: "general-assistant" }, "create");
    expect(input.template).toBe("general-assistant");
    expect(input).not.toHaveProperty("system_prompt");
    expect(input).not.toHaveProperty("autonomy");
    expect(input).not.toHaveProperty("tools");
    expect(buildAgentInput({ ...base, template: "ops-engineer", system_prompt: "自己的提示", tools: [], autonomy: "ask_all" }, "create")).toMatchObject({ system_prompt: "自己的提示", tools: [], autonomy: "ask_all" });
    expect(buildAgentInput({ ...base, template: "ops-engineer" }, "edit")).not.toHaveProperty("template");
  });
});

const provider: Provider = { id: "p1", name: "Provider", kind: "ollama", enabled: true, default_model: "m", created_at: "", updated_at: "" };
const agent: Agent = { id: "a1", name: "Agent", model: { provider_id: "p1", model: "m" }, tools: ["read_file"], extensions: ["disabled", "missing"], autonomy: "ask_risky", workspace_dir: "a1", enabled: true, created_at: "", updated_at: "" };
const installed: Extension[] = [
  { id: "skill", name: "EnabledSkill", kind: "skill", source: "local", install_dir: "skill", enabled: true, installed_at: "", updated_at: "" },
  { id: "disabled", name: "DisabledMcp", kind: "mcp_server", source: "local", install_dir: "disabled", enabled: false, installed_at: "", updated_at: "" },
  { id: "other-disabled", name: "UnassignedDisabled", kind: "plugin", source: "local", install_dir: "other", enabled: false, installed_at: "", updated_at: "" },
];
const specs: ToolSpec[] = [
  { name: "read_file", source: "builtin", description: "read", risk: "read", input_schema: {} },
  { name: "mcp_fetch", source: "mcp:disabled", description: "fetch", risk: "network", input_schema: {} },
];

describe("智能体配置：内置工具与扩展分开绑定", () => {
  it("目录含未选择的标准内置工具和未知已选名称，不把 MCP 声明当作内置工具", () => {
    expect(builtinToolNames(specs, ["old_tool"]).includes("old_tool")).toBe(true);
    expect(builtinToolNames(specs, []).includes("edit_file")).toBe(true);
    expect(builtinToolNames(specs, []).includes("delete_file")).toBe(true);
    expect(builtinToolNames(specs, []).includes("mcp_fetch")).toBe(false);
    expect(selectableExtensionIds(installed, agent.extensions)).toEqual(["skill", "disabled", "missing"]);
  });

  let root: Root;
  let container: HTMLDivElement;
  let client: QueryClient;
  beforeEach(() => {
    vi.clearAllMocks();
    (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    HTMLDialogElement.prototype.showModal = function () { this.open = true; };
    HTMLDialogElement.prototype.close = function () { this.open = false; };
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
    client.setQueryData(queryKeys.agents, [agent]);
    client.setQueryData(queryKeys.providers, [provider]);
    client.setQueryData(queryKeys.extensions(), installed);
    client.setQueryData(queryKeys.agentTools(agent.id), specs);
    mocks.api.agents.list.mockResolvedValue([agent]);
    mocks.api.providers.list.mockResolvedValue([provider]);
    mocks.api.extensions.list.mockResolvedValue(installed);
    mocks.api.agents.tools.mockResolvedValue(specs);
    mocks.api.agents.update.mockResolvedValue(agent);
    mocks.api.agents.create.mockResolvedValue(agent);
    mocks.api.system.version.mockResolvedValue({ capabilities: { "tools.read_document": 1 } });
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    client.clear();
    container.remove();
  });

  async function render() {
    await act(async () => root.render(createElement(QueryClientProvider, { client }, createElement(AgentsPage))));
  }
  function button(text: string): HTMLButtonElement {
    return [...container.querySelectorAll("button")].find((element) => element.textContent === text)!;
  }
  function checkbox(text: string): HTMLInputElement {
    return [...container.querySelectorAll("dialog label.check")].find((element) => element.textContent?.includes(text))!.querySelector("input")!;
  }

  it("旧助手可只启用文档读取，其他工具权限不随之增加", async () => {
    await render();
    await act(async () => button("common.edit").click());
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)); });
    expect(checkbox("read_document").checked).toBe(false);
    await act(async () => checkbox("read_document").click());
    await act(async () => container.querySelector("#agent-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.agents.update).toHaveBeenCalledWith("a1", expect.objectContaining({ tools: ["read_file", "read_document"] }));
  });

  it("旧守护进程未声明文档读取时不提供新增选项", async () => {
    mocks.api.system.version.mockResolvedValue({ capabilities: {} });
    await render(); await act(async () => button("common.edit").click());
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)); });
    expect(container.querySelector("dialog")!.textContent).not.toContain("read_document");
    expect(checkbox("read_file").checked).toBe(true);
  });

  it("编辑时显示已停用与已卸载绑定，用户可以增加技能并移除失效绑定后保存", async () => {
    await render();
    await act(async () => button("common.edit").click());
    expect(checkbox("DisabledMcp").checked).toBe(true);
    expect(checkbox("missing").checked).toBe(true);
    expect(container.querySelector("dialog")!.textContent).not.toContain("UnassignedDisabled");
    expect(container.querySelector("dialog")!.textContent).not.toContain("mcp_fetch");
    await act(async () => { checkbox("EnabledSkill").click(); checkbox("missing").click(); });
    await act(async () => container.querySelector("#agent-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.agents.update).toHaveBeenCalledWith("a1", expect.objectContaining({ extensions: ["disabled", "skill"], tools: ["read_file"] }));
  });

  it("创建时可直接选择内置工具，并将显式空列表保存为禁用全部工具", async () => {
    await render();
    await act(async () => button("agents.create").click());
    await act(async () => checkbox("agents.toolsDefault").click());
    expect(checkbox("edit_file").checked).toBe(false);
    const name = container.querySelector<HTMLInputElement>("#agent-form input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "NewAgent");
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => container.querySelector("#agent-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.agents.create).toHaveBeenCalledWith(expect.objectContaining({ name: "NewAgent", tools: [], extensions: [] }));
  });

  it("保存待处理期间的连续提交只产生一次更新", async () => {
    let finish: (value: Agent) => void = () => undefined;
    mocks.api.agents.update.mockImplementation(() => new Promise<Agent>((resolve) => { finish = resolve; }));
    await render();
    await act(async () => button("common.edit").click());
    const form = container.querySelector("#agent-form")!;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(mocks.api.agents.update).toHaveBeenCalledTimes(1);
    await act(async () => finish(agent));
  });

  it("模板选择只在新建显示，选择后保留已填写的名称并提交模板ID", async () => {
    await render();
    await act(async () => button("common.edit").click());
    expect(container.querySelector('[aria-label="agentTemplates.label"]')).toBeNull();
    await act(async () => button("common.cancel").click());
    await act(async () => button("agents.create").click());
    const name = container.querySelector<HTMLInputElement>("#agent-form input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "Local helper");
      name.dispatchEvent(new Event("input", { bubbles: true }));
      const select = container.querySelector<HTMLSelectElement>('[aria-label="agentTemplates.label"]')!;
      select.value = "general-assistant";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(name.value).toBe("Local helper");
    await act(async () => container.querySelector("#agent-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.agents.create).toHaveBeenCalledWith(expect.objectContaining({ name: "Local helper", template: "general-assistant" }));
    expect(mocks.api.agents.create.mock.calls[0]![0]).not.toHaveProperty("system_prompt");
  });
});

describe("agentTools", () => {
  it("契约保证是数组；旧版守护进程省略空数组时按 []（不启用任何内置工具）处理", () => {
    expect(agentTools({ tools: ["a"] })).toEqual(["a"]);
    expect(agentTools({ tools: [] })).toEqual([]);
    expect(agentTools({})).toEqual([]);
    expect(agentTools({ tools: null })).toEqual([]);
  });
});
