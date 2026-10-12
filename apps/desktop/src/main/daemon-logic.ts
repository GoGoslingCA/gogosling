// 守护进程管理的纯逻辑：数据目录、可执行文件定位、句柄文件解析、退避与重启决策。
//
// 本文件不触碰文件系统、网络与 electron，所有分支都能在 vitest 中直接覆盖；
// 真正的 I/O 在 daemon.ts 里通过注入的依赖完成。
//
// 启动前通过守护进程写出的 daemon.json（pid + endpoint + token）探测已有实例。

import * as path from "node:path";

/** 守护进程可执行文件的基础名（Windows 追加 .exe）。 */
export const DAEMON_BASENAME = "gogoslingd";

/** 覆盖可执行文件位置的环境变量。 */
export const ENV_DAEMON_PATH = "GOGOSLING_DAEMON_PATH";

/** 覆盖数据目录的环境变量（与 internal/config.EnvHome 一致）。 */
export const ENV_HOME = "GOGOSLING_HOME";

/** 资源根目录的环境变量（与 internal/app.EnvResources 一致；其下有 extensions/bundled）。 */
export const ENV_RESOURCES = "GOGOSLING_RESOURCES";

/** 运行时句柄文件名（与 internal/identity.HandleFileName 一致）。 */
export const HANDLE_FILE_NAME = "daemon.json";

/** 守护进程 stdout/stderr 的捕获文件（位于数据目录 logs/ 下）。 */
export const DAEMON_STDIO_LOG_NAME = "gogoslingd-stdio.log";

/** 按平台返回可执行文件名。 */
export function daemonFileName(platform: NodeJS.Platform): string {
  return platform === "win32" ? `${DAEMON_BASENAME}.exe` : DAEMON_BASENAME;
}

/**
 * 解析数据目录：GOGOSLING_HOME（转为绝对路径）优先，否则 ~/.gogosling。
 * 与 internal/config.resolveDataDir 的规则一致，壳与守护进程看到同一个目录。
 */
export function resolveDataDir(env: NodeJS.ProcessEnv, homeDir: string): string {
  const override = env[ENV_HOME]?.trim();
  if (override) return path.resolve(override);
  return path.join(homeDir, ".gogosling");
}

/** 句柄文件的完整路径。 */
export function handleFilePath(dataDir: string): string {
  return path.join(dataDir, HANDLE_FILE_NAME);
}

/** 守护进程 stdio 捕获文件的完整路径。 */
export function daemonStdioLogPath(dataDir: string): string {
  return path.join(dataDir, "logs", DAEMON_STDIO_LOG_NAME);
}

/** 候选可执行文件的来源，按优先级排序。 */
export type CandidateSource = "env" | "resources" | "path";

export interface ExecutableCandidate {
  source: CandidateSource;
  file: string;
}

export interface LocateInput {
  env: NodeJS.ProcessEnv;
  /** Electron 的 process.resourcesPath（打包后为 <App>/resources）。 */
  resourcesPath: string;
  platform: NodeJS.Platform;
}

/**
 * 按优先级列出候选路径：GOGOSLING_DAEMON_PATH > <resources>/bin/gogoslingd[.exe] > PATH 各目录。
 * 不检查文件是否存在；去重后交给 locateDaemonExecutable 逐个探测。
 */
export function daemonExecutableCandidates(input: LocateInput): ExecutableCandidate[] {
  const p = input.platform === "win32" ? path.win32 : path.posix;
  const fileName = daemonFileName(input.platform);
  const out: ExecutableCandidate[] = [];
  const seen = new Set<string>();
  const push = (source: CandidateSource, file: string) => {
    if (seen.has(file)) return;
    seen.add(file);
    out.push({ source, file });
  };

  const fromEnv = input.env[ENV_DAEMON_PATH]?.trim();
  if (fromEnv) push("env", p.resolve(fromEnv));

  push("resources", p.join(input.resourcesPath, "bin", fileName));

  // Windows 的环境变量名不区分大小写，但 Node 在非 Windows 平台上不会归一化。
  const pathVar = input.env.PATH ?? input.env.Path ?? "";
  for (const raw of pathVar.split(p.delimiter)) {
    const dir = raw.trim().replace(/^"(.*)"$/, "$1");
    if (!dir) continue;
    push("path", p.join(dir, fileName));
  }
  return out;
}

/** 返回第一个通过 isExecutable 探测的候选；全部失败返回 null。 */
export async function locateDaemonExecutable(
  candidates: readonly ExecutableCandidate[],
  isExecutable: (file: string) => Promise<boolean>,
): Promise<ExecutableCandidate | null> {
  for (const candidate of candidates) {
    if (await isExecutable(candidate.file)) return candidate;
  }
  return null;
}

