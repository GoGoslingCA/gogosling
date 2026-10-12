import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, Settings, VersionInfo } from "../api/types";
import { ApiError } from "../api/client";
import { queryKeys } from "../state/queryKeys";
import { readUiPreferences } from "../lib/uiPreferences";
import { SettingsPage } from "./SettingsPage";

vi.mock("./AgentsPage", () => ({ AgentsPage: () => <div data-testid="managed-agents">agent-manager</div> }));
vi.mock("./ProvidersPage", () => ({ ProvidersPage: () => <div data-testid="managed-models">provider-manager</div> }));
vi.mock("./ExtensionsPage", () => ({ ExtensionsPage: () => <div data-testid="managed-extensions">extension-manager</div> }));
vi.mock("./ChannelsPage", () => ({ ChannelsPage: () => <div data-testid="managed-channels">channel-manager</div> }));
vi.mock("./AuditPage", () => ({ AuditPage: () => <div data-testid="managed-audit">audit-history</div> }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: translate, i18n: { language: testLanguage } }) }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => api }));
vi.mock("../components/Toast", () => ({ useToast: () => ({ show: toast }) }));
vi.mock("../i18n", async (importOriginal) => ({
  ...await importOriginal<typeof import("../i18n")>(),
  applyLocale: (...args: unknown[]) => applyLocale(...args),
}));
const toast = vi.fn(); const applyLocale = vi.fn(async (..._args: unknown[]) => undefined);
const translate = vi.fn((key: string, _options?: unknown) => key);
const agent: Agent = { id: "agent-a", name: "写作助手", model: { provider_id: "provider-a", model: "test-model" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "/workspace/project-a", enabled: true, created_at: "2026-10-12T00:00:00Z", updated_at: "2026-10-12T00:00:00Z" };
let api: ReturnType<typeof fixture>;
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let narrow: boolean;
let testLanguage: string;
function fixture() { return { system: { version: vi.fn(async (): Promise<VersionInfo> => ({ version: "9.9.9", commit: "abcdef1234567890", started_at: "2026-10-12T01:00:00Z", worker: { status: "ready", version: "worker-1", python: "3.12.0" }, capabilities: { "workbench.limits": { text_bytes: 524288 } } })), resources: vi.fn(async () => ({max_parallel: 2, max_queued: 32, running: 1, queued: 3, awaiting_approval: 1})), setResources: vi.fn(async (n: number) => ({max_parallel: n, max_queued: 32, running: 1, queued: 3, awaiting_approval: 1})) }, settings: { get: vi.fn(async (): Promise<Settings> => ({ timezone: "UTC", locale: "zh" })), update: vi.fn(async (input: Settings) => input) }, agents: { list: vi.fn(async () => [agent]) } }; }
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear(); delete document.documentElement.dataset.appearance; narrow = false;
  api = fixture(); toast.mockClear(); applyLocale.mockClear(); translate.mockClear(); testLanguage = "zh";
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({ matches: query.includes("max-width") ? narrow : false, media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); localStorage.clear(); delete document.documentElement.dataset.appearance; vi.restoreAllMocks(); vi.unstubAllGlobals(); });
async function ready(assertion: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { timeout: 2000, interval: 10 }); }
async function mount(entry = "/settings") { await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[entry]}><SettingsPage /></MemoryRouter></QueryClientProvider>)); await ready(() => expect(api.settings.get).toHaveBeenCalled()); }
async function nav(id: string) { await act(async () => host.querySelector<HTMLButtonElement>(`#settings-nav-${id}`)!.click()); }
async function input(id: string, value: string) { await act(async () => { const node = host.querySelector<HTMLInputElement>(`#${id}`)!; Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(node, value); node.dispatchEvent(new Event("input", { bubbles: true })); node.dispatchEvent(new Event("change", { bubbles: true })); }); }
async function select(id: string, value: string) { await act(async () => { const node = host.querySelector<HTMLSelectElement>(`#${id}`)!; node.value = value; node.dispatchEvent(new Event("change", { bubbles: true })); }); }
async function save() { await act(async () => host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()); }

