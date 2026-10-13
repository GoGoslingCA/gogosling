// 智能体：列表、创建/编辑（名称、描述、系统提示、供应商与模型、自治档位、工具勾选）。
//
// 工具的三态语义（契约 AgentInput.tools）：缺席 = 不修改（创建时用默认工具集）；null = 重置为默认工具集；
// [] = 不启用任何内置工具。输出侧 Agent.tools 始终是数组，[] 显示为"未启用任何内置工具"，
// 编辑时可"恢复默认工具集"（保存时发 PATCH {tools: null}）。

import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { useWorkbenchCapabilities } from "../state/useWorkbenchCapabilities";
import { Badge, Dialog, Empty, ErrorBanner, Field, Spinner } from "../components/ui";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import { agentTools, type Agent, type AgentInput, type AutonomyLevel, type Extension, type Provider, type ToolSpec } from "../api/types";

const AUTONOMY_LEVELS: AutonomyLevel[] = ["ask_all", "ask_risky", "auto_in_sandbox"];

// 基础工具目录兼容旧守护进程；新增工具只在声明能力后提供，已有选择仍可保留或移除。
const BUILTIN_TOOL_NAMES = ["read_file", "write_file", "edit_file", "delete_file", "list_dir", "search_files", "run_command", "fetch_url"];

/** 目录仅纳入内置工具；未知的已选名称保留，扩展声明由扩展绑定提供。 */
export function builtinToolNames(specs: ToolSpec[], selected: string[], readDocument = false): string[] {
  const names = new Set(BUILTIN_TOOL_NAMES);
  if (readDocument) names.add("read_document");
  for (const spec of specs) if (spec.source === "builtin") names.add(spec.name);
  for (const name of selected) {
    if (!specs.some((spec) => spec.name === name && spec.source !== "builtin")) names.add(name);
  }
  return [...names].sort();
}

/** 已停用或已卸载的绑定仍可看见和移除，保存其他字段时不静默丢弃。 */
export function selectableExtensionIds(extensions: Extension[], selected: string[]): string[] {
  return [...new Set([...extensions.filter((extension) => extension.enabled).map((extension) => extension.id), ...selected])];
}

export function AgentsPage() {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<Agent | null | "new">(null);

  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list() });
  const providers = useQuery({ queryKey: queryKeys.providers, queryFn: () => api.providers.list() });

  const remove = useMutation({
    mutationFn: (id: string) => api.agents.remove(id),
    onSuccess: () => {
      toast.show(t("agents.deleted"), "ok");
      void queryClient.invalidateQueries({ queryKey: queryKeys.agents });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });

  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => api.agents.update(id, { enabled }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: queryKeys.agents }),
    onError: (err) => toast.show(describeError(err, t), "error"),
  });

  const providerName = (id: string) => providers.data?.find((p) => p.id === id)?.name ?? id;

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("agents.title")}</h1>
        <button type="button" className="btn primary" onClick={() => setEditing("new")}>
          {t("agents.create")}
        </button>
      </div>
      <ErrorBanner error={agents.error} />
      {agents.isPending ? <Spinner /> : null}
      {agents.data && agents.data.length === 0 ? <Empty>{t("agents.empty")}</Empty> : null}
      {agents.data?.map((a) => (
        <div className="card" key={a.id}>
          <div className="card-title">
            <span>{a.name}</span>
            <Badge tone={a.enabled ? "ok" : undefined}>{a.enabled ? t("common.enabled") : t("common.disabled")}</Badge>
            <Badge>{t(`agents.autonomy_${a.autonomy}`)}</Badge>
            <span className="spacer" />
            <button type="button" className="btn sm" disabled={toggle.isPending} onClick={() => toggle.mutate({ id: a.id, enabled: !a.enabled })}>
              {a.enabled ? t("common.disable") : t("common.enable")}
            </button>
            <button type="button" className="btn sm" onClick={() => setEditing(a)}>
              {t("common.edit")}
            </button>
            <button
              type="button"
              className="btn sm danger"
              disabled={remove.isPending}
              onClick={() => {
                if (window.confirm(t("agents.confirmDelete", { name: a.name }))) remove.mutate(a.id);
              }}
            >
              {t("common.delete")}
            </button>
          </div>
          {a.description ? <div>{a.description}</div> : null}
          <div className="meta">
            {t("agents.model")}: {providerName(a.model.provider_id)} / {a.model.model || providers.data?.find((item) => item.id === a.model.provider_id)?.default_model || t("common.unknown")}
            {providers.data?.some((item) => item.id === a.model.provider_id && !item.enabled) ? <Badge tone="warn">{t("agents.providerUnavailable")}</Badge> : null}
            {" · "}
            {t("agents.workspace")}: <code>{a.workspace_dir}</code>
            {` · ${agentTools(a).length === 0 ? t("agents.toolsNone") : t("agents.toolsCount", { n: agentTools(a).length })}`}
            {a.extensions?.length ? ` · ${t("agents.extensionsCount", { n: a.extensions.length })}` : ""}
          </div>
        </div>
      ))}
      {editing !== null ? (
        <AgentForm
          agent={editing === "new" ? null : editing}
          providers={providers.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            toast.show(t("agents.saved"), "ok");
            void queryClient.invalidateQueries({ queryKey: queryKeys.agents });
          }}
        />
      ) : null}
    </div>
  );
}

