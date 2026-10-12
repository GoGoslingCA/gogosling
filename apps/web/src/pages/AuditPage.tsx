// 审计查看：过滤由 API 执行，分页显示；所有详情均按文本渲染，不执行记录里的 HTML。

import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { useTimeZone } from "../state/useSettings";
import { Badge, Empty, ErrorBanner, Field, Spinner } from "../components/ui";
import { formatDateTime } from "../lib/time";
import { prettyJson } from "../lib/json";
import type { AuditEntry } from "../api/types";
import type { AuditQuery } from "../api/modules/audit";

export const AUDIT_PAGE_SIZE = 50;

export interface AuditFilters {
  agent_id: string;
  session_id: string;
  run_id: string;
  actor: string;
  action: string;
  since: string;
}

const EMPTY_FILTERS: AuditFilters = { agent_id: "", session_id: "", run_id: "", actor: "", action: "", since: "" };
/** RFC 3339（必须有时区），避免日期输入隐式使用浏览器时区。 */
const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/i;

/** 过滤输入裁剪后才提交；since 严格带时区并规范化为 UTC。 */
export function buildAuditQuery(filters: AuditFilters): { query: AuditQuery; error: null } | { query: null; error: "invalid_since" } {
  const query: AuditQuery = {};
  for (const field of ["agent_id", "session_id", "run_id", "actor", "action"] as const) {
    const value = filters[field].trim();
    if (value) query[field] = value;
  }
  const since = filters.since.trim();
  if (since) {
    const parts = RFC3339.exec(since);
    const date = new Date(since);
    if (!parts || !Number.isFinite(date.getTime())) return { query: null, error: "invalid_since" };
    const [year, month, day, hour, minute, second] = parts.slice(1).map(Number);
    const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    const daysInMonth = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1];
    // Date 会把 2 月 30 日、24:00 等滚入下一天；筛选不接受这类无效输入。
    if (!daysInMonth || day < 1 || day > daysInMonth || hour > 23 || minute > 59 || second > 59) return { query: null, error: "invalid_since" };
    query.since = date.toISOString();
  }
  return { query, error: null };
}

export function AuditPage() {
  const { t, i18n } = useTranslation();
  const api = useApi();
  const timeZone = useTimeZone();
  const [filters, setFilters] = useState<AuditFilters>(EMPTY_FILTERS);
  const [applied, setApplied] = useState<AuditQuery>({});
  const [offset, setOffset] = useState(0);
  const [filterError, setFilterError] = useState<string | null>(null);
  const query: AuditQuery = { ...applied, limit: AUDIT_PAGE_SIZE, offset };
  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list() });
  // 审批事件可能早于审计写入；可见页面定期刷新，避免事件抢先查询后长期停留旧记录。
  const entries = useQuery({ queryKey: queryKeys.audit(query), queryFn: ({ signal }) => api.audit.list(query, signal), refetchInterval: 5_000 });
  const apply = (e: FormEvent) => {
    e.preventDefault();
    const next = buildAuditQuery(filters);
    if (next.error) { setFilterError(t("audit.sinceInvalid")); return; }
    setFilterError(null);
    setApplied(next.query);
    setOffset(0);
  };
  const clear = () => {
    setFilters(EMPTY_FILTERS);
    setApplied({});
    setOffset(0);
    setFilterError(null);
  };
  const agentName = (id: string) => agents.data?.find((a) => a.id === id)?.name ?? id;

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("audit.title")}</h1>
        <button type="button" className="btn" disabled={entries.isFetching} onClick={() => void entries.refetch()}>{t("audit.refresh")}</button>
      </div>
      <p className="status-item">{t("audit.intro")}</p>
      <form className="card" onSubmit={apply}>
        <div className="grid-2">
          <Field label={t("audit.agent")} hint={t("audit.agentHint")}>
            <input className="input" list="audit-agents" aria-label={t("audit.agent")} value={filters.agent_id} onChange={(e) => setFilters({ ...filters, agent_id: e.target.value })} spellCheck={false} />
            <datalist id="audit-agents">{agents.data?.map((a) => <option key={a.id} value={a.id} label={a.name} />)}</datalist>
          </Field>
          {(["actor", "action", "session_id", "run_id"] as const).map((field) => <Field key={field} label={t(`audit.${field}`)}>
            <input className="input" aria-label={t(`audit.${field}`)} value={filters[field]} onChange={(e) => setFilters({ ...filters, [field]: e.target.value })} spellCheck={false} />
          </Field>)}
          <Field label={t("audit.since")} hint={t("audit.sinceHint")}><input className="input" aria-label={t("audit.since")} aria-invalid={filterError !== null} value={filters.since} onChange={(e) => { setFilters({ ...filters, since: e.target.value }); setFilterError(null); }} spellCheck={false} /></Field>
        </div>
        {filterError ? <div className="banner error" role="alert">{filterError}</div> : null}
        <div className="row">
          <button type="submit" className="btn primary">{t("audit.apply")}</button>
          <button type="button" className="btn" onClick={clear}>{t("audit.clear")}</button>
        </div>
      </form>
      <ErrorBanner error={agents.error} />
      <ErrorBanner error={entries.error} />
      {entries.isPending ? <Spinner /> : null}
      {entries.data?.length === 0 ? <Empty>{t("audit.empty")}</Empty> : null}
      {entries.data?.map((entry) => <AuditCard key={entry.id} entry={entry} timeZone={timeZone} locale={i18n.language} agentName={agentName} />)}
      <div className="row" aria-label={t("audit.pagination")}>
        <button type="button" className="btn" disabled={offset === 0 || entries.isFetching} onClick={() => setOffset((value) => Math.max(0, value - AUDIT_PAGE_SIZE))}>{t("audit.previous")}</button>
        <span className="status-item">{t("audit.page", { n: Math.floor(offset / AUDIT_PAGE_SIZE) + 1 })}</span>
        <button type="button" className="btn" disabled={entries.isFetching || !entries.data || entries.data.length < AUDIT_PAGE_SIZE} onClick={() => setOffset((value) => value + AUDIT_PAGE_SIZE)}>{t("audit.next")}</button>
      </div>
    </div>
  );
}

function AuditCard({ entry, timeZone, locale, agentName }: { entry: AuditEntry; timeZone: string | undefined; locale: string; agentName: (id: string) => string }) {
  const { t } = useTranslation();
  const tone = entry.outcome === "allowed" || entry.outcome === "ok" ? "ok" : entry.outcome === "denied" || entry.outcome === "failed" ? "bad" : undefined;
  return (
    <div className="card">
      <div className="card-title"><code>{entry.action}</code><Badge tone={tone}>{entry.outcome}</Badge><span className="spacer" /><time dateTime={entry.at} className="status-item">{formatDateTime(entry.at, { timeZone, locale })}</time></div>
      <div className="meta">{t("audit.actor")}: {entry.actor}{entry.agent_id ? ` · ${t("audit.agent")}: ${agentName(entry.agent_id)}` : ""}</div>
      {entry.resource ? <div className="meta">{t("audit.resource")}: <code>{entry.resource}</code></div> : null}
      {entry.session_id || entry.run_id ? <div className="meta">{entry.session_id ? `${t("audit.session_id")}: ${entry.session_id}` : ""}{entry.session_id && entry.run_id ? " · " : ""}{entry.run_id ? `${t("audit.run_id")}: ${entry.run_id}` : ""}</div> : null}
      {entry.details && Object.keys(entry.details).length > 0 ? <details className="tool-card"><summary>{t("audit.details")}</summary><pre>{prettyJson(entry.details)}</pre></details> : null}
    </div>
  );
}
