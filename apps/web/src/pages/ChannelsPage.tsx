// 通道管理：只提供已注册的 console / telegram 创建选项；凭据仅留在表单内存并只写入 API。

import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { Badge, Dialog, Empty, ErrorBanner, Field, Spinner } from "../components/ui";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import type { Agent, Channel, ChannelInput, ChannelKind, ChannelPatch } from "../api/types";

/** 当前组合根注册的适配器；其他契约平台不能在此假装可用。 */
export const CREATABLE_CHANNEL_KINDS = ["console", "telegram"] as const;

export interface ChannelFormState {
  agent_id: string;
  kind: ChannelKind;
  name: string;
  secret: string;
  config: string;
  enabled: boolean;
}

type ParsedConfig = { config: Record<string, unknown>; error: null } | { config: null; error: "invalid_json" | "not_object" };

/** 空白按 {} 处理，便于恢复默认配置；数组、null 与标量都不属于通道配置对象。 */
export function parseChannelConfig(text: string): ParsedConfig {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text.trim() || "{}");
  } catch {
    return { config: null, error: "invalid_json" };
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { config: null, error: "not_object" };
  }
  return { config: parsed as Record<string, unknown>, error: null };
}

export function buildChannelInput(form: ChannelFormState, config: Record<string, unknown>, mode: "create"): ChannelInput;
export function buildChannelInput(form: ChannelFormState, config: Record<string, unknown>, mode: "edit"): ChannelPatch;
/** 编辑时不发送不可修改的归属与平台；留空凭据表示保留，避免误清除已有密钥。 */
export function buildChannelInput(form: ChannelFormState, config: Record<string, unknown>, mode: "create" | "edit"): ChannelInput | ChannelPatch {
  const patch: ChannelPatch = { name: form.name.trim(), config, enabled: form.enabled };
  const secret = form.secret.trim();
  if (secret) patch.secret = secret;
  return mode === "create" ? { ...patch, name: form.name.trim(), agent_id: form.agent_id, kind: form.kind } : patch;
}

export function ChannelsPage() {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [agentFilter, setAgentFilter] = useState("");
  const [editing, setEditing] = useState<Channel | null | "new">(null);

  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list() });
  const channels = useQuery({
    queryKey: queryKeys.channels(agentFilter || undefined),
    queryFn: ({ signal }) => api.channels.list(agentFilter || undefined, signal),
    // 适配器重连与失败在后台发生；当前契约尚无通道状态 SSE。
    refetchInterval: 5_000,
  });
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: queryKeys.channelsRoot });
  const afterChange = (message: string) => {
    toast.show(message, "ok");
    invalidate();
    void queryClient.invalidateQueries({ queryKey: queryKeys.auditRoot });
  };
  const remove = useMutation({
    mutationFn: (id: string) => api.channels.remove(id),
    onSuccess: () => afterChange(t("channels.deleted")),
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => api.channels.update(id, { enabled }),
    onSuccess: () => afterChange(t("channels.saved")),
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const reload = useMutation({
    mutationFn: (id: string) => api.channels.reload(id),
    onSuccess: () => afterChange(t("channels.reloaded")),
    onError: (err) => {
      toast.show(describeError(err, t), "error");
      // 重载失败也可能已把通道标为 failed。
      invalidate();
    },
  });
  const pending = remove.isPending || toggle.isPending || reload.isPending;
  const agentName = (id: string) => agents.data?.find((a) => a.id === id)?.name ?? id;
  const canCreate = agents.data?.some((a) => a.enabled) ?? false;

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("channels.title")}</h1>
        <select className="select" style={{ width: 200 }} aria-label={t("channels.filterAgent")} value={agentFilter} onChange={(e) => setAgentFilter(e.target.value)}>
          <option value="">{t("chat.allAgents")}</option>
          {agents.data?.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
        <button type="button" className="btn" disabled={channels.isFetching} onClick={() => void channels.refetch()}>{t("channels.refresh")}</button>
        <button type="button" className="btn primary" disabled={!canCreate} onClick={() => setEditing("new")}>{t("channels.create")}</button>
      </div>
      <p className="status-item">{t("channels.intro")}</p>
      <ErrorBanner error={agents.error} />
      <ErrorBanner error={channels.error} />
      {channels.isPending ? <Spinner /> : null}
      {!agents.isPending && !agents.error && !canCreate ? <div className="banner warn">{t("channels.noAgent")}</div> : null}
      {channels.data?.length === 0 ? <Empty>{t("channels.empty")}</Empty> : null}
      {channels.data?.map((channel) => {
        const status = channel.status ?? "stopped";
        return (
          <div className="card" key={channel.id}>
            <div className="card-title">
              <span>{channel.name}</span>
              <Badge>{t(`channels.kind_${channel.kind}`)}</Badge>
              <Badge tone={channel.enabled ? "ok" : undefined}>{channel.enabled ? t("common.enabled") : t("common.disabled")}</Badge>
              <Badge tone={status === "running" ? "ok" : status === "failed" ? "bad" : status === "starting" ? "warn" : undefined}>{t(`channels.status_${status}`)}</Badge>
              {channel.kind !== "console" ? <Badge tone={channel.has_secret ? "accent" : "warn"}>{channel.has_secret ? t("channels.hasSecret") : t("channels.noSecret")}</Badge> : null}
              <span className="spacer" />
              <button type="button" className="btn sm" disabled={pending || !channel.enabled} onClick={() => reload.mutate(channel.id)}>{t("channels.reload")}</button>
              <button type="button" className="btn sm" disabled={pending} onClick={() => toggle.mutate({ id: channel.id, enabled: !channel.enabled })}>{channel.enabled ? t("common.disable") : t("common.enable")}</button>
              <button type="button" className="btn sm" disabled={pending} onClick={() => setEditing(channel)}>{t("common.edit")}</button>
              <button type="button" className="btn sm danger" disabled={pending} onClick={() => {
                if (window.confirm(t("channels.confirmDelete", { name: channel.name }))) remove.mutate(channel.id);
              }}>{t("common.delete")}</button>
            </div>
            <div className="meta">{t("channels.agent")}: {agentName(channel.agent_id)}</div>
            {channel.last_error ? <div className="banner error" role="alert" style={{ marginTop: 8, marginBottom: 0 }}>{channel.last_error}</div> : null}
          </div>
        );
      })}
      {editing !== null ? (
        <ChannelForm channel={editing === "new" ? null : editing} agents={agents.data ?? []} defaultAgentId={agentFilter} onClose={() => setEditing(null)} onSaved={() => {
          setEditing(null);
          afterChange(t("channels.saved"));
        }} />
      ) : null}
    </div>
  );
}

