// 停止流程（daemon-stop.ts）的分支测试：假的 HTTP / 文件 / 进程，不启动任何真实进程、不访问网络。

import { describe, expect, it } from "vitest";
import { handleFilePath, parseHandle, type DaemonHandle } from "../src/main/daemon-logic.js";
import {
  isTimeoutError,
  releaseForeignDaemon,
  stopOwnedDaemon,
  verifyIdentity,
  type OwnedProcess,
  type ProbeRequest,
  type ProbeResponse,
  type StopIO,
  type StopTimings,
} from "../src/main/daemon-stop.js";

const DATA_DIR = "/data";
const ENDPOINT = "http://127.0.0.1:46750";
const STARTED_AT = "2026-10-10T08:00:00Z";
const TIMINGS: StopTimings = { probeTimeoutMs: 10, gracefulTimeoutMs: 30_000, termTimeoutMs: 10_000 };

type Route = (req: { method: string; url: string; auth: string }) => ProbeResponse | Error;

class FakeIO implements StopIO {
  readonly files = new Map<string, string>();
  readonly requests: Array<{ method: string; url: string; auth: string }> = [];
  /** 真正等满（没有因进程退出而被取消）的等待时长。 */
  readonly elapsed: number[] = [];
  readonly logs: string[] = [];
  route: Route = () => new Error("ECONNREFUSED");

  readonly logger = {
    debug: (m: string) => this.logs.push(`debug ${m}`),
    info: (m: string) => this.logs.push(`info ${m}`),
    warn: (m: string) => this.logs.push(`warn ${m}`),
    error: (m: string) => this.logs.push(`error ${m}`),
  };

  fetch = async (url: string, init: ProbeRequest): Promise<ProbeResponse> => {
    const req = { method: init.method ?? "GET", url, auth: init.headers?.Authorization ?? "" };
    this.requests.push(req);
    const res = this.route(req);
    if (res instanceof Error) throw res;
    return res;
  };

  readFile = async (file: string): Promise<string> => {
    const text = this.files.get(file);
    if (text === undefined) throw new Error("ENOENT");
    return text;
  };

  removeFile = async (file: string): Promise<void> => {
    this.files.delete(file);
  };

  sleep = async (ms: number, signal?: AbortSignal): Promise<void> => {
    await new Promise<void>((resolve) => setImmediate(resolve));
    if (!signal?.aborted) this.elapsed.push(ms);
  };

  posts(): string[] {
    return this.requests.filter((r) => r.method === "POST").map((r) => r.url);
  }

  handleText(): string | undefined {
    return this.files.get(handleFilePath(DATA_DIR));
  }
}

/** 写入 daemon.json 并返回解析后的句柄。 */
function putHandle(io: FakeIO, pid: number, token: string, startedAt = STARTED_AT): DaemonHandle {
  const text = JSON.stringify({ pid, endpoint: ENDPOINT, token, version: "0.1.0", started_at: startedAt });
  io.files.set(handleFilePath(DATA_DIR), text);
  const parsed = parseHandle(text);
  if (!parsed.ok) throw new Error(parsed.reason);
  return parsed.handle;
}

function versionOk(startedAt = STARTED_AT): ProbeResponse {
  return { status: 200, body: JSON.stringify({ version: "0.1.0", started_at: startedAt, worker: { status: "ready" } }) };
}

function timeoutError(): Error {
  return Object.assign(new Error("The operation was aborted due to timeout"), { name: "TimeoutError" });
}

interface FakeProc {
  proc: OwnedProcess;
  calls: string[];
  exit(): void;
}

