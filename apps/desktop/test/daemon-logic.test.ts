import * as path from "node:path";
import { describe, expect, it } from "vitest";
import {
  backoffDelay,
  bundledResourcesDir,
  daemonExecutableCandidates,
  daemonSpawnEnv,
  daemonStdioLogPath,
  DEFAULT_RESTART_POLICY,
  handleFilePath,
  healthUrl,
  isLoopbackEndpoint,
  locateDaemonExecutable,
  nextRestart,
  normalizeEndpoint,
  parseHandle,
  parseVersionStartedAt,
  resolveDataDir,
  shouldRotateLog,
  shutdownUrl,
  startedAtMatches,
  versionUrl,
} from "../src/main/daemon-logic.js";

describe("resolveDataDir", () => {
  it("GOGOSLING_HOME 优先并转为绝对路径", () => {
    expect(resolveDataDir({ GOGOSLING_HOME: "/tmp/gg" }, "/home/u")).toBe(path.resolve("/tmp/gg"));
  });

  it("未设置时使用 ~/.gogosling", () => {
    expect(resolveDataDir({}, "/home/u")).toBe(path.join("/home/u", ".gogosling"));
  });

  it("空白值视为未设置", () => {
    expect(resolveDataDir({ GOGOSLING_HOME: "   " }, "/home/u")).toBe(path.join("/home/u", ".gogosling"));
  });
});

describe("daemonSpawnEnv / bundledResourcesDir", () => {
  it("资源目录与内核的安装布局一致：<resources>/share/gogosling（exe 在 <resources>/bin）", () => {
    expect(bundledResourcesDir("/App/Contents/Resources", "darwin")).toBe("/App/Contents/Resources/share/gogosling");
    expect(bundledResourcesDir("C:\\App\\resources", "win32")).toBe("C:\\App\\resources\\share\\gogosling");
  });

  it("随包分发的二进制：设置 GOGOSLING_HOME 与 GOGOSLING_RESOURCES，其余环境原样继承", () => {
    const env = daemonSpawnEnv({ env: { PATH: "/usr/bin" }, dataDir: "/data", resourcesPath: "/App/res", platform: "linux", source: "resources" });
    expect(env).toEqual({ PATH: "/usr/bin", GOGOSLING_HOME: "/data", GOGOSLING_RESOURCES: "/App/res/share/gogosling" });
  });

  it("用户显式设置的 GOGOSLING_RESOURCES 不被覆盖", () => {
    const env = daemonSpawnEnv({ env: { GOGOSLING_RESOURCES: "/mine" }, dataDir: "/data", resourcesPath: "/App/res", platform: "linux", source: "resources" });
    expect(env.GOGOSLING_RESOURCES).toBe("/mine");
  });

  it("GOGOSLING_DAEMON_PATH / PATH 找到的二进制不设置（让内核按自己的查找顺序定位）", () => {
    for (const source of ["env", "path"] as const) {
      const env = daemonSpawnEnv({ env: {}, dataDir: "/data", resourcesPath: "/App/res", platform: "linux", source });
      expect(env.GOGOSLING_RESOURCES).toBeUndefined();
      expect(env.GOGOSLING_HOME).toBe("/data");
    }
  });
});

