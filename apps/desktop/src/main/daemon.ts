// DaemonManager：发现或拉起 gogoslingd，并监管它的生命周期。
//
// 流程：
//   start()
//     ├─ 读 <dataDir>/daemon.json → GET /healthz → GET /api/v1/version（Bearer）
//     │     有活着的守护进程且令牌有效 → ready（owned=false），不再拉起；
//     │     活着但令牌被拒      → failed(stale_handle)：不拉起第二个实例去抢端口
//     ├─ 定位可执行文件（GOGOSLING_DAEMON_PATH > <resources>/bin > PATH）
//     └─ spawn（stdio → logs/gogoslingd-stdio.log；环境带 GOGOSLING_HOME，来自资源目录的二进制再带
//        GOGOSLING_RESOURCES=<resources>/share/gogosling 以找到内置扩展），轮询"句柄 pid == 子进程 pid 且 /healthz 200"直到就绪（默认 30s）
//   子进程退出（非主动停止）→ nextRestart 决策（指数退避、上限 5 次、稳定运行后重新计数）→ 回到 start 的流程
//   stop()（应用退出与托盘"重启守护进程"共用；restart = stop + start）：见 daemon-stop.ts
//     ├─ owned 子进程：确认 daemon.json 身份（GET /api/v1/version 200 且 started_at 一致、pid == 子进程）
//     │     → POST /api/v1/system/shutdown → 202 后等它自己退出（默认 30s）→ 超时强制结束；
//     │     端点不可用（连不上、非 202）或身份未确认 → POSIX SIGTERM 等 30s 再 SIGKILL，Windows 直接 taskkill /T /F；
//     │     退出后删除仍写着它 pid 的 daemon.json（被强制结束时守护进程来不及删）
//     └─ 不是本壳启动的：只确认身份——通过则断开连接、让它继续运行；失败则把 daemon.json 当残留删除，不发信号
//
// 所有 I/O 通过 DaemonDeps 注入，测试里用假实现驱动完整状态机。
// 令牌只保存在本对象内存里，通过 getConnection() 交给主进程的 API 代理（api-proxy.ts）；
// 不进日志、不进状态事件，打包后也不进渲染进程。

import { execFile, spawn } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import type { DaemonErrorCode, DaemonState, DaemonStatus } from "../shared/ipc.js";
import type { DaemonConnection } from "./api-proxy.js";
import {
  DEFAULT_RESTART_POLICY,
  daemonExecutableCandidates,
  daemonSpawnEnv,
  daemonStdioLogPath,
  handleFilePath,
  healthUrl,
  locateDaemonExecutable,
  nextRestart,
  parseHandle,
  shouldRotateLog,
  versionUrl,
  type DaemonHandle,
  type ExecutableCandidate,
  type HandleParseFailure,
  type RestartPolicy,
} from "./daemon-logic.js";
import { releaseForeignDaemon, stopOwnedDaemon, type ProbeRequest, type ProbeResponse, type StopIO, type StopTimings } from "./daemon-stop.js";
import type { Logger } from "./log.js";

export type { ProbeRequest, ProbeResponse } from "./daemon-stop.js";

/** spawn 返回的子进程所需的最小接口；node:child_process.ChildProcess 满足它。 */
export interface SpawnedProcess {
  /** spawn 失败（例如 ENOENT）时为 undefined，随后会收到 error 事件。 */
  readonly pid?: number | undefined;
  kill(signal?: NodeJS.Signals): boolean;
  on(event: "exit", listener: (code: number | null, signal: NodeJS.Signals | null) => void): unknown;
  on(event: "error", listener: (err: Error) => void): unknown;
}

export interface SpawnRequest {
  file: string;
  args: string[];
  cwd: string;
  env: NodeJS.ProcessEnv;
  /** stdout/stderr 写入的文件描述符；null 表示丢弃。 */
  stdioFd: number | null;
}

export interface OpenedFile {
  fd: number;
  close(): Promise<void>;
}

