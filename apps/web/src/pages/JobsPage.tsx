// 定时任务：列表、创建/编辑、立即运行。

import { useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { useSettingsQuery, useTimeZone } from "../state/useSettings";
import { Badge, Dialog, Empty, ErrorBanner, Field, Spinner } from "../components/ui";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import { formatDateTime, resolveTimeZone } from "../lib/time";
import type { Agent, Job, JobInput, JobKind, MissedPolicy } from "../api/types";

const SCHEDULE_PRESETS = ["@hourly", "@daily", "0 9 * * 1-5", "*/30 * * * *", "0 20 * * 0"];

export function JobsPage() {
  const { t, i18n } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const timeZone = useTimeZone();
  const [agentFilter, setAgentFilter] = useState("");
  const [editing, setEditing] = useState<Job | null | "new">(null);

  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list() });
  const jobs = useQuery({ queryKey: queryKeys.jobs(agentFilter || undefined), queryFn: () => api.jobs.list(agentFilter || undefined) });

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: queryKeys.jobsRoot });

  const remove = useMutation({
    mutationFn: (id: string) => api.jobs.remove(id),
    onSuccess: () => {
      toast.show(t("jobs.deleted"), "ok");
      invalidate();
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const runNow = useMutation({
    mutationFn: (id: string) => api.jobs.runNow(id),
    onSuccess: () => {
      toast.show(t("jobs.triggered"), "ok");
      invalidate();
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => api.jobs.update(id, { enabled }),
    onSuccess: invalidate,
    onError: (err) => toast.show(describeError(err, t), "error"),
  });

  const agentName = (id: string) => agents.data?.find((a) => a.id === id)?.name ?? id;
  const fmt = (iso: string | undefined) => formatDateTime(iso, { timeZone, locale: i18n.language });

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("jobs.title")}</h1>
        <select className="select" style={{ width: 200 }} value={agentFilter} onChange={(e) => setAgentFilter(e.target.value)}>
          <option value="">{t("chat.allAgents")}</option>
          {agents.data?.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        <button type="button" className="btn primary" onClick={() => setEditing("new")} disabled={!agents.data || agents.data.length === 0}>
          {t("jobs.create")}
        </button>
      </div>
      <ErrorBanner error={jobs.error} />
      <ErrorBanner error={agents.error} />
      {jobs.isPending ? <Spinner /> : null}
      {jobs.data && jobs.data.length === 0 ? <Empty>{t("jobs.empty")}</Empty> : null}
      {jobs.data?.map((j) => (
        <div className="card" key={j.id}>
          <div className="card-title">
            <span>{j.name}</span>
            <Badge>{t(`jobs.kind_${j.kind}`)}</Badge>
            <Badge tone={j.enabled ? "ok" : undefined}>{j.enabled ? t("common.enabled") : t("common.disabled")}</Badge>
            {j.last_status ? <Badge tone={j.last_status === "ok" ? "ok" : j.last_status === "failed" ? "bad" : "warn"}>{t(`jobs.status_${j.last_status}`)}</Badge> : null}
            <span className="spacer" />
            <button type="button" className="btn sm" onClick={() => runNow.mutate(j.id)} disabled={runNow.isPending || !agents.data?.some((agent) => agent.id === j.agent_id && (agent.enabled || j.kind === "push_text"))}>
              {t("jobs.runNow")}
            </button>
            <button type="button" className="btn sm" disabled={toggle.isPending || (!j.enabled && j.kind === "run_agent" && !agents.data?.some((agent) => agent.id === j.agent_id && agent.enabled))} onClick={() => toggle.mutate({ id: j.id, enabled: !j.enabled })}>
              {j.enabled ? t("common.disable") : t("common.enable")}
            </button>
            <button type="button" className="btn sm" onClick={() => setEditing(j)}>
              {t("common.edit")}
            </button>
            <button
              type="button"
              className="btn sm danger"
              disabled={remove.isPending}
              onClick={() => {
                if (window.confirm(t("jobs.confirmDelete", { name: j.name }))) remove.mutate(j.id);
              }}
            >
              {t("common.delete")}
            </button>
          </div>
          <div className="meta">
            {agentName(j.agent_id)} · <code>{j.schedule}</code>
            {j.timezone ? ` (${j.timezone})` : ""} · {t("jobs.nextRun")}: {fmt(j.next_run_at)} · {t("jobs.lastRun")}: {fmt(j.last_run_at)}
          </div>
          {j.last_error ? <div className="banner error" style={{ marginTop: 6, marginBottom: 0 }}>{j.last_error}</div> : null}
          <div className="meta" style={{ marginTop: 4 }}>
            {j.input}
          </div>
        </div>
      ))}
      {editing !== null ? (
        <JobForm
          job={editing === "new" ? null : editing}
          agents={agents.data ?? []}
          defaultAgentId={agentFilter}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            toast.show(t("jobs.saved"), "ok");
            invalidate();
          }}
        />
      ) : null}
    </div>
  );
}

