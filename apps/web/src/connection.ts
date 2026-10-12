// 连接信息的发现与解析。优先级：桌面壳 preload 注入 > URL hash > 用户在"连接"页手动输入。
// 连接信息只存在内存里，绝不写 localStorage / sessionStorage（AGENTS.md §5）。
// 桌面壳打包后是代理模式：endpoint 为 app://gogosling（与页面同源）、token 为空，令牌由主进程注入。
//
// URL hash 只作为一次性的交接通道，读取后立即从地址栏抹掉。
// 令牌不进入持久存储或网络请求的查询串。

import { normalizeBaseUrl } from "./api/client";

export type ConnectionSource = "electron" | "hash" | "manual";

export interface Connection {
  /** 守护进程地址；空串表示同源（开发代理）；app://gogosling 表示桌面壳的同源代理。 */
  endpoint: string;
  /** 访问令牌；桌面壳代理模式下为空串（不发 Authorization 头）。 */
  token: string;
  source: ConnectionSource;
}

/**
 * 解析 URL hash 中的连接信息。支持两种形式：
 *   #endpoint=http%3A%2F%2F127.0.0.1%3A46750&token=xxx
 *   #/connect?endpoint=...&token=...   （hash 路由形式）
 * 没有 token 时返回 null。
 */
export function parseConnectionHash(hash: string): Connection | null {
  if (!hash) return null;
  let raw = hash.startsWith("#") ? hash.slice(1) : hash;
  const q = raw.indexOf("?");
  if (raw.startsWith("/")) {
    if (q === -1) return null;
    raw = raw.slice(q + 1);
  }
  const params = new URLSearchParams(raw);
  const token = (params.get("token") ?? "").trim();
  if (!token) return null;
  return {
    endpoint: normalizeBaseUrl(params.get("endpoint") ?? ""),
    token,
    source: "hash",
  };
}

/** 判断 hash 是否携带了令牌（用于决定是否需要抹掉地址栏）。 */
export function hashCarriesToken(hash: string): boolean {
  return parseConnectionHash(hash) !== null;
}

export interface DiscoverOptions {
  bridge?: Window["gogosling"];
  hash?: string;
  /** 读取 hash 之后把地址栏替换成 replacement（默认 "#/"），避免令牌留在历史记录里。 */
  scrubHash?: (replacement: string) => void;
}

/**
 * 启动时发现连接：先问桌面壳，再看 URL hash。两者都没有时返回 null，由"连接"页接手。
 * 桌面壳的 getConnection 抛错或返回 null 时不中断流程（壳可能尚未发现守护进程）。
 * 桌面壳的代理模式（mode: "proxy"）不带令牌，也是有效连接。
 */
export async function discoverConnection(options: DiscoverOptions = {}): Promise<Connection | null> {
  const bridge = options.bridge ?? (typeof window !== "undefined" ? window.gogosling : undefined);
  if (bridge?.getConnection) {
    try {
      const conn = await bridge.getConnection();
      if (conn && (conn.token || conn.mode === "proxy")) {
        return { endpoint: normalizeBaseUrl(conn.endpoint ?? ""), token: conn.token ?? "", source: "electron" };
      }
    } catch {
      // 壳尚未就绪；继续其他方式。
    }
  }
  const hash = options.hash ?? (typeof location !== "undefined" ? location.hash : "");
  const fromHash = parseConnectionHash(hash);
  if (fromHash) {
    const scrub =
      options.scrubHash ??
      ((replacement: string) => {
        if (typeof history !== "undefined" && typeof location !== "undefined") {
          history.replaceState(null, "", `${location.pathname}${location.search}${replacement}`);
        }
      });
    scrub("#/");
    return fromHash;
  }
  return null;
}

/** 开发环境下的默认守护进程地址（与 vite 代理目标一致）。 */
export const DEFAULT_ENDPOINT = "http://127.0.0.1:46750";