/** DaemonManager 需要的全部副作用。 */
export interface DaemonDeps {
  spawn(req: SpawnRequest): SpawnedProcess;
  /** GET（默认）或 POST；网络错误抛出，超时错误满足 isTimeoutError。readBody 时响应体有上限。 */
  fetch(url: string, init: ProbeRequest): Promise<ProbeResponse>;
  /** 读 UTF-8 文本；不存在时抛出。 */
  readFile(file: string): Promise<string>;
  isExecutable(file: string): Promise<boolean>;
  /** mkdir -p，目录 0700。 */
  ensureDir(dir: string): Promise<void>;
  /** 以追加方式打开（0600）。 */
  openAppend(file: string): Promise<OpenedFile>;
  /** 文件大小；不存在返回 0。 */
  fileSize(file: string): Promise<number>;
  rename(from: string, to: string): Promise<void>;
  /** 删除文件；不存在视为成功。 */
  removeFile(file: string): Promise<void>;
  /**
   * Windows 专用：taskkill /PID <pid> /T /F。不提供不带 /F 的变体：无窗口的控制台进程收不到
   * WM_CLOSE，那样的调用必然失败（XP-07）；温和关闭走 POST /api/v1/system/shutdown。
   */
  forceKillTree(pid: number): Promise<void>;
  /** 可被 signal 提前结束的等待（提前结束时正常 resolve）。 */
  sleep(ms: number, signal?: AbortSignal): Promise<void>;
  now(): number;
  random(): number;
}

export interface DaemonManagerOptions {
  dataDir: string;
  resourcesPath: string;
  platform: NodeJS.Platform;
  env: NodeJS.ProcessEnv;
  logger: Logger;
  /** 等待 /healthz 就绪的上限；默认 30s。 */
  readyTimeoutMs?: number;
  /** 就绪轮询间隔；默认 250ms。 */
  pollIntervalMs?: number;
  /** 单次 HTTP 探测超时；默认 1.5s。 */
  probeTimeoutMs?: number;
  /** 关闭请求被接受（202）后等待退出的上限；默认 30s（守护进程最坏约 20s+10s+5s）。 */
  shutdownTimeoutMs?: number;
  /** 回退到 SIGTERM 后等待退出的上限（强制结束后也最多再等这么久）；默认 30s。 */
  stopTimeoutMs?: number;
  restartPolicy?: RestartPolicy;
  /** stdio 捕获文件轮转阈值；默认 5 MiB。 */
  logSizeLimitBytes?: number;
}

type ExitInfo = { kind: "exit"; code: number | null; signal: NodeJS.Signals | null } | { kind: "error"; err: Error };

type AttachResult = { kind: "none" } | { kind: "attached"; handle: DaemonHandle } | { kind: "stale"; handle: DaemonHandle };

type ReadyResult = { ok: true; handle: DaemonHandle } | { ok: false; error: "exited" | "ready_timeout" };

type StatusListener = (status: DaemonStatus) => void;

/** 探测请求读取响应体的上限（/api/v1/version 只有几百字节）；超过时按空响应体处理。 */
const PROBE_BODY_LIMIT_BYTES = 64 * 1024;

async function readCappedText(res: Response, limit: number): Promise<string> {
  const reader = res.body?.getReader();
  if (!reader) return "";
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel().catch(() => undefined);
      return "";
    }
    chunks.push(value);
  }
  return Buffer.concat(chunks).toString("utf8");
}