/**
 * 安装包里的资源根目录：<resources>/share/gogosling（electron-builder.yml 把 extensions/bundled 打到这里）。
 * 与 internal/app/resources.go 的安装布局 <exe>/../share/gogosling 是同一个目录（exe 位于 <resources>/bin）。
 */
export function bundledResourcesDir(resourcesPath: string, platform: NodeJS.Platform): string {
  const p = platform === "win32" ? path.win32 : path.posix;
  return p.join(resourcesPath, "share", "gogosling");
}

export interface SpawnEnvInput {
  env: NodeJS.ProcessEnv;
  dataDir: string;
  resourcesPath: string;
  platform: NodeJS.Platform;
  /** 可执行文件来自哪里；只有随包分发的二进制（resources）才指向包内资源目录。 */
  source: CandidateSource;
}

/**
 * 守护进程的环境变量：继承壳的环境，设置 GOGOSLING_HOME（壳与守护进程看同一个数据目录）；
 * 二进制来自安装包时再设置 GOGOSLING_RESOURCES，让内核找到内置扩展。
 *
 * 用户自己设置了 GOGOSLING_RESOURCES 时不覆盖；GOGOSLING_DAEMON_PATH / PATH 找到的二进制不设置——
 * 内核对显式但不存在的目录不会再回退到自己的查找顺序（resources.go locate），乱设反而会丢掉内置扩展。
 */
export function daemonSpawnEnv(input: SpawnEnvInput): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...input.env, [ENV_HOME]: input.dataDir };
  if (input.source === "resources" && !input.env[ENV_RESOURCES]?.trim()) {
    env[ENV_RESOURCES] = bundledResourcesDir(input.resourcesPath, input.platform);
  }
  return env;
}

/** daemon.json 的解析结果（字段名与 internal/identity.Handle 对应）。 */
export interface DaemonHandle {
  pid: number;
  /** 已归一化：无尾部斜杠、无查询串与片段。 */
  endpoint: string;
  token: string;
  version: string;
  startedAt: Date | null;
}

export type HandleParseFailure =
  | "not_json"
  | "not_object"
  | "invalid_pid"
  | "invalid_endpoint"
  | "endpoint_not_loopback"
  | "missing_token";

export type ParsedHandle = { ok: true; handle: DaemonHandle } | { ok: false; reason: HandleParseFailure };

/**
 * 解析并校验句柄文件内容。
 *
 * 除了字段完整性，还要求 endpoint 是回环地址：句柄文件若被篡改成远程地址，
 * 未校验回环地址就连接会导致令牌被发给远端。
 */
export function parseHandle(text: string): ParsedHandle {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { ok: false, reason: "not_json" };
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return { ok: false, reason: "not_object" };
  const obj = raw as Record<string, unknown>;

  const pid = obj.pid;
  if (typeof pid !== "number" || !Number.isInteger(pid) || pid <= 0) return { ok: false, reason: "invalid_pid" };

  const endpoint = typeof obj.endpoint === "string" ? normalizeEndpoint(obj.endpoint) : null;
  if (!endpoint) return { ok: false, reason: "invalid_endpoint" };
  if (!isLoopbackEndpoint(endpoint)) return { ok: false, reason: "endpoint_not_loopback" };

  const token = typeof obj.token === "string" ? obj.token.trim() : "";
  if (!token) return { ok: false, reason: "missing_token" };

  const version = typeof obj.version === "string" ? obj.version : "";
  let startedAt: Date | null = null;
  if (typeof obj.started_at === "string") {
    const d = new Date(obj.started_at);
    startedAt = Number.isNaN(d.getTime()) ? null : d;
  }
  return { ok: true, handle: { pid, endpoint, token, version, startedAt } };
}

/**
 * 归一化端点：只接受 http/https、不含用户信息/查询串/片段，去掉尾部斜杠。
 * 不合法返回 null。
 */
export function normalizeEndpoint(raw: string): string | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    return null;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return null;
  if (url.username || url.password || url.search || url.hash) return null;
  const pathname = url.pathname.replace(/\/+$/, "");
  return `${url.origin}${pathname}`;
}

/** 端点主机是否为回环地址（127.0.0.1 / localhost / [::1]）。 */
export function isLoopbackEndpoint(endpoint: string): boolean {
  let url: URL;
  try {
    url = new URL(endpoint);
  } catch {
    return false;
  }
  const host = url.hostname.toLowerCase();
  return host === "127.0.0.1" || host === "localhost" || host === "[::1]";
}

