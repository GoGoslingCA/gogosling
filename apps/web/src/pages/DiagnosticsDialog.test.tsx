import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TaskDiagnosticsRequest, DiagnosticsPreview, DiagnosticsReceipt, DiagnosticsResult } from "../api/diagnostics";
import { DiagnosticsDialog } from "./DiagnosticsDialog";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, values?: Record<string, unknown>) => values ? `${key} ${JSON.stringify(values)}` : key }) }));
let root: Root;
let host: HTMLDivElement;
const cancelPreparation = vi.fn<(requestId: string) => Promise<void>>();
const preview = vi.fn<(request: TaskDiagnosticsRequest) => Promise<DiagnosticsResult<DiagnosticsPreview>>>();
const send = vi.fn<(id: string) => Promise<DiagnosticsResult<DiagnosticsReceipt>>>();
const cancel = vi.fn<(id: string, discard: boolean) => Promise<void>>();
const originalShow = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal");
const idA = "8b4b2efa-a420-4353-8c28-7c7b61a48b32";
const idB = "5ed10725-4446-4166-95bf-80c48c8a6638";
function report(id = idA, runId = "01J00000000000000000000001"): DiagnosticsPreview {
  const body = JSON.stringify({ product: "gogosling-desktop", events: [{ timestamp: "2026-10-13T00:00:00Z", context: { category: "task", task_failure: { session_id: "01J00000000000000000000000", run_id: runId, occurred_at: "2026-10-13T00:00:00Z", status: "failed", error_code: "run_failed" } } }] });
  return { id, body, expiresAt: "2026-10-13T12:00:00Z", byteLength: new TextEncoder().encode(body).length, sha256: "a".repeat(64), destination: "https://api.gogosling.ca/v1/error-logs", retentionDays: 90 };
}
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  cancelPreparation.mockReset().mockResolvedValue(undefined);
  preview.mockReset().mockResolvedValue({ ok: true, value: report() });
  send.mockReset().mockResolvedValue({ ok: true, value: { eventId: idA, duplicate: false } });
  cancel.mockReset().mockResolvedValue(undefined);
  window.gogosling = { diagnosticsTaskPreview: preview, diagnosticsCancelPreparation: cancelPreparation, diagnosticsSend: send, diagnosticsCancel: cancel };
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value(this: HTMLDialogElement) { this.open = true; } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount()); host.remove(); delete window.gogosling;
  if (originalShow) Object.defineProperty(HTMLDialogElement.prototype, "showModal", originalShow);
  else Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
  vi.restoreAllMocks();
});
async function ready(assert: () => void) {
  await vi.waitFor(async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); }); assert(); }, { timeout: 3000, interval: 10 });
}
function Harness({ runId = "01J00000000000000000000001" }: { runId?: string }) {
  const [open, setOpen] = useState(false);
  return <><button onClick={() => setOpen(true)}>open</button>{open ? <DiagnosticsDialog sessionId="01J00000000000000000000000" runId={runId} onClose={() => setOpen(false)} /> : null}</>;
}
async function mount(runId?: string) { await act(async () => root.render(<Harness runId={runId} />)); }
function button(text: string): HTMLButtonElement {
  const found = [...host.querySelectorAll<HTMLButtonElement>("button")].find(node => node.textContent === text);
  if (!found) throw new Error(`未找到按钮 ${text}`);
  return found;
}
async function click(text: string) { await act(async () => button(text).click()); }
async function open() { await mount(); await ready(() => expect(host.textContent).toContain("open")); await click("open"); }
async function loaded() { await open(); await ready(() => expect(host.querySelector(".diagnostics-preview")).not.toBeNull()); }
async function consent() { await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click()); }

