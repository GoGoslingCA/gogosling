/** 编译发行版的手动诊断协议；令牌和原始错误不跨越 IPC。 */
export type DiagnosticCategory = "connection" | "task" | "preview" | "application";

export type DiagnosticErrorCode =
  | "disabled" | "invalid_request" | "stale_preview" | "busy" | "cancelled"
  | "timeout" | "unavailable" | "rate_limited" | "unauthorized" | "invalid_response"
  | "no_error" | "unsupported_report";

export type DiagnosticResult<T> =
  | { ok: true; value: T }
  | { ok: false; code: DiagnosticErrorCode; retryAfterSeconds?: number };

export interface DiagnosticStatus { available: boolean; taskReports?: boolean }

/** 仅指定本地任务；错误正文与发生时间必须由主进程向守护进程核实。 */
export interface DiagnosticTaskRequest {
  kind: "task";
  requestId: string;
  sessionId: string;
  runId: string;
}

/** body 是用户确认的完整上传内容，确认后不再重新采集状态。 */
export interface DiagnosticPreview {
  readonly id: string;
  readonly expiresAt: string;
  readonly body: string;
  readonly byteLength: number;
  readonly sha256: string;
  readonly destination: string;
  readonly retentionDays: 90;
}

export interface DiagnosticReceipt { eventId: string; duplicate: boolean }
