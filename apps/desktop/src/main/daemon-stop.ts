// 停止守护进程的流程（XP-07）：先经鉴权的 POST /api/v1/system/shutdown 请求优雅关闭并等它自己退出；
// 关闭端点不可用（连不上、非 202）或等待超时，才回退到 POSIX 的 SIGTERM → SIGKILL 或 Windows 的
// taskkill /T /F。Windows 上没有能送达无窗口控制台进程的温和信号（不带 /F 的 taskkill 必然失败），
// 所以那里的回退直接强制结束，不再先白等一轮。
//
// 身份规则（与 CLI 的 `gogosling daemon stop` 一致，见 XP-06）：
//   - 关闭请求只发给身份已确认的守护进程：句柄里的令牌让 GET /api/v1/version 返回 200，且 started_at
//     与句柄一致；对本壳的子进程还要求句柄里的 pid 就是子进程的 pid。
//   - 信号只发给本壳 spawn 的子进程（经 ChildProcess 对象 / 它的 pid），从不按 daemon.json 里的 pid 发。
//   - 不是本壳启动的守护进程只做身份确认：通过就让它继续运行（退出壳不终止别人启动的实例）；
//     令牌被拒、started_at 不一致或端点连不上，就把 daemon.json 当残留文件删除，不发任何信号。
//   - 子进程确认退出后，仍写着它 pid 的 daemon.json 也是残留（被强制结束时守护进程来不及删），一并删除，
//     令牌不留在磁盘上。
//
// 本文件不触碰 electron、child_process 与真实网络：副作用经 StopIO / OwnedProcess 注入，
// 由 daemon.ts 组装，vitest 直接覆盖每个分支。

import {
  handleFilePath,
  parseHandle,
  parseVersionStartedAt,
  shutdownUrl,
  startedAtMatches,
  versionUrl,
  type DaemonHandle,
} from "./daemon-logic.js";
import type { Logger } from "./log.js";

export interface ProbeRequest {
  /** 默认 GET。 */
  method?: "GET" | "POST";
  headers?: Record<string, string>;
  timeoutMs: number;
  /** 为 true 时读取响应体文本（实现应设上限）；否则丢弃响应体。 */
  readBody?: boolean;
}

export interface ProbeResponse {
  status: number;
  /** 只有 readBody 时才有。 */
  body?: string;
}

/** 网络错误是否为超时（AbortSignal.timeout 触发的 TimeoutError，或被中止的 AbortError）。 */
export function isTimeoutError(err: unknown): boolean {
  const name = (err as { name?: unknown } | null)?.name;
  return name === "TimeoutError" || name === "AbortError";
}

/** 停止流程需要的副作用。 */
export interface StopIO {
  /** 网络错误抛出；超时错误满足 isTimeoutError。 */
  fetch(url: string, init: ProbeRequest): Promise<ProbeResponse>;
  /** 读 UTF-8 文本；不存在时抛出。 */
  readFile(file: string): Promise<string>;
  /** 删除文件；不存在视为成功。 */
  removeFile(file: string): Promise<void>;
  /** 可被 signal 提前结束的等待（提前结束时正常 resolve）。 */
  sleep(ms: number, signal?: AbortSignal): Promise<void>;
  logger: Logger;
}

export interface StopTimings {
  /** 单次 HTTP 请求（身份确认、关闭请求）的超时。 */
  probeTimeoutMs: number;
  /** 关闭请求被接受（202）后等待进程退出的上限；守护进程最坏约 20s（预算）+10s（引擎）+5s。 */
  gracefulTimeoutMs: number;
  /** 回退到 SIGTERM 后等待退出的上限；强制结束之后也最多再等这么久。 */
  termTimeoutMs: number;
}

/**
 * 身份确认结果：
 *   verified    令牌被接受且 started_at 与句柄一致；
 *   mismatch    令牌被拒（401/403）、端点不是 gogoslingd（404）或 started_at 不一致——句柄已失效；
 *   unreachable 端点连不上（没有进程在监听）——句柄已失效；
 *   unknown     超时或其他状态码——无法判断，保守处理。
 */
export type IdentityVerdict = "verified" | "mismatch" | "unreachable" | "unknown";

/** accepted：202；rejected：其他状态码（例如旧版本的 404）；unreachable：网络错误或超时。 */
export type ShutdownResult = "accepted" | "rejected" | "unreachable";

