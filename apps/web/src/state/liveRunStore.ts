// 按会话订阅实时视图；批量合并展示增量，持久化消息仍由内核负责。
import type { ApiEvent } from "../api/types";
import { isRunStreamEvent } from "../api/types";
import { initialLiveRuns, isTerminal, liveRunsReducer, selectSessionRuns, type LiveRun, type LiveRunsAction } from "./liveRuns";

export interface SessionLiveSnapshot { runs: LiveRun[]; active: LiveRun | null }
const EMPTY: SessionLiveSnapshot = { runs: [], active: null };

export class LiveRunStore {
  private state = initialLiveRuns;
  private listeners = new Map<string, Set<() => void>>();
  private snapshots = new Map<string, SessionLiveSnapshot>();
  private persisted = new Set<string>();
  private pending: ApiEvent[] = [];
  private lastPending = new Map<string, number>();
  private pendingSize = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private receivedSeq = 0;

  constructor(private readonly delay = () => typeof document !== "undefined" && document.hidden ? 300 : 80) {}

  subscribe = (sessionId: string | null, listener: () => void): (() => void) => {
    if (!sessionId) return () => undefined;
    const set = this.listeners.get(sessionId) ?? new Set<() => void>();
    set.add(listener); this.listeners.set(sessionId, set);
    return () => {
      set.delete(listener);
      if (!set.size) {
        this.listeners.delete(sessionId);
        for (const run of selectSessionRuns(this.state, sessionId)) this.prune(run);
        this.snapshots.delete(sessionId);
      }
    };
  };

  snapshot = (sessionId: string | null): SessionLiveSnapshot => {
    if (!sessionId) return EMPTY;
    const cached = this.snapshots.get(sessionId);
    if (cached) return cached;
    const runs = selectSessionRuns(this.state, sessionId);
    const value = runs.length ? { runs, active: [...runs].reverse().find(run => !isTerminal(run.status)) ?? null } : EMPTY;
    this.snapshots.set(sessionId, value);
    return value;
  };

  // Seq 在同一 SSE 实例内单调递增，排队前去重，避免合并后把重放的文本加两次。
  receive = (event: ApiEvent): void => {
    if (!isRunStreamEvent(event)) return;
    if (event.seq && event.seq <= this.receivedSeq) return;
    this.receivedSeq = Math.max(this.receivedSeq, event.seq);
    const { payload } = event;
    if (payload.type === "text.delta" || payload.type === "reasoning.delta") {
      const index = this.lastPending.get(payload.run_id);
      const previous = index === undefined ? undefined : this.pending[index];
      if (previous && isRunStreamEvent(previous) && previous.payload.type === payload.type) {
        this.pending[index!] = { ...event, payload: { ...payload, delta: (previous.payload.delta ?? "") + (payload.delta ?? "") } };
      } else {
        this.lastPending.set(payload.run_id, this.pending.length);
        this.pending.push(event);
      }
      this.pendingSize += payload.delta?.length ?? 0;
      // 即便页面计时器被系统节流，展示缓冲也有界；刷新不丢任何增量。
      if (this.pendingSize >= 65536 || this.pending.length >= 256) this.flush();
      else if (!this.timer) this.timer = setTimeout(this.flush, this.delay());
      return;
    }
    this.flush();
    if (payload.message?.role === "assistant") this.persisted.add(payload.run_id);
    this.apply([{ type: "event", event }]);
  };

  flush = (): void => {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
    const events = this.pending;
    this.pending = []; this.lastPending.clear(); this.pendingSize = 0;
    if (events.length) this.apply(events.map(event => ({ type: "event", event })));
  };

  dispatch = (action: LiveRunsAction): void => {
    this.flush();
    if (action.type === "reset") { this.receivedSeq = 0; this.persisted.clear(); }
    if (action.type === "forget") this.persisted.delete(action.runId);
    this.apply([action]);
  };

  restart = (): void => {
    this.flush(); this.receivedSeq = 0;
    // 新内核从 Seq 1 开始，旧运行仍要能接收恢复后的失败/取消事件。
    this.state = { ...this.state, runs: Object.fromEntries(Object.entries(this.state.runs).map(([id, run]) => [id, { ...run, lastSeq: 0 }])) };
    this.apply([{ type: "gap" }], true);
  };

  private prune(run: LiveRun): void {
    if (isTerminal(run.status) && this.persisted.has(run.runId) && !this.listeners.has(run.sessionId)) {
      this.state = liveRunsReducer(this.state, { type: "forget", runId: run.runId });
      this.persisted.delete(run.runId); this.snapshots.delete(run.sessionId);
    }
  }

  private apply(actions: LiveRunsAction[], force = false): void {
    const before = this.state;
    for (const action of actions) this.state = liveRunsReducer(this.state, action);
    for (const run of Object.values(this.state.runs)) this.prune(run);
    if (before === this.state && !force) return;
    for (const [id, listeners] of this.listeners) {
      const oldRuns = selectSessionRuns(before, id); const newRuns = selectSessionRuns(this.state, id);
      if (!force && oldRuns.length === newRuns.length && oldRuns.every((run, i) => run === newRuns[i])) continue;
      this.snapshots.delete(id);
      for (const listener of listeners) listener();
    }
    // 未订阅页面的快照仅按需创建，事件更新不能让缓存继续持有旧正文。
    for (const id of this.snapshots.keys()) if (!this.listeners.has(id)) this.snapshots.delete(id);
  }
}
