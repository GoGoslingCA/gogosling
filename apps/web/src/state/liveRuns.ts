// 消息流 reducer：把 SSE 上的运行事件按 run_id 聚合成"正在生成"的视图状态。
//
// 纯函数、无副作用，可单独测试。持久化的消息来自 GET /sessions/{id}/messages（react-query），
// 这里只保存尚未落库的增量：流式文本、思考内容（reasoning.delta，不进入最终消息）、进行中的工具调用、
// 等待中的审批、用量、终态与错误。
//
// 用量口径（api/worker-rpc/protocol.md §3）：每个 usage 事件是一次模型调用的增量，这里累加；
// run.completed 携带整次运行的累计值，直接采用。
//
// 增量状态的处理规则：
//   - 以服务端 Seq 做幂等（同一运行收到的旧事件直接丢弃），不用 content.startsWith(prev) 之类的启发式对账；
//   - 一个运行一个条目，工具严格按实际 call.id 对齐，不创建 idx-<n> 回退键；
//   - 断线丢事件（gap）显式标记 truncated，由界面提示并在终态后用持久化消息替换。

import type { ApiEvent, ApprovalRequest, RunEvent, RunStatus, ToolCall, ToolResult, Usage } from "../api/types";
import { isRunStreamEvent } from "../api/types";

export interface LiveToolCall {
  call: ToolCall;
  result?: ToolResult;
  startedAt: string;
  completedAt?: string;
}

export interface LiveRun {
  runId: string;
  sessionId: string;
  agentId: string;
  status: RunStatus;
  /** 已拼接的流式文本。 */
  text: string;
  /** 已拼接的思考内容（reasoning.delta）；只在实时视图展示，不进入持久化消息。 */
  reasoning: string;
  toolCalls: LiveToolCall[];
  /** 等待用户决定的审批；resolved 后清空。 */
  approval: ApprovalRequest | null;
  /** 运行中为各次模型调用增量之和；终态后为 run.completed 给出的累计值。 */
  usage: Usage | null;
  error: string | null;
  /** 曾经发生过事件缺口；text 可能不完整。 */
  truncated: boolean;
  /** 最后处理的事件 Seq，用于幂等。 */
  lastSeq: number;
  startedAt: string;
  endedAt: string | null;
}

export interface LiveRunsState {
  runs: Record<string, LiveRun>;
  /** session_id -> run_id 列表（按出现顺序）。 */
  bySession: Record<string, string[]>;
}

export type LiveRunsAction =
  | { type: "event"; event: ApiEvent }
  | { type: "gap" }
  | { type: "forget"; runId: string }
  | { type: "reset" };

export const initialLiveRuns: LiveRunsState = { runs: {}, bySession: {} };

export function isTerminal(status: RunStatus): boolean {
  return status === "completed" || status === "failed" || status === "cancelled";
}

function newRun(ev: RunEvent, seq: number): LiveRun {
  return {
    runId: ev.run_id,
    sessionId: ev.session_id,
    agentId: ev.agent_id,
    status: ev.type === "message.created" && ev.message?.role === "user" ? "queued" : "running",
    text: "",
    reasoning: "",
    toolCalls: [],
    approval: null,
    usage: null,
    error: null,
    truncated: false,
    lastSeq: seq,
    startedAt: ev.at,
    endedAt: null,
  };
}

function withRun(state: LiveRunsState, run: LiveRun): LiveRunsState {
  const existing = state.bySession[run.sessionId] ?? [];
  const bySession = existing.includes(run.runId)
    ? state.bySession
    : { ...state.bySession, [run.sessionId]: [...existing, run.runId] };
  return { runs: { ...state.runs, [run.runId]: run }, bySession };
}

const USAGE_FIELDS = ["input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"] as const;

// 派生索引不持有数组或运行的强引用。更新后转移到新数组，旧快照分叉时重新构建，
// 因而不会让旧状态读到未来工具，也不会改变 reducer 的结果或不可变快照。
const toolPositions = new WeakMap<LiveToolCall[], Map<string, number>>();

function updateToolCall(calls: LiveToolCall[], id: string, create: () => LiveToolCall, update: (call: LiveToolCall) => LiveToolCall): LiveToolCall[] {
  let positions = toolPositions.get(calls);
  if (!positions) {
    positions = new Map();
    for (let i = 0; i < calls.length; i++) {
      const entry = calls[i]!;
      if (!positions.has(entry.call.id)) positions.set(entry.call.id, i);
    }
  }
  const index = positions.get(id);
  const next = calls.slice();
  if (index === undefined) {
    positions.set(id, next.length);
    next.push(create());
  } else {
    next[index] = update(calls[index]!);
  }
  toolPositions.delete(calls);
  toolPositions.set(next, positions);
  return next;
}

/** 把一次模型调用的用量增量加到已有用量上；delta 缺失时原样返回。 */
export function addUsage(base: Usage | null, delta: Usage | undefined): Usage | null {
  if (!delta) return base;
  const out: Usage = { ...(base ?? {}) };
  for (const key of USAGE_FIELDS) {
    const add = delta[key];
    if (typeof add === "number" && Number.isFinite(add)) out[key] = (out[key] ?? 0) + add;
  }
  return out;
}

