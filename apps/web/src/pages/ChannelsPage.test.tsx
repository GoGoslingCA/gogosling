import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Agent, Channel, ChannelInput, ChannelPatch } from "../api/types";
import { buildChannelInput, ChannelsPage, CREATABLE_CHANNEL_KINDS, parseChannelConfig, type ChannelFormState } from "./ChannelsPage";

const mocks = vi.hoisted(() => ({
  agents: vi.fn<() => Promise<Agent[]>>(),
  list: vi.fn<(agentId?: string, signal?: AbortSignal) => Promise<Channel[]>>(),
  create: vi.fn<(input: ChannelInput) => Promise<Channel>>(),
  update: vi.fn<(id: string, input: ChannelPatch) => Promise<Channel>>(),
  remove: vi.fn<(id: string) => Promise<void>>(),
  reload: vi.fn<(id: string) => Promise<Channel>>(),
  show: vi.fn(),
}));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ agents: { list: mocks.agents }, channels: { list: mocks.list, create: mocks.create, update: mocks.update, remove: mocks.remove, reload: mocks.reload } }) }));
vi.mock("../components/Toast", () => ({ useToast: () => ({ show: mocks.show }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: "en" } }) }));

const agent: Agent = { id: "agent-1", name: "Assistant", model: { provider_id: "p", model: "m" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "work", enabled: true, created_at: "2026-10-12T00:00:00Z", updated_at: "2026-10-12T00:00:00Z" };
const channel: Channel = { id: "channel-1", agent_id: agent.id, kind: "telegram", name: "Messages", has_secret: true, config: { group_mode: "mention" }, enabled: true, status: "running", created_at: "2026-10-12T00:00:00Z", updated_at: "2026-10-12T00:00:00Z" };
const form: ChannelFormState = { agent_id: "agent-1", kind: "telegram", name: " Messages ", secret: "", config: "{}", enabled: true };

describe("通道配置与只写凭据", () => {
  it("只提供已注册的平台创建选项", () => expect(CREATABLE_CHANNEL_KINDS).toEqual(["console", "telegram"]));
  it("解析对象并保留平台字段，空白恢复默认配置", () => {
    expect(parseChannelConfig('{"api_base":"https://example.test","allowed_peers":["42"]}')).toEqual({ config: { api_base: "https://example.test", allowed_peers: ["42"] }, error: null });
    expect(parseChannelConfig("  ")).toEqual({ config: {}, error: null });
  });
  it.each(["null", "[]", '"text"', "true", "12"])("拒绝非对象配置 %s", (value) => expect(parseChannelConfig(value)).toEqual({ config: null, error: "not_object" }));
  it("无效 JSON 不静默变成空配置", () => expect(parseChannelConfig("{bad}")).toEqual({ config: null, error: "invalid_json" }));
  it("编辑留空凭据不清除现有值，也不发送平台与归属", () => {
    const input = buildChannelInput({ ...form, secret: " " }, {}, "edit");
    expect(input).toEqual({ name: "Messages", config: {}, enabled: true });
    expect(input).not.toHaveProperty("secret");
    expect(input).not.toHaveProperty("agent_id");
    expect(input).not.toHaveProperty("kind");
  });
  it("创建携带归属，非空凭据支持轮换", () => {
    expect(buildChannelInput({ ...form, secret: " replacement " }, {}, "create")).toEqual({ agent_id: "agent-1", kind: "telegram", name: "Messages", secret: "replacement", config: {}, enabled: true });
    expect(buildChannelInput({ ...form, secret: " replacement " }, {}, "edit").secret).toBe("replacement");
  });
});

let host: HTMLDivElement;
let root: Root;
let client: QueryClient;
const dialogMethods = { showModal: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal"), close: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "close") };

beforeEach(() => {
  vi.clearAllMocks();
  mocks.agents.mockResolvedValue([agent]);
  mocks.list.mockResolvedValue([channel]);
  mocks.create.mockResolvedValue(channel);
  mocks.update.mockResolvedValue(channel);
  mocks.remove.mockResolvedValue(undefined);
  mocks.reload.mockResolvedValue(channel);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value(this: HTMLDialogElement) { this.open = true; } });
  Object.defineProperty(HTMLDialogElement.prototype, "close", { configurable: true, value(this: HTMLDialogElement) { this.open = false; } });
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
afterEach(async () => {
  await act(async () => root.unmount());
  client.clear();
  host.remove();
  vi.restoreAllMocks();
  for (const method of ["showModal", "close"] as const) {
    const descriptor = dialogMethods[method];
    if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, method, descriptor);
    else Reflect.deleteProperty(HTMLDialogElement.prototype, method);
  }
});
async function renderPage() {
  await act(async () => root.render(<QueryClientProvider client={client}><ChannelsPage /></QueryClientProvider>));
  await settle(() => expect(mocks.list).toHaveBeenCalled());
}
async function settle(assertion: () => void) {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    assertion();
  });
}
function button(label: string): HTMLButtonElement {
  const found = Array.from(host.querySelectorAll("button")).find((node) => node.textContent === label);
  if (!found) throw new Error(`button missing: ${label}`);
  return found;
}
async function click(label: string) { await act(async () => button(label).click()); }
async function fill(label: string, value: string) {
  const input = host.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[aria-label="${label}"]`);
  if (!input) throw new Error(`input missing: ${label}`);
  const prototype = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(prototype, "value")?.set?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function submit() { await act(async () => host.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }))); }

describe("通道管理行为", () => {
  it("创建默认选择有效智能体，只写凭据；失败保留表单并显示服务端原因", async () => {
    mocks.agents.mockResolvedValue([{ ...agent, id: "disabled", enabled: false }, agent]);
    mocks.list.mockResolvedValue([]);
    mocks.create.mockRejectedValue(new ApiError("unavailable", "credential store unavailable", { status: 503 }));
    await renderPage();
    await settle(() => expect(button("channels.create").disabled).toBe(false));
    await click("channels.create");
    expect(Array.from(host.querySelector<HTMLSelectElement>('[aria-label="channels.kind"]')?.options ?? []).map((option) => option.value)).toEqual(["console", "telegram"]);
    expect(host.querySelector<HTMLSelectElement>('[aria-label="channels.agent"]')?.value).toBe(agent.id);
    expect(button("common.save").disabled).toBe(true);
    await fill("channels.name", " New bot ");
    await fill("channels.secret", "memory-only-example");
    await fill("channels.config", '{"allowed_peers":["42"]}');
    await submit();
    await settle(() => expect(host.querySelector('[role="alert"]')?.textContent).toContain("credential store unavailable"));
    expect(mocks.create).toHaveBeenCalledWith({ agent_id: agent.id, kind: "telegram", name: "New bot", secret: "memory-only-example", config: { allowed_peers: ["42"] }, enabled: true });
    expect(host.querySelector<HTMLInputElement>('[aria-label="channels.secret"]')?.value).toBe("memory-only-example");
    expect(host.textContent).not.toContain("memory-only-example");
    expect(host.querySelector<HTMLInputElement>('[aria-label="channels.name"]')?.value).toBe(" New bot ");
  });

  it("编辑保留已有凭据，禁止修改归属和平台；无效配置阻止 API 调用", async () => {
    await renderPage();
    await settle(() => expect(host.textContent).toContain(channel.name));
    await click("common.edit");
    expect(host.querySelector<HTMLInputElement>('[aria-label="channels.secret"]')?.value).toBe("");
    expect(host.querySelector<HTMLSelectElement>('[aria-label="channels.agent"]')?.disabled).toBe(true);
    expect(host.querySelector<HTMLSelectElement>('[aria-label="channels.kind"]')?.disabled).toBe(true);
    await fill("channels.config", "[]");
    await submit();
    expect(host.querySelector('[role="alert"]')?.textContent).toBe("channels.configObject");
    expect(mocks.update).not.toHaveBeenCalled();
    await fill("channels.config", "");
    await fill("channels.name", "Renamed");
    await submit();
    await settle(() => expect(mocks.update).toHaveBeenCalledWith(channel.id, { name: "Renamed", config: {}, enabled: true }));
    await settle(() => expect(host.querySelector("dialog")).toBeNull());
  });

  it("重载、启停与删除调用已有管理 API，并刷新失败状态", async () => {
    let next: Channel[] = [channel];
    mocks.list.mockImplementation(async () => next);
    mocks.reload.mockImplementation(async () => { next = [{ ...channel, status: "failed", last_error: "network unavailable" }]; return next[0]; });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await renderPage();
    await settle(() => expect(host.textContent).toContain("channels.status_running"));
    await click("channels.reload");
    await settle(() => expect(host.textContent).toContain("channels.status_failed"));
    expect(mocks.reload).toHaveBeenCalledWith(channel.id);
    expect(host.querySelector('[role="alert"]')?.textContent).toBe("network unavailable");
    await click("common.disable");
    await settle(() => expect(mocks.update).toHaveBeenCalledWith(channel.id, { enabled: false }));
    await settle(() => expect(button("common.delete").disabled).toBe(false));
    await click("common.delete");
    await settle(() => expect(mocks.remove).toHaveBeenCalledWith(channel.id));
  });
});
