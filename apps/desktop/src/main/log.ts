// 壳的日志：写到数据目录 logs/desktop.log（0600），开发态同时镜像到控制台。
//
// 与内核 internal/observability 一样按键名脱敏：含 token / secret / password 等的字段值
// 一律替换为 [redacted]，所以哪怕有人把 ConnectionInfo 整个传进来也不会把令牌写进文件。

import * as fs from "node:fs";
import * as path from "node:path";

export type LogLevel = "debug" | "info" | "warn" | "error";

export type LogFields = Record<string, unknown>;

/** 由组合根创建并注入；模块内不持有全局 logger。 */
export interface Logger {
  debug(message: string, fields?: LogFields): void;
  info(message: string, fields?: LogFields): void;
  warn(message: string, fields?: LogFields): void;
  error(message: string, fields?: LogFields): void;
}

const LEVEL_RANK: Record<LogLevel, number> = { debug: 10, info: 20, warn: 30, error: 40 };

const SENSITIVE_SEGMENTS = new Set(["password", "passwd", "secret", "authorization", "credential", "credentials", "cookie", "apikey", "token"]);

/**
 * 键名是否敏感：按非字母数字切分成片段，任一片段命中敏感词，或出现 api+key / private+key 组合。
 * "input_tokens" 的片段是 "tokens"，不会被误伤。
 */
export function isSensitiveKey(key: string): boolean {
  // 先把 camelCase 拆开（accessToken → access_Token），再按非字母数字切分。
  const segments = key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);
  for (let i = 0; i < segments.length; i++) {
    const s = segments[i]!;
    if (SENSITIVE_SEGMENTS.has(s)) return true;
    if (s === "key" && i > 0 && (segments[i - 1] === "api" || segments[i - 1] === "private")) return true;
  }
  return false;
}

const MAX_DEPTH = 6;

/** 递归脱敏；Error 转为 {name, message}；深度超过上限时截断。 */
export function redactFields(fields: LogFields): LogFields {
  return redactValue(fields, 0) as LogFields;
}

function redactValue(value: unknown, depth: number): unknown {
  if (value instanceof Error) return { name: value.name, message: value.message };
  if (Array.isArray(value)) return depth >= MAX_DEPTH ? "[truncated]" : value.map((v) => redactValue(v, depth + 1));
  if (value && typeof value === "object") {
    if (depth >= MAX_DEPTH) return "[truncated]";
    const out: LogFields = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      out[k] = isSensitiveKey(k) && typeof v === "string" ? "[redacted]" : redactValue(v, depth + 1);
    }
    return out;
  }
  if (typeof value === "bigint") return value.toString();
  return value;
}

/** 单行日志格式：时间 级别 消息 JSON字段。纯函数，便于测试。 */
export function formatLine(level: LogLevel, message: string, fields: LogFields | undefined, at: Date): string {
  let suffix = "";
  if (fields && Object.keys(fields).length > 0) {
    try {
      suffix = " " + JSON.stringify(redactFields(fields));
    } catch {
      suffix = " [unserializable]";
    }
  }
  return `${at.toISOString()} ${level.toUpperCase().padEnd(5)} ${message}${suffix}\n`;
}

export interface FileLoggerOptions {
  /** 同时输出到 stderr（开发态）。 */
  mirrorToConsole?: boolean;
  /** 低于此级别的日志丢弃；默认 info。 */
  minLevel?: LogLevel;
  now?: () => Date;
}

export interface FileLogger extends Logger {
  readonly path: string;
}

/**
 * 创建写文件的 logger。目录在首次写入时创建（0700），文件 0600。
 * 写文件失败只在控制台提示一次，不影响壳运行。
 */
export function createFileLogger(filePath: string, opts: FileLoggerOptions = {}): FileLogger {
  const minRank = LEVEL_RANK[opts.minLevel ?? "info"];
  const now = opts.now ?? (() => new Date());
  let dirReady = false;
  let writeFailed = false;

  const write = (level: LogLevel, message: string, fields?: LogFields) => {
    if (LEVEL_RANK[level] < minRank) return;
    const line = formatLine(level, message, fields, now());
    if (opts.mirrorToConsole) process.stderr.write(line);
    if (writeFailed) return;
    try {
      if (!dirReady) {
        fs.mkdirSync(path.dirname(filePath), { recursive: true, mode: 0o700 });
        dirReady = true;
      }
      fs.appendFileSync(filePath, line, { mode: 0o600 });
    } catch (err) {
      writeFailed = true;
      process.stderr.write(formatLine("error", "写入日志文件失败，后续日志只输出到控制台", { path: filePath, err }, now()));
    }
  };

  return {
    path: filePath,
    debug: (m, f) => write("debug", m, f),
    info: (m, f) => write("info", m, f),
    warn: (m, f) => write("warn", m, f),
    error: (m, f) => write("error", m, f),
  };
}
