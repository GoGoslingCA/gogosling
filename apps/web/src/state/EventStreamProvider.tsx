// 事件流提供者：维护到 /api/v1/events 的长连接，把事件分发给
//   1) react-query 缓存失效（会话、消息、运行、审批、任务、版本、智能体、供应商、扩展）；
//   2) liveRuns reducer（流式文本、思考内容、工具调用、用量）；
//   3) 工作进程状态（worker.status）。
// 收到 gap、或 hello 显示守护进程换了实例时，全量刷新并把进行中的运行标记为 truncated。

import { createContext, useContext, useEffect, useMemo, useCallback, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { decodeApiEvent, EventStream, type EventStreamStatus } from "../api/sse";
import type { ApiEvent, WorkerStatus } from "../api/types";
import {
  isAgentEvent,
  isExtensionEvent,
  isGapEvent,
  isJobTriggeredEvent,
  isProviderEvent,
  isRunStreamEvent,
  isSessionLifecycleEvent,
  isWorkerStatusEvent,
} from "../api/types";
import { useApi } from "./ConnectionContext";
import { type LiveRun, type LiveRunsAction } from "./liveRuns";
import { LiveRunStore } from "./liveRunStore";
import { queryKeys } from "./queryKeys";

interface StreamStatusValue {
  status: EventStreamStatus;
  attempt: number;
  worker: WorkerStatus | null;
}

const StreamStatusContext = createContext<StreamStatusValue>({ status: "idle", attempt: 0, worker: null });
const LiveRunsContext = createContext<LiveRunStore>(new LiveRunStore());
const LiveRunsDispatchContext = createContext<(a: LiveRunsAction) => void>(() => undefined);

export function EventStreamProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [liveRuns] = useState(() => new LiveRunStore());
  const dispatch = liveRuns.dispatch;
  const [status, setStatus] = useState<EventStreamStatus>("idle");
  const [attempt, setAttempt] = useState(0);
  const [worker, setWorker] = useState<WorkerStatus | null>(null);
  const streamRef = useRef<EventStream | null>(null);

  useEffect(() => {
    const invalidate = (key: readonly unknown[]) => void queryClient.invalidateQueries({ queryKey: key });

    const refreshAll = () => {
      dispatch({ type: "gap" });
      void queryClient.invalidateQueries();
    };

    const handle = (ev: ApiEvent) => {
      if (isGapEvent(ev)) {
        refreshAll();
        return;
      }
      if (ev.type.startsWith("project.")) {
        invalidate(queryKeys.projects); invalidate(["workbench", "materials"]); invalidate(queryKeys.artifacts);
        return;
      }
      if (ev.type.startsWith("artifact.")) { invalidate(queryKeys.artifacts); return; }
      if (ev.type.startsWith("draft.")) { invalidate(queryKeys.drafts); return; }
      if (ev.type.startsWith("changeset.")) { invalidate(queryKeys.changes); invalidate(queryKeys.auditRoot); return; }
      if (ev.type.startsWith("approval.") && !(ev.payload && typeof ev.payload === "object" && "run_id" in ev.payload && ev.payload.run_id)) {
        invalidate(queryKeys.approvalsRoot); invalidate(queryKeys.changes); invalidate(queryKeys.auditRoot); return;
      }
      if (isWorkerStatusEvent(ev)) {
        setWorker(ev.payload);
        invalidate(queryKeys.version);
        invalidate(queryKeys.resources);
        return;
      }
      if (isSessionLifecycleEvent(ev)) {
        invalidate(queryKeys.sessionsRoot);
        invalidate(queryKeys.artifacts);
        if (ev.payload.id) invalidate(queryKeys.session(ev.payload.id));
        if (ev.type === "session.deleted" && ev.payload.id) {
          queryClient.removeQueries({ queryKey: queryKeys.messages(ev.payload.id) });
          queryClient.removeQueries({ queryKey: queryKeys.session(ev.payload.id) });
        }
        return;
      }
      if (isJobTriggeredEvent(ev)) {
        invalidate(queryKeys.jobsRoot);
        return;
      }
      if (isAgentEvent(ev)) {
        // 删除智能体会级联删除其会话、任务与通道（契约 Event 描述），一并失效。
        invalidate(queryKeys.agents);
        if (ev.type === "agent.deleted") {
          invalidate(queryKeys.workbench);
          invalidate(queryKeys.sessionsRoot);
          invalidate(queryKeys.sessionRoot);
          invalidate(queryKeys.jobsRoot);
          invalidate(queryKeys.channelsRoot);
        }
        return;
      }
      if (isProviderEvent(ev)) {
        invalidate(queryKeys.providers);
        return;
      }
      if (isExtensionEvent(ev)) {
        // 扩展列表、运行时状态（tools_changed）与智能体可用工具目录都可能变化。
        invalidate(queryKeys.extensionsRoot);
        invalidate(queryKeys.agents);
        return;
      }
      if (isRunStreamEvent(ev)) {
        liveRuns.receive(ev);
        // 只在执行状态改变时刷新槽位；文本增量不触发额外状态请求。
        if (["message.created", "run.started", "run.completed", "run.failed", "run.cancelled", "approval.requested", "approval.resolved"].includes(ev.payload.type)) invalidate(queryKeys.resources);
        const sessionId = ev.payload.session_id;
        switch (ev.payload.type) {
          case "message.created":
            if (sessionId) invalidate(queryKeys.messages(sessionId));
            invalidate(queryKeys.sessionsRoot);
            if (sessionId) invalidate(queryKeys.session(sessionId));
            break;
          case "run.completed":
          case "run.failed":
          case "run.cancelled":
            if (sessionId) invalidate(queryKeys.messages(sessionId));
            invalidate(queryKeys.sessionsRoot);
            if (sessionId) invalidate(queryKeys.session(sessionId));
            invalidate(queryKeys.approvalsRoot);
            invalidate(queryKeys.auditRoot);
            if (ev.payload.run_id) invalidate(queryKeys.run(ev.payload.run_id));
            break;
          case "approval.requested":
          case "approval.resolved":
            invalidate(queryKeys.approvalsRoot);
            invalidate(queryKeys.auditRoot);
            if (ev.payload.run_id) invalidate(queryKeys.run(ev.payload.run_id));
            break;
          default:
            break;
        }
      }
    };

    const stream = new EventStream({
      url: api.client.url("/api/v1/events"),
      headers: () => api.client.authHeaders(),
      onMessage: (msg) => {
        const ev = decodeApiEvent(msg);
        if (ev) handle(ev);
      },
      // 守护进程重启：旧实例的 Seq 与缓存全部作废，流已从 since=0 重连。
      onInstanceChange: () => { liveRuns.restart(); void queryClient.invalidateQueries(); },
      onStatus: (s, info) => {
        setStatus(s);
        setAttempt(info?.attempt ?? 0);
        if (s === "open") {
          // 每次（重新）连上都刷新一次关键列表：断线期间的变更可能已经超出回放窗口之外。
          invalidate(queryKeys.version);
          invalidate(queryKeys.approvalsRoot);
        }
      },
    });
    streamRef.current = stream;
    stream.start();
    return () => {
      stream.stop();
      streamRef.current = null;
      dispatch({ type: "reset" });
    };
  }, [api, queryClient, liveRuns, dispatch]);

  const statusValue = useMemo<StreamStatusValue>(() => ({ status, attempt, worker }), [status, attempt, worker]);

  return (
    <StreamStatusContext.Provider value={statusValue}>
      <LiveRunsDispatchContext.Provider value={dispatch}>
        <LiveRunsContext.Provider value={liveRuns}>{children}</LiveRunsContext.Provider>
      </LiveRunsDispatchContext.Provider>
    </StreamStatusContext.Provider>
  );
}

export function useStreamStatus(): StreamStatusValue {
  return useContext(StreamStatusContext);
}

export function useLiveRunsDispatch(): (a: LiveRunsAction) => void {
  return useContext(LiveRunsDispatchContext);
}

/** 某个会话的运行视图：全部（含终态）与当前进行中的。 */
export function useSessionLiveRuns(sessionId: string | null): { runs: LiveRun[]; active: LiveRun | null } {
  const store = useContext(LiveRunsContext);
  const subscribe = useCallback((listener: () => void) => store.subscribe(sessionId, listener), [store, sessionId]);
  const snapshot = useCallback(() => store.snapshot(sessionId), [store, sessionId]);
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
