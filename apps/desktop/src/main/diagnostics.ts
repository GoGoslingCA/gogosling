/** 手动诊断：仅内存中的白名单状态，经用户确认后使用短效票据上传。 */
import { createHash, randomUUID } from "node:crypto";
import type {
  DiagnosticCategory, DiagnosticErrorCode, DiagnosticPreview, DiagnosticReceipt,
  DiagnosticResult, DiagnosticStatus,
} from "../shared/diagnostics.js";

const DESTINATION = "https://api.gogosling.ca/v1/error-logs";
const TICKET_ENDPOINT = "https://api.gogosling.ca/v1/desktop-diagnostics/tickets";
const PREVIEW_TTL = 10 * 60_000;
const REQUEST_TIMEOUT = 15_000;
const RESPONSE_LIMIT = 8 * 1024;
const categories = new Set(["connection", "task", "preview", "application"]);
const states = new Set(["idle", "attaching", "locating", "starting", "ready", "restarting", "stopping", "stopped", "failed"]);
const daemonCodes = new Set(["executable_not_found", "spawn_failed", "ready_timeout", "exited", "stale_handle", "restart_limit"]);
const recordCodes = new Set([
  ...daemonCodes, "daemon_start_failed", "daemon_unavailable", "daemon_exited",
  "renderer_unresponsive", "renderer_gone", "load_failed", "api_proxy_failed", "static_resource_failed",
  "http_400", "http_401", "http_403", "http_404", "http_409", "http_413", "http_422",
  "http_429", "http_500", "http_502", "http_503", "http_504",
]);
const platforms = new Set(["darwin", "linux", "win32"]);
const architectures = new Set(["arm64", "x64", "ia32", "arm", "ppc64", "s390x", "riscv64"]);

export interface DiagnosticDependencies {
  enabled: boolean;
  version: string;
  platform: string;
  arch: string;
  snapshot: () => unknown;
  fetch?: (url: string, init: RequestInit) => Promise<Response>;
  now?: () => number;
}

interface StoredPreview { value: DiagnosticPreview; eventId: string; expires: number }
interface ActiveSend { id: string; controller: AbortController; reason?: "cancelled" | "timeout" }
interface RecentError { code: string; timestamp: string }

class Failure extends Error {
  constructor(readonly code: DiagnosticErrorCode, readonly retryAfterSeconds?: number) { super(code); }
}

function fail<T>(code: DiagnosticErrorCode, retryAfterSeconds?: number): DiagnosticResult<T> {
  return retryAfterSeconds === undefined ? { ok: false, code } : { ok: false, code, retryAfterSeconds };
}

// 只读取自有数据属性；未知字段、访问器和原型字段不会进入诊断。
function field(value: unknown, key: string): unknown {
  if (typeof value !== "object" || value === null) return undefined;
  const descriptor = Object.getOwnPropertyDescriptor(value, key);
  return descriptor && "value" in descriptor ? descriptor.value : undefined;
}

function safeSnapshot(value: unknown): Record<string, string | number | boolean> {
  const result: Record<string, string | number | boolean> = {};
  const state = field(value, "daemonState");
  const owned = field(value, "daemonOwned");
  const attempts = field(value, "restartAttempts");
  const code = field(value, "errorCode");
  const exit = field(value, "exitCode");
  if (typeof state === "string" && states.has(state)) result.daemon_state = state;
  if (typeof owned === "boolean") result.daemon_owned = owned;
  if (typeof attempts === "number" && Number.isInteger(attempts) && attempts >= 0 && attempts <= 5) result.restart_attempts = attempts;
  if (typeof code === "string" && daemonCodes.has(code)) result.error_code = code;
  if (typeof exit === "number" && Number.isInteger(exit) && exit >= -2147483648 && exit <= 4294967295) result.exit_code = exit;
  return result;
}

function abortable<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const aborted = () => reject(new Failure("cancelled"));
    signal.addEventListener("abort", aborted, { once: true });
    // 即便开始等待时已经取消，也接收底层 Promise 的迟到异常。
    promise.then(
      (value) => { signal.removeEventListener("abort", aborted); resolve(value); },
      (error: unknown) => { signal.removeEventListener("abort", aborted); reject(error); },
    );
    if (signal.aborted) { signal.removeEventListener("abort", aborted); aborted(); }
  });
}

function discardBody(response: Response): void {
  if (response.body && !response.body.locked) void response.body.cancel().catch(() => undefined);
}