/** 真实环境下的依赖实现。 */
export function defaultDaemonDeps(platform: NodeJS.Platform): DaemonDeps {
  return {
    spawn: (req) =>
      spawn(req.file, req.args, {
        cwd: req.cwd,
        env: req.env,
        stdio: req.stdioFd === null ? "ignore" : ["ignore", req.stdioFd, req.stdioFd],
        windowsHide: true,
        detached: false,
      }),
    fetch: async (url, init) => {
      const res = await fetch(url, {
        method: init.method ?? "GET",
        headers: init.headers,
        redirect: "manual",
        signal: AbortSignal.timeout(init.timeoutMs),
      });
      if (init.readBody) return { status: res.status, body: await readCappedText(res, PROBE_BODY_LIMIT_BYTES) };
      // 不需要响应体；取消以释放连接。
      await res.body?.cancel().catch(() => undefined);
      return { status: res.status };
    },
    readFile: (file) => fs.promises.readFile(file, "utf8"),
    isExecutable: async (file) => {
      try {
        const st = await fs.promises.stat(file);
        if (!st.isFile()) return false;
        if (platform === "win32") return true;
        await fs.promises.access(file, fs.constants.X_OK);
        return true;
      } catch {
        return false;
      }
    },
    ensureDir: async (dir) => {
      await fs.promises.mkdir(dir, { recursive: true, mode: 0o700 });
    },
    openAppend: async (file) => {
      const handle = await fs.promises.open(file, "a", 0o600);
      return { fd: handle.fd, close: () => handle.close() };
    },
    fileSize: async (file) => {
      try {
        return (await fs.promises.stat(file)).size;
      } catch {
        return 0;
      }
    },
    rename: (from, to) => fs.promises.rename(from, to),
    removeFile: (file) => fs.promises.rm(file, { force: true }),
    forceKillTree: (pid) =>
      new Promise((resolve) => {
        execFile("taskkill", ["/PID", String(pid), "/T", "/F"], { windowsHide: true }, () => resolve());
      }),
    sleep: (ms, signal) =>
      new Promise((resolve) => {
        if (signal?.aborted) {
          resolve();
          return;
        }
        const done = () => {
          clearTimeout(timer);
          signal?.removeEventListener("abort", done);
          resolve();
        };
        const timer = setTimeout(done, ms);
        signal?.addEventListener("abort", done, { once: true });
      }),
    now: () => Date.now(),
    random: Math.random,
  };
}

/**
 * 守护进程管理器。所有公开方法都可以并发调用：start/restart 会等待上一轮收敛，
 * stop 会打断正在进行的等待。
 */
export class DaemonManager {
  private readonly dataDir: string;
  private readonly resourcesPath: string;
  private readonly platform: NodeJS.Platform;
  private readonly env: NodeJS.ProcessEnv;
  private readonly logger: Logger;
  private readonly readyTimeoutMs: number;
  private readonly pollIntervalMs: number;
  private readonly probeTimeoutMs: number;
  private readonly shutdownTimeoutMs: number;
  private readonly stopTimeoutMs: number;
  private readonly policy: RestartPolicy;
  private readonly logSizeLimitBytes: number;
  private readonly deps: DaemonDeps;

  private readonly listeners = new Set<StatusListener>();
  private status: DaemonStatus;
  private connection: DaemonConnection | null = null;
  private child: SpawnedProcess | null = null;
  private childExit: Promise<ExitInfo> | null = null;
  /** 子进程就绪时读到的句柄（pid == 子进程 pid）；停止时用它请求关闭。 */
  private childHandle: DaemonHandle | null = null;
  private spawnedAt = 0;
  private generation = 0;
  private stopping = false;
  private abort = new AbortController();
  private pendingFailure: DaemonErrorCode | null = null;
  private lastHandleProblem: HandleParseFailure | "missing" | null = null;
  private inFlight: Promise<void> | null = null;
  private stopInFlight: Promise<void> | null = null;

  constructor(options: DaemonManagerOptions, deps: DaemonDeps = defaultDaemonDeps(options.platform)) {
    this.dataDir = options.dataDir;
    this.resourcesPath = options.resourcesPath;
    this.platform = options.platform;
    this.env = options.env;
    this.logger = options.logger;
    this.readyTimeoutMs = options.readyTimeoutMs ?? 30_000;
    this.pollIntervalMs = options.pollIntervalMs ?? 250;
    this.probeTimeoutMs = options.probeTimeoutMs ?? 1_500;
    this.shutdownTimeoutMs = options.shutdownTimeoutMs ?? 30_000;
    this.stopTimeoutMs = options.stopTimeoutMs ?? 30_000;
    this.policy = options.restartPolicy ?? DEFAULT_RESTART_POLICY;
    this.logSizeLimitBytes = options.logSizeLimitBytes ?? 5 * 1024 * 1024;
    this.deps = deps;
    this.status = { state: "idle", owned: false, attempt: 0, maxAttempts: this.policy.maxAttempts };
  }