describe("daemonExecutableCandidates", () => {
  it("顺序：环境变量 > resources/bin > PATH 各目录", () => {
    const candidates = daemonExecutableCandidates({
      env: { GOGOSLING_DAEMON_PATH: "/opt/gg/gogoslingd", PATH: "/usr/local/bin:/usr/bin" },
      resourcesPath: "/app/resources",
      platform: "linux",
    });
    expect(candidates.map((c) => c.source)).toEqual(["env", "resources", "path", "path"]);
    expect(candidates.map((c) => c.file)).toEqual([
      "/opt/gg/gogoslingd",
      "/app/resources/bin/gogoslingd",
      "/usr/local/bin/gogoslingd",
      "/usr/bin/gogoslingd",
    ]);
  });

  it("Windows 使用 .exe、分号分隔的 Path，并去掉目录两侧的引号", () => {
    const candidates = daemonExecutableCandidates({
      env: { Path: 'C:\\Tools;"C:\\Program Files\\gg"' },
      resourcesPath: "C:\\App\\resources",
      platform: "win32",
    });
    expect(candidates).toEqual([
      { source: "resources", file: "C:\\App\\resources\\bin\\gogoslingd.exe" },
      { source: "path", file: "C:\\Tools\\gogoslingd.exe" },
      { source: "path", file: "C:\\Program Files\\gg\\gogoslingd.exe" },
    ]);
  });

  it("去重并忽略空目录", () => {
    const candidates = daemonExecutableCandidates({
      env: { PATH: "/usr/bin::/usr/bin: " },
      resourcesPath: "/app/resources",
      platform: "darwin",
    });
    expect(candidates.filter((c) => c.source === "path").map((c) => c.file)).toEqual(["/usr/bin/gogoslingd"]);
  });

  it("没有 PATH 时只有环境变量与资源目录", () => {
    const candidates = daemonExecutableCandidates({ env: {}, resourcesPath: "/app/resources", platform: "linux" });
    expect(candidates).toEqual([{ source: "resources", file: "/app/resources/bin/gogoslingd" }]);
  });
});

describe("locateDaemonExecutable", () => {
  const candidates = [
    { source: "env" as const, file: "/a" },
    { source: "resources" as const, file: "/b" },
    { source: "path" as const, file: "/c" },
  ];

  it("返回第一个可执行的候选", async () => {
    const found = await locateDaemonExecutable(candidates, async (file) => file === "/b" || file === "/c");
    expect(found).toEqual({ source: "resources", file: "/b" });
  });

  it("全部不可执行时返回 null", async () => {
    expect(await locateDaemonExecutable(candidates, async () => false)).toBeNull();
  });
});

describe("parseHandle", () => {
  const good = {
    pid: 4242,
    endpoint: "http://127.0.0.1:46750",
    token: "tok-abc",
    version: "0.1.0",
    started_at: "2026-10-09T17:02:03Z",
  };

  it("解析合法句柄", () => {
    const parsed = parseHandle(JSON.stringify(good));
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) return;
    expect(parsed.handle.pid).toBe(4242);
    expect(parsed.handle.endpoint).toBe("http://127.0.0.1:46750");
    expect(parsed.handle.token).toBe("tok-abc");
    expect(parsed.handle.version).toBe("0.1.0");
    expect(parsed.handle.startedAt?.toISOString()).toBe("2026-10-09T17:02:03.000Z");
  });

  it("去掉 endpoint 的尾部斜杠，令牌去空白", () => {
    const parsed = parseHandle(JSON.stringify({ ...good, endpoint: "http://localhost:46750/", token: "  t  " }));
    expect(parsed).toMatchObject({ ok: true, handle: { endpoint: "http://localhost:46750", token: "t" } });
  });

  it("缺少 version / started_at 时给默认值", () => {
    const parsed = parseHandle(JSON.stringify({ pid: 1, endpoint: good.endpoint, token: "t" }));
    expect(parsed).toMatchObject({ ok: true, handle: { version: "", startedAt: null } });
  });

  it("非法 started_at 置为 null 而不是失败", () => {
    const parsed = parseHandle(JSON.stringify({ ...good, started_at: "yesterday" }));
    expect(parsed).toMatchObject({ ok: true, handle: { startedAt: null } });
  });

  it.each([
    ["不是 JSON", "{nope", "not_json"],
    ["数组", "[]", "not_object"],
    ["null", "null", "not_object"],
    ["pid 为 0", JSON.stringify({ ...good, pid: 0 }), "invalid_pid"],
    ["pid 是字符串", JSON.stringify({ ...good, pid: "4242" }), "invalid_pid"],
    ["pid 非整数", JSON.stringify({ ...good, pid: 1.5 }), "invalid_pid"],
    ["缺少 endpoint", JSON.stringify({ pid: 1, token: "t" }), "invalid_endpoint"],
    ["endpoint 带查询串", JSON.stringify({ ...good, endpoint: "http://127.0.0.1:1?x=1" }), "invalid_endpoint"],
    ["endpoint 不是 http", JSON.stringify({ ...good, endpoint: "ftp://127.0.0.1:1" }), "invalid_endpoint"],
    ["endpoint 指向远端", JSON.stringify({ ...good, endpoint: "http://10.0.0.8:46750" }), "endpoint_not_loopback"],
    ["endpoint 指向公网域名", JSON.stringify({ ...good, endpoint: "https://example.com" }), "endpoint_not_loopback"],
    ["令牌为空", JSON.stringify({ ...good, token: "  " }), "missing_token"],
    ["令牌缺失", JSON.stringify({ pid: 1, endpoint: good.endpoint }), "missing_token"],
  ])("拒绝：%s", (_name, text, reason) => {
    expect(parseHandle(text)).toEqual({ ok: false, reason });
  });
});

