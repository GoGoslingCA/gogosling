// 任务进展仅来自真实消息、工具调用和运行状态；不把预设计划当成执行记录。
import { useTranslation } from "react-i18next";
import { memo, useCallback, useLayoutEffect, useRef, useState } from "react";
import { Icon } from "../../components/Icon";
import type { Message, RunStatus, ToolCall, ToolResult } from "../../api/types";
import type { LiveRun } from "../../state/liveRuns";
import { ToolHistoryPage } from "./ToolHistoryPage";
import { indexToolResults } from "./toolResults";
import "../../components/compact-workbench.css";

interface TaskProgressProps {
  messages: Message[]; activeStatus: RunStatus | null | undefined; latestStatus?: RunStatus;
  activeRunId?: string | null; latestRunId?: string; statusPending?: boolean; statusFailed?: boolean;
  liveRuns: LiveRun[]; onRetry: () => void;
}

function latestMessage(messages: Message[], matches: (message: Message) => boolean): Message | undefined {
  for (let index = messages.length - 1; index >= 0; index--) if (matches(messages[index]!)) return messages[index];
  return undefined;
}

export function TaskProgress(props: TaskProgressProps) {
  const retry = useRef(props.onRetry);
  useLayoutEffect(() => { retry.current = props.onRetry; }, [props.onRetry]);
  const onRetry = useCallback(() => retry.current(), []);
  return <TaskProgressContent {...props} onRetry={onRetry} />;
}

const TaskProgressContent = memo(function TaskProgressContent({ messages, activeStatus, activeRunId, latestStatus, latestRunId, statusPending = false, statusFailed = false, liveRuns, onRetry }: TaskProgressProps) {
  const { t } = useTranslation();
  const [historyOpen, setHistoryOpen] = useState(false);
  const goal = messages.find((message) => message.role === "user")?.parts.filter((part) => part.type === "text").map((part) => part.text).join("\n");
  const lastRequest = latestMessage(messages, (message) => message.role === "user")?.parts.filter((part) => part.type === "text").map((part) => part.text).join("\n");
  const live = liveRuns.at(-1);
  const latestRunMessage = latestRunId ? latestMessage(messages, (message) => message.run_id === latestRunId) : undefined;
  // 新终态可能先于消息落库；反之，尚未清理的旧 live 也不能盖过后来落库的新轮次。
  const persistedIsNewer = live && latestRunMessage && latestRunId !== live.runId
    && Date.parse(latestRunMessage.created_at) > Date.parse(live.startedAt);
  const status = activeStatus !== undefined ? activeStatus ?? "running" : persistedIsNewer ? latestStatus : live?.status ?? latestStatus;
  const { key: toolKey, results } = indexToolResults(messages, liveRuns);
  const steps = new Map<string, { call: ToolCall; result?: ToolResult; runId?: string }>();
  for (const message of messages) for (const part of message.parts) if (part.tool_call) {
    const key = toolKey(part.tool_call.id, message.run_id);
    steps.set(key, { call: part.tool_call, result: results.get(key), runId: message.run_id });
  }
  for (const run of liveRuns) for (const step of run.toolCalls) {
    const key = toolKey(step.call.id, run.runId);
    steps.set(key, { call: step.call, result: step.result ?? steps.get(key)?.result, runId: run.runId });
  }
  const recordedSteps = [...steps.values()];
  const recentSteps = recordedSteps.slice(-4);
  const olderSteps = recordedSteps.slice(0, -4);
  const renderStep = ({ call, result, runId }: typeof recordedSteps[number]) => {
    const active = Boolean(activeRunId && runId === activeRunId && activeStatus !== undefined);
    const state = result ? result.is_error ? "error" : "done" : active ? activeStatus === "awaiting_approval" ? "approval" : "running" : "unknown";
    return <li key={toolKey(call.id, runId)} data-state={state} aria-current={state === "running" || state === "approval" ? "step" : undefined}>
      <span className="trail-marker"><Icon name={state === "done" ? "check" : state === "error" ? "close" : state === "approval" ? "shield" : "clock"} size={14} /></span>
      <span className="trail-content"><span className="trail-name" title={call.name}>{t(`tools.${call.name}`, { defaultValue: call.name })}</span><small>{t(result ? result.is_error ? "chat.toolError" : "chat.toolDone" : active ? state === "approval" ? "work.state_awaiting_approval" : "chat.toolRunning" : "work.noStepResult")}</small></span>
    </li>;
  };
  const resultReady = status === "completed";
  const interrupted = status === "failed" || status === "cancelled";
  return <section className={`task-progress ${interrupted ? "interrupted" : ""}`} aria-label={t("work.taskProgress")}>
    <div className="progress-summary" role="status">
      <span className={`progress-symbol ${resultReady ? "complete" : ""}`}><Icon name={resultReady ? "check" : interrupted ? "clock" : "spark"} size={15} /></span>
      <span>{t(status ? `work.state_${status}` : statusPending ? "work.state_restoring" : statusFailed ? "work.state_unknown" : "work.state_ready")}</span>
      {steps.size ? <span className="progress-step-count">{t("compact.recordedSteps", { n: steps.size })}</span> : null}
      {interrupted ? <button type="button" className="btn ghost sm" onClick={onRetry}>{t("work.prepareRetry")}<Icon name="arrow" size={14} /></button> : null}
    </div>
    {recentSteps.length ? <ol className="execution-trail" aria-label={t("compact.executionTrail")}>{recentSteps.map(renderStep)}</ol> : null}
    {goal || steps.size ? <details className="progress-details" onToggle={event => setHistoryOpen(event.currentTarget.open)}>
      <summary><Icon name="chevron" size={12} /><span>{t("compact.goalAndHistory")}</span>{olderSteps.length ? <span className="muted">{t("compact.olderSteps", { n: olderSteps.length })}</span> : null}</summary>
      {goal ? <p className="task-goal"><strong>{t("work.taskGoal")}</strong>{goal}</p> : null}
      {lastRequest && lastRequest !== goal ? <p className="task-goal"><strong>{t("work.latestRequest")}</strong>{lastRequest}</p> : null}
      {historyOpen && olderSteps.length ? <div aria-label={t("compact.earlierRecords")}><ToolHistoryPage items={olderSteps} renderItem={renderStep} listClassName="execution-history" /></div> : !steps.size ? <p className="muted">{t("work.noToolSteps")}</p> : null}
    </details> : null}
  </section>;
}, (previous, next) => previous.messages === next.messages
  && previous.activeStatus === next.activeStatus && previous.activeRunId === next.activeRunId
  && previous.latestStatus === next.latestStatus && previous.latestRunId === next.latestRunId
  && previous.statusPending === next.statusPending && previous.statusFailed === next.statusFailed
  && previous.onRetry === next.onRetry && previous.liveRuns.length === next.liveRuns.length
  && previous.liveRuns.every((run, index) => {
    const other = next.liveRuns[index]!;
    return run.runId === other.runId && run.status === other.status && run.startedAt === other.startedAt && run.toolCalls === other.toolCalls;
  }));
