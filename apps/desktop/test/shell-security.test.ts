import { describe, expect, it } from "vitest";
import {
  describeDeepLink,
  findDeepLink,
  isTrustedPageUrl,
  MAX_EXTERNAL_URL_LENGTH,
  parseDevWebUrl,
  sanitizeExternalUrl,
  shouldStartHidden,
} from "../src/main/shell-security.js";

describe("isTrustedPageUrl", () => {
  it("只信任 app://gogosling 与配置的开发服务器", () => {
    expect(isTrustedPageUrl("app://gogosling/", { devOrigin: null })).toBe(true);
    expect(isTrustedPageUrl("app://gogosling/index.html#/chat", { devOrigin: null })).toBe(true);
    expect(isTrustedPageUrl("app://evil/", { devOrigin: null })).toBe(false);
    expect(isTrustedPageUrl("app://gogosling.evil/", { devOrigin: null })).toBe(false);
    expect(isTrustedPageUrl("http://127.0.0.1:5173/", { devOrigin: null })).toBe(false);
    expect(isTrustedPageUrl("http://127.0.0.1:5173/", { devOrigin: "http://127.0.0.1:5173" })).toBe(true);
    expect(isTrustedPageUrl("http://127.0.0.1:5174/", { devOrigin: "http://127.0.0.1:5173" })).toBe(false);
    expect(isTrustedPageUrl("about:blank", { devOrigin: null })).toBe(false);
    expect(isTrustedPageUrl("", { devOrigin: null })).toBe(false);
  });
});

describe("sanitizeExternalUrl", () => {
  it("接受 http / https / mailto", () => {
    expect(sanitizeExternalUrl("https://example.com/docs?x=1")).toBe("https://example.com/docs?x=1");
    expect(sanitizeExternalUrl("  http://example.com ")).toBe("http://example.com/");
    expect(sanitizeExternalUrl("mailto:someone@example.com")).toBe("mailto:someone@example.com");
  });

  it.each([
    ["javascript", "javascript:alert(1)"],
    ["file", "file:///etc/passwd"],
    ["自定义协议", "gogosling://open"],
    ["data", "data:text/html,hi"],
    ["空 mailto", "mailto:"],
    ["带用户信息", "https://user:pass@example.com/"],
    ["没有主机", "http://"],
    ["空串", ""],
    ["NUL", "https://example.com/\0"],
  ])("拒绝：%s", (_name, raw) => {
    expect(sanitizeExternalUrl(raw)).toBeNull();
  });

  it("拒绝非字符串与超长 URL", () => {
    expect(sanitizeExternalUrl(42)).toBeNull();
    expect(sanitizeExternalUrl(null)).toBeNull();
    expect(sanitizeExternalUrl({ toString: () => "https://example.com" })).toBeNull();
    expect(sanitizeExternalUrl("https://example.com/" + "a".repeat(MAX_EXTERNAL_URL_LENGTH))).toBeNull();
  });
});

describe("findDeepLink / describeDeepLink", () => {
  it("从 argv 中找出第一个 gogosling:// 链接（不区分大小写）", () => {
    expect(findDeepLink(["/usr/bin/app", "--hidden", "GOGOSLING://open/session?id=1"])).toBe("gogosling://open/session?id=1");
    expect(findDeepLink(["/usr/bin/app"])).toBeNull();
    expect(findDeepLink([])).toBeNull();
  });

  it("可记录形式不含查询串", () => {
    expect(describeDeepLink("gogosling://open/session?token=secret#x")).toBe("gogosling://open/session");
    expect(describeDeepLink("::")).toBe("gogosling://<invalid>");
  });
});

describe("parseDevWebUrl", () => {
  it("只接受回环地址的 http(s)", () => {
    expect(parseDevWebUrl("http://127.0.0.1:5173")).toEqual({ url: "http://127.0.0.1:5173/", origin: "http://127.0.0.1:5173" });
    expect(parseDevWebUrl("http://localhost:5173/")).toEqual({ url: "http://localhost:5173/", origin: "http://localhost:5173" });
    expect(parseDevWebUrl("http://10.0.0.8:5173")).toBeNull();
    expect(parseDevWebUrl("ftp://127.0.0.1")).toBeNull();
    expect(parseDevWebUrl("")).toBeNull();
    expect(parseDevWebUrl(undefined)).toBeNull();
  });
});

describe("shouldStartHidden", () => {
  it("识别 --hidden", () => {
    expect(shouldStartHidden(["app", "--hidden"])).toBe(true);
    expect(shouldStartHidden(["app"])).toBe(false);
  });
});