function ChannelForm({ channel, agents, defaultAgentId, onClose, onSaved }: { channel: Channel | null; agents: Agent[]; defaultAgentId: string; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const api = useApi();
  const [form, setForm] = useState<ChannelFormState>(() => ({
    agent_id: channel?.agent_id ?? agents.find((a) => a.id === defaultAgentId && a.enabled)?.id ?? agents.find((a) => a.enabled)?.id ?? "",
    kind: channel?.kind ?? "telegram",
    name: channel?.name ?? "",
    secret: "",
    config: JSON.stringify(channel?.config ?? { group_mode: "mention", allowed_peers: [] }, null, 2),
    enabled: channel?.enabled ?? true,
  }));
  const [error, setError] = useState<unknown>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const selectedAgent = agents.find((a) => a.id === form.agent_id);
  const validAgent = channel !== null || selectedAgent?.enabled === true;
  const needsSecret = !channel && form.kind === "telegram";
  const valid = Boolean(form.name.trim() && form.agent_id && validAgent && (!needsSecret || form.secret.trim()));
  const save = useMutation({
    mutationFn: (config: Record<string, unknown>) => channel
      ? api.channels.update(channel.id, buildChannelInput(form, config, "edit"))
      : api.channels.create(buildChannelInput(form, config, "create")),
    onSuccess: onSaved,
    onError: setError,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!valid || save.isPending) return;
    setError(null);
    const parsed = parseChannelConfig(form.config);
    if (parsed.error !== null) {
      setConfigError(t(parsed.error === "invalid_json" ? "channels.configInvalid" : "channels.configObject"));
      return;
    }
    setConfigError(null);
    save.mutate(parsed.config);
  };

  return (
    <Dialog open title={channel ? t("channels.edit") : t("channels.create")} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose} disabled={save.isPending}>{t("common.cancel")}</button>
      <button type="submit" form="channel-form" className="btn primary" disabled={save.isPending || !valid}>{t("common.save")}</button>
    </>}>
      <form id="channel-form" onSubmit={submit}>
        <ErrorBanner error={error} />
        <Field label={t("channels.name")}><input className="input" aria-label={t("channels.name")} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required /></Field>
        <div className="grid-2">
          <Field label={t("channels.agent")}>
            <select className="select" aria-label={t("channels.agent")} value={form.agent_id} onChange={(e) => setForm({ ...form, agent_id: e.target.value })} disabled={channel !== null}>
              {agents.map((a) => <option key={a.id} value={a.id} disabled={!a.enabled}>{a.name}{a.enabled ? "" : ` (${t("common.disabled")})`}</option>)}
              {channel && !selectedAgent ? <option value={channel.agent_id}>{channel.agent_id}</option> : null}
            </select>
          </Field>
          <Field label={t("channels.kind")}>
            <select className="select" aria-label={t("channels.kind")} value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value as ChannelKind })} disabled={channel !== null}>
              {CREATABLE_CHANNEL_KINDS.map((kind) => <option key={kind} value={kind}>{t(`channels.kind_${kind}`)}</option>)}
              {channel && !CREATABLE_CHANNEL_KINDS.some((kind) => kind === channel.kind) ? <option value={channel.kind}>{t(`channels.kind_${channel.kind}`)}</option> : null}
            </select>
          </Field>
        </div>
        <Field label={t("channels.secret")} hint={channel ? t("channels.secretKeep") : t(form.kind === "telegram" ? "channels.secretTelegram" : "channels.secretOptional")}>
          <input className="input" type="password" autoComplete="new-password" aria-label={t("channels.secret")} value={form.secret} onChange={(e) => setForm({ ...form, secret: e.target.value })} required={needsSecret} />
        </Field>
        <Field label={t("channels.config")} hint={t("channels.configHint")}>
          <textarea className="textarea" rows={8} spellCheck={false} aria-label={t("channels.config")} aria-invalid={configError !== null} value={form.config} onChange={(e) => { setForm({ ...form, config: e.target.value }); setConfigError(null); }} />
        </Field>
        {configError ? <div className="banner error" role="alert">{configError}</div> : null}
        <label className="check"><input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />{t("common.enabled")}</label>
      </form>
    </Dialog>
  );
}
