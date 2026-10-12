// fetch 包装：baseURL + Bearer 令牌（有令牌时）+ Accept-Language；把服务端错误信封解析为 ApiError。
//
// 统一请求入口与请求头，约束如下：
//   - 令牌只存在于 ApiClient 实例内存，不读写 localStorage / sessionStorage；
//   - 错误是带 code/details/status 的 ApiError，而不是把响应体拼进 Error.message 再用正则反解；
//   - 401 通过注入的回调交给 React 状态机处理，不在这里做整页跳转；
//   - 没有模块级可变全局状态（活动智能体、重定向标志等）。

import type { ClientErrorCode, ErrorCode, ErrorResponse, ServerErrorCode } from "./types";

/** 服务端或客户端产生的 API 错误。code 稳定可用于分支判断；message 已按 Accept-Language 本地化。 */
export class ApiError extends Error {
  readonly code: ErrorCode;
  readonly details: Record<string, unknown>;
  /** HTTP 状态码；客户端错误（network / aborted）为 0。 */
  readonly status: number;

  constructor(code: ErrorCode, message: string, options: { status?: number; details?: Record<string, unknown>; cause?: unknown } = {}) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = "ApiError";
    this.code = code;
    this.status = options.status ?? 0;
    this.details = options.details ?? {};
  }

  /** 判断任意值是否为某个错误码的 ApiError。 */
  static is(err: unknown, code?: ErrorCode): err is ApiError {
    return err instanceof ApiError && (code === undefined || err.code === code);
  }
}

const SERVER_CODES: ReadonlySet<string> = new Set<ServerErrorCode>([
  "invalid",
  "unauthorized",
  "forbidden",
  "not_found",
  "conflict",
  "denied",
  "unavailable",
  "timeout",
  "cancelled",
  "internal",
]);

/** 没有可解析的错误信封时按 HTTP 状态码推断错误码（例如代理返回的 HTML 502 页面）。 */
export function codeFromStatus(status: number): ServerErrorCode {
  switch (status) {
    case 400:
    case 413:
    case 415:
    case 422:
      return "invalid";
    case 401:
      return "unauthorized";
    case 403:
      return "forbidden";
    case 404:
      return "not_found";
    case 409:
      return "conflict";
    case 408:
    case 504:
      return "timeout";
    case 502:
    case 503:
      return "unavailable";
    default:
      return "internal";
  }
}

/**
 * 从响应体解析错误信封。只接受契约规定的形状 {error:{code,message,details}}；
 * code 不在已知集合内时按状态码推断，避免把未知字符串当作错误码进入界面分支。
 */
export function parseErrorBody(status: number, body: unknown): { code: ServerErrorCode; message: string; details: Record<string, unknown> } {
  if (body && typeof body === "object" && "error" in body) {
    const err = (body as Partial<ErrorResponse>).error;
    if (err && typeof err === "object") {
      const rawCode = typeof err.code === "string" ? err.code : "";
      const code = SERVER_CODES.has(rawCode) ? (rawCode as ServerErrorCode) : codeFromStatus(status);
      const message = typeof err.message === "string" ? err.message : "";
      const details = err.details && typeof err.details === "object" ? err.details : {};
      return { code, message, details };
    }
  }
  return { code: codeFromStatus(status), message: "", details: {} };
}

/** 判断 fetch 抛出的异常是否是网络层失败（离线、拒绝连接、DNS）而不是我们自己抛出的错误。 */
export function classifyFetchFailure(err: unknown): ClientErrorCode {
  if (err && typeof err === "object" && "name" in err && (err as { name?: unknown }).name === "AbortError") {
    return "aborted";
  }
  return "network";
}

export type QueryValue = string | number | boolean | undefined | null;

export interface RequestOptions {
  query?: Record<string, QueryValue>;
  /** JSON 请求体；传 FormData 时按 multipart 发送（由浏览器设置 Content-Type）。 */
  body?: unknown;
  signal?: AbortSignal;
  headers?: Record<string, string>;
  /** 已鉴权的成果正文和下载复用相同错误处理；默认仍为 JSON。 */
  responseType?: "text" | "blob";
}

export interface ApiClientOptions {
  /**
   * 守护进程地址，例如 http://127.0.0.1:46750；空串表示同源（开发服务器代理）；
   * 桌面壳打包后为 app://gogosling（同源，由主进程代理）。
   */
  baseUrl: string;
  /** 访问令牌；只在内存中保存。为空时不发 Authorization 头（桌面壳代理模式由主进程注入）。 */
  token: string;
  /** 当前界面语言，用于 Accept-Language；用函数以便语言切换后立即生效。 */
  locale?: () => string;
  /** 收到 401 时的回调（令牌失效）；由连接状态机决定如何处理。 */
  onUnauthorized?: (err: ApiError) => void;
  /** 可注入的 fetch 实现（测试用）。 */
  fetchImpl?: typeof fetch;
}