function bearer(handle: DaemonHandle): Record<string, string> {
  return { Authorization: `Bearer ${handle.token}` };
}

/** 用句柄里的令牌请求 GET /api/v1/version，确认端点上的守护进程就是写出这份句柄的那一个。 */
export async function verifyIdentity(handle: DaemonHandle, io: Pick<StopIO, "fetch">, timeoutMs: number): Promise<IdentityVerdict> {
  let res: ProbeResponse;
  try {
    res = await io.fetch(versionUrl(handle.endpoint), { headers: bearer(handle), timeoutMs, readBody: true });
  } catch (err) {
    return isTimeoutError(err) ? "unknown" : "unreachable";
  }
  if (res.status === 200) return startedAtMatches(parseVersionStartedAt(res.body), handle.startedAt) ? "verified" : "mismatch";
  if (res.status === 401 || res.status === 403 || res.status === 404) return "mismatch";
  return "unknown";
}

/** POST /api/v1/system/shutdown；返回 202 才算接受。 */
export async function requestShutdown(
  handle: DaemonHandle,
  io: Pick<StopIO, "fetch">,
  timeoutMs: number,
): Promise<{ result: ShutdownResult; status?: number }> {
  try {
    const res = await io.fetch(shutdownUrl(handle.endpoint), { method: "POST", headers: bearer(handle), timeoutMs });
    return { result: res.status === 202 ? "accepted" : "rejected", status: res.status };
  } catch {
    return { result: "unreachable" };
  }
}

/** 本壳 spawn 的守护进程。 */
export interface OwnedProcess {
  pid: number;
  /** 进程退出（或 spawn 报错）时 resolve。 */
  exited: Promise<unknown>;
  /** 温和终止（POSIX SIGTERM）；平台没有能送达守护进程的温和信号时为 null（Windows）。 */
  terminate: (() => Promise<void>) | null;
  /** 强制终止（POSIX SIGKILL；Windows taskkill /PID <pid> /T /F）。 */
  forceKill: () => Promise<void>;
}

export type StopSignal = "term" | "kill";

export interface OwnedStopReport {
  /** 句柄的身份确认结果；没有属于该子进程的句柄（尚未就绪）时为 no_handle。 */
  identity: IdentityVerdict | "no_handle";
  /** 关闭请求的结果；身份未确认时不发请求（skipped）。 */
  shutdown: ShutdownResult | "skipped";
  /** 关闭请求的 HTTP 状态码（有响应时）。 */
  shutdownStatus?: number;
  /** 实际发出的信号，按顺序。 */
  signals: StopSignal[];
  /** 是否确认进程已退出（强制结束后仍未退出时为 false）。 */
  exited: boolean;
  /** 是否删除了残留的 daemon.json。 */
  handleRemoved: boolean;
}

/**
 * 停止本壳启动的守护进程。
 *
 * handle 是壳持有的连接所来自的句柄（daemon.json 的 endpoint + token）；它的 pid 必须等于子进程 pid
 * 才会被用来请求关闭。流程：
 *   身份确认通过 → POST shutdown → 202 → 等待退出（gracefulTimeoutMs）→ 超时则强制结束；
 *   身份未确认、端点不可用或非 202 → POSIX SIGTERM 等 termTimeoutMs 再 SIGKILL；Windows 直接 /T /F。
 * 进程确认退出后删除仍写着它 pid 的 daemon.json。
 */