export interface JobFormState {
  agent_id: string;
  name: string;
  schedule: string;
  timezone: string;
  kind: JobKind;
  input: string;
  session_key: string;
  missed_policy: MissedPolicy;
  enabled: boolean;
}

/** 空筛选或失效筛选回落到可运行的智能体；仅有停用智能体时仍可配置推送文本任务。 */
export function initialJobAgentId(agents: Agent[], defaultAgentId: string): string {
  const preferred = agents.find((agent) => agent.id === defaultAgentId && agent.enabled);
  return preferred?.id ?? agents.find((agent) => agent.enabled)?.id ?? agents[0]?.id ?? "";
}

/** 统一创建与编辑请求的规范化，输入内部的换行保持原样。 */
export function buildJobInput(form: JobFormState): JobInput {
  return {
    agent_id: form.agent_id,
    name: form.name.trim(),
    schedule: form.schedule.trim(),
    timezone: form.timezone.trim(),
    kind: form.kind,
    input: form.input.trim(),
    session_key: form.session_key.trim(),
    missed_policy: form.missed_policy,
    enabled: form.enabled,
  };
}

/** 停用智能体可保留已停用任务或推送文本任务，但不能启用运行智能体的计划。 */
export function validJobForm(form: JobFormState, agents: Agent[]): boolean {
  const agent = agents.find((item) => item.id === form.agent_id);
  const timezone = form.timezone.trim();
  return Boolean(agent && (agent.enabled || form.kind === "push_text" || !form.enabled)
    && form.name.trim() && Array.from(form.name.trim()).length <= 80 && form.schedule.trim()
    && form.input.trim() && Array.from(form.input.trim()).length <= 4000
    && (!timezone || (timezone !== "Local" && resolveTimeZone(timezone))));
}

