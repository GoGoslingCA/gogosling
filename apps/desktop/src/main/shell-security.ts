// 壳层的安全判定（纯函数）：IPC 发送方是否可信、外链是否允许、深链与开发地址解析。
//
// 使用 Electron 的 setWindowOpenHandler / will-navigate / ipcMain 通道，
// 并在主进程校验发送方 origin。

import { APP_ORIGIN } from "./app-protocol.js";

/** 深链协议名（gogosling://…）。 */
export const DEEP_LINK_SCHEME = "gogosling";

/** 开发态指向 vite dev server 的环境变量。 */
export const ENV_WEB_URL = "GOGOSLING_WEB_URL";

/** 开机自启动时传给自身的参数：启动后不显示窗口，只驻留托盘。 */
export const HIDDEN_ARG = "--hidden";

/** 外链最大长度；更长的 URL 没有正当用途。 */
export const MAX_EXTERNAL_URL_LENGTH = 2048;

export interface TrustOptions {
  /** 开发服务器 origin；null 表示只信任 app://gogosling。 */
  devOrigin: string | null;
}

/** 页面 URL 是否属于我们自己的渲染进程（app://gogosling/... 或开发服务器）。 */
export function isTrustedPageUrl(rawUrl: string, opts: TrustOptions): boolean {
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch {
    return false;
  }
  if (url.origin === APP_ORIGIN) return true;
  // 非 standard scheme 的 origin 可能是 "null"，再按前缀兜底。
  if (url.href.startsWith(`${APP_ORIGIN}/`)) return true;
  return opts.devOrigin !== null && url.origin === opts.devOrigin;
}

/**
 * 校验要交给系统浏览器打开的链接：只允许带主机的 http/https，以及非空的 mailto。
 * 其他协议（file:、javascript:、自定义协议）一律返回 null。
 */
export function sanitizeExternalUrl(raw: unknown): string | null {
  if (typeof raw !== "string") return null;
  const trimmed = raw.trim();
  if (!trimmed || trimmed.length > MAX_EXTERNAL_URL_LENGTH || trimmed.includes("\0")) return null;
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    return null;
  }
  switch (url.protocol) {
    case "http:":
    case "https:":
      if (!url.hostname || url.username || url.password) return null;
      return url.href;
    case "mailto:":
      return url.pathname.trim() ? url.href : null;
    default:
      return null;
  }
}

/** 从命令行参数里找出第一个合法的 gogosling:// 深链。 */
export function findDeepLink(argv: readonly string[]): string | null {
  const prefix = `${DEEP_LINK_SCHEME}://`;
  for (const arg of argv) {
    if (!arg.toLowerCase().startsWith(prefix)) continue;
    try {
      const url = new URL(arg);
      if (url.protocol === `${DEEP_LINK_SCHEME}:`) return url.href;
    } catch {
      // 继续找下一个
    }
  }
  return null;
}

/** 深链的可记录形式：去掉查询串与片段，避免把可能的敏感参数写进日志。 */
export function describeDeepLink(rawUrl: string): string {
  try {
    const url = new URL(rawUrl);
    return `${url.protocol}//${url.host}${url.pathname}`;
  } catch {
    return `${DEEP_LINK_SCHEME}://<invalid>`;
  }
}

export interface DevWebTarget {
  /** 要加载的完整 URL。 */
  url: string;
  /** 该 URL 的 origin，用于 CSP 与可信页面判断。 */
  origin: string;
}

/** 解析 GOGOSLING_WEB_URL；只接受回环地址的 http(s)，其他值视为未设置。 */
export function parseDevWebUrl(raw: string | undefined): DevWebTarget | null {
  const trimmed = raw?.trim();
  if (!trimmed) return null;
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    return null;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return null;
  const host = url.hostname.toLowerCase();
  if (host !== "127.0.0.1" && host !== "localhost" && host !== "[::1]") return null;
  return { url: url.href, origin: url.origin };
}

/** 是否以隐藏方式启动（开机自启动传入 --hidden）。 */
export function shouldStartHidden(argv: readonly string[]): boolean {
  return argv.includes(HIDDEN_ARG);
}