/** 假子进程：exitOnTerm / exitOnKill 控制它对两类信号的反应；win 时没有温和信号。 */
function fakeProc(pid: number, opts: { exitOnTerm?: boolean; exitOnKill?: boolean; win?: boolean } = {}): FakeProc {
  let resolveExit!: () => void;
  const exited = new Promise<void>((resolve) => {
    resolveExit = resolve;
  });
  const calls: string[] = [];
  const proc: OwnedProcess = {
    pid,
    exited,
    terminate: opts.win
      ? null
      : async () => {
          calls.push("term");
          if (opts.exitOnTerm ?? true) resolveExit();
        },
    forceKill: async () => {
      calls.push("kill");
      if (opts.exitOnKill ?? true) resolveExit();
    },
  };
  return { proc, calls, exit: () => resolveExit() };
}

/** 一个健康、令牌有效的守护进程：POST shutdown 返回 shutdownStatus，202 时 onShutdown 被调用。 */
function healthyDaemon(io: FakeIO, token: string, opts: { shutdownStatus?: number; onShutdown?: () => void; startedAt?: string } = {}): void {
  io.route = ({ method, url, auth }) => {
    if (auth !== `Bearer ${token}`) return { status: 401 };
    if (method === "GET" && url === `${ENDPOINT}/api/v1/version`) return versionOk(opts.startedAt);
    if (method === "POST" && url === `${ENDPOINT}/api/v1/system/shutdown`) {
      const status = opts.shutdownStatus ?? 202;
      if (status === 202) opts.onShutdown?.();
      return { status };
    }
    return { status: 404 };
  };
}

