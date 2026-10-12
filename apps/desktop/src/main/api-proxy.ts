// app://gogosling/api/* → 守护进程本地 HTTP API 的同源代理（主进程侧）。
//
// 为什么要代理：页面来自 app://gogosling，守护进程在 http://127.0.0.1:<port>，两者跨源；
// 守护进程不发任何 CORS 允许头（internal/server/doc.go），预检还会被鉴权拦成 401，
// 所以渲染进程直接 fetch 回环地址在打包后的桌面壳里根本读不到结果。改为：
//   - 渲染进程只访问同源的 app://gogosling/api/...；
//   - 主进程在 protocol.handle 里把 /api/ 前缀的请求用 net.fetch 转发给守护进程，并注入 Bearer 令牌；
//   - 响应体原样以流的形式交回（SSE 长连接因此可用）。
// 令牌因此不再进入页面内存。开发模式（GOGOSLING_WEB_URL 指向 vite）不走这里，保持直连。
//
// 本文件不 import electron：fetch 由调用方注入（生产为 net.fetch），vitest 直接覆盖全部分支。

import { APP_HOST, APP_ORIGIN, APP_SCHEME } from "./app-protocol.js";
import type { ConnectionInfo } from "../shared/ipc.js";

/** 被代理的路径前缀；其余路径仍由静态文件处理。 */
export const API_PATH_PREFIX = "/api/";

/** 守护进程连接（主进程内部使用，含令牌）。 */
export interface DaemonConnection {
  endpoint: string;
  token: string;
}

/** 转发给守护进程的请求头白名单（小写）。Authorization 由主进程注入，Cookie / Origin / Referer 一律不转发。 */
export const FORWARDED_REQUEST_HEADERS: readonly string[] = ["accept", "accept-language", "content-type", "last-event-id", "cache-control"];

/** 交回渲染进程的响应头白名单（小写）。 */
export const FORWARDED_RESPONSE_HEADERS: readonly string[] = [
  "content-type",
  "cache-control",
  "content-disposition",
  "content-language",
  "retry-after",
  "www-authenticate",
  "x-accel-buffering",
];

/** 规范要求不能带响应体的状态码。 */
const NULL_BODY_STATUSES: ReadonlySet<number> = new Set([101, 103, 204, 205, 304]);

/**
 * 若 rawUrl 是 app://gogosling/api/...，返回要转发的 "路径+查询串"；否则返回 null（交给静态文件处理）。
 *
 * WHATWG URL 解析已经把 "." / ".." / "%2e%2e" 段归一化，所以 /api/../x 会变成 /x 而不被代理；
 * 编码的斜杠（%2F）保持编码原样转发，由守护进程的路由自行处理。
 */
export function apiProxyPath(rawUrl: string): string | null {
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch {
    return null;
  }
  if (url.protocol !== `${APP_SCHEME}:` || url.hostname !== APP_HOST) return null;
  if (!url.pathname.startsWith(API_PATH_PREFIX)) return null;
  return `${url.pathname}${url.search}`;
}

/** 把 app://gogosling/api/... 映射为守护进程上的绝对 URL；不是 API 路径时返回 null。 */
export function apiProxyTarget(rawUrl: string, endpoint: string): string | null {
  const p = apiProxyPath(rawUrl);
  if (p === null) return null;
  return `${endpoint.replace(/\/+$/, "")}${p}`;
}

/** 由白名单请求头加上主进程持有的令牌构造转发请求头；页面自带的 Authorization 被丢弃。 */
export function proxyRequestHeaders(incoming: Headers, token: string): Headers {
  const out = new Headers();
  for (const name of FORWARDED_REQUEST_HEADERS) {
    const value = incoming.get(name);
    if (value !== null) out.set(name, value);
  }
  if (token) out.set("authorization", `Bearer ${token}`);
  return out;
}

/** 只交回白名单响应头，并统一加 nosniff。 */
export function proxyResponseHeaders(upstream: Headers): Headers {
  const out = new Headers();
  for (const name of FORWARDED_RESPONSE_HEADERS) {
    const value = upstream.get(name);
    if (value !== null) out.set(name, value);
  }
  out.set("x-content-type-options", "nosniff");
  return out;
}

