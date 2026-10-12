import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, AuditEntry } from "../api/types";
import type { AuditQuery } from "../api/modules/audit";
import { AUDIT_PAGE_SIZE, AuditPage, buildAuditQuery, type AuditFilters } from "./AuditPage";

const mocks = vi.hoisted(() => ({
  agents: vi.fn<() => Promise<Agent[]>>(),
  list: vi.fn<(query?: AuditQuery, signal?: AbortSignal) => Promise<AuditEntry[]>>(),
}));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ agents: { list: mocks.agents }, audit: { list: mocks.list } }) }));
vi.mock("../state/useSettings", () => ({ useTimeZone: () => "Asia/Shanghai" }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: "en" } }) }));

const blank: AuditFilters = { agent_id: "", session_id: "", run_id: "", actor: "", action: "", since: "" };
const entry: AuditEntry = { id: "audit-1", at: "2026-10-12T01:00:00Z", actor: "local", action: "tool.call", resource: "report.txt", outcome: "allowed", details: { value: '<img src="x" onerror="alert(1)">' } };

describe("审计筛选", () => {
  it("空过滤项不发送，全部过滤字段裁剪后提交", () => {
    expect(buildAuditQuery(blank)).toEqual({ query: {}, error: null });
    expect(buildAuditQuery({ agent_id: " a ", session_id: " s ", run_id: " r ", actor: " local ", action: " tool.call ", since: " 2026-10-12T09:00:00+08:00 " })).toEqual({ query: { agent_id: "a", session_id: "s", run_id: "r", actor: "local", action: "tool.call", since: "2026-10-12T01:00:00.000Z" }, error: null });
  });
  it("接受有时区的闰日和毫秒时间", () => {
    expect(buildAuditQuery({ ...blank, since: "2024-02-29T23:59:59.123Z" })).toEqual({ query: { since: "2024-02-29T23:59:59.123Z" }, error: null });
  });
  it.each(["yesterday", "2026-10-12", "2026-10-12T09:00:00", "2026-02-30T00:00:00Z", "2026-02-29T00:00:00Z", "2026-10-12T24:00:00Z", "2026-10-12T09:00:00+24:00"])("拒绝无时区或无效日期 %s", (since) => {
    expect(buildAuditQuery({ ...blank, since })).toEqual({ query: null, error: "invalid_since" });
  });
});

let host: HTMLDivElement;
let root: Root;
let client: QueryClient;
beforeEach(() => {
  vi.clearAllMocks();
  mocks.agents.mockResolvedValue([]);
  mocks.list.mockResolvedValue([entry]);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
afterEach(async () => {
  await act(async () => root.unmount());
  client.clear();
  host.remove();
});
async function settle(assertion: () => void) {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    assertion();
  });
}
async function renderPage() {
  await act(async () => root.render(<QueryClientProvider client={client}><AuditPage /></QueryClientProvider>));
  await settle(() => expect(host.textContent).toContain(entry.action));
}
function button(label: string): HTMLButtonElement {
  const found = Array.from(host.querySelectorAll("button")).find((node) => node.textContent === label);
  if (!found) throw new Error(`button missing: ${label}`);
  return found;
}
async function click(label: string) { await act(async () => button(label).click()); }
async function fill(label: string, value: string) {
  const input = host.querySelector<HTMLInputElement>(`[aria-label="${label}"]`);
  if (!input) throw new Error(`input missing: ${label}`);
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function submit() { await act(async () => host.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }))); }

describe("审计页面行为", () => {
  it("按服务端分页，末页禁用下一页；应用筛选回到第一页并可刷新", async () => {
    const page = Array.from({ length: AUDIT_PAGE_SIZE }, (_, index) => ({ ...entry, id: `audit-${index}` }));
    mocks.list.mockImplementation(async (query) => query?.offset === AUDIT_PAGE_SIZE ? [] : page);
    await renderPage();
    expect(mocks.list).toHaveBeenLastCalledWith({ limit: AUDIT_PAGE_SIZE, offset: 0 }, expect.any(AbortSignal));
    expect(button("audit.previous").disabled).toBe(true);
    expect(button("audit.next").disabled).toBe(false);
    await click("audit.next");
    await settle(() => expect(host.textContent).toContain("audit.empty"));
    expect(mocks.list).toHaveBeenLastCalledWith({ limit: AUDIT_PAGE_SIZE, offset: AUDIT_PAGE_SIZE }, expect.any(AbortSignal));
    expect(button("audit.next").disabled).toBe(true);
    await fill("audit.actor", " local ");
    await submit();
    await settle(() => expect(mocks.list).toHaveBeenLastCalledWith({ actor: "local", limit: AUDIT_PAGE_SIZE, offset: 0 }, expect.any(AbortSignal)));
    await settle(() => expect(button("audit.refresh").disabled).toBe(false));
    const calls = mocks.list.mock.calls.length;
    await click("audit.refresh");
    await settle(() => expect(mocks.list.mock.calls.length).toBe(calls + 1));
    expect(mocks.list).toHaveBeenLastCalledWith({ actor: "local", limit: AUDIT_PAGE_SIZE, offset: 0 }, expect.any(AbortSignal));
    await click("audit.clear");
    expect(host.querySelector<HTMLInputElement>('[aria-label="audit.actor"]')?.value).toBe("");
    expect(button("audit.previous").disabled).toBe(true);
  });

  it("无效开始时间不更改当前查询；详情以文本显示而不执行 HTML", async () => {
    await renderPage();
    expect(JSON.parse(host.querySelector("pre")?.textContent ?? "{}")).toEqual(entry.details);
    expect(host.querySelector("img")).toBeNull();
    expect(host.querySelector("time")?.dateTime).toBe(entry.at);
    expect(host.querySelector("time")?.textContent).toContain("09:00:00");
    const calls = mocks.list.mock.calls.length;
    await fill("audit.since", "2026-10-12T09:00:00");
    await submit();
    expect(host.querySelector('[role="alert"]')?.textContent).toBe("audit.sinceInvalid");
    expect(mocks.list.mock.calls.length).toBe(calls);
    expect(host.textContent).toContain(entry.action);
    await fill("audit.since", "2026-10-12T09:00:00+08:00");
    await submit();
    await settle(() => expect(mocks.list).toHaveBeenLastCalledWith({ since: "2026-10-12T01:00:00.000Z", limit: AUDIT_PAGE_SIZE, offset: 0 }, expect.any(AbortSignal)));
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });
});