async function boundedJSON(response: Response, signal: AbortSignal): Promise<unknown> {
  const declared = response.headers.get("content-length");
  if ((declared !== null && (!/^\d+$/.test(declared) || Number(declared) > RESPONSE_LIMIT)) ||
      response.headers.get("content-type")?.split(";", 1)[0]?.trim().toLowerCase() !== "application/json" ||
      !response.body) {
    discardBody(response);
    throw new Failure("invalid_response");
  }
  const reader = response.body.getReader();
  const cancel = () => { void reader.cancel().catch(() => undefined); };
  signal.addEventListener("abort", cancel, { once: true });
  let bytes = 0;
  const chunks: Uint8Array[] = [];
  try {
    for (;;) {
      const chunk = await abortable(reader.read(), signal);
      if (chunk.done) break;
      bytes += chunk.value.byteLength;
      if (bytes > RESPONSE_LIMIT) throw new Failure("invalid_response");
      chunks.push(chunk.value);
    }
    const buffer = Buffer.concat(chunks, bytes);
    try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(buffer)) as unknown; }
    catch { throw new Failure("invalid_response"); }
  } finally {
    signal.removeEventListener("abort", cancel);
    cancel();
    reader.releaseLock();
  }
}

/** 仅供主进程使用；单实例最多一个预览和一个请求，不创建后台上传任务。 */
export class DesktopDiagnostics {
  private readonly now: () => number;
  private readonly fetch: (url: string, init: RequestInit) => Promise<Response>;
  private readonly version: string;
  private readonly platform: string;
  private readonly arch: string;
  private recent: RecentError[] = [];
  private current?: StoredPreview;
  private previewTimer?: ReturnType<typeof setTimeout>;
  private active?: ActiveSend;
  private disposed = false;
  private cooldown?: { until: number; code: "rate_limited" | "unavailable" };

  constructor(private readonly deps: DiagnosticDependencies) {
    this.now = deps.now ?? Date.now;
    this.fetch = deps.fetch ?? ((url, init) => globalThis.fetch(url, init));
    this.version = /^\d{1,4}\.\d{1,4}\.\d{1,4}(?:-(?:alpha|beta|rc)(?:\.\d{1,4})?)?$/.test(deps.version) ? deps.version : "unknown";
    this.platform = platforms.has(deps.platform) ? deps.platform : "unknown";
    this.arch = architectures.has(deps.arch) ? deps.arch : "unknown";
  }

  status(): DiagnosticStatus { return { available: this.deps.enabled && !this.disposed }; }

  /** 未启用时也不收集状态；不接受自由文本错误或任意附加字段。 */
  record(code: unknown): void {
    if (!this.status().available || typeof code !== "string" || !recordCodes.has(code)) return;
    this.recent.push({ code, timestamp: new Date(this.now()).toISOString() });
    if (this.recent.length > 20) this.recent.shift();
  }

  preview(category: unknown): DiagnosticResult<DiagnosticPreview> {
    if (!this.status().available) return fail("disabled");
    if (this.active) return fail("busy");
    if (typeof category !== "string" || !categories.has(category)) return fail("invalid_request");
    try {
      const now = this.now();
      const eventId = randomUUID();
      const body = JSON.stringify({
        product: "gogosling-desktop",
        events: [{
          event_id: eventId, timestamp: new Date(now).toISOString(), environment: "production",
          level: "error", service: "gogosling-desktop", message: "desktop_diagnostic_report",
          release: this.version,
          tags: { platform: this.platform, arch: this.arch, ...safeSnapshot(this.deps.snapshot()) },
          context: { category: category as DiagnosticCategory, recent_errors: this.recent.map((entry) => ({ ...entry })) },
        }],
      });
      const byteLength = Buffer.byteLength(body, "utf8");
      if (byteLength > 16 * 1024) return fail("invalid_request");
      const expires = now + PREVIEW_TTL;
      const value: DiagnosticPreview = Object.freeze({
        id: randomUUID(), expiresAt: new Date(expires).toISOString(), body, byteLength,
        sha256: createHash("sha256").update(body, "utf8").digest("hex"),
        destination: DESTINATION, retentionDays: 90,
      });
      this.clearPreview();
      this.current = { value, eventId, expires };
      this.previewTimer = setTimeout(() => this.clearPreview(), PREVIEW_TTL);
      this.previewTimer.unref?.();
      return { ok: true, value };
    } catch { return fail("unavailable"); }
  }