describe("stopOwnedDaemon：关闭端点优先", () => {
  it("身份确认通过且端点返回 202：等守护进程自己退出，不发任何信号", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    const p = fakeProc(5000);
    healthyDaemon(io, "tok-a", {
      onShutdown: () => {
        // 优雅关闭：守护进程自己删除 daemon.json 后退出。
        io.files.delete(handleFilePath(DATA_DIR));
        p.exit();
      },
    });

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ identity: "verified", shutdown: "accepted", shutdownStatus: 202, signals: [], exited: true, handleRemoved: false });
    expect(p.calls).toEqual([]);
    expect(io.requests.map((r) => `${r.method} ${r.url}`)).toEqual([`GET ${ENDPOINT}/api/v1/version`, `POST ${ENDPOINT}/api/v1/system/shutdown`]);
    // 两个请求都带句柄里的令牌。
    for (const r of io.requests) expect(r.auth).toBe("Bearer tok-a");
  });

  it("端点返回 404（旧版本守护进程）：回退到 SIGTERM", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    healthyDaemon(io, "tok-a", { shutdownStatus: 404 });
    const p = fakeProc(5000);

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ identity: "verified", shutdown: "rejected", shutdownStatus: 404, signals: ["term"], exited: true });
    expect(p.calls).toEqual(["term"]);
  });

  it("端点连接失败：回退到 SIGTERM；SIGTERM 无响应时超时再 SIGKILL", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    io.route = ({ method }) => (method === "GET" ? versionOk() : new Error("ECONNREFUSED"));
    const p = fakeProc(5000, { exitOnTerm: false });

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ shutdown: "unreachable", signals: ["term", "kill"], exited: true });
    expect(p.calls).toEqual(["term", "kill"]);
    expect(io.elapsed).toEqual([TIMINGS.termTimeoutMs]);
    // 被强制结束的守护进程来不及删句柄：由壳删除，令牌不留在磁盘上。
    expect(report.handleRemoved).toBe(true);
    expect(io.handleText()).toBeUndefined();
  });

  it("Windows 端点不可用：直接 taskkill /T /F（只有一次强制调用），随后删除残留句柄", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    healthyDaemon(io, "tok-a", { shutdownStatus: 503 });
    const p = fakeProc(5000, { win: true });

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ shutdown: "rejected", signals: ["kill"], exited: true, handleRemoved: true });
    expect(p.calls).toEqual(["kill"]);
    // 没有为一次必然失败的温和终止白等。
    expect(io.elapsed).toEqual([]);
  });

  it("端点接受了关闭但守护进程超时未退出：等满 30s 后直接强杀，不再补发 SIGTERM", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    healthyDaemon(io, "tok-a");
    const p = fakeProc(5000);

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ shutdown: "accepted", signals: ["kill"], exited: true, handleRemoved: true });
    expect(p.calls).toEqual(["kill"]);
    expect(io.elapsed).toEqual([30_000]);
    expect(io.logs).toContain("warn 守护进程接受了关闭请求但未在时限内退出，强制终止");
  });

  it("句柄身份确认失败（令牌被拒）：不向该端点请求关闭，只终止自己的子进程", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    healthyDaemon(io, "tok-other");
    const p = fakeProc(5000);

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ identity: "mismatch", shutdown: "skipped", signals: ["term"], exited: true });
    expect(io.posts()).toEqual([]);
  });

  it("started_at 与句柄不一致：视为身份不符，不请求关闭", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    healthyDaemon(io, "tok-a", { startedAt: "2026-10-10T09:00:00Z" });
    const p = fakeProc(5000);

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ identity: "mismatch", shutdown: "skipped", signals: ["term"] });
    expect(io.posts()).toEqual([]);
  });

  it("句柄的 pid 不是本子进程：不使用它的令牌，也不删除它", async () => {
    const io = new FakeIO();
    const foreign = putHandle(io, 4242, "tok-foreign");
    healthyDaemon(io, "tok-foreign");
    const p = fakeProc(5000);

    const report = await stopOwnedDaemon(p.proc, foreign, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ identity: "no_handle", shutdown: "skipped", signals: ["term"], exited: true, handleRemoved: false });
    expect(io.requests).toEqual([]);
    expect(io.handleText()).toBeDefined();
  });

  it("尚未就绪（没有句柄）：直接走信号；退出后删除它刚写出的句柄", async () => {
    const io = new FakeIO();
    putHandle(io, 5000, "tok-late");
    const p = fakeProc(5000);

    const report = await stopOwnedDaemon(p.proc, null, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ identity: "no_handle", signals: ["term"], handleRemoved: true });
    expect(io.requests).toEqual([]);
  });

  it("退出后 daemon.json 已被新实例改写（令牌不同）：不删除", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    healthyDaemon(io, "tok-a", {
      onShutdown: () => {
        putHandle(io, 5000, "tok-new");
        p.exit();
      },
    });
    const p = fakeProc(5000);

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ shutdown: "accepted", exited: true, handleRemoved: false });
    expect(io.handleText()).toContain("tok-new");
  });

  it("强制终止后仍不退出：报告 exited=false，不删除句柄", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 5000, "tok-a");
    io.route = () => new Error("ECONNREFUSED");
    const p = fakeProc(5000, { exitOnTerm: false, exitOnKill: false });

    const report = await stopOwnedDaemon(p.proc, handle, DATA_DIR, io, TIMINGS);

    expect(report).toMatchObject({ identity: "unreachable", signals: ["term", "kill"], exited: false, handleRemoved: false });
    expect(io.logs).toContain("error 强制终止后守护进程仍未退出");
  });
});

