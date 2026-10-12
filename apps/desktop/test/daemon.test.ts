// DaemonManager 状态机测试：用假的 spawn / fetch / 文件系统 / 时钟驱动完整流程，不启动任何真实进程。

import { EventEmitter } from "node:events";
import { describe, expect, it } from "vitest";
import { handleFilePath, healthUrl } from "../src/main/daemon-logic.js";
import { DaemonManager, canRestart, type DaemonDeps, type DaemonManagerOptions, type SpawnRequest, type SpawnedProcess } from "../src/main/daemon.js";
import type { Logger } from "../src/main/log.js";
import type { DaemonStatus } from "../src/shared/ipc.js";

const DATA_DIR = "/data";
const RESOURCES = "/app/resources";
const ENDPOINT = "http://127.0.0.1:46750";
const STARTED_AT = "2026-10-10T08:00:00Z";
const VERSION_PATH = "/api/v1/version";
const SHUTDOWN_PATH = "/api/v1/system/shutdown";

class FakeChild extends EventEmitter implements SpawnedProcess {
  readonly signals: NodeJS.Signals[] = [];
  exited = false;
  /** 模拟对 SIGTERM 无响应的进程。 */
  ignoreSigterm = false;
  /** 模拟接受了关闭请求（202）却迟迟不退出的进程。 */
  ignoreShutdown = false;
  /** 被 taskkill /F 强制结束。 */
  forced = false;

  constructor(readonly pid: number | undefined) {
    super();
  }

  kill(signal: NodeJS.Signals = "SIGTERM"): boolean {
    this.signals.push(signal);
    if (signal === "SIGKILL" || !this.ignoreSigterm) this.exit(null, signal);
    return true;
  }

  exit(code: number | null, signal: NodeJS.Signals | null = null): void {
    if (this.exited) return;
    this.exited = true;
    this.emit("exit", code, signal);
  }

  fail(err: Error): void {
    this.emit("error", err);
  }
}

interface PendingHandle {
  afterReads: number;
  text: string;
  endpoint: string;
}

class World {
  readonly files = new Map<string, string>();
  readonly executables = new Set<string>();
  /** url → 状态码；缺失视为连接被拒绝。 */
  readonly health = new Map<string, number>();
  readonly acceptedTokens = new Set<string>();
  readonly spawned: FakeChild[] = [];
  readonly spawnRequests: SpawnRequest[] = [];
  readonly sleeps: number[] = [];
  /** taskkill /T /F 的目标 pid。 */
  readonly forceKills: number[] = [];
  /** 收到的 POST /api/v1/system/shutdown（记录所带令牌）。 */
  readonly shutdownRequests: string[] = [];
  /** 关闭端点的状态码；"refuse" 表示连接被拒绝。 */
  shutdownStatus: number | "refuse" = 202;
  readonly childByToken = new Map<string, FakeChild>();
  readonly logs: string[] = [];
  clock = 1_000_000;
  readCount = 0;
  pendingHandle: PendingHandle | null = null;
  nextPid = 5000;
  spawnImpl: (req: SpawnRequest) => FakeChild = () => this.healthyChild();

  readonly logger: Logger = {
    debug: (m) => this.logs.push(`debug ${m}`),
    info: (m) => this.logs.push(`info ${m}`),
    warn: (m) => this.logs.push(`warn ${m}`),
    error: (m) => this.logs.push(`error ${m}`),
  };

  /** 写入一份句柄并让其端点健康。 */
  putHandle(pid: number, token: string, endpoint = ENDPOINT): void {
    this.files.set(handleFilePath(DATA_DIR), JSON.stringify({ pid, endpoint, token, version: "0.1.0", started_at: STARTED_AT }));
    this.health.set(healthUrl(endpoint), 200);
  }

  handleText(): string | undefined {
    return this.files.get(handleFilePath(DATA_DIR));
  }