export async function stopOwnedDaemon(
  proc: OwnedProcess,
  handle: DaemonHandle | null,
  dataDir: string,
  io: StopIO,
  timings: StopTimings,
): Promise<OwnedStopReport> {
  const report: OwnedStopReport = { identity: "no_handle", shutdown: "skipped", signals: [], exited: false, handleRemoved: false };
  const pid = proc.pid;
  const own = handle && handle.pid === pid ? handle : null;

  if (own) {
    report.identity = await verifyIdentity(own, io, timings.probeTimeoutMs);
    if (report.identity === "verified") {
      const shut = await requestShutdown(own, io, timings.probeTimeoutMs);
      report.shutdown = shut.result;
      report.shutdownStatus = shut.status;
      if (shut.result === "accepted") {
        report.exited = await waitExit(proc.exited, timings.gracefulTimeoutMs, io);
        if (!report.exited) {
          io.logger.warn("守护进程接受了关闭请求但未在时限内退出，强制终止", { pid, timeout_ms: timings.gracefulTimeoutMs });
        }
      } else {
        io.logger.warn("关闭端点不可用，回退到信号", { pid, result: shut.result, status: shut.status });
      }
    } else {
      io.logger.warn("无法确认 daemon.json 的身份，不经关闭端点，直接终止子进程", { pid, identity: report.identity });
    }
  }

  if (!report.exited) {
    // 关闭请求已被接受时守护进程已经在走同一条优雅关闭路径，再发 SIGTERM 没有意义，超时直接强制结束。
    if (report.shutdown !== "accepted" && proc.terminate) {
      report.signals.push("term");
      await proc.terminate();
      report.exited = await waitExit(proc.exited, timings.termTimeoutMs, io);
      if (!report.exited) io.logger.warn("守护进程未在超时内响应 SIGTERM，强制终止", { pid, timeout_ms: timings.termTimeoutMs });
    }
    if (!report.exited) {
      report.signals.push("kill");
      await proc.forceKill();
      report.exited = await waitExit(proc.exited, timings.termTimeoutMs, io);
      if (!report.exited) io.logger.error("强制终止后守护进程仍未退出", { pid, timeout_ms: timings.termTimeoutMs });
    }
  }

  if (report.exited) {
    report.handleRemoved = await removeHandleIf(dataDir, io, (h) => h.pid === pid && (!own || h.token === own.token));
  }
  return report;
}

export interface ForeignReleaseReport {
  /** daemon.json 不存在或无法解析时为 no_handle。 */
  identity: IdentityVerdict | "no_handle";
  handleRemoved: boolean;
}

/**
 * 处理不是本壳启动的守护进程（复用的实例，或只剩 daemon.json 的残留）：只确认身份，
 * 绝不请求关闭、绝不发信号。确认通过就让它继续运行；mismatch / unreachable 说明句柄是残留文件，
 * 删除它（删除前再读一次，只删仍是同一份的句柄）；unknown 时保留原样。
 */
export async function releaseForeignDaemon(dataDir: string, io: StopIO, timings: Pick<StopTimings, "probeTimeoutMs">): Promise<ForeignReleaseReport> {
  const handle = await readHandleFile(dataDir, io);
  if (!handle) return { identity: "no_handle", handleRemoved: false };
  const identity = await verifyIdentity(handle, io, timings.probeTimeoutMs);
  if (identity !== "mismatch" && identity !== "unreachable") return { identity, handleRemoved: false };
  const handleRemoved = await removeHandleIf(dataDir, io, (h) => h.pid === handle.pid && h.token === handle.token);
  if (handleRemoved) io.logger.info("daemon.json 身份确认失败，按残留文件删除（未发送任何信号）", { pid: handle.pid, identity });
  return { identity, handleRemoved };
}

/** 等进程退出，最多 timeoutMs；返回是否已退出。退出后取消计时器。 */
async function waitExit(exited: Promise<unknown>, timeoutMs: number, io: Pick<StopIO, "sleep">): Promise<boolean> {
  const timer = new AbortController();
  try {
    return await Promise.race([exited.then(() => true), io.sleep(timeoutMs, timer.signal).then(() => false)]);
  } finally {
    timer.abort();
  }
}

async function readHandleFile(dataDir: string, io: Pick<StopIO, "readFile">): Promise<DaemonHandle | null> {
  let text: string;
  try {
    text = await io.readFile(handleFilePath(dataDir));
  } catch {
    return null;
  }
  const parsed = parseHandle(text);
  return parsed.ok ? parsed.handle : null;
}

/** 句柄文件仍满足 match 时删除；损坏的文件无法比较，留给下一次启动覆盖。 */
async function removeHandleIf(dataDir: string, io: StopIO, match: (handle: DaemonHandle) => boolean): Promise<boolean> {
  const current = await readHandleFile(dataDir, io);
  if (!current || !match(current)) return false;
  const file = handleFilePath(dataDir);
  try {
    await io.removeFile(file);
    return true;
  } catch (err) {
    io.logger.warn("删除残留的 daemon.json 失败", { file, err });
    return false;
  }
}
