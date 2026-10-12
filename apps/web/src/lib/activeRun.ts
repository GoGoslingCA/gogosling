// 会话"当前在途运行"的判定（纯函数）。
//
// 首选事件流聚合出的实时运行（liveRuns）。页面刷新、事件流 gap 或守护进程重启之后，实时视图里可能
// 没有这个运行——此时回退到服务端的 Session.active_run_id，并用 GET /runs/{id} 取到的状态确认它仍未结束，
// 这样 Composer 能显示"生成中"和取消按钮，而不是让用户发送后才得到 409 conflict。

import { ApiError } from "../api/client";
import type { Run, RunStatus } from "../api/types";
import { isTerminal, type LiveRun } from "../state/liveRuns";

export interface ActiveRunInput {
  /** 实时视图里该会话的在途运行（selectActiveRun）。 */
  live: LiveRun | null;
  /** 实时视图里该会话出现过的全部运行 ID（含终态）。 */
  liveRunIds: readonly string[];
  /** 服务端会话视图里的 active_run_id。 */
  sessionActiveRunId: string | undefined | null;
}

/**
 * 需要向服务端确认状态的运行 ID：实时视图里没有在途运行、会话报告了 active_run_id，
 * 且实时视图没见过它（见过就以实时视图为准——它可能已经终态，而会话列表还没刷新）。
 */
export function fallbackRunId(input: ActiveRunInput): string | null {
  if (input.live) return null;
  const id = input.sessionActiveRunId ?? null;
  if (!id || input.liveRunIds.includes(id)) return null;
  return id;
}

export interface ActiveRun {
  runId: string;
  /** 实时视图的状态，或 GET /runs/{id} 的状态；还在查询时为 null。 */
  status: RunStatus | null;
  source: "live" | "session";
}

/**
 * 综合判定在途运行。回退路径上：查询中按"在途"处理（服务端刚说过它没结束）；
 * 查到终态或 not_found 则视为没有在途运行。
 */
export function resolveActiveRun(input: ActiveRunInput, fetched: { data: Run | undefined; error: unknown }): ActiveRun | null {
  if (input.live) return { runId: input.live.runId, status: input.live.status, source: "live" };
  const id = fallbackRunId(input);
  if (!id) return null;
  if (ApiError.is(fetched.error, "not_found")) return null;
  const run = fetched.data && fetched.data.id === id ? fetched.data : undefined;
  if (run && isTerminal(run.status)) return null;
  return { runId: id, status: run?.status ?? null, source: "session" };
}
