import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { APP_ORIGIN, contentTypeFor, fallbackToIndex, resolveAppFile, resolveWebRoot } from "../src/main/app-protocol.js";

const ROOT = path.resolve("/srv/web");

describe("resolveAppFile", () => {
  it("根路径与目录路径映射到 index.html", () => {
    expect(resolveAppFile("app://gogosling/", ROOT)).toBe(path.join(ROOT, "index.html"));
    expect(resolveAppFile("app://gogosling", ROOT)).toBe(path.join(ROOT, "index.html"));
    expect(resolveAppFile("app://gogosling/assets/", ROOT)).toBe(path.join(ROOT, "assets", "index.html"));
  });

  it("普通资源映射到 webRoot 下的文件；查询串与哈希被忽略", () => {
    expect(resolveAppFile("app://gogosling/assets/index-abc.js?v=1#x", ROOT)).toBe(path.join(ROOT, "assets", "index-abc.js"));
    expect(resolveAppFile("app://gogosling/index.html#/chat", ROOT)).toBe(path.join(ROOT, "index.html"));
  });

  it("百分号编码会被解码", () => {
    expect(resolveAppFile("app://gogosling/a%20b.txt", ROOT)).toBe(path.join(ROOT, "a b.txt"));
  });

  it.each([
    ["混在路径中的 ..（%2f 不会被解析器当作分隔符）", "app://gogosling/assets/..%2f..%2fsecret"],
    ["NUL 字节", "app://gogosling/a%00.js"],
    ["反斜杠", "app://gogosling/a%5Cb.js"],
    ["驱动器前缀", "app://gogosling/C:/x"],
    ["非法百分号编码", "app://gogosling/%zz"],
    ["错误的主机", "app://other/index.html"],
    ["错误的协议", "http://gogosling/index.html"],
    ["不是 URL", "not a url"],
  ])("拒绝：%s", (_name, url) => {
    expect(resolveAppFile(url, ROOT)).toBeNull();
  });

  it("URL 解析器已归一化的 .. / %2e%2e 不会逃出根目录", () => {
    // WHATWG 解析器把 ".." 与 "%2e%2e" 段在根目录处截断，最终路径仍在 ROOT 内。
    expect(resolveAppFile("app://gogosling/a/../../b.js", ROOT)).toBe(path.join(ROOT, "b.js"));
    expect(resolveAppFile("app://gogosling/%2e%2e/%2e%2e/etc/passwd", ROOT)).toBe(path.join(ROOT, "etc", "passwd"));
    expect(resolveAppFile("app://gogosling/%2e/x", ROOT)).toBe(path.join(ROOT, "x"));
  });
});

describe("contentTypeFor / fallbackToIndex", () => {
  it("常见类型", () => {
    expect(contentTypeFor("/x/index.html")).toBe("text/html; charset=utf-8");
    expect(contentTypeFor("/x/app.JS")).toBe("text/javascript; charset=utf-8");
    expect(contentTypeFor("/x/style.css")).toBe("text/css; charset=utf-8");
    expect(contentTypeFor("/x/logo.svg")).toBe("image/svg+xml");
    expect(contentTypeFor("/x/font.woff2")).toBe("font/woff2");
  });

  it("未知类型用 octet-stream", () => {
    expect(contentTypeFor("/x/file.unknownext")).toBe("application/octet-stream");
    expect(contentTypeFor("/x/noext")).toBe("application/octet-stream");
  });

  it("没有扩展名的路径才回退到 index.html", () => {
    expect(fallbackToIndex("/x/chat")).toBe(true);
    expect(fallbackToIndex("/x/app.js")).toBe(false);
  });
});

describe("resolveWebRoot / APP_ORIGIN", () => {
  it("打包后使用 resources/web", () => {
    expect(resolveWebRoot({ isPackaged: true, resourcesPath: "/App/resources", appPath: "/App/resources/app.asar" })).toBe(path.join("/App/resources", "web"));
  });

  it("开发态使用仓库里的 apps/web/dist", () => {
    expect(resolveWebRoot({ isPackaged: false, resourcesPath: "/x", appPath: "/repo/apps/desktop" })).toBe(path.resolve("/repo/apps/web/dist"));
  });

  it("origin 与 API 契约允许的值一致", () => {
    expect(APP_ORIGIN).toBe("app://gogosling");
  });
});