describe("自愿诊断报告", () => {
  it.each(["no_error", "unsupported_report"] as const)("%s 停止准备且不提供重复发送或刷新入口", async code => {
    preview.mockResolvedValueOnce({ ok: false, code });
    await open(); await ready(() => expect(host.textContent).toContain(`diagnostics.errors.${code}`));
    expect(host.querySelector("pre")).toBeNull();
    expect(host.textContent).not.toContain("diagnostics.retry");
    expect(host.textContent).not.toContain("diagnostics.refresh");
    expect(send).not.toHaveBeenCalled();
  });

  it("服务拒绝任务格式时保留可查看的正文，但不允许重试降级上传", async () => {
    send.mockResolvedValueOnce({ ok: false, code: "unsupported_report" });
    await loaded(); await consent(); await click("diagnostics.send");
    expect(host.textContent).toContain("diagnostics.errors.unsupported_report");
    expect(host.querySelector("pre")?.textContent).toBe(report().body);
    expect(host.textContent).not.toContain("diagnostics.retry");
    expect(preview).toHaveBeenCalledOnce();
    expect(send).toHaveBeenCalledOnce();
  });
  it("只有点击入口才生成本地预览，默认不勾选同意，不发送网络请求", async () => {
    await mount(); await ready(() => expect(host.textContent).toContain("open"));
    expect(preview).not.toHaveBeenCalled(); expect(send).not.toHaveBeenCalled();
    await click("open"); await ready(() => expect(host.querySelector("pre")?.textContent).toBe(report().body));
    expect(preview).toHaveBeenCalledExactlyOnceWith({ kind: "task", requestId: expect.any(String), sessionId: "01J00000000000000000000000", runId: "01J00000000000000000000001" });
    expect(host.querySelector("select")).toBeNull();
    expect(host.querySelector("dialog")?.getAttribute("aria-labelledby")).toBe("diagnostics-title");
    expect(host.querySelector<HTMLDetailsElement>("details")?.open).toBe(false);
    expect(host.textContent).toContain("https://api.gogosling.ca/v1/error-logs");
    expect(host.textContent).toContain('"days":90');
    expect(host.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);
    expect(button("diagnostics.send").disabled).toBe(true);
    await click("diagnostics.send"); expect(send).not.toHaveBeenCalled();
  });

  it("同意后只发送被查看的 ID，同轮双击只发一次，成功清除预览", async () => {
    let finish!: (value: DiagnosticsResult<DiagnosticsReceipt>) => void;
    send.mockImplementation(() => new Promise(resolve => { finish = resolve; }));
    await loaded(); await consent();
    await act(async () => { button("diagnostics.send").click(); button("diagnostics.send").click(); });
    expect(send).toHaveBeenCalledExactlyOnceWith(idA);
    await act(async () => finish({ ok: true, value: { eventId: idA, duplicate: false } }));
    expect(host.textContent).toContain("diagnostics.sent");
    expect(host.querySelector("pre")).toBeNull(); expect(host.querySelector('input[type="checkbox"]')).toBeNull();
    await click("diagnostics.done"); expect(host.querySelector("dialog")).toBeNull();
  });

  it("失败保留相同正文和 ID，重试不生成新报告；异常正文不展示", async () => {
    send.mockRejectedValueOnce(new Error("sensitive remote response"));
    await loaded(); await consent(); await click("diagnostics.send");
    expect(host.textContent).toContain("diagnostics.errors.unavailable");
    expect(host.textContent).not.toContain("sensitive remote response");
    expect(host.querySelector("pre")?.textContent).toBe(report().body);
    send.mockResolvedValueOnce({ ok: true, value: { eventId: idA, duplicate: true } });
    await click("diagnostics.retry");
    expect(send.mock.calls).toEqual([[idA], [idA]]); expect(preview).toHaveBeenCalledOnce();
    expect(host.textContent).toContain("diagnostics.alreadySent");
  });

  it("停止发送释放请求但保留预览，旧发送结果不能覆盖重试状态", async () => {
    let finish!: (value: DiagnosticsResult<DiagnosticsReceipt>) => void;
    send.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await loaded(); await consent(); await click("diagnostics.send"); await click("diagnostics.cancelSend");
    expect(cancel).toHaveBeenCalledWith(idA, false);
    expect(host.textContent).toContain("diagnostics.errors.cancelled");
    expect(host.querySelector("pre")?.textContent).toBe(report().body);
    await act(async () => finish({ ok: true, value: { eventId: "late-response", duplicate: false } }));
    expect(host.textContent).not.toContain("late-response"); expect(host.textContent).toContain("diagnostics.errors.cancelled");
    await click("diagnostics.retry"); expect(send.mock.calls).toEqual([[idA], [idA]]);
  });

  it("切换任务清除同意并取消准备，迟到的预览也被清理", async () => {
    let finish!: (value: DiagnosticsResult<DiagnosticsPreview>) => void;
    await loaded(); await consent();
    preview.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await mount("01J00000000000000000000002"); expect(cancel).toHaveBeenCalledWith(idA, true);
    preview.mockResolvedValueOnce({ ok: true, value: report(idB, "01J00000000000000000000003") });
    await mount("01J00000000000000000000003"); await ready(() => expect(host.querySelector("pre")).not.toBeNull());
    expect(host.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);
    expect(cancelPreparation).toHaveBeenCalledWith(preview.mock.calls[1]![0].requestId);
    await act(async () => finish({ ok: true, value: report(idA, "01J00000000000000000000002") }));
    expect(cancel.mock.calls.filter(call => call[0] === idA && call[1])).toHaveLength(2);
    await consent(); await click("diagnostics.send"); expect(send).toHaveBeenCalledExactlyOnceWith(idB);
  });

  it("关闭发送中的弹窗会取消并销毁，旧回执不污染重新打开的弹窗", async () => {
    let finish!: (value: DiagnosticsResult<DiagnosticsReceipt>) => void;
    send.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await loaded(); await consent(); await click("diagnostics.send");
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="common.close"]')!.click());
    expect(cancel).toHaveBeenCalledWith(idA, true); expect(host.querySelector("dialog")).toBeNull();
    preview.mockResolvedValueOnce({ ok: true, value: report(idB) });
    await click("open"); await ready(() => expect(host.querySelector("pre")).not.toBeNull());
    await act(async () => finish({ ok: true, value: { eventId: "old-dialog-result", duplicate: false } }));
    expect(host.textContent).not.toContain("old-dialog-result"); expect(host.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);
  });

  it("导航卸载取消发送；预览尚未返回时卸载也会清理迟到预览", async () => {
    send.mockImplementationOnce(() => new Promise(() => {}));
    await loaded(); await consent(); await click("diagnostics.send");
    await act(async () => root.render(<div />)); expect(cancel).toHaveBeenCalledWith(idA, true);
    let finish!: (value: DiagnosticsResult<DiagnosticsPreview>) => void;
    preview.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await open(); await ready(() => expect(preview).toHaveBeenCalledTimes(2));
    await act(async () => root.render(<div />));
    expect(cancelPreparation).toHaveBeenCalledWith(preview.mock.calls[1]![0].requestId);
    await act(async () => finish({ ok: true, value: report(idB) }));
    expect(cancel).toHaveBeenCalledWith(idB, true); expect(host.querySelector("dialog")).toBeNull();
  });

  it("过期预览必须重新生成和同意，ESC 关闭并销毁新预览", async () => {
    send.mockResolvedValueOnce({ ok: false, code: "stale_preview" });
    await loaded(); await consent(); await click("diagnostics.send");
    expect(host.textContent).toContain("diagnostics.errors.stale_preview");
    preview.mockResolvedValueOnce({ ok: true, value: report(idB) }); await click("diagnostics.refresh");
    await ready(() => expect(preview).toHaveBeenCalledTimes(2));
    expect(host.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);
    expect(button("diagnostics.send").disabled).toBe(true);
    await act(async () => host.querySelector("dialog")!.dispatchEvent(new Event("cancel", { bubbles: false, cancelable: true })));
    expect(host.querySelector("dialog")).toBeNull(); expect(cancel).toHaveBeenCalledWith(idB, true);
  });
});