/**
 * 请求是否来自我们自己的页面。同源 GET 通常不带 Origin；带了就必须是 app://gogosling。
 * 其他来源（开发服务器、将来意外加载的任何页面）不能借主进程的令牌访问 API（防 CSRF）。
 */
export function isSameOriginRequest(headers: Headers): boolean {
  const origin = headers.get("origin");
  return origin === null || origin === APP_ORIGIN;
}

/** 契约形状的错误响应 {error:{code,message}}，web 的 ApiError 能直接解析。 */
export function errorResponse(status: number, code: string, message: string): Response {
  return new Response(JSON.stringify({ error: { code, message } }), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "x-content-type-options": "nosniff", "cache-control": "no-store" },
  });
}

/** 代理所需的副作用。 */
export interface ApiProxyDeps {
  /** 守护进程就绪时返回连接信息，否则 null。 */
  connection(): DaemonConnection | null;
  /** 生产为 electron 的 net.fetch。 */
  fetch(url: string, init: RequestInit): Promise<Response>;
  /** 转发失败时记日志（不含令牌）。 */
  onError?(info: { path: string; err: unknown }): void;
}

/**
 * 处理一次 app:// 请求。不是 /api/ 路径时返回 null，由调用方继续按静态文件处理。
 *
 *   - 来源不是 app://gogosling → 403 forbidden；
 *   - 守护进程未就绪 → 503 unavailable；
 *   - 转发失败（连接被拒、重定向）→ 502 unavailable；
 *   - 其余按守护进程的状态码与响应体原样返回，响应体是流（SSE 不被缓冲）。
 */
export async function proxyApiRequest(request: Request, deps: ApiProxyDeps): Promise<Response | null> {
  const pathAndQuery = apiProxyPath(request.url);
  if (pathAndQuery === null) return null;
  if (!isSameOriginRequest(request.headers)) return errorResponse(403, "forbidden", "origin not allowed");

  const conn = deps.connection();
  if (!conn) return errorResponse(503, "unavailable", "daemon not ready");

  const method = request.method.toUpperCase();
  const init: RequestInit = {
    method,
    headers: proxyRequestHeaders(request.headers, conn.token),
    // 守护进程不会重定向；出现重定向说明端点不对，宁可失败也不把令牌带到别处。
    redirect: "error",
    signal: request.signal,
  };
  if (method !== "GET" && method !== "HEAD") {
    // 请求体（JSON 或 multipart 附件）先读完再转发：避免流式上传对 duplex 的要求，附件大小由守护进程限制。
    const body = await request.arrayBuffer();
    if (body.byteLength > 0) init.body = body;
  }

  let upstream: Response;
  try {
    upstream = await deps.fetch(`${conn.endpoint.replace(/\/+$/, "")}${pathAndQuery}`, init);
  } catch (err) {
    deps.onError?.({ path: pathAndQuery.split("?")[0] ?? pathAndQuery, err });
    return errorResponse(502, "unavailable", "daemon unreachable");
  }

  const headers = proxyResponseHeaders(upstream.headers);
  if (NULL_BODY_STATUSES.has(upstream.status) || method === "HEAD") {
    await upstream.body?.cancel().catch(() => undefined);
    return new Response(null, { status: upstream.status, statusText: upstream.statusText, headers });
  }
  return new Response(upstream.body, { status: upstream.status, statusText: upstream.statusText, headers });
}

/**
 * 交给渲染进程的连接信息。
 *
 *   - 生产（页面来自 app://gogosling）：{ endpoint: "app://gogosling", token: "", mode: "proxy" }，
 *     页面同源访问，令牌留在主进程；
 *   - 开发（页面来自 GOGOSLING_WEB_URL 的 vite）：保持原样直连 { endpoint, token, mode: "direct" }。
 *
 * 守护进程未就绪时返回 null，页面进入"连接"页，就绪后主进程会重载页面。
 */
export function rendererConnection(conn: DaemonConnection | null, opts: { dev: boolean }): ConnectionInfo | null {
  if (!conn) return null;
  if (opts.dev) return { endpoint: conn.endpoint, token: conn.token, mode: "direct" };
  return { endpoint: APP_ORIGIN, token: "", mode: "proxy" };
}
