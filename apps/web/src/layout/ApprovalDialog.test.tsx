import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ApprovalRequest, Session } from "../api/types";
import { ApprovalDialog } from "./ApprovalDialog";

const mocks = vi.hoisted(() => ({
  list: vi.fn<() => Promise<ApprovalRequest[]>>(),
  get: vi.fn<(id: string) => Promise<Session>>(),
  decide: vi.fn(),
}));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ approvals: { list: mocks.list, decide: mocks.decide }, sessions: { get: mocks.get } }) }));
vi.mock("../state/useSettings", () => ({ useTimeZone: () => "Asia/Shanghai" }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: "zh" } }) }));

const approval: ApprovalRequest = {
  id: "approval-1", session_id: "session-1", agent_id: "agent-1",
  tool_call: { id: "call-1", name: "read_file", arguments: { path: "notes/说明.txt" } },
  risk: "read", summary: "读取说明", status: "pending",
  created_at: "2026-10-12T01:00:00Z", expires_at: "2026-10-12T02:00:00Z",
};
const session: Session = { id: "session-1", agent_id: "agent-1", title: "对内说明", origin: "desktop", key: "desktop", status: "active", created_at: approval.created_at, updated_at: approval.created_at };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

let host: HTMLDivElement;
let root: Root;
let client: QueryClient;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (!HTMLDialogElement.prototype.showModal) {
    HTMLDialogElement.prototype.showModal = function showModal() { this.open = true; };
    HTMLDialogElement.prototype.close = function close() { this.open = false; };
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  client.clear();
});
function approveOnce() { return Array.from(document.body.querySelectorAll("button")).find((button) => button.textContent === "approval.approveOnce")!; }
async function settle(assertion: () => void) {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    assertion();
  });
}

describe("确认框在任务名称明确前不能批准", () => {
  it("任务还在读取时不把标题显示成未命名，批准保持不可用", async () => {
    const pending = deferred<Session>();
    mocks.list.mockResolvedValue([approval]);
    mocks.get.mockReturnValue(pending.promise);
    await act(async () => root.render(<QueryClientProvider client={client}><ApprovalDialog /></QueryClientProvider>));
    await settle(() => expect(document.body.textContent).toContain("approval.taskLoading"));
    expect(document.body.textContent).not.toContain("chat.untitled");
    expect(approveOnce().disabled).toBe(true);
    await act(async () => { pending.resolve(session); });
    await settle(() => expect(document.body.textContent).toContain("对内说明"));
    expect(approveOnce().disabled).toBe(false);
  });

  it("任务名称读取失败时不能批准", async () => {
    mocks.list.mockResolvedValue([approval]);
    mocks.get.mockRejectedValue(new Error("offline"));
    await act(async () => root.render(<QueryClientProvider client={client}><ApprovalDialog /></QueryClientProvider>));
    await settle(() => expect(document.body.textContent).toContain("approval.taskUnknown"));
    expect(approveOnce().disabled).toBe(true);
    expect(Array.from(document.body.querySelectorAll("button")).find((button) => button.textContent === "approval.deny")!.disabled).toBe(false);
  });
});