/** 是否已经带 scheme（http://、https://、桌面壳的 app:// 等）。 */
export function hasUrlScheme(raw: string): boolean {
  return /^[a-z][a-z0-9+.-]*:\/\//i.test(raw);
}

/** 规范化服务地址：没有 scheme 时补 http://、去掉末尾斜杠；空串保持为空（同源）。 */
export function normalizeBaseUrl(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  const withScheme = hasUrlScheme(trimmed) ? trimmed : `http://${trimmed}`;
  return withScheme.replace(/\/+$/, "");
}

/** 把 query 对象编码为 ?a=b 形式；undefined/null 跳过。 */
export function buildQuery(query?: Record<string, QueryValue>): string {
  if (!query) return "";
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null) continue;
    params.set(key, String(value));
  }
  const s = params.toString();
  return s ? `?${s}` : "";
}

/**
 * 本地 API 客户端。一个连接一个实例；令牌不可变，换令牌就换实例，避免并发请求读到半更新状态。
 */
export class ApiClient {
  readonly baseUrl: string;
  private readonly token: string;
  private readonly locale: () => string;
  private readonly onUnauthorized: ((err: ApiError) => void) | undefined;
  private readonly fetchImpl: typeof fetch;

  constructor(options: ApiClientOptions) {
    this.baseUrl = normalizeBaseUrl(options.baseUrl);
    this.token = options.token;
    this.locale = options.locale ?? (() => "zh");
    this.onUnauthorized = options.onUnauthorized;
    // 绑定到 globalThis，避免某些浏览器对脱离 window 调用 fetch 抛 Illegal invocation。
    this.fetchImpl = options.fetchImpl ?? ((input, init) => globalThis.fetch(input, init));
  }

  /** 拼出完整 URL（SSE 等需要直接使用 URL 的地方复用）。 */
  url(path: string, query?: Record<string, QueryValue>): string {
    const p = path.startsWith("/") ? path : `/${path}`;
    return `${this.baseUrl}${p}${buildQuery(query)}`;
  }

  /** 鉴权与语言头；SSE 连接复用。令牌为空（桌面壳代理模式）时不带 Authorization。 */
  authHeaders(): Record<string, string> {
    const headers: Record<string, string> = {
      "Accept-Language": this.locale(),
    };
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    return headers;
  }

  get<T>(path: string, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("GET", path, options);
  }

  post<T>(path: string, body?: unknown, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("POST", path, { ...options, body });
  }

  patch<T>(path: string, body: unknown, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("PATCH", path, { ...options, body });
  }

  delete<T = void>(path: string, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("DELETE", path, options);
  }

  /**
   * 发送请求并解析响应。2xx 且有 JSON 体时返回解析结果；204 返回 undefined。
   * 非 2xx 抛出 ApiError；网络失败抛出 code 为 network/aborted 的 ApiError。
   */
  async request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      ...this.authHeaders(),
      ...(options.headers ?? {}),
    };
    let body: BodyInit | undefined;
    if (options.body instanceof FormData) {
      body = options.body;
    } else if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(options.body);
    }

    let response: Response;
    try {
      response = await this.fetchImpl(this.url(path, options.query), {
        method,
        headers,
        body,
        signal: options.signal,
        // 本地 API 不走浏览器缓存，避免列表在代理层被缓存。
        cache: "no-store",
      });
    } catch (err) {
      const code = classifyFetchFailure(err);
      throw new ApiError(code, code === "aborted" ? "request aborted" : "network error", { cause: err });
    }

    if (!response.ok) {
      throw await this.toError(response);
    }
    if (response.status === 204) {
      return undefined as T;
    }
    if (options.responseType === "blob") return await response.blob() as T;
    if (options.responseType === "text") return await response.text() as T;
    const text = await response.text();
    if (!text) return undefined as T;
    try {
      return JSON.parse(text) as T;
    } catch (err) {
      throw new ApiError("internal", "invalid JSON in response", { status: response.status, cause: err });
    }
  }

  private async toError(response: Response): Promise<ApiError> {
    let parsed: unknown = null;
    try {
      const text = await response.text();
      parsed = text ? JSON.parse(text) : null;
    } catch {
      parsed = null;
    }
    const { code, message, details } = parseErrorBody(response.status, parsed);
    const err = new ApiError(code, message || `${response.status} ${response.statusText}`.trim(), {
      status: response.status,
      details,
    });
    if (code === "unauthorized" && this.onUnauthorized) {
      this.onUnauthorized(err);
    }
    return err;
  }
}
