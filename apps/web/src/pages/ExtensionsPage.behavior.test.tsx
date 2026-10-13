import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ExtensionsPage } from "./ExtensionsPage";

const { list, status } = vi.hoisted(() => ({ list: vi.fn(), status: vi.fn() }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ extensions: { list, status } }) }));
vi.mock("../components/Toast", () => ({ useToast: () => ({ show: vi.fn() }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
let root: Root; let host: HTMLDivElement; let client: QueryClient;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  list.mockReset().mockResolvedValue([{ id: "local-server", name: "Document tools", kind: "mcp_server", source: "local", enabled: true }]);
  status.mockReset().mockResolvedValue({ id: "local-server", connected: false, tool_count: 0, tools_changed: false });
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); vi.useRealTimers(); });
async function settle() { for (let i = 0; i < 3; i++) await act(async () => { await vi.advanceTimersByTimeAsync(1); }); }
async function mount() { await act(async () => root.render(<QueryClientProvider client={client}><ExtensionsPage /></QueryClientProvider>)); await settle(); }

it("未使用的连接不循环轮询，手动刷新读取真实状态", async () => {
  await mount(); expect(status).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain("extensionControls.onDemand");
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(status).toHaveBeenCalledTimes(1);
  status.mockResolvedValue({ id: "local-server", connected: true, tool_count: 2, tools_changed: false });
  await act(async () => [...host.querySelectorAll("button")].find(button => button.textContent === "extensionControls.refresh")!.click()); await settle();
  expect(status).toHaveBeenCalledTimes(2); expect(host.textContent).toContain("extensions.connected");
});

it("名称筛选只使用已载入列表，隐藏连接行取消未完成读取", async () => {
  let signal: AbortSignal | undefined;
  status.mockImplementation((_id: string, value: AbortSignal) => { signal = value; return new Promise(() => undefined); });
  await mount(); expect(signal?.aborted).toBe(false);
  const input = host.querySelector<HTMLInputElement>('input[type="search"]')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "nothing-matches");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(signal?.aborted).toBe(true); expect(list).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain("extensionControls.noMatches");
});

it("状态读取失败保留手动重试，不进行后台重试", async () => {
  status.mockRejectedValue(new Error("offline")); await mount();
  expect(host.querySelector('[role="alert"]')).not.toBeNull();
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(status).toHaveBeenCalledTimes(1);
  expect([...host.querySelectorAll("button")].some(button => button.textContent === "extensionControls.refresh")).toBe(true);
});