  async send(id: unknown): Promise<DiagnosticResult<DiagnosticReceipt>> {
    if (!this.status().available) return fail("disabled");
    if (typeof id !== "string") return fail("invalid_request");
    if (this.active) return fail("busy");
    const preview = this.current;
    if (!preview || preview.value.id !== id || preview.expires <= this.now()) return fail("stale_preview");
    if (this.cooldown && this.cooldown.until > this.now()) {
      return fail(this.cooldown.code, Math.ceil((this.cooldown.until - this.now()) / 1000));
    }
    const active: ActiveSend = { id, controller: new AbortController() };
    this.active = active;
    const timer = setTimeout(() => {
      active.reason = "timeout";
      active.controller.abort();
    }, REQUEST_TIMEOUT);
    timer.unref?.();
    let token = "";
    try {
      const ticket = await this.request(TICKET_ENDPOINT, JSON.stringify({
        schema_version: 1, body_sha256: preview.value.sha256,
        body_bytes: preview.value.byteLength, event_id: preview.eventId,
      }), active, undefined, [201]);
      const credential = field(ticket, "token");
      const expiration = field(ticket, "expires_at");
      const expiry = typeof expiration === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(expiration)
        ? Date.parse(expiration) : NaN;
      const remaining = expiry - this.now();
      if (typeof credential !== "string" || credential.length < 32 || credential.length > 2048 ||
          !/^[A-Za-z0-9\-._~+/]+=*$/.test(credential) ||
          !Number.isFinite(remaining) || remaining <= 0 || remaining > 120_000 ||
          field(ticket, "body_sha256") !== preview.value.sha256) throw new Failure("invalid_response");
      token = credential;
      const receipt = await this.request(DESTINATION, preview.value.body, active, token, [201]);
      const accepted = field(receipt, "accepted");
      const duplicates = field(receipt, "duplicates");
      const ids = field(receipt, "event_ids");
      if (field(receipt, "success") !== true || !Number.isInteger(accepted) || !Number.isInteger(duplicates) ||
          !((accepted === 1 && duplicates === 0) || (accepted === 0 && duplicates === 1)) ||
          !Array.isArray(ids) || ids.length !== 1 || ids[0] !== preview.eventId) throw new Failure("invalid_response");
      // 收到已持久化或已去重的确认后立即释放内容，不依赖渲染进程再调用取消。
      if (this.current?.value.id === id) this.clearPreview();
      return { ok: true, value: { eventId: preview.eventId, duplicate: duplicates === 1 } };
    } catch (error) {
      if (active.reason) return fail(active.reason);
      if (error instanceof Failure && error.retryAfterSeconds !== undefined &&
          (error.code === "rate_limited" || error.code === "unavailable")) {
        this.cooldown = { until: this.now() + error.retryAfterSeconds * 1000, code: error.code };
      }
      return error instanceof Failure ? fail(error.code, error.retryAfterSeconds) : fail("unavailable");
    } finally {
      token = "";
      clearTimeout(timer);
      if (this.active === active) this.active = undefined;
    }
  }

  /** 取消立即释放并发槽；discard=false 保留已确认的内容，允许手动幂等重试。 */
  cancel(id: unknown, discard: boolean): DiagnosticResult<null> {
    if (!this.status().available) return fail("disabled");
    if (typeof id !== "string" || typeof discard !== "boolean") return fail("invalid_request");
    if (this.current?.value.id !== id && this.active?.id !== id) return fail("stale_preview");
    if (this.active?.id === id) {
      const active = this.active;
      this.active = undefined;
      active.reason = "cancelled";
      active.controller.abort();
    }
    if (discard && this.current?.value.id === id) this.clearPreview();
    return { ok: true, value: null };
  }

  /** 页面离开时清除预览和请求，保留有限的安全生命周期代码。 */
  clear(): void {
    if (this.active) {
      this.active.reason = "cancelled";
      this.active.controller.abort();
      this.active = undefined;
    }
    this.clearPreview();
  }

  dispose(): void {
    this.clear();
    this.recent = [];
    this.cooldown = undefined;
    this.disposed = true;
  }

  private clearPreview(): void {
    if (this.previewTimer) clearTimeout(this.previewTimer);
    this.previewTimer = undefined;
    this.current = undefined;
  }

  private async request(url: string, body: string, active: ActiveSend, token: string | undefined, success: number[]): Promise<unknown> {
    const headers: Record<string, string> = { "Content-Type": "application/json", Accept: "application/json" };
    if (token) headers.Authorization = `Bearer ${token}`;
    const signal = active.controller.signal;
    if (signal.aborted) throw new Failure("cancelled");
    const pending = this.fetch(url, {
      method: "POST", headers, body, signal, redirect: "error", credentials: "omit",
      cache: "no-store", referrerPolicy: "no-referrer",
    });
    // 即使替身 fetch 或适配器忽略中断，迟到的响应流也立即丢弃。
    void pending.then((response) => { if (signal.aborted) discardBody(response); }, () => undefined);
    const response = await abortable(pending, signal);
    if (response.redirected) { discardBody(response); throw new Failure("invalid_response"); }
    if (!success.includes(response.status)) {
      discardBody(response);
      const retry = this.retryAfter(response.headers.get("retry-after"));
      if (response.status === 401 || response.status === 403) throw new Failure("unauthorized");
      if (response.status === 429) throw new Failure("rate_limited", retry);
      if (response.status === 503) throw new Failure("unavailable", retry);
      if (response.status >= 200 && response.status < 400) throw new Failure("invalid_response");
      throw new Failure("unavailable");
    }
    return boundedJSON(response, signal);
  }

  private retryAfter(value: string | null): number | undefined {
    if (value === null || value.length > 128) return undefined;
    const seconds = /^\d+$/.test(value) ? Number(value) : Math.ceil((Date.parse(value) - this.now()) / 1000);
    return Number.isFinite(seconds) && seconds >= 0 ? Math.min(3600, Math.max(1, Math.ceil(seconds))) : undefined;
  }
}
