import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ResourceSettings } from "./ResourceSettings";
import { queryKeys } from "../state/queryKeys";

const { resources } = vi.hoisted(() => ({ resources: vi.fn() }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ system: { resources } }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
let root: Root; let host: HTMLDivElement; let client: QueryClient;
const idle = { max_parallel: 2, max_queued: 32, running: 0, queued: 0, awaiting_approval: 0 };
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers(); resources.mockReset().mockResolvedValue(idle);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); vi.useRealTimers(); });
async function mount() { await act(async () => root.render(<QueryClientProvider client={client}><ResourceSettings /></QueryClientProvider>)); await act(async () => { await vi.advanceTimersByTimeAsync(2); }); }

it("空闲资源页降低读取频率，事件失效可立即恢复活动刷新", async () => {
  await mount(); expect(resources).toHaveBeenCalledTimes(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(25_000); }); expect(resources).toHaveBeenCalledTimes(1);
  resources.mockResolvedValue({ ...idle, running: 1 });
  await act(async () => { await client.invalidateQueries({ queryKey: queryKeys.resources }); await vi.advanceTimersByTimeAsync(2); });
  expect(resources).toHaveBeenCalledTimes(2);
  await act(async () => { await vi.advanceTimersByTimeAsync(5_001); }); expect(resources).toHaveBeenCalledTimes(3);
});

it("离开资源页会取消读取且不保留轮询", async () => {
  let signal: AbortSignal | undefined;
  resources.mockImplementation((value: AbortSignal) => { signal = value; return new Promise(() => undefined); });
  await mount(); await act(async () => root.render(null));
  expect(signal?.aborted).toBe(true);
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); }); expect(resources).toHaveBeenCalledTimes(1);
});
