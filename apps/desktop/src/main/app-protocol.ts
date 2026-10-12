// app://gogosling/ 协议的纯逻辑：URL → 磁盘路径的安全映射、MIME 类型、web 产物目录。
//
// 自定义协议从本地文件提供静态资源，渲染进程的 origin 固定为 app://gogosling（契约允许的 Origin），
// 守护进程的 API 只需要暴露 JSON，不需要托管任何页面。同一协议下 /api/ 前缀的请求不走这里，
// 由 api-proxy.ts 转发给守护进程（页面因此与 API 同源，不需要 CORS）。

import * as path from "node:path";

/** 自定义协议名。 */
export const APP_SCHEME = "app";

/** 协议主机名；其他主机一律 404。 */
export const APP_HOST = "gogosling";

/** 渲染进程的 origin，与 api/openapi 安全约定中允许的 Origin 一致。 */
export const APP_ORIGIN = `${APP_SCHEME}://${APP_HOST}`;

/** 默认入口文件。 */
export const INDEX_FILE = "index.html";

export interface WebRootInput {
  isPackaged: boolean;
  /** Electron 的 process.resourcesPath。 */
  resourcesPath: string;
  /** app.getAppPath()：开发态是 apps/desktop，打包后是 app.asar。 */
  appPath: string;
}

/** web 产物目录：打包后是 extraResources 的 resources/web，开发态是仓库里的 apps/web/dist。 */
export function resolveWebRoot(input: WebRootInput): string {
  if (input.isPackaged) return path.join(input.resourcesPath, "web");
  return path.resolve(input.appPath, "..", "web", "dist");
}

/**
 * 把 app://gogosling/<path> 映射为 webRoot 下的绝对路径。
 *
 * 返回 null 的情况：不是本协议/主机、百分号解码失败、含 NUL、任一段为 ".."、
 * 或最终路径落在 webRoot 之外。目录形式（空路径或以 / 结尾）映射到 index.html。
 * 不检查文件是否存在；SPA 回退由调用方结合 fallbackToIndex 决定。
 */
export function resolveAppFile(rawUrl: string, webRoot: string): string | null {
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch {
    return null;
  }
  if (url.protocol !== `${APP_SCHEME}:` || url.hostname !== APP_HOST) return null;

  let pathname: string;
  try {
    pathname = decodeURIComponent(url.pathname);
  } catch {
    return null;
  }
  if (pathname.includes("\0")) return null;

  const segments = pathname.split("/").filter((s) => s.length > 0);
  for (const segment of segments) {
    if (segment === "." || segment === "..") return null;
    // Windows 下 "\" 与驱动器前缀会逃出目录；统一拒绝。
    if (segment.includes("\\") || segment.includes(":")) return null;
  }
  const relative = segments.length === 0 || pathname.endsWith("/") ? [...segments, INDEX_FILE] : segments;

  const root = path.resolve(webRoot);
  const full = path.resolve(root, ...relative);
  const rel = path.relative(root, full);
  if (rel.startsWith("..") || path.isAbsolute(rel)) return null;
  return full;
}

/** 没有扩展名的路径（例如 /chat）在文件不存在时应回退到 index.html（哈希路由不会触发，保留以防外部链接）。 */
export function fallbackToIndex(filePath: string): boolean {
  return path.extname(filePath) === "";
}

const CONTENT_TYPES: Readonly<Record<string, string>> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttf": "font/ttf",
  ".txt": "text/plain; charset=utf-8",
  ".wasm": "application/wasm",
};

/** 按扩展名给出 Content-Type；未知类型用 application/octet-stream（配合 nosniff 不会被当作脚本执行）。 */
export function contentTypeFor(filePath: string): string {
  return CONTENT_TYPES[path.extname(filePath).toLowerCase()] ?? "application/octet-stream";
}
