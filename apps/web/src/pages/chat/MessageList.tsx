// 消息流：持久化消息 + 实时运行（流式文本、折叠的思考内容、工具调用卡片、用量、状态）
// + 从 Session.active_run_id 恢复的在途运行占位（实时视图里没有它时）。

import { forwardRef, useEffect, useLayoutEffect, useImperativeHandle, useRef, useMemo, useCallback, useState, memo } from "react";
import { useTranslation } from "react-i18next";
import { Badge, Spinner } from "../../components/ui";
import { Icon } from "../../components/Icon";
import { formatMessageTime } from "../../lib/time";
import { prettyJson, previewJson } from "../../lib/json";
import type { Attachment, Message, Part, RunStatus, ToolCall, ToolResult } from "../../api/types";
import type { LiveRun, LiveToolCall } from "../../state/liveRuns";
import { extractArtifacts, artifactLanguageLabel } from "../../lib/artifacts";
import { isTerminal } from "../../state/liveRuns";
import { Markdown } from "./Markdown";
import { CopyButton } from "./CopyButton";
import { ToolHistoryPage } from "./ToolHistoryPage";
import { indexToolResults, type ToolResultIndex } from "./toolResults";

/** 实时视图里没有、但服务端报告仍在进行的运行（status 为 null 表示还在查询）。 */
export interface RecoveredRun {
  runId: string;
  status: RunStatus | null;
}

export const MessageList = forwardRef<
  HTMLDivElement,
  { messages: Message[]; liveRuns: LiveRun[]; recovered?: RecoveredRun | null; loading: boolean; timeZone: string | undefined; onOpenArtifact?: (messageId: string, codeIndex?: number) => void; persistResults?: boolean; onOpenFile?: (attachment: Attachment) => void }
>(
  function MessageList({ messages, liveRuns, recovered = null, loading, timeZone, onOpenArtifact, persistResults = false, onOpenFile }, ref) {
    const { t } = useTranslation();
    const el = useRef<HTMLDivElement>(null);
    const stickToBottom = useRef(true);
    // 父级随流式增量更新动作闭包，历史消息无需因此重绘；点击时使用本次已提交的动作。
    const actions = useRef({ onOpenArtifact, onOpenFile });
    useLayoutEffect(() => { actions.current = { onOpenArtifact, onOpenFile }; }, [onOpenArtifact, onOpenFile]);
    const openArtifact = useCallback((...args: [string, number?]) => actions.current.onOpenArtifact?.(...args), []);
    const openFile = useCallback((attachment: Attachment) => actions.current.onOpenFile?.(attachment), []);
    useImperativeHandle(ref, () => el.current as HTMLDivElement);

    // 用户停留在底部时才自动跟随；向上翻看历史时不打扰。
    const liveText = liveRuns.map((r) => r.text.length + r.reasoning.length + r.toolCalls.length + r.status).join("|");
    useEffect(() => {
      const node = el.current;
      if (!node || !stickToBottom.current) return;
      node.scrollTop = node.scrollHeight;
    }, [messages.length, liveText]);

    const onScroll = () => {
      const node = el.current;
      if (!node) return;
      stickToBottom.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40;
    };

    const results = useMemo(() => indexToolResults(messages), [messages]);
    // 已经被持久化消息覆盖的终态运行由父组件移除；这里只渲染剩下的。
    const completedMessages = useMemo(() => new Set(messages.filter(m => m.role === "assistant").map(m => m.run_id)), [messages]);
    const visibleRuns = liveRuns.filter((r) => r.status !== "completed" || !completedMessages.has(r.runId));

    return (
      <div className="messages" ref={el} onScroll={onScroll}>
        {loading ? <Spinner /> : null}
        {!loading && messages.length === 0 && visibleRuns.length === 0 && !recovered ? <div className="status-item">{t("chat.emptyMessages")}</div> : null}
        {messages.map((m) => (
          <MessageBubble key={m.id} message={m} timeZone={timeZone} results={results} onOpenArtifact={onOpenArtifact ? openArtifact : undefined} persistResults={persistResults} onOpenFile={onOpenFile ? openFile : undefined} />
        ))}
        {visibleRuns.map((run) => (
          <LiveRunBubble key={run.runId} run={run} />
        ))}
        {recovered ? <RecoveredRunBubble run={recovered} /> : null}
      </div>
    );
  },
);