  /**
   * 默认的子进程：spawn 后再读两次句柄文件才"写出"自己的 daemon.json；
   * 正常退出时像真实守护进程一样删除句柄、释放端口（HandleFile.Remove + 监听关闭）；
   * 被强制结束（SIGKILL / taskkill /F）时来不及删除句柄。
   */
  healthyChild(token = `tok-${this.nextPid}`): FakeChild {
    const child = new FakeChild(this.nextPid++);
    const text = JSON.stringify({ pid: child.pid, endpoint: ENDPOINT, token, version: "0.1.0", started_at: STARTED_AT });
    this.acceptedTokens.add(token);
    this.childByToken.set(token, child);
    this.pendingHandle = { afterReads: this.readCount + 2, text, endpoint: ENDPOINT };
    child.on("exit", (_code: number | null, signal: NodeJS.Signals | null) => {
      const forced = signal === "SIGKILL" || child.forced;
      if (!forced && this.files.get(handleFilePath(DATA_DIR)) === text) this.files.delete(handleFilePath(DATA_DIR));
      if (this.pendingHandle?.text === text) this.pendingHandle = null;
      this.health.delete(healthUrl(ENDPOINT));
      this.acceptedTokens.delete(token);
    });
    return child;
  }

  /** 永远不写句柄的子进程（启动卡死）。 */
  hangingChild(): FakeChild {
    return new FakeChild(this.nextPid++);
  }

  readonly deps: DaemonDeps = {
    spawn: (req) => {
      this.spawnRequests.push(req);
      const child = this.spawnImpl(req);
      this.spawned.push(child);
      return child;
    },
    fetch: async (url, init) => {
      const token = (init.headers?.Authorization ?? "").replace(/^Bearer /, "");
      if (url.endsWith(VERSION_PATH)) {
        if (!this.health.has(healthUrl(url.slice(0, -VERSION_PATH.length)))) throw new Error("ECONNREFUSED");
        if (!this.acceptedTokens.has(token)) return { status: 401 };
        return { status: 200, body: JSON.stringify({ version: "0.1.0", started_at: STARTED_AT }) };
      }
      if (url.endsWith(SHUTDOWN_PATH)) {
        if (init.method !== "POST") return { status: 405 };
        if (!this.health.has(healthUrl(url.slice(0, -SHUTDOWN_PATH.length)))) throw new Error("ECONNREFUSED");
        if (!this.acceptedTokens.has(token)) return { status: 401 };
        this.shutdownRequests.push(token);
        if (this.shutdownStatus === "refuse") throw new Error("ECONNREFUSED");
        if (this.shutdownStatus !== 202) return { status: this.shutdownStatus };
        // 优雅关闭：守护进程自己删除 daemon.json 后退出（healthyChild 的 exit 监听负责删除）。
        const child = this.childByToken.get(token);
        if (child && !child.ignoreShutdown) setImmediate(() => child.exit(0));
        return { status: 202 };
      }
      const status = this.health.get(url);
      if (status === undefined) throw new Error("ECONNREFUSED");
      return { status };
    },
    readFile: async (file) => {
      this.readCount++;
      if (this.pendingHandle && this.readCount >= this.pendingHandle.afterReads) {
        this.files.set(handleFilePath(DATA_DIR), this.pendingHandle.text);
        this.health.set(healthUrl(this.pendingHandle.endpoint), 200);
        this.pendingHandle = null;
      }
      const text = this.files.get(file);
      if (text === undefined) throw new Error("ENOENT");
      return text;
    },
    isExecutable: async (file) => this.executables.has(file),
    ensureDir: async () => undefined,
    openAppend: async () => ({ fd: 7, close: async () => undefined }),
    fileSize: async () => 0,
    rename: async () => undefined,
    removeFile: async (file) => {
      this.files.delete(file);
    },
    forceKillTree: async (pid) => {
      this.forceKills.push(pid);
      const child = this.spawned.find((c) => c.pid === pid);
      if (!child) return;
      child.forced = true;
      child.exit(1);
    },
    sleep: async (ms) => {
      this.sleeps.push(ms);
      this.clock += ms;
      await new Promise<void>((resolve) => setImmediate(resolve));
    },
    now: () => this.clock,
    random: () => 0.5,
  };
}

function makeManager(world: World, overrides: Partial<DaemonManagerOptions> = {}): DaemonManager {
  return new DaemonManager(
    {
      dataDir: DATA_DIR,
      resourcesPath: RESOURCES,
      platform: "linux",
      env: {},
      logger: world.logger,
      readyTimeoutMs: 100,
      pollIntervalMs: 10,
      probeTimeoutMs: 10,
      shutdownTimeoutMs: 3000,
      stopTimeoutMs: 1000,
      restartPolicy: { maxAttempts: 3, baseMs: 100, maxMs: 1000, jitter: 0, stableUptimeMs: 60_000 },
      ...overrides,
    },
    world.deps,
  );
}