/** 免鉴权的存活检查地址。 */
export function healthUrl(endpoint: string): string {
  return `${endpoint}/healthz`;
}

/** 需要令牌的版本接口，用来验证句柄里的令牌仍被守护进程接受。 */
export function versionUrl(endpoint: string): string {
  return `${endpoint}/api/v1/version`;
}

/** 需要令牌的关闭接口：POST 返回 202 后守护进程开始优雅关闭（契约 /api/v1/system/shutdown）。 */
export function shutdownUrl(endpoint: string): string {
  return `${endpoint}/api/v1/system/shutdown`;
}

/** 从 GET /api/v1/version 的响应体（VersionInfo）中取 started_at；缺失或不是 JSON 对象时返回 null。 */
export function parseVersionStartedAt(body: string | undefined): string | null {
  if (!body) return null;
  let raw: unknown;
  try {
    raw = JSON.parse(body);
  } catch {
    return null;
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const value = (raw as Record<string, unknown>).started_at;
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

/**
 * /version 报告的 started_at（RFC 3339，秒精度）与句柄里的 started_at 是否指同一次启动。
 * 两边都截到秒再比较（与 CLI 的 matchStartedAt 一致）；任一方缺失或无法解析都判为不一致——
 * 无法证明身份时宁可不信任句柄。
 */
export function startedAtMatches(reported: string | null, handleStartedAt: Date | null): boolean {
  if (!reported || !handleStartedAt) return false;
  const t = Date.parse(reported);
  const h = handleStartedAt.getTime();
  if (Number.isNaN(t) || Number.isNaN(h)) return false;
  return Math.floor(t / 1000) === Math.floor(h / 1000);
}

export interface BackoffOptions {
  /** 第 1 次重试前的等待。 */
  baseMs: number;
  /** 等待上限。 */
  maxMs: number;
  /** 抖动比例 [0, 1]：实际等待在 [exp*(1-jitter), exp*(1+jitter)] 内，再受 maxMs 约束。 */
  jitter: number;
}

/**
 * 指数退避：attempt 从 1 开始，等待 = base * 2^(attempt-1)，带抖动、封顶。
 * random 可注入以便测试确定性。
 */
export function backoffDelay(attempt: number, opts: BackoffOptions, random: () => number = Math.random): number {
  const n = Math.max(1, Math.floor(attempt));
  const exp = Math.min(opts.maxMs, opts.baseMs * 2 ** (n - 1));
  const jitter = Math.min(Math.max(opts.jitter, 0), 1);
  if (jitter === 0) return Math.round(exp);
  const spread = exp * jitter;
  const value = exp - spread + random() * 2 * spread;
  return Math.round(Math.min(opts.maxMs, Math.max(0, value)));
}

/** 崩溃后的重启策略。 */
export interface RestartPolicy extends BackoffOptions {
  /** 连续重启上限；超过后放弃并提示用户。 */
  maxAttempts: number;
  /** 守护进程存活超过这么久再崩溃，视为新的故障而不是上一轮的延续：计数归零。 */
  stableUptimeMs: number;
}

export const DEFAULT_RESTART_POLICY: RestartPolicy = {
  maxAttempts: 5,
  baseMs: 500,
  maxMs: 15_000,
  jitter: 0.2,
  stableUptimeMs: 60_000,
};

export type RestartDecision = { kind: "restart"; attempt: number; delayMs: number } | { kind: "give_up"; attempt: number };

/**
 * 根据上一轮的尝试次数与本次存活时长决定是否重启。
 *
 * 稳定运行一段时间后的崩溃重新计数，避免"跑了三天崩一次"把几个月前的失败累加起来；
 * 连续快速崩溃则每次翻倍等待，到上限即放弃。
 */
export function nextRestart(
  previousAttempt: number,
  uptimeMs: number,
  policy: RestartPolicy = DEFAULT_RESTART_POLICY,
  random: () => number = Math.random,
): RestartDecision {
  const attempt = uptimeMs >= policy.stableUptimeMs ? 1 : previousAttempt + 1;
  if (attempt > policy.maxAttempts) return { kind: "give_up", attempt: previousAttempt };
  return { kind: "restart", attempt, delayMs: backoffDelay(attempt, policy, random) };
}

/** stdio 捕获文件超过上限时在下次启动前轮转一次（只保留一份旧文件）。 */
export function shouldRotateLog(sizeBytes: number, limitBytes: number): boolean {
  return limitBytes > 0 && sizeBytes >= limitBytes;
}