describe("设置双栏页面", () => {
  it("搜索筛选真实分组，方向键切换面板，清除后恢复完整导航", async () => {
    await mount(); await ready(() => expect(host.querySelector<HTMLInputElement>("#settings-timezone")?.value).toBe("UTC"));
    expect(host.textContent).toContain("9.9.9");
    expect(host.textContent).toContain("status.worker_ready");
    expect(host.textContent).toContain("worker-1 · 3.12.0");
    expect(host.textContent).toContain("settingsLayout.capabilitiesNone");
    expect(host.querySelectorAll('[role="tab"]')).toHaveLength(9);
    const general = host.querySelector<HTMLButtonElement>("#settings-nav-general")!;
    await act(async () => general.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
    expect(document.activeElement?.id).toBe("settings-nav-appearance");
    expect(host.querySelector('[role="tabpanel"]')?.id).toBe("settings-panel-appearance");
    const search = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "settings.timezone"); search.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(host.querySelectorAll('[role="tab"]')).toHaveLength(1);
    expect(host.querySelector('[role="tabpanel"]')?.id).toBe("settings-panel-general");
    await act(async () => search.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(host.querySelectorAll('[role="tab"]')).toHaveLength(9);
  });

  it.each(["en", "fr"] as const)("语言 %s 经内核成功保存后才切换，同轮重复提交只有一次", async (locale) => {
    let finish!: (settings: Settings) => void;
    api.settings.update.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await mount(); await ready(() => expect(host.querySelector<HTMLInputElement>("#settings-timezone")?.value).toBe("UTC"));
    await input("settings-timezone", "Asia/Shanghai"); await select("settings-language", locale);
    await act(async () => { host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click(); host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click(); });
    expect(api.settings.update).toHaveBeenCalledOnce(); expect(api.settings.update).toHaveBeenCalledWith({ timezone: "Asia/Shanghai", locale });
    expect(applyLocale).not.toHaveBeenCalled();
    await act(async () => finish({ timezone: "Asia/Shanghai", locale }));
    await ready(() => expect(applyLocale).toHaveBeenCalledWith(locale));
    expect(translate).toHaveBeenCalledWith("settings.saved", { lng: locale });
    expect(toast).toHaveBeenCalledWith("settings.saved", "ok");
  });

  it("未保存偏好时沿用浏览器法语，不误报未保存修改", async () => {
    testLanguage = "fr-CA";
    api.settings.get.mockResolvedValue({ timezone: "UTC" });
    await mount(); await ready(() => expect(host.querySelector<HTMLSelectElement>("#settings-language")?.value).toBe("fr"));
    expect(host.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true);
    expect(api.settings.update).not.toHaveBeenCalled();
  });

  it("旧内核拒绝新语言时保留选择且不提前切换，可改回英文保存", async () => {
    await mount(); await ready(() => expect(host.querySelector<HTMLInputElement>("#settings-timezone")?.value).toBe("UTC"));
    await select("settings-language", "fr");
    api.settings.update.mockRejectedValueOnce(new ApiError("invalid", "")); await save();
    await ready(() => expect(toast).toHaveBeenCalledWith("errors.invalid", "error"));
    expect(applyLocale).not.toHaveBeenCalled();
    expect(host.querySelector<HTMLSelectElement>("#settings-language")?.value).toBe("fr");
    expect(client.getQueryData<Settings>(queryKeys.settings)?.locale).toBe("zh");
    await select("settings-language", "en"); await save();
    await ready(() => expect(applyLocale).toHaveBeenCalledWith("en"));
  });

  it("无效时区阻止保存，保存失败和后台刷新都保留当前编辑", async () => {
    await mount(); await ready(() => expect(host.querySelector<HTMLInputElement>("#settings-timezone")?.value).toBe("UTC"));
    await input("settings-timezone", "not/a-zone"); await save();
    expect(api.settings.update).not.toHaveBeenCalled(); expect(host.textContent).toContain("settings.timezoneInvalid");
    await input("settings-timezone", "Asia/Tokyo");
    api.settings.update.mockRejectedValueOnce(new ApiError("network", "")); await save();
    await ready(() => expect(toast).toHaveBeenCalledWith("errors.network", "error"));
    await act(async () => { client.setQueryData(queryKeys.settings, { timezone: "Europe/London", locale: "zh" }); });
    expect(host.querySelector<HTMLInputElement>("#settings-timezone")?.value).toBe("Asia/Tokyo");
    await nav("appearance"); await nav("general");
    expect(host.querySelector<HTMLInputElement>("#settings-timezone")?.value).toBe("Asia/Tokyo");
  });

  it("跟随系统开关、手动主题和成果宽度立即作用于本机偏好，不改服务设置", async () => {
    await mount(); await nav("appearance");
    const toggle = host.querySelector<HTMLButtonElement>('[role="switch"]')!;
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    expect(host.querySelector<HTMLSelectElement>("#settings-theme")!.disabled).toBe(true);
    await act(async () => toggle.click()); await select("settings-theme", "dark");
    expect(document.documentElement.dataset.appearance).toBe("dark"); expect(readUiPreferences().appearance).toBe("dark");
    await input("settings-panel-width", "610"); expect(readUiPreferences().artifactWidth).toBe(610);
    await act(async () => toggle.click()); expect(readUiPreferences().appearance).toBe("system"); expect(document.documentElement.dataset.appearance).toBe("system");
    expect(api.settings.update).not.toHaveBeenCalled();
  });

  it("模型和文件入口保持真实路由，权限范围只读且无空的开机或休眠开关", async () => {
    await mount();
    expect(host.querySelector('[role="switch"]')).toBeNull();
    expect(host.textContent).toContain("settingsLayout.startupHint");
    await nav("models"); await ready(() => expect(host.querySelector('[data-testid="managed-models"]')).not.toBeNull());
    await nav("files"); await ready(() => expect(host.querySelector(".settings-path")?.textContent).toBe(agent.workspace_dir));
    expect(host.textContent).toContain("agents.autonomy_ask_risky");
    expect(host.textContent).toContain("test-model");
    expect(host.textContent).toContain("settingsLayout.textLimit");
    expect(host.querySelector('a[href="/settings?section=agents"]')).not.toBeNull();
    expect(host.querySelectorAll(".settings-content input,.settings-content select")).toHaveLength(0);
    expect(host.querySelector<HTMLDetailsElement>(".settings-details")?.open).toBe(false); expect(host.textContent).toContain("preferences.fileAccessLimit");
    expect(host.querySelector('a[href="/chat"]')).not.toBeNull();
  });

  it("窄窗口用横向导航语义，搜索无结果时提供清除入口", async () => {
    narrow = true; await mount();
    expect(host.querySelector('[role="tablist"]')?.getAttribute("aria-orientation")).toBe("horizontal");
    const search = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "unmatched-search"); search.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(host.querySelectorAll('[role="tab"]')).toHaveLength(0); expect(host.textContent).toContain("settingsLayout.noResults");
    await act(async () => host.querySelector<HTMLButtonElement>('.settings-empty button')!.click());
    expect(host.querySelectorAll('[role="tab"]')).toHaveLength(9);
  });
});