function collect(manager: DaemonManager): DaemonStatus[] {
  const list: DaemonStatus[] = [];
  manager.onStatus((s) => list.push(s));
  return list;
}

async function settle(predicate: () => boolean, maxTicks = 20_000): Promise<void> {
  for (let i = 0; i < maxTicks; i++) {
    if (predicate()) return;
    await new Promise<void>((resolve) => setImmediate(resolve));
  }
  throw new Error("条件未在预期内满足");
}

describe("DaemonManager.start：发现与复用", () => {
  it("已有守护进程在跑且令牌有效时直接复用，不 spawn", async () => {
    const world = new World();
    world.putHandle(4242, "tok-live");
    world.acceptedTokens.add("tok-live");
    const manager = makeManager(world);

    const status = await manager.start();

    expect(status).toMatchObject({ state: "ready", owned: false, pid: 4242, endpoint: ENDPOINT, attempt: 0 });
    expect(manager.getConnection()).toEqual({ endpoint: ENDPOINT, token: "tok-live" });
    expect(world.spawned).toHaveLength(0);
  });

  it("句柄存在但 /healthz 无响应时视为陈旧，拉起新进程", async () => {
    const world = new World();
    world.files.set(handleFilePath(DATA_DIR), JSON.stringify({ pid: 1, endpoint: ENDPOINT, token: "old" }));
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);

    const status = await manager.start();

    expect(status).toMatchObject({ state: "ready", owned: true, pid: 5000, endpoint: ENDPOINT });
    expect(world.spawnRequests[0]).toMatchObject({ file: `${RESOURCES}/bin/gogoslingd`, cwd: DATA_DIR, args: [] });
    expect(world.spawnRequests[0]!.env.GOGOSLING_HOME).toBe(DATA_DIR);
    // 随包分发的二进制：内置扩展在 <resources>/share/gogosling 下。
    expect(world.spawnRequests[0]!.env.GOGOSLING_RESOURCES).toBe(`${RESOURCES}/share/gogosling`);
    expect(manager.getConnection()?.token).toBe("tok-5000");
  });

  it("守护进程在跑但拒绝句柄里的令牌：failed(stale_handle)，不拉起第二个实例", async () => {
    const world = new World();
    world.putHandle(4242, "tok-stale");
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);

    const status = await manager.start();

    expect(status).toMatchObject({ state: "failed", error: "stale_handle", owned: false, endpoint: ENDPOINT });
    expect(manager.getConnection()).toBeNull();
    expect(world.spawned).toHaveLength(0);
  });

  it("找不到可执行文件：failed(executable_not_found)", async () => {
    const world = new World();
    const manager = makeManager(world, { env: { PATH: "/usr/bin" } });

    const status = await manager.start();

    expect(status).toMatchObject({ state: "failed", error: "executable_not_found" });
    expect(world.logs).toContain("error 找不到 gogoslingd 可执行文件");
  });

  it("环境变量 GOGOSLING_DAEMON_PATH 优先于资源目录", async () => {
    const world = new World();
    world.executables.add("/custom/gogoslingd");
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world, { env: { GOGOSLING_DAEMON_PATH: "/custom/gogoslingd" } });

    await manager.start();

    expect(world.spawnRequests[0]!.file).toBe("/custom/gogoslingd");
    expect(world.spawnRequests[0]!.env.GOGOSLING_RESOURCES).toBeUndefined();
  });

  it("状态事件按顺序发出，且不包含令牌", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    const seen = collect(manager);

    await manager.start();

    expect(seen.map((s) => s.state)).toEqual(["attaching", "locating", "starting", "ready"]);
    for (const s of seen) expect(JSON.stringify(s)).not.toContain("tok-");
  });

  it("已就绪时再次 start 直接返回", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    await manager.start();
    await manager.start();
    expect(world.spawned).toHaveLength(1);
  });
});

