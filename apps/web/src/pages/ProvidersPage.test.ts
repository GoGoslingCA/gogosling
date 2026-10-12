import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Provider } from "../api/types";
import { queryKeys } from "../state/queryKeys";
import { ProvidersPage, buildProviderInput, validProviderBaseUrl, type ProviderFormState } from "./ProvidersPage";

const mocks = vi.hoisted(() => ({ api: { providers: { list: vi.fn(), update: vi.fn(), create: vi.fn(), remove: vi.fn(), test: vi.fn() } } }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => mocks.api }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

describe("供应商请求与地址校验", () => {
  const form: ProviderFormState = { preset: "custom", name: " Local ", kind: "ollama", base_url: " http://127.0.0.1:11434 ", api_key: "", default_model: " model ", enabled: true };
  it("空密钥不随保存发送，不覆盖现有凭据", () => {
    expect(buildProviderInput(form)).toEqual({ name: "Local", kind: "ollama", base_url: "http://127.0.0.1:11434", default_model: "model", enabled: true });
    expect("api_key" in buildProviderInput({ ...form, api_key: " " })).toBe(false);
  });
  it("仅接受 HTTP 地址且禁止用户信息、查询参数和片段", () => {
    expect(validProviderBaseUrl("ollama", "")).toBe(true);
    expect(validProviderBaseUrl("anthropic", "")).toBe(true);
    expect(validProviderBaseUrl("openai_compatible", "")).toBe(false);
    for (const value of ["file:///model", "https://user@example.com", "https://example.com?key=x", "https://example.com#fragment", "invalid"]) {
      expect(validProviderBaseUrl("openai_compatible", value), value).toBe(false);
    }
    expect(validProviderBaseUrl("openai_compatible", "https://example.com/v1")).toBe(true);
  });
});

describe("供应商模型发现交互", () => {
  const provider: Provider = { id: "p1", name: "Local", kind: "ollama", base_url: "http://127.0.0.1:11434", default_model: "", models: ["previous"], has_secret: false, enabled: true, created_at: "", updated_at: "" };
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
    client.setQueryData(queryKeys.providers, [provider]);
    mocks.api.providers.list.mockResolvedValue([provider]);
    mocks.api.providers.update.mockResolvedValue(provider);
    mocks.api.providers.test.mockResolvedValue({ ok: true, models: ["discovered-a", "discovered-b"], latency_ms: 5 });
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    client.clear();
    container.remove();
  });
  function button(text: string): HTMLButtonElement {
    return [...container.querySelectorAll("button")].find((element) => element.textContent === text)!;
  }
  async function renderEdit() {
    await act(async () => root.render(createElement(QueryClientProvider, { client }, createElement(ProvidersPage))));
    await act(async () => button("common.edit").click());
  }
  async function change(label: string, value: string) {
    const input = container.querySelector<HTMLInputElement>(`[aria-label="${label}"]`)!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it("测试已保存配置后提供模型候选，空默认模型填入发现结果并可保存", async () => {
    await renderEdit();
    await act(async () => button("providers.discoverModels").click());
    await vi.waitFor(() => expect(container.querySelectorAll("#provider-model-options option")).toHaveLength(2));
    expect(mocks.api.providers.test).toHaveBeenCalledWith("p1");
    expect(container.querySelector<HTMLInputElement>('[aria-label="providers.defaultModel"]')!.value).toBe("discovered-a");
    await change("providers.defaultModel", "discovered-b");
    await act(async () => container.querySelector("#provider-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.providers.update).toHaveBeenCalledWith("p1", expect.objectContaining({ default_model: "discovered-b" }));
    expect(mocks.api.providers.update.mock.calls[0]![1]).not.toHaveProperty("api_key");
  });

  it("地址有未保存变更时阻止测试旧配置，并拒绝无效地址提交", async () => {
    await renderEdit();
    await change("providers.baseUrl", "https://example.com/v1");
    expect(button("providers.discoverModels").disabled).toBe(true);
    expect(container.querySelector("dialog")!.textContent).toContain("providers.discoverSaveFirst");
    await change("providers.baseUrl", "file:///tmp/model");
    await act(async () => container.querySelector("#provider-form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.providers.update).not.toHaveBeenCalled();
    expect(container.querySelector('[aria-label="providers.baseUrl"]')!.getAttribute("aria-invalid")).toBe("true");
  });

  it("模型发现失败在当前表单显示原因，并保留用户的默认模型", async () => {
    mocks.api.providers.test.mockResolvedValue({ ok: false, error: "Model service unavailable" });
    await renderEdit();
    await change("providers.defaultModel", "manual-model");
    await act(async () => button("providers.discoverModels").click());
    await vi.waitFor(() => expect(container.querySelector("dialog")!.textContent).toContain("Model service unavailable"));
    expect(container.querySelector<HTMLInputElement>('[aria-label="providers.defaultModel"]')!.value).toBe("manual-model");
    expect(container.querySelectorAll("#provider-model-options option")).toHaveLength(1);
  });
});