describe("资源设置兼容与真实状态", () => {
  it("只有支持能力时显示入口，打开资源页才查询，成功后更新并发数", async () => {
    api.system.version.mockResolvedValue({version: "test", capabilities: {"runtime.resources": 1}});
    await mount(); await ready(() => expect(host.querySelector("#settings-nav-resources")).not.toBeNull());
    expect(api.system.resources).not.toHaveBeenCalled();
    await nav("resources"); await ready(() => expect(host.querySelector<HTMLSelectElement>("#parallel-limit")?.value).toBe("2"));
    expect(host.querySelectorAll('[data-occupied="true"]')).toHaveLength(1);
    await select("parallel-limit", "1"); await ready(() => expect(host.querySelector<HTMLSelectElement>("#parallel-limit")?.value).toBe("1"));
    expect(api.system.setResources).toHaveBeenCalledExactlyOnceWith(1);
  });
  it("保存失败保持服务端原值，旧版本不发出资源请求", async () => {
    await mount(); await ready(() => expect(api.system.version).toHaveBeenCalled());
    expect(host.querySelector("#settings-nav-resources")).toBeNull(); expect(api.system.resources).not.toHaveBeenCalled();
    await act(async () => { client.setQueryData(queryKeys.version, {version: "test", capabilities: {"runtime.resources":1}}); });
    await ready(() => expect(host.querySelector("#settings-nav-resources")).not.toBeNull());
    await nav("resources"); await ready(() => expect(host.querySelector<HTMLSelectElement>("#parallel-limit")?.value).toBe("2"));
    api.system.setResources.mockRejectedValueOnce(new ApiError("network", "")); await select("parallel-limit", "4");
    await ready(() => expect(host.textContent).toContain("errors.network"));
    expect(host.querySelector<HTMLSelectElement>("#parallel-limit")?.value).toBe("2");
  });
});


describe("设置内承载管理页", () => {
  it.each(["agents", "models", "extensions", "channels", "audit"])("直接链接恢复 %s 分组，其他管理页不挂载", async (section) => {
    await mount(`/settings?section=${section}`);
    await ready(() => expect(host.querySelector(`[data-testid="managed-${section}"]`)).not.toBeNull());
    expect(host.querySelectorAll('[data-testid^="managed-"]')).toHaveLength(1);
    expect(host.querySelector(`#settings-nav-${section}`)?.getAttribute("aria-selected")).toBe("true");
    expect(api.agents.list).not.toHaveBeenCalled();
  });
  it("未知分组回到通用，搜索能找到移动后的扩展入口", async () => {
    await mount("/settings?section=unknown"); expect(host.querySelector('[role="tabpanel"]')?.id).toBe("settings-panel-general");
    const search = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "settingsLayout.extensionsSearch"); search.dispatchEvent(new Event("input", {bubbles:true})); });
    await ready(() => expect(host.querySelector('[data-testid="managed-extensions"]')).not.toBeNull());
    expect(host.querySelectorAll('[role="tab"]')).toHaveLength(1);
  });
});

it("切换设置分组后仍能回到原任务及成果", async () => {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[{pathname:"/settings",state:{returnTo:"/chat/previous?artifact=result"}}]}><SettingsPage/></MemoryRouter></QueryClientProvider>));
  await nav("appearance"); await nav("shortcuts");
  expect(host.querySelector(".settings-back")?.getAttribute("href")).toBe("/chat/previous?artifact=result");
});