describe("DaemonManager：崩溃与重启", () => {
  it("就绪超时：终止子进程，按退避重启，达到上限后放弃", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.spawnImpl = () => world.hangingChild();
    const manager = makeManager(world);
    const seen = collect(manager);

    await manager.start();
    await settle(() => manager.getStatus().state === "failed");

    expect(manager.getStatus()).toMatchObject({ state: "failed", error: "restart_limit", attempt: 3 });
    // 1 次初始 + 3 次重启
    expect(world.spawned).toHaveLength(4);
    for (const child of world.spawned) expect(child.signals).toContain("SIGTERM");
    expect(seen.filter((s) => s.state === "restarting").map((s) => s.attempt)).toEqual([1, 2, 3]);
    expect(seen.filter((s) => s.state === "restarting").map((s) => s.error)).toEqual(["ready_timeout", "ready_timeout", "ready_timeout"]);
    expect(world.sleeps.filter((ms) => [100, 200, 400].includes(ms))).toEqual([100, 200, 400]);
  });

  it("子进程崩溃后自动重启并拿到新令牌；稳定运行后的崩溃重新计数", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    const seen = collect(manager);

    await manager.start();
    const first = manager.getConnection();
    expect(first?.token).toBe("tok-5000");

    world.spawned[0]!.exit(1);
    await settle(() => manager.getStatus().state === "ready" && manager.getStatus().pid === 5001);

    expect(manager.getStatus()).toMatchObject({ state: "ready", owned: true, attempt: 1 });
    expect(manager.getConnection()?.token).toBe("tok-5001");
    expect(seen.find((s) => s.state === "restarting")).toMatchObject({ attempt: 1, error: "exited", lastExit: { code: 1, signal: null } });

    // 稳定运行一分钟后再崩溃：计数回到 1 而不是 2。
    world.clock += 61_000;
    world.spawned[1]!.exit(null, "SIGSEGV");
    await settle(() => manager.getStatus().state === "ready" && manager.getStatus().pid === 5002);
    expect(manager.getStatus().attempt).toBe(1);
  });

  it("spawn 异步报错（error 事件）按 spawn_failed 处理并进入重启", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    let calls = 0;
    world.spawnImpl = () => {
      calls++;
      if (calls === 1) {
        const broken = new FakeChild(undefined);
        setImmediate(() => broken.fail(new Error("spawn ENOENT")));
        return broken;
      }
      return world.healthyChild();
    };
    const manager = makeManager(world);
    const seen = collect(manager);

    await manager.start();
    await settle(() => manager.getStatus().state === "ready");

    expect(seen.find((s) => s.state === "restarting")).toMatchObject({ attempt: 1, error: "spawn_failed" });
    expect(world.spawned).toHaveLength(2);
  });

  it("失败后用户重试（再次 start）从零开始计数", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.spawnImpl = () => world.hangingChild();
    const manager = makeManager(world);
    await manager.start();
    await settle(() => manager.getStatus().state === "failed");

    world.spawnImpl = () => world.healthyChild();
    const status = await manager.start();

    expect(status).toMatchObject({ state: "ready", attempt: 0, owned: true });
  });
});