export interface AgentFormState {
  template?: string;
  name: string;
  description: string;
  system_prompt: string;
  provider_id: string;
  model: string;
  autonomy: AutonomyLevel;
  enabled: boolean;
  /**
   * 工具列表。null 的含义：创建时 = 使用默认工具集（不发 tools）；编辑时 = 恢复默认工具集（发 tools: null）。
   * [] 表示不启用任何内置工具。
   */
  tools: string[] | null;
  extensions: string[];
}

/** 由表单构造请求体；tools 的三态语义见文件头。 */
export function buildAgentInput(form: AgentFormState, mode: "create" | "edit"): AgentInput {
  const input: AgentInput = {
    name: form.name.trim(),
    description: form.description.trim(),
    system_prompt: form.system_prompt,
    model: { provider_id: form.provider_id, model: form.model.trim() },
    autonomy: form.autonomy,
    enabled: form.enabled,
    extensions: [...form.extensions],
  };
  if (form.tools !== null) input.tools = form.tools;
  else if (mode === "edit") input.tools = null;
  // 模板只用于创建；空提示和默认审批档位交给守护进程，显式自定义仍保留。
  if (mode === "create" && form.template) {
    input.template = form.template;
    if (!form.system_prompt.trim()) delete input.system_prompt;
    if (form.autonomy === "ask_risky") delete input.autonomy;
  }
  return input;
}

