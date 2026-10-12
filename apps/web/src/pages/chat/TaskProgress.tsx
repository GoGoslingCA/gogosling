// 任务进展仅来自真实消息、工具调用和运行状态；不把预设计划当成执行记录。
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/Icon";
import type { Message, RunStatus } from "../../api/types";
import type { LiveRun } from "../../state/liveRuns";
import "../../components/compact-workbench.css";

export function TaskProgress({ messages, activeStatus, activeRunId, latestStatus, latestRunId, statusPending = false, statusFailed = false, liveRuns, onRetry }: {
  messages: Message[]; activeStatus: RunStatus | null | undefined; latestStatus?: RunStatus;
  activeRunId?: string | null; latestRunId?: string; statusPending?: boolean; statusFailed?: boolean;
  liveRuns: LiveRun[]; onRetry: () => void;
}) {
  const { t } = useTranslation();
  const goal = messages.find((message) => message.role === "user")?.parts.filter((part) => part.type === "text").map((part) => part.text).join("\n");
  const lastRequest = [...messages].reverse().find((message) => message.role === "user")?.parts.filter((part) => part.type === "text").map((part) => part.text).join("\n");
  const live = liveRuns.at(-1);
  const latestRunMessage = latestRunId ? [...messages].reverse().find((message) => message.run_id === latestRunId) : undefined;
  // 新终态可能先于消息落库；反之，尚未清理的旧 live 也不能盖过后来落库的新轮次。
  const persistedIsNewer = live && latestRunMessage && latestRunId !== live.runId
    && Date.parse(latestRunMessage.created_at) > Date.parse(live.startedAt);
  const status = activeStatus !== undefined ? activeStatus ?? "running" : persistedIsNewer ? latestStatus : live?.status ?? latestStatus;
  const results = new Map(messages.flatMap((message) => message.parts.flatMap((part) => part.tool_result ? [[part.tool_result.call_id, part.tool_result] as const] : [])));
  const steps = new Map(messages.flatMap((message) => message.parts.flatMap((part) => part.tool_call ? [[part.tool_call.id, { call: part.tool_call, result: results.get(part.tool_call.id), runId: message.run_id }] as const] : [])));
  for (const run of liveRuns) for (const step of run.toolCalls) steps.set(step.call.id, { call: step.call, result: step.result ?? steps.get(step.call.id)?.result, runId: run.runId });
  const recordedSteps = [...steps.values()];
  const recentSteps = recordedSteps.slice(-4);
  const olderSteps = recordedSteps.slice(0, -4);
  const renderStep = ({ call, result, runId }: typeof recordedSteps[number]) => {
    const active = Boolean(activeRunId && runId === activeRunId && activeStatus !== undefined);
    const state = result ? result.is_error ? "error" : "done" : active ? activeStatus === "awaiting_approval" ? "approval" : "running" : "unknown";
    return <li key={call.id} data-state={state} aria-current={state === "running" || state === "approval" ? "step" : undefined}>
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
    {goal || steps.size ? <details className="progress-details">
      <summary><Icon name="chevron" size={12} /><span>{t("compact.goalAndHistory")}</span>{olderSteps.length ? <span className="muted">{t("compact.olderSteps", { n: olderSteps.length })}</span> : null}</summary>
      {goal ? <p className="task-goal"><strong>{t("work.taskGoal")}</strong>{goal}</p> : null}
      {lastRequest && lastRequest !== goal ? <p className="task-goal"><strong>{t("work.latestRequest")}</strong>{lastRequest}</p> : null}
      {olderSteps.length ? <ol className="execution-history" aria-label={t("compact.earlierRecords")}>{olderSteps.map(renderStep)}</ol> : !steps.size ? <p className="muted">{t("work.noToolSteps")}</p> : null}
    </details> : null}
  </section>;
}