describe("releaseForeignDaemon：不是本壳启动的守护进程", () => {
  it("身份确认失败（令牌被拒）：只删除句柄，不请求关闭", async () => {
    const io = new FakeIO();
    putHandle(io, 4242, "tok-stale");
    healthyDaemon(io, "tok-live");

    const report = await releaseForeignDaemon(DATA_DIR, io, TIMINGS);

    expect(report).toEqual({ identity: "mismatch", handleRemoved: true });
    expect(io.handleText()).toBeUndefined();
    expect(io.posts()).toEqual([]);
  });

  it("端点连不上：句柄是残留文件，删除", async () => {
    const io = new FakeIO();
    putHandle(io, 4242, "tok-dead");

    const report = await releaseForeignDaemon(DATA_DIR, io, TIMINGS);

    expect(report).toEqual({ identity: "unreachable", handleRemoved: true });
    expect(io.posts()).toEqual([]);
  });

  it("started_at 不一致：删除句柄", async () => {
    const io = new FakeIO();
    putHandle(io, 4242, "tok-a", "2026-10-09T00:00:00Z");
    healthyDaemon(io, "tok-a");

    expect(await releaseForeignDaemon(DATA_DIR, io, TIMINGS)).toEqual({ identity: "mismatch", handleRemoved: true });
  });

  it("身份确认通过：让它继续运行，句柄保留、不请求关闭", async () => {
    const io = new FakeIO();
    putHandle(io, 4242, "tok-live");
    healthyDaemon(io, "tok-live");

    const report = await releaseForeignDaemon(DATA_DIR, io, TIMINGS);

    expect(report).toEqual({ identity: "verified", handleRemoved: false });
    expect(io.handleText()).toBeDefined();
    expect(io.posts()).toEqual([]);
  });

  it("超时无法判断：保留句柄", async () => {
    const io = new FakeIO();
    putHandle(io, 4242, "tok-a");
    io.route = () => timeoutError();

    expect(await releaseForeignDaemon(DATA_DIR, io, TIMINGS)).toEqual({ identity: "unknown", handleRemoved: false });
    expect(io.handleText()).toBeDefined();
  });

  it("没有句柄或句柄损坏：什么也不做", async () => {
    const io = new FakeIO();
    expect(await releaseForeignDaemon(DATA_DIR, io, TIMINGS)).toEqual({ identity: "no_handle", handleRemoved: false });
    io.files.set(handleFilePath(DATA_DIR), "{not json");
    expect(await releaseForeignDaemon(DATA_DIR, io, TIMINGS)).toEqual({ identity: "no_handle", handleRemoved: false });
    expect(io.handleText()).toBe("{not json");
    expect(io.requests).toEqual([]);
  });
});

describe("verifyIdentity / isTimeoutError", () => {
  it("按状态码与 started_at 归类", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 1, "tok");
    const cases: Array<[ProbeResponse | Error, string]> = [
      [versionOk(), "verified"],
      [versionOk("2026-10-10T08:00:01Z"), "mismatch"],
      [{ status: 200, body: "{}" }, "mismatch"],
      [{ status: 200 }, "mismatch"],
      [{ status: 401 }, "mismatch"],
      [{ status: 403 }, "mismatch"],
      [{ status: 404 }, "mismatch"],
      [{ status: 500 }, "unknown"],
      [timeoutError(), "unknown"],
      [new TypeError("fetch failed"), "unreachable"],
    ];
    for (const [res, want] of cases) {
      io.route = () => res;
      expect(await verifyIdentity(handle, io, 10), JSON.stringify(res)).toBe(want);
    }
  });

  it("真实格式：daemon.json 是纳秒精度（RFC3339Nano），/version 是秒精度——判为同一次启动", async () => {
    const io = new FakeIO();
    const handle = putHandle(io, 1, "tok", "2026-10-10T08:00:00.987654321Z");
    io.route = () => versionOk("2026-10-10T16:00:00+08:00");
    expect(await verifyIdentity(handle, io, 10)).toBe("verified");
  });

  it("句柄没有 started_at 时无法证明身份", async () => {
    const io = new FakeIO();
    io.files.set(handleFilePath(DATA_DIR), JSON.stringify({ pid: 1, endpoint: ENDPOINT, token: "tok" }));
    const parsed = parseHandle(io.files.get(handleFilePath(DATA_DIR))!);
    if (!parsed.ok) throw new Error(parsed.reason);
    io.route = () => versionOk();
    expect(await verifyIdentity(parsed.handle, io, 10)).toBe("mismatch");
  });

  it("识别 AbortSignal.timeout 与中止产生的错误", () => {
    expect(isTimeoutError(timeoutError())).toBe(true);
    expect(isTimeoutError(Object.assign(new Error("x"), { name: "AbortError" }))).toBe(true);
    expect(isTimeoutError(new TypeError("fetch failed"))).toBe(false);
    expect(isTimeoutError(null)).toBe(false);
  });
});
