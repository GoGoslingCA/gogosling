// 按会话订阅实时视图；批量合并展示增量，持久化消息仍由内核负责。
import type { ApiEvent } from "../api/types";
import { isRunStreamEvent } from "../api/types";
import { initialLiveRuns, isTerminal, liveRunsReducer, selectSessionRuns, type LiveRun, type LiveRunsAction, type LiveRunsState } from "./liveRuns";

export interface SessionLiveSnapshot { runs: LiveRun[]; active: LiveRun | null }
const EMPTY: SessionLiveSnapshot = { runs: [], active: null };

export class LiveRunStore {
  // reducer 的不可变副本只覆盖本会话；历史后台会话不参与前台增量的复制和清理。
  private states = new Map<string, LiveRunsState>();
  private runSessions = new Map<string, string>();
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
    let subscribed = true;
    return () => {
      if (!subscribed) return;
      subscribed = false;
      set.delete(listener);
      if (!set.size) {
        this.listeners.delete(sessionId);
        this.prune(sessionId);
        this.snapshots.delete(sessionId);
      }
    };
  };

  snapshot = (sessionId: string | null): SessionLiveSnapshot => {
    if (!sessionId) return EMPTY;
    const cached = this.snapshots.get(sessionId);
    if (cached) return cached;
    const runs = selectSessionRuns(this.states.get(sessionId) ?? initialLiveRuns, sessionId);
    let active: LiveRun | null = null;
    for (let i = runs.length - 1; i >= 0; i--) {
      if (!isTerminal(runs[i]!.status)) { active = runs[i]!; break; }
    }
    const value = runs.length ? { runs, active } : EMPTY;
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
    for (const [id, state] of this.states) {
      this.states.set(id, { ...state, runs: Object.fromEntries(Object.entries(state.runs).map(([runId, run]) => [runId, { ...run, lastSeq: 0 }])) });
    }
    this.apply([{ type: "gap" }], true);
  };

  private prune(sessionId: string): void {
    const state = this.states.get(sessionId);
    if (!state || this.listeners.has(sessionId)) return;
    let runs = state.runs;
    for (const run of Object.values(state.runs)) {
      if (!isTerminal(run.status) || !this.persisted.has(run.runId)) continue;
      // 多条终态一起退出时仅复制一次，未落库的失败/取消内容仍保留。
      if (runs === state.runs) runs = { ...runs };
      delete runs[run.runId];
      this.runSessions.delete(run.runId); this.persisted.delete(run.runId);
    }
    if (runs !== state.runs) {
      const ids = state.bySession[sessionId]!.filter(id => runs[id]);
      if (ids.length) this.states.set(sessionId, { runs, bySession: { [sessionId]: ids } });
      else this.states.delete(sessionId);
      this.snapshots.delete(sessionId);
    }
  }

  private apply(actions: LiveRunsAction[], force = false): void {
    const before = new Map<string, LiveRunsState>();
    const update = (id: string, action: LiveRunsAction) => {
      const state = this.states.get(id) ?? initialLiveRuns;
      if (!before.has(id)) before.set(id, state);
      const next = liveRunsReducer(state, action);
      if (next.bySession[id]?.length) this.states.set(id, next);
      else this.states.delete(id);
      if (action.type === "event" && isRunStreamEvent(action.event) && next.runs[action.event.payload.run_id]) {
        this.runSessions.set(action.event.payload.run_id, id);
      } else if (action.type === "forget") this.runSessions.delete(action.runId);
    };
    for (const action of actions) {
      if (action.type === "reset") {
        for (const [id, state] of this.states) if (!before.has(id)) before.set(id, state);
        this.states.clear(); this.runSessions.clear();
      } else if (action.type === "gap") {
        for (const id of this.states.keys()) update(id, action);
      } else if (action.type === "forget") {
        const id = this.runSessions.get(action.runId);
        if (id) update(id, action);
      } else if (isRunStreamEvent(action.event)) {
        const { run_id, session_id } = action.event.payload;
        if (run_id && session_id) update(this.runSessions.get(run_id) ?? session_id, action);
      }
    }
    if (force) {
      // 实例切换会重置所有 lastSeq，也必须更新无订阅但曾被读取过的快照。
      this.snapshots.clear();
      for (const id of this.listeners.keys()) if (!before.has(id)) before.set(id, initialLiveRuns);
    }
    for (const [id, state] of before) {
      this.prune(id);
      if (!force && state === (this.states.get(id) ?? initialLiveRuns)) continue;
      this.snapshots.delete(id);
      for (const listener of this.listeners.get(id) ?? []) listener();
    }
  }
}