describe("normalizeEndpoint / isLoopbackEndpoint", () => {
  it("归一化 origin 并去尾部斜杠", () => {
    expect(normalizeEndpoint(" http://127.0.0.1:46750/// ")).toBe("http://127.0.0.1:46750");
    expect(normalizeEndpoint("http://[::1]:46750/api/")).toBe("http://[::1]:46750/api");
  });

  it("拒绝用户信息、片段与非 http 协议", () => {
    expect(normalizeEndpoint("http://u:p@127.0.0.1:1")).toBeNull();
    expect(normalizeEndpoint("http://127.0.0.1:1/#x")).toBeNull();
    expect(normalizeEndpoint("unix:///tmp/sock")).toBeNull();
    expect(normalizeEndpoint("")).toBeNull();
    expect(normalizeEndpoint("not a url")).toBeNull();
  });

  it("只有 127.0.0.1 / localhost / [::1] 算回环", () => {
    expect(isLoopbackEndpoint("http://127.0.0.1:1")).toBe(true);
    expect(isLoopbackEndpoint("http://LOCALHOST:1")).toBe(true);
    expect(isLoopbackEndpoint("http://[::1]:1")).toBe(true);
    expect(isLoopbackEndpoint("http://127.0.0.2:1")).toBe(false);
    expect(isLoopbackEndpoint("http://0.0.0.0:1")).toBe(false);
    expect(isLoopbackEndpoint("garbage")).toBe(false);
  });
});

describe("backoffDelay", () => {
  it("无抖动时按 2 的幂增长并封顶", () => {
    const delays = [1, 2, 3, 4, 5, 6].map((a) => backoffDelay(a, { baseMs: 500, maxMs: 15_000, jitter: 0 }));
    expect(delays).toEqual([500, 1000, 2000, 4000, 8000, 15_000]);
  });

  it("抖动落在 ±jitter 范围内且不超过上限", () => {
    const opts = { baseMs: 500, maxMs: 15_000, jitter: 0.2 };
    expect(backoffDelay(1, opts, () => 0)).toBe(400);
    expect(backoffDelay(1, opts, () => 1)).toBe(600);
    expect(backoffDelay(1, opts, () => 0.5)).toBe(500);
    expect(backoffDelay(10, opts, () => 1)).toBe(15_000);
  });

  it("attempt 小于 1 按 1 处理", () => {
    expect(backoffDelay(0, { baseMs: 300, maxMs: 1000, jitter: 0 })).toBe(300);
    expect(backoffDelay(-5, { baseMs: 300, maxMs: 1000, jitter: 0 })).toBe(300);
  });
});

