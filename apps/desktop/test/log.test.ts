import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { createFileLogger, formatLine, isSensitiveKey, redactFields } from "../src/main/log.js";

describe("isSensitiveKey", () => {
  it.each(["token", "access_token", "accessToken", "Authorization", "api_key", "apiKey", "private_key", "password", "client_secret", "cookie", "credentials"])(
    "%s 是敏感键",
    (key) => {
      expect(isSensitiveKey(key)).toBe(true);
    },
  );

  it.each(["input_tokens", "tokens", "key", "name", "endpoint", "pid", "keyboard", "tokenizer"])("%s 不是敏感键", (key) => {
    expect(isSensitiveKey(key)).toBe(false);
  });
});

describe("redactFields / formatLine", () => {
  it("递归脱敏字符串值，保留数值与非敏感字段", () => {
    const out = redactFields({
      token: "abc",
      conn: { endpoint: "http://127.0.0.1:1", token: "xyz" },
      list: [{ api_key: "k" }],
      input_tokens: 12,
      retries: 3,
    });
    expect(out).toEqual({
      token: "[redacted]",
      conn: { endpoint: "http://127.0.0.1:1", token: "[redacted]" },
      list: [{ api_key: "[redacted]" }],
      input_tokens: 12,
      retries: 3,
    });
  });

  it("Error 转为 name/message；BigInt 转字符串", () => {
    expect(redactFields({ err: new TypeError("boom"), big: 10n })).toEqual({ err: { name: "TypeError", message: "boom" }, big: "10" });
  });

  it("一行日志的格式", () => {
    const at = new Date("2026-10-10T01:02:03.000Z");
    expect(formatLine("info", "hello", undefined, at)).toBe("2026-10-10T01:02:03.000Z INFO  hello\n");
    expect(formatLine("warn", "x", { token: "t", n: 1 }, at)).toBe('2026-10-10T01:02:03.000Z WARN  x {"token":"[redacted]","n":1}\n');
  });

  it("循环引用的字段被深度截断，不会让日志抛错", () => {
    const circular: Record<string, unknown> = {};
    circular.self = circular;
    let line = "";
    expect(() => {
      line = formatLine("error", "x", circular, new Date(0));
    }).not.toThrow();
    expect(line).toContain("[truncated]");
  });

  it("JSON 无法表达的值不会让日志抛错", () => {
    const line = formatLine("error", "x", { bad: { toJSON: () => { throw new Error("nope"); } } }, new Date(0));
    expect(line).toContain("[unserializable]");
  });
});

describe("createFileLogger", () => {
  const dirs: string[] = [];
  afterEach(() => {
    for (const d of dirs.splice(0)) fs.rmSync(d, { recursive: true, force: true });
  });

  it("自动创建目录、追加写入，并过滤低于 minLevel 的日志", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "gogosling-desktop-log-"));
    dirs.push(dir);
    const file = path.join(dir, "nested", "desktop.log");
    const logger = createFileLogger(file, { now: () => new Date("2026-01-01T00:00:00.000Z"), minLevel: "info" });

    logger.debug("skip me");
    logger.info("hello", { token: "secret", pid: 1 });
    logger.error("bad", { err: new Error("x") });

    const content = fs.readFileSync(file, "utf8");
    expect(content).not.toContain("skip me");
    expect(content).not.toContain("secret");
    expect(content).toContain('INFO  hello {"token":"[redacted]","pid":1}');
    expect(content).toContain('ERROR bad {"err":{"name":"Error","message":"x"}}');
    if (process.platform !== "win32") {
      expect(fs.statSync(file).mode & 0o777).toBe(0o600);
      expect(fs.statSync(path.dirname(file)).mode & 0o777).toBe(0o700);
    }
    expect(logger.path).toBe(file);
  });
});
