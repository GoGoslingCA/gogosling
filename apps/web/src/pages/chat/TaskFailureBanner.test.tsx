import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TaskFailureBanner } from "./TaskFailureBanner";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: "en" } }) }));
vi.mock("../DiagnosticsDialog", () => ({ DiagnosticsDialog: ({ sessionId, runId }: { sessionId: string; runId: string }) => <output>{`${sessionId}/${runId}`}</output> }));
let host: HTMLDivElement;
let root: Root;
const preview = vi.fn(); const send = vi.fn();
const failure = { sessionId: "task-a", runId: "run-a", occurredAt: "2026-10-13T00:00:00Z", detail: "Synthetic local failure" };
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  preview.mockReset(); send.mockReset();
  window.gogosling = { diagnosticsStatus: async () => ({ available: true, taskReports: true }), diagnosticsTaskPreview: preview, diagnosticsCancelPreparation: vi.fn(), diagnosticsSend: send, diagnosticsCancel: vi.fn() };
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); delete window.gogosling; });
async function mount(runId = "run-a") { await act(async () => root.render(<TaskFailureBanner key={runId} failure={{ ...failure, runId }} timeZone="UTC" />)); }
function button(label: string) { return [...host.querySelectorAll<HTMLButtonElement>("button")].find(node => node.textContent === label || node.getAttribute("aria-label") === label); }
describe("任务错误提示", () => {
  it.each(["browser", "legacy", "source", "generic-only", "status-error"])("%s 只展示必要错误，不显示上传入口", async mode => {
    if (mode === "browser") delete window.gogosling;
    if (mode === "legacy") window.gogosling = {};
    if (mode === "source") window.gogosling!.diagnosticsStatus = async () => ({ available: false, taskReports: false });
    if (mode === "generic-only") window.gogosling!.diagnosticsStatus = async () => ({ available: true });
    if (mode === "status-error") window.gogosling!.diagnosticsStatus = async () => { throw new Error("private status failure"); };
    await mount(); expect(host.textContent).toContain("taskFailure.message");
    expect(button("taskFailure.report")).toBeUndefined(); expect(host.querySelector("output")).toBeNull();
    expect(preview).not.toHaveBeenCalled(); expect(send).not.toHaveBeenCalled();
  });
  it("默认折叠本机详情，不自动打开弹窗或上传，明确点击才打开对应任务", async () => {
    await mount(); expect(host.querySelector<HTMLDetailsElement>("details")?.open).toBe(false);
    expect(host.textContent).toContain("Synthetic local failure"); expect(host.querySelector("time")?.dateTime).toBe(failure.occurredAt);
    expect(host.textContent).not.toContain("Request ID"); expect(host.textContent).not.toContain("Trace ID");
    expect(host.querySelector("output")).toBeNull(); expect(preview).not.toHaveBeenCalled(); expect(send).not.toHaveBeenCalled();
    await act(async () => button("taskFailure.report")!.click());
    await vi.waitFor(async () => { await act(async () => new Promise(resolve => setTimeout(resolve, 0))); expect(host.querySelector("output")?.textContent).toBe("task-a/run-a"); });
  });
  it("关闭提示只隐藏当前失败，下一轮失败重新出现", async () => {
    await mount(); await act(async () => button("taskFailure.dismiss")!.click()); expect(host.textContent).toBe("");
    await mount("run-b"); expect(host.textContent).toContain("taskFailure.message");
    expect(preview).not.toHaveBeenCalled(); expect(send).not.toHaveBeenCalled();
  });
  it("旧设置中的非法时区回退到本机时区，错误提示仍能打开", async () => {
    await act(async () => root.render(<TaskFailureBanner failure={failure} timeZone="invalid/zone" />));
    expect(host.textContent).toContain("taskFailure.message"); expect(host.querySelector("time")?.dateTime).toBe(failure.occurredAt);
  });
});