describe("nextRestart", () => {
  const policy = { ...DEFAULT_RESTART_POLICY, jitter: 0 };

  it("连续快速崩溃时递增尝试次数，超过上限放弃", () => {
    let attempt = 0;
    const seen: number[] = [];
    for (let i = 0; i < 5; i++) {
      const d = nextRestart(attempt, 1000, policy);
      expect(d.kind).toBe("restart");
      if (d.kind !== "restart") return;
      attempt = d.attempt;
      seen.push(d.delayMs);
    }
    expect(seen).toEqual([500, 1000, 2000, 4000, 8000]);
    expect(nextRestart(attempt, 1000, policy)).toEqual({ kind: "give_up", attempt: 5 });
  });

  it("稳定运行后再崩溃重新从第 1 次计数", () => {
    expect(nextRestart(4, policy.stableUptimeMs, policy)).toEqual({ kind: "restart", attempt: 1, delayMs: 500 });
    expect(nextRestart(5, policy.stableUptimeMs + 1, policy)).toEqual({ kind: "restart", attempt: 1, delayMs: 500 });
  });

  it("默认策略上限 5 次、起步 500ms、封顶 15s", () => {
    expect(DEFAULT_RESTART_POLICY).toMatchObject({ maxAttempts: 5, baseMs: 500, maxMs: 15_000 });
  });
});

describe("路径与 URL 辅助", () => {
  it("句柄与日志文件位于数据目录内", () => {
    expect(handleFilePath("/d")).toBe(path.join("/d", "daemon.json"));
    expect(daemonStdioLogPath("/d")).toBe(path.join("/d", "logs", "gogoslingd-stdio.log"));
  });

  it("健康检查、版本与关闭接口地址", () => {
    expect(healthUrl("http://127.0.0.1:46750")).toBe("http://127.0.0.1:46750/healthz");
    expect(versionUrl("http://127.0.0.1:46750")).toBe("http://127.0.0.1:46750/api/v1/version");
    expect(shutdownUrl("http://127.0.0.1:46750")).toBe("http://127.0.0.1:46750/api/v1/system/shutdown");
  });

  it("日志轮转阈值", () => {
    expect(shouldRotateLog(10, 10)).toBe(true);
    expect(shouldRotateLog(9, 10)).toBe(false);
    expect(shouldRotateLog(100, 0)).toBe(false);
  });
});

describe("parseVersionStartedAt / startedAtMatches", () => {
  it("从 VersionInfo 中取 started_at；缺失、空白或不是对象时为 null", () => {
    expect(parseVersionStartedAt(JSON.stringify({ version: "0.1.0", started_at: "2026-10-10T08:00:00Z" }))).toBe("2026-10-10T08:00:00Z");
    expect(parseVersionStartedAt(JSON.stringify({ version: "0.1.0" }))).toBeNull();
    expect(parseVersionStartedAt(JSON.stringify({ started_at: "  " }))).toBeNull();
    expect(parseVersionStartedAt(JSON.stringify({ started_at: 1 }))).toBeNull();
    expect(parseVersionStartedAt("[]")).toBeNull();
    expect(parseVersionStartedAt("not json")).toBeNull();
    expect(parseVersionStartedAt(undefined)).toBeNull();
  });

  it("按秒比较：/version 的 RFC 3339（可带时区偏移）与句柄的纳秒时间指同一次启动", () => {
    const handle = new Date("2026-10-10T08:00:00.987Z");
    expect(startedAtMatches("2026-10-10T08:00:00Z", handle)).toBe(true);
    expect(startedAtMatches("2026-10-10T16:00:00+08:00", handle)).toBe(true);
    expect(startedAtMatches("2026-10-10T08:00:01Z", handle)).toBe(false);
    expect(startedAtMatches("2026-10-10T07:59:59Z", handle)).toBe(false);
  });

  it("任一方缺失或无法解析都判为不一致", () => {
    const handle = new Date("2026-10-10T08:00:00Z");
    expect(startedAtMatches(null, handle)).toBe(false);
    expect(startedAtMatches("2026-10-10T08:00:00Z", null)).toBe(false);
    expect(startedAtMatches("yesterday", handle)).toBe(false);
  });
});