const MessageBubble = memo(function MessageBubble({ message, timeZone, onOpenArtifact, results, persistResults, onOpenFile }: { message: Message; timeZone: string | undefined; onOpenArtifact?: (messageId: string, codeIndex?: number) => void; persistResults?: boolean; onOpenFile?: (attachment: Attachment) => void; results: ToolResultIndex }) {
  const { t } = useTranslation();
  const partial = message.meta?.partial === true;
  const groups = useMemo(() => groupToolParts(message.parts), [message.parts]);
  return (
    <div className={`msg ${message.role}`}>
      {groups.map(group => group.kind === "tools"
        ? <HistoricalToolParts key={group.index} parts={group.parts} historyLimit={group.historyLimit} results={results} runId={message.run_id} />
        : <PartView key={group.index} part={group.part} result={group.part.tool_call ? results.results.get(results.key(group.part.tool_call.id, message.run_id)) : undefined} markdown={message.role === "assistant"} onOpenFile={onOpenFile} />)}
      {message.role === "assistant" && onOpenArtifact && message.parts.some((part) => part.type === "text" && part.text) ? <button className="btn sm result-open" onClick={() => onOpenArtifact(message.id)}>{t(persistResults ? "versions.openAndSave" : "work.reviewArtifact")} →</button> : null}
      {persistResults && onOpenArtifact && message.role === "assistant" ? extractArtifacts([message]).filter((item) => item.kind === "code").map((item) => <button key={item.id} className="btn sm result-open" onClick={() => onOpenArtifact(message.id, item.index)}>{t("versions.openCode", { language: artifactLanguageLabel(item.language), n: item.index })}</button>) : null}
      <div className="msg-meta">
        {t(`role.${message.role}`)} · {formatMessageTime(message.created_at, { timeZone })}
        {partial ? ` · ${t("chat.partial")}` : ""}
        {message.parts.some((part) => part.type === "text" && part.text) ? <CopyButton text={message.parts.filter((part) => part.type === "text").map((part) => part.text ?? "").join("\n")} /> : null}
      </div>
    </div>
  );
}, (previous, next) => previous.message === next.message && previous.timeZone === next.timeZone
  && previous.onOpenArtifact === next.onOpenArtifact && previous.persistResults === next.persistResults
  && previous.onOpenFile === next.onOpenFile
  && (previous.results === next.results || next.message.parts.every(part => !part.tool_call
    || previous.results.results.get(previous.results.key(part.tool_call.id, next.message.run_id)) === next.results.results.get(next.results.key(part.tool_call.id, next.message.run_id)))));

type PartGroup = { kind: "part"; index: number; part: Part } | { kind: "tools"; index: number; parts: Part[]; historyLimit: number };

function groupToolParts(parts: Part[]): PartGroup[] {
  const groups: PartGroup[] = [];
  // 常显预算属于整条消息，不能让每段进度文字重新获得二十条工具记录配额。
  let historyRemaining = Math.max(0, parts.reduce((total, part) => total + Number(part.type === "tool_call" || part.type === "tool_result"), 0) - 20);
  for (let index = 0; index < parts.length;) {
    const start = index;
    while (index < parts.length && (parts[index]!.type === "tool_call" || parts[index]!.type === "tool_result")) index++;
    if (index > start && historyRemaining > 0) {
      const historyLimit = Math.min(historyRemaining, index - start);
      groups.push({ kind: "tools", index: start, parts: parts.slice(start, index), historyLimit });
      historyRemaining -= historyLimit;
    }
    else for (let position = start; position < index; position++) groups.push({ kind: "part", index: position, part: parts[position]! });
    if (index < parts.length) { groups.push({ kind: "part", index, part: parts[index]! }); index++; }
  }
  return groups;
}

