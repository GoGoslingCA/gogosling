import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ApiEvent } from "../api/types";
import { EventStreamProvider } from "./EventStreamProvider";

const captured = vi.hoisted(() => ({ message: null as null | ((message: ApiEvent) => void) }));
vi.mock("../api/sse", () => ({
  decodeApiEvent: (event: ApiEvent) => event,
  EventStream: class {
    constructor(options: { onMessage: (message: ApiEvent) => void }) { captured.message = options.onMessage; }
    start() {} stop() {}
  },
}));
const api = { client: { url: (path: string) => path, authHeaders: () => ({}) } };
vi.mock("./ConnectionContext", () => ({ useApi: () => api }));
let root: Root; let host: HTMLDivElement; let client: QueryClient;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  client = new QueryClient(); host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); captured.message = null; });

it("运行状态改变刷新资源槽位，文本分片不产生资源请求", async () => {
  const invalidate = vi.spyOn(client, "invalidateQueries");
  await act(async () => root.render(<QueryClientProvider client={client}><EventStreamProvider><p>task</p></EventStreamProvider></QueryClientProvider>));
  const resourceCalls = () => invalidate.mock.calls.filter(([filter]) => filter?.queryKey?.[0] === "resources").length;
  const event = (type: string, seq: number): ApiEvent => ({ type, seq, at: "2026-10-13T00:00:00Z", payload: { type, run_id: "run-test", session_id: "session-test", agent_id: "agent-test", at: "2026-10-13T00:00:00Z", delta: "text" } });
  await act(async () => { for (let seq = 1; seq <= 50; seq++) captured.message!(event("text.delta", seq)); });
  expect(resourceCalls()).toBe(0);
  await act(async () => captured.message!(event("run.started", 51))); expect(resourceCalls()).toBe(1);
  await act(async () => captured.message!(event("run.failed", 52))); expect(resourceCalls()).toBe(2);
});