function JobForm({ job, agents, defaultAgentId, onClose, onSaved }: { job: Job | null; agents: Agent[]; defaultAgentId: string; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const api = useApi();
  const settings = useSettingsQuery();
  const [form, setForm] = useState<JobFormState>(() => ({
    agent_id: job?.agent_id ?? initialJobAgentId(agents, defaultAgentId),
    name: job?.name ?? "",
    schedule: job?.schedule ?? "@daily",
    timezone: job?.timezone ?? "",
    kind: job?.kind ?? "run_agent",
    input: job?.input ?? "",
    session_key: job?.session_key ?? "",
    missed_policy: job?.missed_policy ?? "skip",
    enabled: job?.enabled ?? true,
  }));
  const [error, setError] = useState<unknown>(null);
  const saveInFlight = useRef(false);
  const selectedAgent = agents.find((agent) => agent.id === form.agent_id);
  const timezone = form.timezone.trim();
  const timezoneInvalid = Boolean(timezone && (timezone === "Local" || !resolveTimeZone(timezone)));
  const valid = validJobForm(form, agents);

  const save = useMutation({
    mutationFn: (input: JobInput) => (job ? api.jobs.update(job.id, input) : api.jobs.create(input)),
    onSuccess: onSaved,
    onError: setError,
    onSettled: () => { saveInFlight.current = false; },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!valid || saveInFlight.current) return;
    saveInFlight.current = true;
    setError(null);
    save.mutate(buildJobInput(form));
  };

  return (
    <Dialog
      open
      title={job ? t("jobs.edit") : t("jobs.create")}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button type="submit" form="job-form" className="btn primary" disabled={save.isPending || !valid}>
            {t("common.save")}
          </button>
        </>
      }
    >
      <form id="job-form" onSubmit={onSubmit}>
        <ErrorBanner error={error} />
        {form.kind === "run_agent" && !selectedAgent?.enabled ? <div className="banner warn">{t("jobs.agentUnavailable")}</div> : null}
        <div className="grid-2">
          <Field label={t("jobs.name")}>
            <input className="input" aria-label={t("jobs.name")} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
          </Field>
          <Field label={t("jobs.agent")}>
            <select className="select" aria-label={t("jobs.agent")} value={form.agent_id} onChange={(e) => setForm({ ...form, agent_id: e.target.value })} disabled={job !== null}>
              {!selectedAgent ? <option value={form.agent_id}>{form.agent_id || t("jobs.selectAgent")}</option> : null}
              {(job ? agents : agents.filter((agent) => agent.enabled)).map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}{a.enabled ? "" : ` (${t("common.disabled")})`}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t("jobs.schedule")} hint={t("jobs.scheduleHint")}>
            <input className="input" list="schedule-presets" value={form.schedule} onChange={(e) => setForm({ ...form, schedule: e.target.value })} required spellCheck={false} />
            <datalist id="schedule-presets">
              {SCHEDULE_PRESETS.map((s) => (
                <option key={s} value={s} />
              ))}
            </datalist>
          </Field>
          <Field label={t("jobs.timezone")} hint={t("jobs.timezoneHint", { tz: settings.data?.timezone || t("settings.systemTimezone") })}>
            <input className="input" value={form.timezone} aria-label={t("jobs.timezone")} aria-invalid={timezoneInvalid} onChange={(e) => setForm({ ...form, timezone: e.target.value })} placeholder="Asia/Shanghai" spellCheck={false} />
            {timezoneInvalid ? <span className="hint" role="alert">{t("settings.timezoneInvalid")}</span> : null}
          </Field>
        </div>
        <Field label={t("jobs.kind")}>
          <div>
            {(["run_agent", "push_text"] as JobKind[]).map((k) => (
              <label key={k} className="check">
                <input type="radio" name="kind" checked={form.kind === k} onChange={() => setForm({ ...form, kind: k })} />
                {t(`jobs.kind_${k}`)}
              </label>
            ))}
          </div>
        </Field>
        <Field label={t("jobs.input")} hint={form.kind === "run_agent" ? t("jobs.inputHintRun") : t("jobs.inputHintPush")}>
          <textarea className="textarea" rows={4} aria-label={t("jobs.input")} value={form.input} onChange={(e) => setForm({ ...form, input: e.target.value })} required />
          {Array.from(form.input.trim()).length > 4000 ? <span className="hint" role="alert">{t("jobs.inputTooLong", { n: 4000 })}</span> : null}
        </Field>
        <div className="grid-2">
          <Field label={t("jobs.sessionKey")} hint={t("jobs.sessionKeyHint")}>
            <input className="input" value={form.session_key} onChange={(e) => setForm({ ...form, session_key: e.target.value })} spellCheck={false} />
          </Field>
          <Field label={t("jobs.missedPolicy")}>
            <select className="select" value={form.missed_policy} onChange={(e) => setForm({ ...form, missed_policy: e.target.value as MissedPolicy })}>
              <option value="skip">{t("jobs.missed_skip")}</option>
              <option value="run_once">{t("jobs.missed_run_once")}</option>
            </select>
          </Field>
        </div>
        <label className="check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
          {t("common.enabled")}
        </label>
      </form>
    </Dialog>
  );
}