describe("DaemonManager.stop / restart", () => {
  it("自己启动的进程经关闭端点优雅停止：不发信号，守护进程自己删除句柄；连接信息被清空", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    const seen = collect(manager);
    await manager.start();

    await manager.stop();

    expect(world.shutdownRequests).toEqual(["tok-5000"]);
    expect(world.spawned[0]!.signals).toEqual([]);
    expect(world.forceKills).toEqual([]);
    expect(world.handleText()).toBeUndefined();
    expect(manager.getStatus()).toMatchObject({ state: "stopped", owned: false });
    expect(manager.getConnection()).toBeNull();
    expect(seen.map((s) => s.state).slice(-2)).toEqual(["stopping", "stopped"]);
    // 主动停止不触发重启
    await new Promise<void>((resolve) => setImmediate(resolve));
    expect(world.spawned).toHaveLength(1);
  });

  it("关闭端点 404（旧版本守护进程）时回退到 SIGTERM", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.shutdownStatus = 404;
    const manager = makeManager(world);
    await manager.start();

    await manager.stop();

    expect(world.shutdownRequests).toEqual(["tok-5000"]);
    expect(world.spawned[0]!.signals).toEqual(["SIGTERM"]);
    expect(manager.getStatus().state).toBe("stopped");
  });

  it("关闭端点连接失败且 SIGTERM 无响应：超时后 SIGKILL，并删除守护进程来不及删的句柄", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.shutdownStatus = "refuse";
    world.spawnImpl = () => {
      const child = world.healthyChild();
      child.ignoreSigterm = true;
      return child;
    };
    const manager = makeManager(world, { stopTimeoutMs: 50 });
    await manager.start();
    expect(world.handleText()).toBeDefined();

    await manager.stop();

    expect(world.spawned[0]!.signals).toEqual(["SIGTERM", "SIGKILL"]);
    expect(world.sleeps).toContain(50);
    expect(world.handleText()).toBeUndefined();
    expect(manager.getStatus().state).toBe("stopped");
  });

  it("接受关闭却超时未退出：等满 shutdownTimeoutMs 后直接强杀，不补发 SIGTERM", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.spawnImpl = () => {
      const child = world.healthyChild();
      child.ignoreShutdown = true;
      return child;
    };
    const manager = makeManager(world);
    await manager.start();

    await manager.stop();

    expect(world.shutdownRequests).toEqual(["tok-5000"]);
    expect(world.spawned[0]!.signals).toEqual(["SIGKILL"]);
    expect(world.sleeps).toContain(3000);
    expect(world.handleText()).toBeUndefined();
  });

  it("Windows：关闭端点可用时不调用 taskkill", async () => {
    const world = new World();
    world.executables.add("C:\\App\\resources\\bin\\gogoslingd.exe");
    const manager = makeManager(world, { platform: "win32", resourcesPath: "C:\\App\\resources" });
    await manager.start();
    expect(world.spawnRequests[0]!.file).toBe("C:\\App\\resources\\bin\\gogoslingd.exe");

    await manager.stop();

    expect(world.shutdownRequests).toEqual(["tok-5000"]);
    expect(world.forceKills).toEqual([]);
    expect(world.spawned[0]!.signals).toEqual([]);
  });

  it("Windows：关闭端点不可用时直接 taskkill /T /F 一次，不先白等一轮，并删除残留句柄", async () => {
    const world = new World();
    world.executables.add("C:\\App\\resources\\bin\\gogoslingd.exe");
    world.shutdownStatus = 503;
    const manager = makeManager(world, { platform: "win32", resourcesPath: "C:\\App\\resources", stopTimeoutMs: 20 });
    await manager.start();
    const sleepsBefore = world.sleeps.length;

    await manager.stop();

    expect(world.forceKills).toEqual([5000]);
    expect(world.spawned[0]!.signals).toEqual([]);
    expect(world.handleText()).toBeUndefined();
    // 强制结束后只做一次有上限的等待（进程已退出，立即取消），没有"温和终止 → 等 stopTimeoutMs"那一轮。
    expect(world.sleeps.slice(sleepsBefore)).toEqual([20]);
  });

  it("就绪前停止（还没有句柄）：不请求关闭端点，直接走信号", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.spawnImpl = () => world.hangingChild();
    const manager = makeManager(world, { readyTimeoutMs: 60_000 });
    void manager.start();
    await settle(() => world.spawned.length === 1);

    await manager.stop();

    expect(world.shutdownRequests).toEqual([]);
    expect(world.spawned[0]!.signals).toEqual(["SIGTERM"]);
    expect(manager.getStatus().state).toBe("stopped");
  });

  it("复用的守护进程：确认身份后只断开连接，不请求关闭、不发信号、不删句柄", async () => {
    const world = new World();
    world.putHandle(4242, "tok-live");
    world.acceptedTokens.add("tok-live");
    const manager = makeManager(world);
    await manager.start();

    await manager.stop();

    expect(manager.getStatus().state).toBe("stopped");
    expect(world.shutdownRequests).toEqual([]);
    expect(world.forceKills).toEqual([]);
    expect(world.spawned).toHaveLength(0);
    expect(world.handleText()).toBeDefined();
  });

  it("复用的守护进程已崩溃（句柄残留）：退出时只删除句柄，不发信号", async () => {
    const world = new World();
    world.putHandle(4242, "tok-live");
    world.acceptedTokens.add("tok-live");
    const manager = makeManager(world);
    await manager.start();
    // 别人启动的守护进程崩溃：端口关闭，daemon.json 留在磁盘上。
    world.health.delete(healthUrl(ENDPOINT));

    await manager.stop();

    expect(world.handleText()).toBeUndefined();
    expect(world.shutdownRequests).toEqual([]);
    expect(world.forceKills).toEqual([]);
  });

  it("stale_handle 时重启：身份确认失败，只删除残留句柄（不发信号），随后拉起自己的实例", async () => {
    const world = new World();
    world.putHandle(4242, "tok-stale");
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    expect(await manager.start()).toMatchObject({ state: "failed", error: "stale_handle" });

    const status = await manager.restart();

    expect(world.shutdownRequests).toEqual([]);
    expect(world.forceKills).toEqual([]);
    expect(status).toMatchObject({ state: "ready", owned: true, pid: 5000 });
    expect(world.handleText()).toContain('"pid":5000');
  });

  it("restart 经关闭端点停止旧进程并拉起新进程，令牌随之变化", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    await manager.start();
    const before = manager.getConnection();

    const status = await manager.restart();

    expect(status).toMatchObject({ state: "ready", owned: true, pid: 5001 });
    expect(world.shutdownRequests).toEqual(["tok-5000"]);
    expect(world.spawned[0]!.signals).toEqual([]);
    expect(manager.getConnection()?.token).not.toBe(before?.token);
  });

  it("退避等待期间 stop 会立刻打断，不再拉起", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world, { restartPolicy: { maxAttempts: 3, baseMs: 5_000, maxMs: 5_000, jitter: 0, stableUptimeMs: 60_000 } });
    await manager.start();

    world.spawned[0]!.exit(1);
    await settle(() => manager.getStatus().state === "restarting");
    await manager.stop();
    await new Promise<void>((resolve) => setImmediate(resolve));

    expect(manager.getStatus().state).toBe("stopped");
    expect(world.spawned).toHaveLength(1);
  });
});

