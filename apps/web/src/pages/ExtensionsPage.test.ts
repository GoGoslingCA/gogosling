import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";
import { extensionsApi } from "../api/modules/extensions";
import { queryKeys } from "../state/queryKeys";
import { ExtensionsPage, parseEnvLines, parseHeaderLines } from "./ExtensionsPage";

const mocks = vi.hoisted(() => ({ api: { extensions: { list: vi.fn(), install: vi.fn(), setEnabled: vi.fn(), uninstall: vi.fn() } } }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => mocks.api }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

describe("parseEnvLines", () => {
  it("解析 KEY=VALUE，忽略空行、注释与无等号的行，值里的 = 保留", () => {
    expect(parseEnvLines("A=1\n\n# c\nB=secret:ref\nbad\nC=x=y\n")).toEqual({ A: "1", B: "secret:ref", C: "x=y" });
  });
  it("特殊属性名称作为普通配置键保存，不修改对象原型", () => {
    const env = parseEnvLines("__proto__=plain\nconstructor=command");
    expect(Object.getPrototypeOf(env)).toBeNull();
    expect(JSON.parse(JSON.stringify(env))).toEqual({ ["__proto__"]: "plain", constructor: "command" });
  });
});

describe("parseHeaderLines", () => {
  it("支持 Name: value 与 Name=value，以先出现的分隔符拆分", () => {
    const { headers, invalid } = parseHeaderLines("Authorization: Bearer a=b\nX-Key=secret:my-ref\n\n# note\nX-Empty:\n");
    expect(headers).toEqual({ Authorization: "Bearer a=b", "X-Key": "secret:my-ref", "X-Empty": "" });
    expect(invalid).toEqual([]);
  });

  it("名称不合法或没有分隔符的行进入 invalid，不静默丢弃", () => {
    const { headers, invalid } = parseHeaderLines("no separator\nBad Name: x\n: novalue\nOk: 1");
    expect(headers).toEqual({ Ok: "1" });
    expect(invalid).toEqual(["no separator", "Bad Name: x", ": novalue"]);
  });
  it("合法的特殊头名不会触发对象原型设置器", () => {
    const { headers } = parseHeaderLines("__proto__: value");
    expect(Object.getPrototypeOf(headers)).toBeNull();
    expect(JSON.stringify(headers)).toBe('{"__proto__":"value"}');
  });
});

describe("MCP 注册：请求状态与失败反馈", () => {
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
    client.setQueryData(queryKeys.extensions(), []);
    mocks.api.extensions.list.mockResolvedValue([]);
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    client.clear();
    container.remove();
  });

  it("注册待处理时阻止重复提交，失败原因留在表单内并保留配置", async () => {
    let rejectInstall: (error: Error) => void = () => undefined;
    mocks.api.extensions.install.mockImplementation(() => new Promise((_, reject) => { rejectInstall = reject; }));
    await act(async () => root.render(createElement(QueryClientProvider, { client }, createElement(ExtensionsPage))));
    const open = [...container.querySelectorAll("button")].find((element) => element.textContent === "extensions.registerMcp")!;
    await act(async () => open.click());
    const inputs = [...container.querySelectorAll<HTMLInputElement>("#mcp-form input")];
    await act(async () => {
      for (const [input, value] of [[inputs[0]!, "LocalMcp"], [inputs[1]!, "npx"]] as const) {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      }
    });
    const form = container.querySelector("#mcp-form")!;
    await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(mocks.api.extensions.install).toHaveBeenCalledTimes(1);
    expect(mocks.api.extensions.install).toHaveBeenCalledWith({ name: "LocalMcp", mcp: { transport: "stdio", command: "npx" } });
    await act(async () => rejectInstall(new Error("MCP launch failed")));
    await vi.waitFor(() => expect(container.querySelector("dialog [role=alert]")!.textContent).toBe("MCP launch failed"));
    expect(inputs[0]!.value).toBe("LocalMcp");
  });
});

describe("extensionsApi：运行时状态与确认工具变化", () => {
  it("status → GET /extensions/{id}/status；acceptTools → POST /extensions/{id}/accept-tools", async () => {
    const fetchImpl = vi.fn(async () => new Response('{"id":"e/1","connected":true,"tool_count":3,"tools_changed":false}', { status: 200 }));
    const api = extensionsApi(new ApiClient({ baseUrl: "app://gogosling", token: "", fetchImpl }));

    const st = await api.status("e/1");
    expect(st).toMatchObject({ connected: true, tool_count: 3, tools_changed: false });
    await api.acceptTools("e/1");

    const calls = fetchImpl.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(calls[0]![0]).toBe("app://gogosling/api/v1/extensions/e%2F1/status");
    expect(calls[0]![1].method).toBe("GET");
    expect(calls[1]![0]).toBe("app://gogosling/api/v1/extensions/e%2F1/accept-tools");
    expect(calls[1]![1].method).toBe("POST");
  });
});