function applyRunEvent(run: LiveRun, ev: RunEvent, seq: number): LiveRun {
  const next: LiveRun = { ...run, lastSeq: seq };
  switch (ev.type) {
    case "run.started":
      next.status = "running";
      next.startedAt = ev.at;
      return next;
    case "text.delta":
      next.text = run.text + (ev.delta ?? "");
      if (run.status === "awaiting_approval") next.status = "running";
      return next;
    case "reasoning.delta":
      next.reasoning = run.reasoning + (ev.delta ?? "");
      if (run.status === "awaiting_approval") next.status = "running";
      return next;
    case "tool.call.started": {
      if (!ev.tool_call) return next;
      const call = ev.tool_call;
      next.toolCalls = updateToolCall(run.toolCalls, call.id,
        () => ({ call, startedAt: ev.at }),
        (entry) => ({ ...entry, call }));
      return next;
    }
    case "tool.call.completed": {
      if (!ev.tool_result) return next;
      const result = ev.tool_result;
      next.toolCalls = updateToolCall(run.toolCalls, result.call_id,
        // 没看到 started（例如在缺口中丢失）：补一条占位，名称未知。
        () => ({ call: { id: result.call_id, name: "", arguments: {} }, result, startedAt: ev.at, completedAt: ev.at }),
        (entry) => ({ ...entry, result, completedAt: ev.at }));
      if (run.status === "awaiting_approval") next.status = "running";
      return next;
    }
    case "approval.requested":
      next.approval = ev.approval ?? null;
      next.status = "awaiting_approval";
      return next;
    case "approval.resolved":
      next.approval = null;
      if (run.status === "awaiting_approval") next.status = "running";
      return next;
    case "usage":
      // 增量：多步工具回路里每步一次，累加才是到目前为止的总量。
      next.usage = addUsage(run.usage, ev.usage);
      return next;
    case "message.created":
      return next;
    case "run.completed":
      next.status = "completed";
      // 终态携带整次运行的累计值，直接采用。
      next.usage = ev.usage ?? run.usage;
      next.approval = null;
      next.endedAt = ev.at;
      // 终态携带的最终消息包含全部文本；若流式文本有缺口，用它补全。
      if (ev.message) {
        const text = ev.message.parts.filter((p) => p.type === "text").map((p) => p.text ?? "").join("");
        if (text) next.text = text;
      }
      return next;
    case "run.failed":
      next.status = "failed";
      next.error = ev.error ?? "";
      next.approval = null;
      next.endedAt = ev.at;
      return next;
    case "run.cancelled":
      next.status = "cancelled";
      next.approval = null;
      next.endedAt = ev.at;
      return next;
    default:
      return next;
  }
}

/** 纯 reducer。 */
export function liveRunsReducer(state: LiveRunsState, action: LiveRunsAction): LiveRunsState {
  switch (action.type) {
    case "reset":
      return initialLiveRuns;
    case "forget": {
      const run = state.runs[action.runId];
      if (!run) return state;
      const runs = { ...state.runs };
      delete runs[action.runId];
      const list = (state.bySession[run.sessionId] ?? []).filter((id) => id !== action.runId);
      const bySession = { ...state.bySession };
      if (list.length === 0) delete bySession[run.sessionId];
      else bySession[run.sessionId] = list;
      return { runs, bySession };
    }
    case "gap": {
      // 缺口之后无法判断丢了什么：所有未结束的运行标记为 truncated。
      let changed = false;
      const runs: Record<string, LiveRun> = {};
      for (const [id, run] of Object.entries(state.runs)) {
        if (!isTerminal(run.status) && !run.truncated) {
          runs[id] = { ...run, truncated: true };
          changed = true;
        } else {
          runs[id] = run;
        }
      }
      return changed ? { ...state, runs } : state;
    }
    case "event": {
      const ev = action.event;
      if (!isRunStreamEvent(ev)) return state;
      const payload = ev.payload;
      if (!payload.run_id || !payload.session_id) return state;
      const existing = state.runs[payload.run_id];
      if (existing) {
        if (ev.seq !== 0 && ev.seq <= existing.lastSeq) return state; // 重复/乱序事件
        if (isTerminal(existing.status)) return state; // 终态之后的事件忽略
        return withRun(state, applyRunEvent(existing, payload, ev.seq));
      }
      // 第一次见到这个运行：中途加入（例如页面刷新后）也要能跟上。
      const fresh = newRun(payload, ev.seq);
      if (payload.type !== "run.started")
        fresh.truncated = payload.type === "text.delta" || payload.type === "reasoning.delta" || payload.type === "tool.call.completed" || payload.type === "usage";
      return withRun(state, applyRunEvent(fresh, payload, ev.seq));
    }
    default:
      return state;
  }
}

/** 某个会话的运行列表（按出现顺序）。 */
export function selectSessionRuns(state: LiveRunsState, sessionId: string): LiveRun[] {
  const ids = state.bySession[sessionId] ?? [];
  const out: LiveRun[] = [];
  for (const id of ids) {
    const run = state.runs[id];
    if (run) out.push(run);
  }
  return out;
}

/** 某个会话里仍在进行（非终态）的运行；同一会话最多一个。 */
export function selectActiveRun(state: LiveRunsState, sessionId: string): LiveRun | null {
  const runs = selectSessionRuns(state, sessionId);
  for (let i = runs.length - 1; i >= 0; i--) {
    const run = runs[i];
    if (run && !isTerminal(run.status)) return run;
  }
  return null;
}
