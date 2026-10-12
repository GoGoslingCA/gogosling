import { describe, expect, it } from "vitest";
import { buildCsp } from "../src/main/csp.js";

function directives(csp: string): Map<string, string[]> {
  const map = new Map<string, string[]>();
  for (const part of csp.split(";")) {
    const [name, ...values] = part.trim().split(/\s+/);
    if (name) map.set(name, values);
  }
  return map;
}

describe("buildCsp", () => {
  it("生产态：脚本与连接都只允许同源（API 经 app://gogosling/api 代理），禁止嵌入与表单", () => {
    const d = directives(buildCsp({ devOrigin: null }));
    expect(d.get("default-src")).toEqual(["'self'"]);
    expect(d.get("script-src")).toEqual(["'self'"]);
    expect(d.get("connect-src")).toEqual(["'self'"]);
    expect(d.get("style-src")).toEqual(["'self'", "'unsafe-inline'"]);
    expect(d.get("object-src")).toEqual(["'none'"]);
    expect(d.get("base-uri")).toEqual(["'none'"]);
    expect(d.get("form-action")).toEqual(["'none'"]);
    expect(d.get("frame-ancestors")).toEqual(["'none'"]);
    expect(d.get("frame-src")).toEqual(["'self'", "blob:"]);
    expect(d.get("media-src")).toEqual(["'self'", "blob:"]);
  });

  it("开发态：放开内联脚本、开发服务器的 http/ws，以及直连守护进程的回环地址", () => {
    const d = directives(buildCsp({ devOrigin: "http://127.0.0.1:5173" }));
    expect(d.get("script-src")).toEqual(["'self'", "'unsafe-inline'", "http://127.0.0.1:5173"]);
    expect(d.get("connect-src")).toContain("ws://127.0.0.1:5173");
    expect(d.get("connect-src")).toContain("http://127.0.0.1:5173");
    expect(d.get("connect-src")).toEqual(expect.arrayContaining(["http://127.0.0.1:*", "http://localhost:*", "http://[::1]:*"]));
  });

  it("生产态不含 unsafe-inline 脚本、ws 与任何回环地址", () => {
    const csp = buildCsp({ devOrigin: null });
    expect(csp).not.toMatch(/script-src[^;]*unsafe-inline/);
    expect(csp).not.toContain("ws://");
    expect(csp).not.toContain("127.0.0.1");
    expect(csp).not.toContain("localhost");
  });
});
