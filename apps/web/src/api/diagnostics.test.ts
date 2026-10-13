import { afterEach, describe, expect, it, vi } from "vitest";
import { diagnosticsAvailable, previewDiagnostics, sendDiagnostics, type DiagnosticsPreview } from "./diagnostics";

afterEach(() => { delete window.gogosling; });
describe("诊断 IPC 的安全失败", () => {
  it("部分接口缺失时隐藏入口，不因旧壳异常而破坏设置", async () => {
    const status = vi.fn(async () => ({ available: true }));
    window.gogosling = { diagnosticsStatus: status };
    expect(await diagnosticsAvailable()).toBe(false); expect(status).not.toHaveBeenCalled();
    expect(await previewDiagnostics("application")).toEqual({ ok: false, code: "disabled" });
  });
  it.each(["destination", "body", "sha256", "byteLength"])("异常 %s 字段不展示为有效预览", async field => {
    const value: DiagnosticsPreview = { id: "923f2e8f-c452-48b9-9e2d-17ed9f49a2ea", body: "{}", expiresAt: "2026-10-13T12:00:00Z", byteLength: 2, destination: "https://api.gogosling.ca/v1/error-logs", retentionDays: 90, sha256: "f".repeat(64) };
    const broken = { ...value, [field]: field === "byteLength" ? 20000 : "unexpected" };
    window.gogosling = { diagnosticsPreview: vi.fn(async () => ({ ok: true as const, value: broken })) };
    expect(await previewDiagnostics("task")).toEqual({ ok: false, code: "invalid_response" });
  });
  it("只返回白名单错误码及有界等待时间，不返回远端错误正文", async () => {
    window.gogosling = { diagnosticsSend: vi.fn(async () => ({ ok: false as const, code: "rate_limited" as const, retryAfterSeconds: 999999, message: "private response" })) };
    expect(await sendDiagnostics("id")).toEqual({ ok: false, code: "rate_limited", retryAfterSeconds: 3600 });
    window.gogosling!.diagnosticsSend = vi.fn(async () => ({ ok: false as const, code: "private error" as "unavailable" }));
    expect(await sendDiagnostics("id")).toEqual({ ok: false, code: "invalid_response" });
  });
});