function AgentForm({ agent, providers, onClose, onSaved }: { agent: Agent | null; providers: Provider[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const api = useApi();
  const enabledProviders = useMemo(() => providers.filter((p) => p.enabled), [providers]);
  const [form, setForm] = useState<AgentFormState>(() => ({
    template: "",
    name: agent?.name ?? "",
    description: agent?.description ?? "",
    system_prompt: agent?.system_prompt ?? "",
    provider_id: agent?.model.provider_id ?? enabledProviders[0]?.id ?? providers[0]?.id ?? "",
    model: agent?.model.model ?? enabledProviders[0]?.default_model ?? "",
    autonomy: agent?.autonomy ?? "ask_risky",
    enabled: agent?.enabled ?? true,
    tools: agent ? agentTools(agent) : null,
    extensions: [...(agent?.extensions ?? [])],
  }));
  const [error, setError] = useState<unknown>(null);
  const saveInFlight = useRef(false);
  const extensions = useQuery({ queryKey: queryKeys.extensions(), queryFn: () => api.extensions.list() });

  // 编辑时拉取当前工具的描述和风险；创建时使用内置工具名列表，保存前无需创建临时智能体。
  const tools = useQuery({
    queryKey: queryKeys.agentTools(agent?.id ?? ""),
    queryFn: () => api.agents.tools(agent?.id ?? ""),
    enabled: agent !== null,
  });

  const provider = providers.find((p) => p.id === form.provider_id);
  const modelOptions = useMemo(() => {
    const set = new Set<string>(provider?.models ?? []);
    if (provider?.default_model) set.add(provider.default_model);
    return [...set];
  }, [provider]);

  useEffect(() => {
    // 切换供应商后模型为空时填入默认值，手动填写的模型名保持原样。
    if (provider && !form.model && provider.default_model) setForm((f) => ({ ...f, model: provider.default_model ?? "" }));
  }, [provider, form.model]);

  const caps = useWorkbenchCapabilities();
  const toolNames = useMemo(() => builtinToolNames(tools.data ?? [], form.tools ?? [], caps.readDocument), [tools.data, form.tools, caps.readDocument]);
  const extensionIds = selectableExtensionIds(extensions.data ?? [], form.extensions);
  const valid = Boolean(form.name.trim() && provider?.enabled && form.model.trim());

  const save = useMutation({
    mutationFn: (input: AgentInput) => (agent ? api.agents.update(agent.id, input) : api.agents.create(input)),
    onSuccess: onSaved,
    onError: setError,
    onSettled: () => { saveInFlight.current = false; },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!valid || saveInFlight.current) return;
    saveInFlight.current = true;
    setError(null);
    save.mutate(buildAgentInput(form, agent ? "edit" : "create"));
  };

  const toggleTool = (name: string, checked: boolean) => {
    setForm((f) => {
      const current = new Set(f.tools ?? []);
      if (checked) current.add(name);
      else current.delete(name);
      return { ...f, tools: [...current] };
    });
  };

  return (
    <Dialog
      open
      title={agent ? t("agents.edit") : t("agents.create")}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button type="submit" form="agent-form" className="btn primary" disabled={save.isPending || !valid}>
            {t("common.save")}
          </button>
        </>
      }
    >
      <form id="agent-form" onSubmit={onSubmit}>
        <ErrorBanner error={error} />
        {enabledProviders.length === 0 ? <div className="banner warn">{t("agents.noProviders")}</div> : null}
        {form.provider_id && !provider?.enabled ? <div className="banner warn">{t("agents.providerUnavailable")}</div> : null}
        {!agent ? <Field label={t("agentTemplates.label")} hint={t(form.template ? `agentTemplates.${form.template}Hint` : "agentTemplates.customHint")}>
          <select className="select" aria-label={t("agentTemplates.label")} value={form.template} onChange={(event) => { const template = event.target.value; setForm((previous) => ({ ...previous, template })); }}>
            <option value="">{t("agentTemplates.custom")}</option>
            <option value="general-assistant">{t("agentTemplates.general-assistant")}</option>
            <option value="ops-engineer">{t("agentTemplates.ops-engineer")}</option>
          </select>
        </Field> : null}
        <Field label={t("agents.name")}>
          <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
        </Field>
        <Field label={t("agents.description")}>
          <input className="input" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
        </Field>
        <Field label={t("agents.systemPrompt")} hint={t(!agent && form.template ? "agentTemplates.promptHint" : "agents.systemPromptHint")}>
          <textarea className="textarea" rows={5} value={form.system_prompt} onChange={(e) => setForm({ ...form, system_prompt: e.target.value })} />
        </Field>
        <div className="grid-2">
          <Field label={t("agents.provider")}>
            <select className="select" value={form.provider_id} onChange={(e) => setForm({ ...form, provider_id: e.target.value, model: "" })}>
              {!provider ? <option value={form.provider_id}>{form.provider_id || t("agents.selectProvider")}</option> : null}
              {providers.map((p) => (
                <option key={p.id} value={p.id} disabled={!p.enabled}>
                  {p.name}
                  {p.enabled ? "" : ` (${t("common.disabled")})`}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t("agents.model")} hint={t("agents.modelHint")}>
            <input className="input" list="agent-model-options" value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} required />
            <datalist id="agent-model-options">
              {modelOptions.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </Field>
        </div>
        <Field label={t("agents.autonomy")}>
          <div>
            {AUTONOMY_LEVELS.map((level) => (
              <label key={level} className="check">
                <input type="radio" name="autonomy" checked={form.autonomy === level} onChange={() => setForm({ ...form, autonomy: level })} />
                {t(`agents.autonomy_${level}`)}
                <span className="hint">— {t(`agents.autonomyHint_${level}`)}</span>
              </label>
            ))}
          </div>
        </Field>
        <Field label={t("agents.tools")} hint={t("agents.toolsHint")}>
          <ErrorBanner error={tools.error} />
          {agent ? (
            <>
              {form.tools === null ? (
                <div className="row">
                  <span className="status-item">{t("agents.toolsWillRestore")}</span>
                  <button type="button" className="btn sm" onClick={() => setForm({ ...form, tools: agentTools(agent) })}>
                    {t("agents.toolsUndoRestore")}
                  </button>
                </div>
              ) : (
                <div>
                  {tools.isPending ? <Spinner /> : null}
                  {form.tools.length === 0 ? <div className="status-item">{t("agents.toolsNone")}</div> : null}
                  {toolNames.map((name) => {
                    const spec = tools.data?.find((s) => s.name === name && s.source === "builtin");
                    return (
                      <label key={name} className="check" title={spec?.description ?? ""}>
                        <input type="checkbox" checked={form.tools?.includes(name) ?? false} onChange={(e) => toggleTool(name, e.target.checked)} />
                        <span>{t(`tools.${name}`, { defaultValue: name })}</span> <code>{name}</code>
                        {spec ? <Badge>{t(`risk.${spec.risk}`)}</Badge> : null}
                      </label>
                    );
                  })}
                  <button type="button" className="btn sm" onClick={() => setForm({ ...form, tools: null })}>
                    {t("agents.toolsRestoreDefault")}
                  </button>
                </div>
              )}
            </>
          ) : (
            <>
              <label className="check">
                <input type="checkbox" checked={form.tools === null} onChange={(e) => setForm({ ...form, tools: e.target.checked ? null : [] })} />
                {t("agents.toolsDefault")}
              </label>
              {form.tools !== null ? (
                <>
                  {form.tools.length === 0 ? <div className="status-item">{t("agents.toolsNone")}</div> : null}
                  {toolNames.map((name) => (
                    <label key={name} className="check">
                      <input type="checkbox" checked={form.tools?.includes(name) ?? false} onChange={(e) => toggleTool(name, e.target.checked)} />
                      <span>{t(`tools.${name}`, { defaultValue: name })}</span> <code>{name}</code>
                    </label>
                  ))}
                </>
              ) : null}
            </>
          )}
        </Field>
        <Field label={t("agents.extensions")} hint={t("agents.extensionsHint")}>
          <ErrorBanner error={extensions.error} />
          {extensions.isPending ? <Spinner /> : null}
          {!extensions.isPending && extensionIds.length === 0 ? <div className="status-item">{t("agents.extensionsEmpty")}</div> : null}
          {extensionIds.map((id) => {
            const extension = extensions.data?.find((item) => item.id === id);
            const selected = form.extensions.includes(id);
            return (
              <label className="check" key={id}>
                <input type="checkbox" checked={selected} disabled={!extension?.enabled && !selected} onChange={(event) => {
                  const checked = event.target.checked;
                  setForm((current) => ({ ...current, extensions: checked ? [...current.extensions, id] : current.extensions.filter((value) => value !== id) }));
                }} />
                {extension?.name ?? id}
                {extension ? <Badge>{t(`extensions.kind_${extension.kind}`)}</Badge> : null}
                {!extension ? <Badge tone="warn">{t("agents.extensionMissing")}</Badge> : !extension.enabled ? <Badge tone="warn">{t("common.disabled")}</Badge> : null}
              </label>
            );
          })}
        </Field>
        <label className="check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
          {t("agents.enabled")}
        </label>
      </form>
    </Dialog>
  );
}