describe("canRestart", () => {
  it("只有静止状态允许用户触发重启", () => {
    expect(canRestart("ready")).toBe(true);
    expect(canRestart("failed")).toBe(true);
    expect(canRestart("stopped")).toBe(true);
    expect(canRestart("idle")).toBe(true);
    expect(canRestart("starting")).toBe(false);
    expect(canRestart("restarting")).toBe(false);
    expect(canRestart("stopping")).toBe(false);
    expect(canRestart("attaching")).toBe(false);
    expect(canRestart("locating")).toBe(false);
  });
});

describe("DaemonManager 启停并发与日志资源", () => {
  it("启动状态监听器重入 start 会等待同一次启动而不是提前返回或重复 spawn", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    let reentrant: Promise<DaemonStatus> | undefined;
    manager.onStatus((status) => {
      if (status.state === "attaching" && !reentrant) reentrant = manager.start();
    });
    expect(await manager.start()).toMatchObject({ state: "ready", owned: true, pid: 5000 });
    expect(await reentrant).toMatchObject({ state: "ready", owned: true, pid: 5000 });
    expect(world.spawned).toHaveLength(1);
  });

  it("停止状态监听器请求启动会等待关闭完成，不复用即将停止的旧进程", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    await manager.start();
    const fetch = world.deps.fetch;
    let shutdownPending = false;
    let release!: () => void;
    world.deps.fetch = async (url, init) => {
      if (url.endsWith(SHUTDOWN_PATH)) {
        shutdownPending = true;
        await new Promise<void>((resolve) => { release = resolve; });
      }
      return fetch(url, init);
    };
    let reopening: Promise<DaemonStatus> | undefined;
    let reopened = false;
    manager.onStatus((status) => {
      if (status.state === "stopping") {
        reopening = manager.start();
        void reopening.then(() => { reopened = true; });
      }
    });
    const stopping = manager.stop();
    await settle(() => shutdownPending);
    await new Promise<void>((resolve) => setImmediate(resolve));
    expect(reopened).toBe(false);
    expect(world.spawned).toHaveLength(1);
    release();
    await stopping;
    expect(await reopening).toMatchObject({ state: "ready", owned: true, pid: 5001 });
    expect(world.spawned[0]!.exited).toBe(true);
    expect(manager.getConnection()?.token).toBe("tok-5001");
  });

  it("spawn 失败后等待日志关闭期间停止不会被旧的失败状态覆盖", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.spawnImpl = () => { throw new Error("spawn failed"); };
    let closing = false;
    let release!: () => void;
    world.deps.openAppend = async () => ({
      fd: 7,
      close: async () => {
        closing = true;
        await new Promise<void>((resolve) => { release = resolve; });
      },
    });
    const manager = makeManager(world);
    const starting = manager.start();
    await settle(() => closing);
    await manager.stop();
    release();
    expect(await starting).toMatchObject({ state: "stopped", owned: false });
    expect(world.spawned).toHaveLength(0);
  });

  it("旧子进程在健康请求期间退出后，迟到的成功响应不会覆盖新实例连接", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const fetch = world.deps.fetch;
    let probingOld = false;
    let release!: () => void;
    world.deps.fetch = async (url, init) => {
      if (url === healthUrl(ENDPOINT) && world.spawned.length === 1) {
        probingOld = true;
        await new Promise<void>((resolve) => { release = resolve; });
        return { status: 200 };
      }
      return fetch(url, init);
    };
    const manager = makeManager(world);
    const starting = manager.start();
    await settle(() => probingOld);
    world.spawned[0]!.exit(1);
    await settle(() => world.spawned.length === 2 && manager.getStatus().state === "ready");
    release();
    expect(await starting).toMatchObject({ state: "ready", owned: true, pid: 5001 });
    expect(manager.getConnection()?.token).toBe("tok-5001");
    expect(world.spawned[0]!.exited).toBe(true);
  });

  it("打开日志期间停止不会遗留后来启动的守护进程，并释放日志描述符", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    let opening = false;
    let closed = 0;
    let release!: () => void;
    world.deps.openAppend = async () => {
      opening = true;
      await new Promise<void>((resolve) => { release = resolve; });
      return { fd: 7, close: async () => { closed++; } };
    };
    const manager = makeManager(world);
    const starting = manager.start();
    await settle(() => opening);
    await manager.stop();
    release();
    expect(await starting).toMatchObject({ state: "stopped", owned: false });
    expect(world.spawned).toHaveLength(0);
    expect(closed).toBe(1);
    expect(manager.getConnection()).toBeNull();
  });

  it("spawn 后日志关闭仍在等待时停止也会结束已拥有的子进程", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    let closing = false;
    let release!: () => void;
    world.deps.openAppend = async () => ({
      fd: 7,
      close: async () => {
        closing = true;
        await new Promise<void>((resolve) => { release = resolve; });
      },
    });
    const manager = makeManager(world);
    const starting = manager.start();
    await settle(() => closing && world.spawned.length === 1);
    await manager.stop();
    expect(world.spawned[0]!.exited).toBe(true);
    release();
    expect(await starting).toMatchObject({ state: "stopped", owned: false });
    expect(world.handleText()).toBeUndefined();
    expect(manager.getConnection()).toBeNull();
  });

  it("日志描述符关闭失败不丢失已启动的进程，仍可正常停止", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    world.deps.openAppend = async () => ({ fd: 7, close: async () => { throw new Error("close failed"); } });
    const manager = makeManager(world);
    expect(await manager.start()).toMatchObject({ state: "ready", owned: true });
    await manager.stop();
    expect(world.spawned[0]!.exited).toBe(true);
    expect(manager.getStatus().state).toBe("stopped");
  });

  it("并发停止只关闭一次，停止期间请求启动会等旧进程退出后再拉起", async () => {
    const world = new World();
    world.executables.add(`${RESOURCES}/bin/gogoslingd`);
    const manager = makeManager(world);
    await manager.start();
    const fetch = world.deps.fetch;
    let stopping = false;
    let release!: () => void;
    world.deps.fetch = async (url, init) => {
      if (url.endsWith(SHUTDOWN_PATH)) {
        stopping = true;
        await new Promise<void>((resolve) => { release = resolve; });
      }
      return fetch(url, init);
    };
    const firstStop = manager.stop();
    const secondStop = manager.stop();
    const starting = manager.start();
    await settle(() => stopping);
    expect(world.spawned).toHaveLength(1);
    release();
    await Promise.all([firstStop, secondStop]);
    expect(await starting).toMatchObject({ state: "ready", owned: true, pid: 5001 });
    expect(world.shutdownRequests).toEqual(["tok-5000"]);
    expect(world.spawned[0]!.exited).toBe(true);
    expect(world.spawned[1]!.exited).toBe(false);
    expect(manager.getConnection()?.token).toBe("tok-5001");
  });
});
