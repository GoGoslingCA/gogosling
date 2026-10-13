// 从已认证的本地守护进程读取运行记录；不访问数据库，不接受渲染进程提供的地址或凭据。
import type { DaemonConnection } from "./api-proxy.js";
import type { DiagnosticTaskRequest } from "../shared/diagnostics.js";

const RESPONSE_LIMIT = 64 * 1024;

/** 本地校验失败仅携带固定错误码，不保留响应正文。 */
export class DiagnosticTaskReadError extends Error {
  constructor(readonly code: "unavailable" | "invalid_response" | "no_error") { super(code); }
}

function discard(response: Response): void {
  if (response.body && !response.body.locked) void response.body.cancel().catch(() => undefined);
}

function abortable<T>(pending: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const cancel = () => reject(new DiagnosticTaskReadError("unavailable"));
    signal.addEventListener("abort", cancel, { once: true });
    pending.then(value => { signal.removeEventListener("abort", cancel); resolve(value); }, error => {
      signal.removeEventListener("abort", cancel); reject(error);
    });
    if (signal.aborted) { signal.removeEventListener("abort", cancel); cancel(); }
  });
}

/** 仅读取固定运行路由；上层负责三秒截止时间和任务归属、失败状态的验证。 */
export async function readDiagnosticTask(
  request: DiagnosticTaskRequest,
  signal: AbortSignal,
  deps: { connection(): DaemonConnection | null; fetch(url: string, init: RequestInit): Promise<Response> },
): Promise<unknown> {
  const connection = deps.connection();
  if (!connection || signal.aborted) throw new DiagnosticTaskReadError("unavailable");
  // 即使连接来源被误配置，也不向公网发送守护进程令牌。
  let endpoint: URL;
  try { endpoint = new URL(connection.endpoint); } catch { throw new DiagnosticTaskReadError("unavailable"); }
  if (endpoint.protocol !== "http:" || !["127.0.0.1", "[::1]"].includes(endpoint.hostname) ||
      endpoint.username || endpoint.password || endpoint.search || endpoint.hash || endpoint.pathname !== "/") {
    throw new DiagnosticTaskReadError("unavailable");
  }
  const pending = deps.fetch(`${endpoint.origin}/api/v1/runs/${encodeURIComponent(request.runId)}`, {
    method: "GET", headers: { Authorization: `Bearer ${connection.token}`, Accept: "application/json" },
    redirect: "error", credentials: "omit", cache: "no-store", referrerPolicy: "no-referrer", signal,
  });
  void pending.then(response => { if (signal.aborted) discard(response); }, () => undefined);
  const response = await abortable(pending, signal);
  const length = response.headers.get("content-length");
  if (response.redirected || response.status !== 200 ||
      response.headers.get("content-type")?.split(";", 1)[0]?.trim().toLowerCase() !== "application/json" ||
      (length !== null && (!/^\d+$/.test(length) || Number(length) > RESPONSE_LIMIT)) || !response.body) {
    discard(response);
    throw new DiagnosticTaskReadError(response.status === 404 ? "no_error" : "invalid_response");
  }
  const reader = response.body.getReader();
  const cancel = () => { void reader.cancel().catch(() => undefined); };
  signal.addEventListener("abort", cancel, { once: true });
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  try {
    for (;;) {
      const chunk = await abortable(reader.read(), signal);
      if (chunk.done) break;
      bytes += chunk.value.byteLength;
      if (bytes > RESPONSE_LIMIT) throw new DiagnosticTaskReadError("invalid_response");
      chunks.push(chunk.value);
    }
    try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks, bytes))) as unknown; }
    catch { throw new DiagnosticTaskReadError("invalid_response"); }
  } finally {
    signal.removeEventListener("abort", cancel);
    cancel();
    reader.releaseLock();
  }
}