function HistoricalToolParts({ parts, historyLimit, results, runId }: { parts: Part[]; historyLimit: number; results: ToolResultIndex; runId?: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  let historyCount = historyLimit;
  for (let index = 0; index < historyCount; index++) {
    const call = parts[index]!.tool_call;
    if (call && !results.results.has(results.key(call.id, runId))) { historyCount = index; break; }
  }
  const history = useMemo(() => parts.slice(0, historyCount).map((part, index) => ({ part, index })), [parts, historyCount]);
  const renderPart = ({ part, index }: { part: Part; index: number }) => <PartView key={index} part={part} result={part.tool_call ? results.results.get(results.key(part.tool_call.id, runId)) : undefined} markdown={false} />;
  return <>
    {historyCount ? <details className="tool-history" onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>{t("compact.olderToolRecords", { n: historyCount })}</summary>
      {open ? <ToolHistoryPage items={history} renderItem={renderPart} /> : null}
    </details> : null}
    {parts.slice(historyCount).map((part, offset) => renderPart({ part, index: historyCount + offset }))}
  </>;
}

function PartView({ part, markdown, result, onOpenFile }: { part: Part; markdown: boolean; result?: ToolResult; onOpenFile?: (attachment: Attachment) => void }) {
  switch (part.type) {
    case "text":
      return markdown ? <Markdown text={part.text ?? ""} /> : <div className="text">{part.text}</div>;
    case "tool_call":
      return part.tool_call ? <ToolCallCard call={part.tool_call} persistedResult={result} historical /> : null;
    case "tool_result":
      return part.tool_result ? <ToolResultCard result={part.tool_result} /> : null;
    case "attachment":
      return part.attachment ? (
        onOpenFile ? <button type="button" className="attachment-chip preview-attachment-button" title={part.attachment.path} onClick={() => onOpenFile(part.attachment!)}>📎 {part.attachment.name} ({formatSize(part.attachment.size)})</button> : <span className="attachment-chip" title={part.attachment.path}>📎 {part.attachment.name} ({formatSize(part.attachment.size)})</span>
      ) : null;
    default:
      return null;
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export const ToolCallCard = memo(function ToolCallCard({ call, live, persistedResult, historical = false }: { call: ToolCall; live?: LiveToolCall; persistedResult?: ToolResult; historical?: boolean }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const result = live?.result ?? persistedResult;
  const done = result !== undefined;
  const argumentPreview = useMemo(() => previewJson(call.arguments, 70), [call.arguments]);
  const fullArguments = useMemo(() => open ? prettyJson(call.arguments) : "", [open, call.arguments]);
  return (
    <details className="tool-card" onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>
        <Icon name="chevron" size={13} />
        <Badge tone={done ? (result.is_error ? "bad" : "ok") : "warn"}>{done ? (result.is_error ? t("chat.toolError") : t("chat.toolDone")) : t(historical ? "work.noStepResult" : "chat.toolRunning")}</Badge>
        <code>{call.name || t("common.unknown")}</code>
        {done && result.truncated ? <Badge tone="warn">{t("chat.resultTruncated")}</Badge> : null}
        <span className="status-item">{argumentPreview}</span>
      </summary>
      {open ? <><pre>{fullArguments}</pre>{done ? <ToolResultContent result={result} /> : null}</> : null}
    </details>
  );
});

const LiveToolList = memo(function LiveToolList({ calls }: { calls: LiveRun["toolCalls"] }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  // 仅折叠已经完成的连续前缀，尚未回传结果的工具始终可见。
  const historyCount = useMemo(() => {
    const limit = Math.max(0, calls.length - 20);
    let index = 0;
    for (const call of calls) {
      if (index === limit) break;
      if (!call.result) return index;
      index++;
    }
    return limit;
  }, [calls]);
  const renderCall = (tc: LiveToolCall) => <ToolCallCard key={tc.call.id} call={tc.call} live={tc} />;
  return <>
    {historyCount ? <details className="tool-history" onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>{t("compact.olderSteps", { n: historyCount })}</summary>
      {open ? <ToolHistoryPage items={calls} end={historyCount} renderItem={renderCall} /> : null}
    </details> : null}
    {calls.slice(historyCount).map(renderCall)}
  </>;
});

function ToolResultCard({ result }: { result: ToolResult }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <details className="tool-card" onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>
        <Icon name="chevron" size={13} />
        <Badge tone={result.is_error ? "bad" : "ok"}>{result.is_error ? t("chat.toolError") : t("chat.toolResult")}</Badge>
        <code>{result.call_id}</code>
        {result.truncated ? <Badge tone="warn">{t("chat.resultTruncated")}</Badge> : null}
      </summary>
      {open ? <ToolResultContent result={result} /> : null}
    </details>
  );
}

/** 工具结果正文；被截断时在末尾说明（内容超过大小上限，模型看到的也是截断后的版本）。 */
function ToolResultContent({ result }: { result: ToolResult }) {
  const { t } = useTranslation();
  return (
    <>
      <pre>{result.content}</pre>
      {result.truncated ? <div className="status-item">{t("chat.resultTruncatedHint")}</div> : null}
    </>
  );
}

/** 模型的思考内容（reasoning.delta）：默认折叠，不进入持久化消息。 */
function ReasoningBlock({ text, live }: { text: string; live: boolean }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <details className="tool-card reasoning" onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>
        <Badge tone={live ? "accent" : undefined}>{live ? t("chat.reasoningLive") : t("chat.reasoning")}</Badge>
      </summary>
      {open ? <pre>{text}</pre> : null}
    </details>
  );
}

/** 实时视图里没有、从 Session.active_run_id 恢复的在途运行：只显示状态，内容在运行结束后随持久化消息出现。 */
function RecoveredRunBubble({ run }: { run: RecoveredRun }) {
  const { t } = useTranslation();
  const awaiting = run.status === "awaiting_approval";
  return (
    <div className="msg assistant">
      <div className="status-item">{t("chat.recoveredRun")}</div>
      <span className="cursor" aria-hidden="true" />
      <div className="msg-meta">
        <Badge tone={awaiting ? "warn" : "accent"} title={run.status === "queued" && !awaiting ? t("chat.queuedHint") : undefined}>{awaiting ? t("chat.awaitingApproval") : run.status === "queued" ? t("chat.queued") : t("chat.running")}</Badge>
      </div>
    </div>
  );
}

const LiveRunBubble = memo(function LiveRunBubble({ run }: { run: LiveRun }) {
  const { t } = useTranslation();
  const statusLabel = (() => {
    switch (run.status) {
      case "queued":
        return t("chat.queued");
      case "awaiting_approval":
        return t("chat.awaitingApproval");
      case "failed":
        return t("chat.failed");
      case "cancelled":
        return t("chat.cancelled");
      case "completed":
        return t("chat.completed");
      default:
        return t("chat.running");
    }
  })();
  const tone = run.status === "failed" ? "bad" : run.status === "awaiting_approval" ? "warn" : run.status === "completed" ? "ok" : "accent";
  return (
    <div className="msg assistant">
      {run.truncated ? <div className="banner warn">{t("chat.streamTruncated")}</div> : null}
      {run.reasoning ? <ReasoningBlock text={run.reasoning} live={!isTerminal(run.status) && !run.text} /> : null}
      <LiveToolList calls={run.toolCalls} />
      {run.text ? <Markdown text={run.text} /> : null}
      {!isTerminal(run.status) ? <span className="cursor" aria-hidden="true" /> : null}
      {run.error ? <div className="banner error" style={{ marginTop: 6, marginBottom: 0 }}>{run.error}</div> : null}
      <div className="msg-meta">
        <Badge tone={tone} title={run.status === "queued" ? t("chat.queuedHint") : undefined}>{statusLabel}</Badge>
        {run.usage && run.usage.input_tokens !== undefined && run.usage.output_tokens !== undefined ? ` · ${t("chat.usage", { in: run.usage.input_tokens, out: run.usage.output_tokens })}` : ""}
        {run.text ? <CopyButton text={run.text} /> : null}
      </div>
    </div>
  );
});
