// 仅选择当前会话的最新失败轮次；历史失败不能覆盖后来运行或恢复后的终态。
import type { Message, Run } from "../../api/types";
import type { LiveRun } from "../../state/liveRuns";

export interface TaskFailure {
  sessionId: string;
  runId: string;
  occurredAt?: string;
  detail: string;
}

export function latestTaskRunId(sessionId: string | null, messages: readonly Message[], liveRuns: readonly LiveRun[]): string | undefined {
  if (!sessionId) return undefined;
  const latest = messages.filter(message => message.session_id === sessionId && message.run_id).reduce<Message | undefined>((previous, message) => !previous || message.seq > previous.seq ? message : previous, undefined);
  // 同轮的第一条消息给出开始时间，避免较晚持久化的回复把新运行遮住。
  const first = latest && messages.filter(message => message.session_id === sessionId && message.run_id === latest.run_id).reduce((previous, message) => message.seq < previous.seq ? message : previous, latest);
  let id = latest?.run_id;
  const persistedStart = first ? Date.parse(first.created_at) : -Infinity;
  let started = persistedStart;
  let lastSeq = -Infinity;
  let ambiguousFailure = false;
  const known = new Set(messages.filter(message => message.session_id === sessionId).map(message => message.run_id));
  for (const run of liveRuns) {
    if (run.sessionId !== sessionId) continue;
    // 已知消息的顺序优先于时间戳；残留的旧流不能靠异常时间盖过新消息。
    if (known.has(run.runId)) continue;
    const time = Date.parse(run.startedAt);
    if (!id || (Number.isFinite(time) && (time > started || (time === started && run.lastSeq > lastSeq)))) {
      // 未落库的新终态可能与旧轮次同毫秒；不能把旧失败复用到新轮次。
      // 失败的先后无法确认时先不展示，等待已有消息查询完成，无需额外轮询。
      ambiguousFailure = !!latest && time === persistedStart && run.status === "failed";
      id = run.runId; started = time; lastSeq = run.lastSeq;
    }
  }
  return ambiguousFailure ? undefined : id;
}

export function resolveTaskFailure(sessionId: string | null, runId: string | undefined, liveRuns: readonly LiveRun[], persisted?: Run, activeRunId?: string | null): TaskFailure | null {
  if (!sessionId || !runId || activeRunId) return null;
  const live = liveRuns.find(run => run.sessionId === sessionId && run.runId === runId);
  if (live) {
    if (live.status !== "failed") return null;
    return { sessionId, runId, occurredAt: live.endedAt ?? undefined, detail: (live.error ?? "").slice(0, 2048) };
  }
  if (persisted?.id !== runId || persisted.session_id !== sessionId || persisted.status !== "failed") return null;
  return { sessionId, runId, occurredAt: persisted.ended_at, detail: (persisted.error ?? "").slice(0, 2048) };
}
