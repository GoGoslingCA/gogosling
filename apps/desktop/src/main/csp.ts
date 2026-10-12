// 渲染进程的内容安全策略。
//
// 生产态页面来自 app://gogosling，'self' 即该 origin；页面只访问同源的 app://gogosling/api/...，
// 由主进程代理到守护进程（api-proxy.ts），所以 connect-src 只需要 'self'，不再放行回环地址。
// 开发态（vite dev server）页面直连守护进程，需要放开回环地址（端口由 daemon.json 决定，所以用通配）、
// 内联脚本与 WebSocket（HMR）。

export interface CspOptions {
  /** 开发服务器 origin（例如 http://127.0.0.1:5173）；null 表示生产态。 */
  devOrigin: string | null;
}

/** 开发态下守护进程可能出现的回环来源。 */
export const DAEMON_CONNECT_SOURCES = ["http://127.0.0.1:*", "http://localhost:*", "http://[::1]:*"] as const;

/** 生成 Content-Security-Policy 头的值。 */
export function buildCsp(opts: CspOptions): string {
  const dev = opts.devOrigin;
  const scriptSrc = ["'self'"];
  const connectSrc = ["'self'"];
  if (dev) {
    // @vitejs/plugin-react 在开发态注入内联的 refresh 预热脚本；HMR 走 ws；页面直连守护进程。
    scriptSrc.push("'unsafe-inline'", dev);
    connectSrc.push(...DAEMON_CONNECT_SOURCES, dev, dev.replace(/^http/, "ws"));
  }
  const directives: Array<[string, string[]]> = [
    ["default-src", ["'self'"]],
    ["script-src", scriptSrc],
    // React 的 style 属性需要 'unsafe-inline'；没有外部样式来源。
    ["style-src", ["'self'", "'unsafe-inline'"]],
    ["img-src", ["'self'", "data:", "blob:"]],
    ["media-src", ["'self'", "blob:"]],
    ["font-src", ["'self'", "data:"]],
    ["connect-src", connectSrc],
    ["worker-src", ["'self'", "blob:"]],
    ["object-src", ["'none'"]],
    ["base-uri", ["'none'"]],
    ["form-action", ["'none'"]],
    ["frame-ancestors", ["'none'"]],
    // 仅允许本地 PDF Blob 和受 sandbox/CSP 双重限制的 srcdoc；不允许远程嵌入。
    ["frame-src", ["'self'", "blob:"]],
  ];
  return directives.map(([name, values]) => `${name} ${values.join(" ")}`).join("; ");
}
