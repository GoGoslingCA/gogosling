import { describe, expect, it, vi } from "vitest";
import * as http from "node:http";
import * as net from "node:net";
import { browserBounds, browserTabId, browserURL, isPublicBrowserAddress } from "../src/main/workspace-browser-policy.js";
import { resolveBrowserAddress, WorkspaceBrowserEgress } from "../src/main/workspace-browser-egress.js";
import { installWorkspaceBrowserIpc } from "../src/main/workspace-browser-ipc.js";

describe("工作台网页边界", () => {
  it.each(["127.0.0.1", "0.0.0.0", "10.2.3.4", "100.64.0.1", "172.16.0.1", "169.254.169.254", "192.168.1.1", "192.0.0.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "255.255.255.255", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "2002:7f00:1::", "3fff::1"])("拒绝非公网地址 %s", address => {
    expect(isPublicBrowserAddress(address)).toBe(false);
  });
  it.each(["8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888"])("接受公网地址 %s", address => {
    expect(isPublicBrowserAddress(address)).toBe(true);
  });
  it.each(["file:///tmp/a", "app://gogosling/api/v1", "javascript:alert(1)", "data:text/html,a", "https://localhost/", "http://127.1/", "http://2130706433/", "http://0x7f000001/", "https://[::1]/", "https://printer.local/", "https://local.internal/", "https://host/", "https://user:pass@example.com/", "https://example.com:8443/", "https://example.com/\n"])("拒绝不允许的地址 %s", url => {
    expect(browserURL(url)).toBeNull();
  });
  it("标准端口规范化且只保留窗口内有限布局", () => {
    expect(browserURL("https://example.com:443/docs")?.href).toBe("https://example.com/docs");
    expect(browserTabId("web_1-a")).toBe(true);
    expect(browserTabId("web a")).toBe(false);
    expect(browserBounds({ x: 800, y: 100, width: 1000, height: 900 }, { width: 1200, height: 800 })).toEqual({ x: 800, y: 100, width: 400, height: 700 });
    expect(browserBounds({ x: 0, y: 0, width: 400, height: 600 }, { width: 1200, height: 800 })?.y).toBe(40);
    expect(browserBounds({ x: NaN, y: 0, width: 20, height: 20 }, { width: 1200, height: 800 })).toBeNull();
  });
  it("每次解析都验证全部地址，不复用旧解析或接受混合私网地址", async () => {
    const resolve = vi.fn().mockResolvedValueOnce([{ address: "8.8.8.8", family: 4 }]).mockResolvedValueOnce([{ address: "127.0.0.1", family: 4 }]);
    expect(await resolveBrowserAddress("example.com", resolve)).toEqual({ address: "8.8.8.8", family: 4 });
    await expect(resolveBrowserAddress("example.com", resolve)).rejects.toThrow("blocked_address");
    await expect(resolveBrowserAddress("example.com", async () => [{ address: "8.8.8.8", family: 4 }, { address: "10.0.0.1", family: 4 }])).rejects.toThrow("blocked_address");
  });
  it("私网 HTTP 和 CONNECT 被真实出口拒绝", async () => {
    const proxy = new WorkspaceBrowserEgress(async () => [{ address: "127.0.0.1", family: 4 }]);
    const endpoint = new URL(await proxy.start());
    try {
      const status = await new Promise<number>(resolve => {
        http.get({ host: endpoint.hostname, port: endpoint.port, path: "http://example.com/" }, response => { response.resume(); resolve(response.statusCode!); });
      });
      expect(status).toBe(403);
      const response = await new Promise<string>((resolve, reject) => {
        const client = net.connect(Number(endpoint.port), endpoint.hostname);
        client.on("error", reject);
        client.once("connect", () => client.write("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"));
        client.once("data", data => { resolve(data.toString()); client.destroy(); });
      });
      expect(response).toContain("403 Forbidden");
    } finally { proxy.dispose(); }
  });
  it("取消关闭真实连接且延迟解析不会恢复出口", async () => {
    let complete!: (value: { address: string; family: number }[]) => void;
    let started!: () => void;
    const resolving = new Promise<void>(resolve => { started = resolve; });
    const proxy = new WorkspaceBrowserEgress(() => { started(); return new Promise(resolve => { complete = resolve; }); });
    const endpoint = new URL(await proxy.start());
    const client = net.connect(Number(endpoint.port), endpoint.hostname);
    const closed = new Promise<void>(resolve => client.once("close", () => resolve()));
    client.once("connect", () => client.write("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"));
    await resolving;
    proxy.dispose();
    complete([{ address: "8.8.8.8", family: 4 }]);
    await closed;
    await expect(proxy.start()).rejects.toThrow("disposed");
  });
  it("出口启动过程中关闭也不会遗留监听端口", async () => {
    const proxy = new WorkspaceBrowserEgress();
    const starting = proxy.start();
    proxy.dispose();
    await expect(starting).rejects.toThrow("disposed");
  });
  it("请求取消或重建出口后未完成 DNS 仍占用解析槽位", async () => {
    const pending: ((value: { address: string; family: number }[]) => void)[] = [];
    let allStarted!: () => void;
    const started = new Promise<void>(resolve => { allStarted = resolve; });
    const resolver = vi.fn(() => {
      if (pending.length === 32) return Promise.resolve([{ address: "127.0.0.1", family: 4 }]);
      return new Promise<{ address: string; family: number }[]>(resolve => { pending.push(resolve); if (pending.length === 32) allStarted(); });
    });
    const resolutionBudget = { active: 0 };
    let proxy = new WorkspaceBrowserEgress(resolver, resolutionBudget);
    let endpoint = new URL(await proxy.start());
    const clients: http.ClientRequest[] = [];
    const request = () => new Promise<number>((resolve, reject) => {
      const client = http.get({ host: endpoint.hostname, port: endpoint.port, path: "http://example.com/", agent: false }, response => { response.resume(); resolve(response.statusCode!); });
      client.on("error", reject);
    });
    try {
      for (let i = 0; i < 32; ++i) {
        const client = http.get({ host: endpoint.hostname, port: endpoint.port, path: `http://example${i}.com/`, agent: false });
        client.on("error", () => {});
        clients.push(client);
      }
      await started;
      await Promise.all(clients.map(client => new Promise<void>(resolve => { client.once("close", resolve); client.destroy(); })));
      await new Promise(resolve => setTimeout(resolve, 20));
      expect(await request()).toBe(403);
      expect(resolver).toHaveBeenCalledTimes(32);
      proxy.dispose();
      proxy = new WorkspaceBrowserEgress(resolver, resolutionBudget);
      endpoint = new URL(await proxy.start());
      expect(await request()).toBe(403);
      expect(resolver).toHaveBeenCalledTimes(32);
      for (const complete of pending) complete([]);
      await new Promise(resolve => setImmediate(resolve));
      expect(await request()).toBe(403);
      expect(resolver).toHaveBeenCalledTimes(33);
    } finally { proxy.dispose(); for (const complete of pending) complete([]); for (const client of clients) client.destroy(); }
  });
  it("每个 IPC 通道都先拒绝远程来宾", () => {
    const handlers = new Map<string, (event: boolean, payload?: unknown) => unknown>();
    const browser = { show: vi.fn(), hide: vi.fn(), close: vi.fn(), navigate: vi.fn(), action: vi.fn(), current: vi.fn() };
    installWorkspaceBrowserIpc({ browser, handle: (channel, handler) => handlers.set(channel, handler), assertTrusted: event => { if (!event) throw new Error("untrusted"); } });
    expect(handlers.size).toBe(6);
    for (const handler of handlers.values()) expect(() => handler(false, {})).toThrow("untrusted");
    for (const handler of Object.values(browser)) expect(handler).not.toHaveBeenCalled();
  });
});
