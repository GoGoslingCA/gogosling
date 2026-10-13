// 网页网络与布局策略：独立于 Electron，供宿主、出口转发器和测试共同使用。
import { isIP } from "node:net";
import type { WorkspaceBrowserBounds } from "../shared/workspace-browser.js";

/** 只允许公网单播地址；保留、文档、过渡及私网地址全部拒绝。 */
export function isPublicBrowserAddress(raw: string): boolean {
  const address = raw.toLowerCase().replace(/^\[|\]$/g, "");
  if (isIP(address) === 4) {
    const [a = 0, b = 0, c = 0] = address.split(".").map(Number);
    return !(a === 0 || a === 10 || a === 127 || a >= 224 ||
      (a === 100 && b >= 64 && b <= 127) || (a === 169 && b === 254) ||
      (a === 172 && b >= 16 && b <= 31) || (a === 192 && (b === 0 || b === 168 || (b === 88 && c === 99))) ||
      (a === 198 && (b === 18 || b === 19 || (b === 51 && c === 100))) || (a === 203 && b === 0 && c === 113));
  }
  if (isIP(address) !== 6) return false;
  const [first = 0, second = 0] = address.split(":").map(part => Number.parseInt(part || "0", 16));
  return first >= 0x2000 && first <= 0x3fff && first !== 0x2002 && first !== 0x3fff && !(first === 0x2001 && (second < 0x200 || second === 0xdb8));
}

/** URL 限定 http(s) 标准端口；拒绝本机名称、凭据和危险协议。 */
export function browserURL(raw: unknown): URL | null {
  if (typeof raw !== "string" || raw.length > 8192 || /[\u0000-\u0020\u007f]/.test(raw)) return null;
  try {
    const url = new URL(raw);
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.port) return null;
    const host = url.hostname.toLowerCase().replace(/^\[|\]$/g, "").replace(/\.$/, "");
    if (!host || host === "localhost" || !host.includes(".") && !isIP(host) || /\.(?:localhost|local|internal|test|invalid|onion)$/.test(host)) return null;
    if (isIP(host) && !isPublicBrowserAddress(host)) return null;
    return url;
  } catch { return null; }
}

/** 标签 ID 有固定上限，不能用网页地址冒充内部标识。 */
export function browserTabId(raw: unknown): raw is string {
  return typeof raw === "string" && /^[a-zA-Z0-9_-]{1,96}$/.test(raw);
}

/** 将原生视图限定在窗口内，保留顶部操作栏，拒绝异常或不可见尺寸。 */
export function browserBounds(raw: unknown, size: { width: number; height: number }): WorkspaceBrowserBounds | null {
  if (!raw || typeof raw !== "object") return null;
  const bounds = raw as Record<string, unknown>;
  if (![bounds.x, bounds.y, bounds.width, bounds.height].every(value => typeof value === "number" && Number.isFinite(value) && Math.abs(value) <= 100_000)) return null;
  const x = Math.max(0, Math.floor(bounds.x as number));
  const y = Math.max(40, Math.floor(bounds.y as number));
  const width = Math.min(Math.floor(bounds.width as number), size.width - x);
  const height = Math.min(Math.floor(bounds.height as number), size.height - y);
  return width >= 1 && height >= 1 ? { x, y, width, height } : null;
}
