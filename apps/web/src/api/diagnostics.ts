// 桌面诊断只经过本地 IPC；接口镜像来源：apps/desktop/src/shared/diagnostics.ts。
// 此模块不读取错误正文、文件或会话，也不持有上传凭据。
export type DiagnosticsCategory = "connection" | "task" | "preview" | "application";
export type DiagnosticsCode = "disabled" | "invalid_request" | "stale_preview" | "busy" | "cancelled" | "timeout" | "unavailable" | "rate_limited" | "unauthorized" | "invalid_response";
export type DiagnosticsResult<T> = { ok: true; value: T } | { ok: false; code: DiagnosticsCode; retryAfterSeconds?: number };
export interface DiagnosticsPreview {
  id: string;
  expiresAt: string;
  body: string;
  byteLength: number;
  sha256: string;
  destination: string;
  retentionDays: 90;
}
export interface DiagnosticsReceipt { eventId: string; duplicate: boolean }

const codes: readonly string[] = ["disabled", "invalid_request", "stale_preview", "busy", "cancelled", "timeout", "unavailable", "rate_limited", "unauthorized", "invalid_response"];
const unavailable = { ok: false, code: "unavailable" } as const;
const disabled = { ok: false, code: "disabled" } as const;
const invalid = { ok: false, code: "invalid_response" } as const;

// 即使旧壳或异常 IPC 返回未约定的错误，也只保留本地化错误码。
function failure(value: unknown): DiagnosticsResult<never> {
  if (!value || typeof value !== "object" || !("code" in value) || typeof value.code !== "string" || !codes.includes(value.code)) return invalid;
  const retry = "retryAfterSeconds" in value ? value.retryAfterSeconds : undefined;
  return { ok: false, code: value.code as DiagnosticsCode, ...(typeof retry === "number" && Number.isFinite(retry) && retry > 0 ? { retryAfterSeconds: Math.min(Math.ceil(retry), 3600) } : {}) };
}

export async function diagnosticsAvailable(): Promise<boolean> {
  const bridge = window.gogosling;
  if (!bridge?.diagnosticsStatus || !bridge.diagnosticsPreview || !bridge.diagnosticsSend || !bridge.diagnosticsCancel) return false;
  try { return (await bridge.diagnosticsStatus())?.available === true; } catch { return false; }
}

export async function previewDiagnostics(category: DiagnosticsCategory): Promise<DiagnosticsResult<DiagnosticsPreview>> {
  if (!window.gogosling?.diagnosticsPreview) return disabled;
  try {
    const result = await window.gogosling.diagnosticsPreview(category);
    if (!result?.ok) return failure(result);
    const value = result.value;
    if (!value || typeof value.id !== "string" || !/^[0-9a-f-]{36}$/i.test(value.id) || typeof value.body !== "string" || typeof value.expiresAt !== "string" || !Number.isFinite(Date.parse(value.expiresAt)) || value.destination !== "https://api.gogosling.ca/v1/error-logs" || value.retentionDays !== 90 || !/^[0-9a-f]{64}$/.test(value.sha256) || value.byteLength > 16384 || value.byteLength !== new TextEncoder().encode(value.body).length) return invalid;
    return { ok: true, value };
  } catch { return unavailable; }
}

export async function sendDiagnostics(id: string): Promise<DiagnosticsResult<DiagnosticsReceipt>> {
  if (!window.gogosling?.diagnosticsSend) return disabled;
  try {
    const result = await window.gogosling.diagnosticsSend(id);
    if (!result?.ok) return failure(result);
    if (!result.value || typeof result.value.eventId !== "string" || !/^[a-zA-Z0-9_-]{1,128}$/.test(result.value.eventId) || typeof result.value.duplicate !== "boolean") return invalid;
    return { ok: true, value: { eventId: result.value.eventId, duplicate: result.value.duplicate } };
  } catch { return unavailable; }
}

export async function cancelDiagnostics(id: string, discard: boolean): Promise<void> {
  try { await window.gogosling?.diagnosticsCancel?.(id, discard); } catch { /* 清理不显示底层异常正文。 */ }
}