  /** 当前状态的副本（不含令牌）。 */
  getStatus(): DaemonStatus {
    return { ...this.status, lastExit: this.status.lastExit ? { ...this.status.lastExit } : undefined };
  }

  /** 就绪时返回连接信息（含令牌，只在主进程使用），否则 null。 */
  getConnection(): DaemonConnection | null {
    return this.connection ? { ...this.connection } : null;
  }

  /** 订阅状态变化；返回取消函数。监听器抛错只记日志，不影响其他监听器。 */
  onStatus(listener: StatusListener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /** 发现或启动守护进程，直到就绪或失败。已就绪时立即返回。 */
  async start(): Promise<DaemonStatus> {
    while (this.stopInFlight || this.inFlight) await (this.stopInFlight ?? this.inFlight);
    if (this.status.state === "ready") return this.getStatus();

    const gen = ++this.generation;
    this.stopping = false;
    this.abort = new AbortController();
    // 先登记承诺，再发布可触发监听器重入的状态。
    const run = Promise.resolve().then(() => this.bringUp(gen, 0));
    this.track(run);
    await run;
    return this.getStatus();
  }

  /**
   * 停止由本壳启动的守护进程（先经关闭端点，必要时回退到信号）；别人启动的只确认身份后断开连接，
   * 身份确认失败时把 daemon.json 当残留删除。应用退出与托盘"重启守护进程"都走这里。
   */
  async stop(): Promise<void> {
    if (this.stopInFlight) return this.stopInFlight;
    const run = Promise.resolve().then(() => this.stopCurrent());
    this.stopInFlight = run;
    try {
      await run;
    } finally {
      if (this.stopInFlight === run) this.stopInFlight = null;
    }
  }

  private async stopCurrent(): Promise<void> {
    this.generation++;
    this.stopping = true;
    this.abort.abort();
    const child = this.child;
    const exit = this.childExit;
    if (child && exit) {
      this.publish({ state: "stopping", owned: true, attempt: 0, pid: child.pid });
      await this.stopChild(child, exit);
    } else {
      const report = await releaseForeignDaemon(this.dataDir, this.stopIO(), this.stopTimings());
      if (report.identity !== "no_handle") this.logger.info("断开不是本壳启动的守护进程", { ...report });
    }
    this.child = null;
    this.childExit = null;
    this.childHandle = null;
    this.connection = null;
    this.publish({ state: "stopped", owned: false, attempt: 0, lastExit: this.status.lastExit });
  }

  /** 停止后重新发现/启动（与应用退出共用 stop 的路径）。 */
  async restart(): Promise<DaemonStatus> {
    await this.stop();
    return this.start();
  }

  private track(run: Promise<void>): void {
    this.inFlight = run;
    const clear = () => {
      if (this.inFlight === run) this.inFlight = null;
    };
    void run.then(clear, clear);
  }

  private alive(gen: number): boolean {
    return gen === this.generation && !this.stopping;
  }

  private publish(next: Omit<DaemonStatus, "maxAttempts">): void {
    const status: DaemonStatus = { ...next, maxAttempts: this.policy.maxAttempts };
    if (sameStatus(this.status, status)) return;
    this.status = status;
    for (const listener of this.listeners) {
      try {
        listener(this.getStatus());
      } catch (err) {
        this.logger.error("状态监听器抛出异常", { err });
      }
    }
  }

  private async bringUp(gen: number, attempt: number): Promise<void> {
    this.publish({ state: "attaching", owned: false, attempt });
    const attach = await this.tryAttach();
    if (!this.alive(gen)) return;
    if (attach.kind === "attached") {
      const { handle } = attach;
      this.connection = { endpoint: handle.endpoint, token: handle.token };
      this.logger.info("复用已在运行的守护进程", { pid: handle.pid, endpoint: handle.endpoint, version: handle.version });
      this.publish({ state: "ready", owned: false, attempt, pid: handle.pid, endpoint: handle.endpoint });
      return;
    }
    if (attach.kind === "stale") {
      this.logger.error("守护进程在运行但拒绝了 daemon.json 中的令牌，不再拉起第二个实例", {
        pid: attach.handle.pid,
        endpoint: attach.handle.endpoint,
      });
      this.publish({ state: "failed", owned: false, attempt, error: "stale_handle", endpoint: attach.handle.endpoint });
      return;
    }

    this.publish({ state: "locating", owned: false, attempt });
    const candidates = daemonExecutableCandidates({ env: this.env, resourcesPath: this.resourcesPath, platform: this.platform });
    const found = await locateDaemonExecutable(candidates, this.deps.isExecutable);
    if (!this.alive(gen)) return;
    if (!found) {
      this.logger.error("找不到 gogoslingd 可执行文件", { candidates: candidates.map((c) => c.file) });
      this.publish({ state: "failed", owned: false, attempt, error: "executable_not_found" });
      return;
    }
    this.logger.info("找到 gogoslingd", { source: found.source, file: found.file });
    await this.launch(gen, found, attempt);
  }

  private async launch(gen: number, found: ExecutableCandidate, attempt: number): Promise<void> {
    const file = found.file;
    this.publish({ state: "starting", owned: true, attempt });

    let log: OpenedFile | null = null;
    try {
      log = await this.openStdioLog();
    } catch (err) {
      this.logger.warn("无法打开守护进程 stdio 捕获文件，输出将被丢弃", { err });
    }

    // 打开或轮转日志期间 stop() 可能已完成；旧的一轮不再拉起子进程。
    if (!this.alive(gen)) {
      await this.closeStdioLog(log);
      return;
    }

    let child: SpawnedProcess;
    try {
      child = this.deps.spawn({
        file,
        args: [],
        cwd: this.dataDir,
        env: daemonSpawnEnv({
          env: this.env,
          dataDir: this.dataDir,
          resourcesPath: this.resourcesPath,
          platform: this.platform,
          source: found.source,
        }),
        stdioFd: log?.fd ?? null,
      });
    } catch (err) {
      await this.closeStdioLog(log);
      if (!this.alive(gen)) return;
      this.logger.error("spawn 失败", { file, err });
      this.publish({ state: "failed", owned: false, attempt, error: "spawn_failed" });
      return;
    }
    // 在第一次异步等待之前登记所有权与退出监听：stop() 必须能找到已 spawn 的子进程。
    const exit = trackExit(child);
    this.child = child;
    this.childExit = exit;
    this.spawnedAt = this.deps.now();
    this.pendingFailure = null;
    void exit.then((info) => this.onChildExit(gen, child, info));
    this.logger.info("已启动守护进程", { pid: child.pid, file });

    // 子进程已持有描述符副本；父进程关闭自己的。关闭失败不丢弃子进程的生命周期。
    await this.closeStdioLog(log);
    if (!this.alive(gen) || this.child !== child) return;

    const ready = await this.waitUntilReady(child, exit);
    if (!this.alive(gen) || this.child !== child) return;
    if (!ready.ok) {
      if (ready.error === "ready_timeout") {
        this.logger.error("守护进程超时未就绪，终止它", { pid: child.pid, timeout_ms: this.readyTimeoutMs });
        this.pendingFailure = "ready_timeout";
        await this.stopChild(child, exit);
      }
      // "exited" 由 onChildExit 处理重启决策。
      return;
    }
    this.connection = { endpoint: ready.handle.endpoint, token: ready.handle.token };
    this.childHandle = ready.handle;
    this.logger.info("守护进程就绪", { pid: child.pid, endpoint: ready.handle.endpoint, version: ready.handle.version });
    this.publish({ state: "ready", owned: true, attempt, pid: child.pid, endpoint: ready.handle.endpoint });
  }

  private onChildExit(gen: number, child: SpawnedProcess, info: ExitInfo): void {
    if (this.child !== child) return;
    this.child = null;
    this.childExit = null;
    this.childHandle = null;
    this.connection = null;

    const uptimeMs = Math.max(0, this.deps.now() - this.spawnedAt);
    const lastExit = info.kind === "exit" ? { code: info.code, signal: info.signal } : { code: null, signal: null };
    if (info.kind === "error") this.logger.error("守护进程启动出错", { pid: child.pid, err: info.err });
    else this.logger.warn("守护进程退出", { pid: child.pid, code: info.code, signal: info.signal, uptime_ms: uptimeMs });

    if (gen !== this.generation || this.stopping) {
      this.publish({ state: "stopped", owned: false, attempt: 0, lastExit });
      return;
    }

    const reason: DaemonErrorCode = this.pendingFailure ?? (info.kind === "error" ? "spawn_failed" : "exited");
    this.pendingFailure = null;
    const decision = nextRestart(this.status.attempt, reason === "exited" ? uptimeMs : 0, this.policy, this.deps.random);
    if (decision.kind === "give_up") {
      this.logger.error("守护进程连续重启达到上限，放弃", { attempts: decision.attempt, reason });
      this.publish({ state: "failed", owned: false, attempt: decision.attempt, error: "restart_limit", lastExit });
      return;
    }
    this.track(Promise.resolve().then(() => this.restartAfter(gen, decision.attempt, decision.delayMs)));
    this.publish({ state: "restarting", owned: false, attempt: decision.attempt, error: reason, lastExit });
  }

  private async restartAfter(gen: number, attempt: number, delayMs: number): Promise<void> {
    this.logger.info("退避后重启守护进程", { attempt, delay_ms: delayMs });
    await this.deps.sleep(delayMs, this.abort.signal);
    if (!this.alive(gen)) return;
    await this.bringUp(gen, attempt);
  }

  private async waitUntilReady(child: SpawnedProcess, exit: Promise<ExitInfo>): Promise<ReadyResult> {
    let exited = false;
    void exit.then(() => {
      exited = true;
    });
    const signal = this.abort.signal;
    const deadline = this.deps.now() + this.readyTimeoutMs;
    for (;;) {
      if (exited || signal.aborted) return { ok: false, error: "exited" };
      const handle = await this.readHandle();
      if (handle && child.pid !== undefined && handle.pid === child.pid && (await this.probeHealth(handle.endpoint))) {
        return { ok: true, handle };
      }
      if (this.deps.now() >= deadline) return { ok: false, error: "ready_timeout" };
      await this.deps.sleep(this.pollIntervalMs, signal);
    }
  }

  private async tryAttach(): Promise<AttachResult> {
    const handle = await this.readHandle();
    if (!handle) return { kind: "none" };
    if (!(await this.probeHealth(handle.endpoint))) {
      this.logger.info("daemon.json 指向的守护进程没有响应，视为已退出", { pid: handle.pid, endpoint: handle.endpoint });
      return { kind: "none" };
    }
    const verdict = await this.verifyToken(handle);
    if (verdict === "rejected") return { kind: "stale", handle };
    if (verdict === "unknown") this.logger.warn("无法确认令牌是否有效，仍按已运行处理", { endpoint: handle.endpoint });
    return { kind: "attached", handle };
  }

  private async verifyToken(handle: DaemonHandle): Promise<"accepted" | "rejected" | "unknown"> {
    try {
      const res = await this.deps.fetch(versionUrl(handle.endpoint), {
        headers: { Authorization: `Bearer ${handle.token}` },
        timeoutMs: this.probeTimeoutMs,
      });
      if (res.status === 401 || res.status === 403) return "rejected";
      return res.status === 200 ? "accepted" : "unknown";
    } catch {
      return "unknown";
    }
  }

  private async probeHealth(endpoint: string): Promise<boolean> {
    try {
      const res = await this.deps.fetch(healthUrl(endpoint), { timeoutMs: this.probeTimeoutMs });
      return res.status === 200;
    } catch {
      return false;
    }
  }

  private async readHandle(): Promise<DaemonHandle | null> {
    const file = handleFilePath(this.dataDir);
    let text: string;
    try {
      text = await this.deps.readFile(file);
    } catch {
      this.noteHandleProblem("missing");
      return null;
    }
    const parsed = parseHandle(text);
    if (!parsed.ok) {
      this.noteHandleProblem(parsed.reason);
      return null;
    }
    this.lastHandleProblem = null;
    return parsed.handle;
  }

  /** 轮询期间同一个问题只记一次日志。 */
  private noteHandleProblem(problem: HandleParseFailure | "missing"): void {
    if (this.lastHandleProblem === problem) return;
    this.lastHandleProblem = problem;
    if (problem === "missing") this.logger.debug("daemon.json 不存在");
    else this.logger.warn("daemon.json 无法使用", { reason: problem });
  }

  /**
   * 停止本壳启动的子进程：句柄身份确认通过时先 POST /api/v1/system/shutdown，否则或失败时回退到信号；
   * 信号只发给子进程本身（ChildProcess.kill / 它的 pid 的 taskkill），不按 daemon.json 的 pid 发。
   */
  private async stopChild(child: SpawnedProcess, exit: Promise<ExitInfo>): Promise<void> {
    const pid = child.pid;
    if (pid === undefined) return;
    // 还没就绪时也可能已经写出了句柄；只接受写着这个子进程 pid 的那份。
    let handle = this.childHandle;
    if (!handle || handle.pid !== pid) {
      const fresh = await this.readHandle();
      handle = fresh && fresh.pid === pid ? fresh : null;
    }
    const win = this.platform === "win32";
    const report = await stopOwnedDaemon(
      {
        pid,
        exited: exit,
        terminate: win
          ? null
          : async () => {
              child.kill("SIGTERM");
            },
        forceKill: win
          ? () => this.deps.forceKillTree(pid)
          : async () => {
              child.kill("SIGKILL");
            },
      },
      handle,
      this.dataDir,
      this.stopIO(),
      this.stopTimings(),
    );
    this.logger.info("停止守护进程完成", { pid, ...report });
  }

  private stopIO(): StopIO {
    return {
      fetch: (url, init) => this.deps.fetch(url, init),
      readFile: (file) => this.deps.readFile(file),
      removeFile: (file) => this.deps.removeFile(file),
      sleep: (ms, signal) => this.deps.sleep(ms, signal),
      logger: this.logger,
    };
  }

  private stopTimings(): StopTimings {
    return { probeTimeoutMs: this.probeTimeoutMs, gracefulTimeoutMs: this.shutdownTimeoutMs, termTimeoutMs: this.stopTimeoutMs };
  }

  private async openStdioLog(): Promise<OpenedFile> {
    const file = daemonStdioLogPath(this.dataDir);
    await this.deps.ensureDir(path.dirname(file));
    const size = await this.deps.fileSize(file);
    if (shouldRotateLog(size, this.logSizeLimitBytes)) {
      try {
        await this.deps.rename(file, `${file}.1`);
      } catch (err) {
        this.logger.warn("轮转守护进程 stdio 日志失败", { file, err });
      }
    }
    return this.deps.openAppend(file);
  }

  private async closeStdioLog(log: OpenedFile | null): Promise<void> {
    try {
      await log?.close();
    } catch (err) {
      this.logger.warn("关闭守护进程 stdio 日志描述符失败", { err });
    }
  }
}

function trackExit(child: SpawnedProcess): Promise<ExitInfo> {
  return new Promise((resolve) => {
    child.on("exit", (code, signal) => resolve({ kind: "exit", code, signal }));
    child.on("error", (err) => resolve({ kind: "error", err }));
  });
}

function sameStatus(a: DaemonStatus, b: DaemonStatus): boolean {
  return (
    a.state === b.state &&
    a.owned === b.owned &&
    a.attempt === b.attempt &&
    a.pid === b.pid &&
    a.endpoint === b.endpoint &&
    a.error === b.error &&
    a.lastExit?.code === b.lastExit?.code &&
    a.lastExit?.signal === b.lastExit?.signal
  );
}

/** 状态是否允许用户触发"重启"。 */
export function canRestart(state: DaemonState): boolean {
  return state === "ready" || state === "failed" || state === "stopped" || state === "idle";
}
