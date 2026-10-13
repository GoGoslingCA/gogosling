// 独立网页会话的按需网络出口。解析后固定连接公网 IP，避免浏览器再次解析造成重绑定。
// 不记录网址、头部或正文；不缓存数据。关闭出口同时停止全部套接字。
import * as http from "node:http";
import { lookup } from "node:dns/promises";
import * as net from "node:net";
import type { Duplex } from "node:stream";
import { browserURL, isPublicBrowserAddress } from "./workspace-browser-policy.js";

type Address = { address: string; family: number };
type Resolve = (hostname: string) => Promise<Address[]>;

/** 同一浏览器宿主在重建出口时共用 DNS 槽位；已取消请求不能绕过真实解析上限。 */
export interface WorkspaceBrowserResolutionBudget { active: number }

/** 检查全部解析结果；不缓存地址，返回值只用于本次固定 IP 连接。 */
export async function resolveBrowserAddress(hostname: string, resolve: Resolve = host => lookup(host, { all: true })): Promise<Address> {
  const host = hostname.replace(/^\[|\]$/g, "");
  if (net.isIP(host)) {
    if (!isPublicBrowserAddress(host)) throw new Error("blocked_address");
    return { address: host, family: net.isIP(host) };
  }
  let timeout: ReturnType<typeof setTimeout> | undefined;
  try {
    const addresses = await Promise.race([
      resolve(host),
      new Promise<never>((_resolve, reject) => { timeout = setTimeout(() => reject(new Error("resolve_timeout")), 3000); timeout.unref(); }),
    ]);
    if (!addresses.length || addresses.length > 64 || addresses.some(item => !isPublicBrowserAddress(item.address))) throw new Error("blocked_address");
    return addresses.find(item => item.family === 4) ?? addresses[0]!;
  } finally { if (timeout) clearTimeout(timeout); }
}

/** 一个工作台网页共享一个有界、无磁盘缓存的转发器。 */
export class WorkspaceBrowserEgress {
  private readonly sockets = new Set<Duplex>();
  private server: http.Server | null = null;
  private disposed = false;
  private pending: Promise<string> | null = null;
  private requests = 0;
  private cancelStart: (() => void) | null = null;

  constructor(
    private readonly resolve: Resolve = host => lookup(host, { all: true }),
    private readonly resolutionBudget: WorkspaceBrowserResolutionBudget = { active: 0 },
  ) {}

  private get resolutions(): number { return this.resolutionBudget.active; }

  /** 首次显示网页时启动，返回 Chromium 固定代理地址。 */
  start(): Promise<string> {
    if (this.disposed) return Promise.reject(new Error("disposed"));
    this.pending ??= this.listen();
    return this.pending;
  }

  /** 无需等远端响应即可释放全部连接；调用可重复。 */
  dispose(): void {
    this.disposed = true;
    this.cancelStart?.();
    this.cancelStart = null;
    for (const socket of this.sockets) socket.destroy();
    this.sockets.clear();
    this.server?.close();
    this.server = null;
  }

  private async listen(): Promise<string> {
    const server = http.createServer({ maxHeaderSize: 16_384 }, (request, response) => { void this.forward(request, response); });
    this.server = server;
    server.maxConnections = 64;
    server.headersTimeout = 10_000;
    server.requestTimeout = 30_000;
    server.keepAliveTimeout = 3000;
    server.on("connection", socket => this.track(socket));
    server.on("connect", (request, client, head) => { void this.tunnel(request, client, head); });
    server.on("upgrade", (_request, socket) => socket.destroy());
    server.on("clientError", (_error, socket) => socket.destroy());
    await new Promise<void>((resolve, reject) => {
      this.cancelStart = () => reject(new Error("disposed"));
      server.once("error", reject);
      server.listen(0, "127.0.0.1", () => {
        if (this.disposed) { server.close(); reject(new Error("disposed")); return; }
        server.removeListener("error", reject);
        resolve();
      });
    }).finally(() => { this.cancelStart = null; });
    server.on("error", () => this.dispose());
    if (this.disposed) { server.close(); throw new Error("disposed"); }
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("unavailable");
    return `http://127.0.0.1:${address.port}`;
  }

  private track(socket: Duplex): void {
    this.sockets.add(socket);
    if (socket instanceof net.Socket) socket.setTimeout(30_000, () => socket.destroy());
    socket.once("close", () => this.sockets.delete(socket));
    socket.on("error", () => socket.destroy());
  }

  private acquire(): (() => void) | null {
    if (this.disposed || this.requests >= 32) return null;
    ++this.requests;
    let released = false;
    return () => { if (!released) { released = true; --this.requests; } };
  }

  private async resolveBounded(hostname: string): Promise<Address[]> {
    if (this.disposed || this.resolutions >= 32) throw new Error("resolve_busy");
    ++this.resolutionBudget.active;
    // 系统 DNS 不能取消；只在真实解析结束时归还槽位，客户端取消不会绕过上限。
    try { return await this.resolve(hostname); } finally { --this.resolutionBudget.active; }
  }

  private async tunnel(request: http.IncomingMessage, client: Duplex, head: Buffer): Promise<void> {
    const release = this.acquire();
    if (!release) { client.end("HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\n\r\n"); return; }
    client.once("close", release);
    try {
      const authority = request.url ?? "";
      if (!authority.endsWith(":443") || authority.includes("/") || authority.includes("@")) throw new Error("blocked_url");
      const url = browserURL(`https://${authority}`);
      if (!url) throw new Error("blocked_url");
      const address = await resolveBrowserAddress(url.hostname, host => this.resolveBounded(host));
      if (this.disposed || client.destroyed) return;
      const upstream = net.connect({ host: address.address, family: address.family, port: 443 });
      this.track(upstream);
      client.once("close", () => upstream.destroy());
      upstream.once("close", () => client.destroy());
      upstream.once("connect", () => {
        client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
        if (head.length) upstream.write(head);
        client.pipe(upstream).pipe(client);
      });
    } catch { release(); if (!client.destroyed) client.end("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n"); }
  }

  private async forward(request: http.IncomingMessage, response: http.ServerResponse): Promise<void> {
    const release = this.acquire();
    if (!release) { response.writeHead(503); response.end(); return; }
    response.once("close", release);
    try {
      const url = browserURL(request.url);
      if (!url || url.protocol !== "http:") throw new Error("blocked_url");
      const address = await resolveBrowserAddress(url.hostname, host => this.resolveBounded(host));
      if (this.disposed || response.destroyed) return;
      const headers: http.OutgoingHttpHeaders = { ...request.headers, host: url.host };
      for (const name of ["proxy-authorization", "proxy-connection", "connection", "upgrade"]) delete headers[name];
      const upstream = http.request({ hostname: address.address, family: address.family, port: 80, path: url.pathname + url.search, method: request.method, headers, agent: false }, remote => {
        const responseHeaders = { ...remote.headers };
        delete responseHeaders["alt-svc"];
        response.writeHead(remote.statusCode ?? 502, responseHeaders);
        remote.pipe(response);
      });
      upstream.on("socket", socket => this.track(socket));
      upstream.on("error", () => { if (!response.headersSent) response.writeHead(502); response.end(); });
      response.once("close", () => upstream.destroy());
      request.pipe(upstream);
    } catch { release(); if (!response.destroyed) { response.writeHead(403); response.end(); } }
  }
}
