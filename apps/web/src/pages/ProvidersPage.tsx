// 供应商：列表、创建/编辑（预设选择、base_url、api_key 只写不显示、测试按钮）。

import { useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { Badge, Dialog, Empty, ErrorBanner, Field, Spinner } from "../components/ui";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import type { Provider, ProviderInput, ProviderKind, ProviderTestResult } from "../api/types";

interface Preset {
  id: string;
  name: string;
  kind: ProviderKind;
  base_url: string;
  default_model: string;
  needsKey: boolean;
}

/** 常见服务的预设；只是填表的起点，用户可以改任何字段。模型名只是建议。 */
const PRESETS: Preset[] = [
  { id: "openai", name: "OpenAI", kind: "openai_compatible", base_url: "https://api.openai.com/v1", default_model: "gpt-4o-mini", needsKey: true },
  { id: "anthropic", name: "Anthropic", kind: "anthropic", base_url: "https://api.anthropic.com", default_model: "claude-sonnet-4-5", needsKey: true },
  { id: "deepseek", name: "DeepSeek", kind: "openai_compatible", base_url: "https://api.deepseek.com/v1", default_model: "deepseek-chat", needsKey: true },
  { id: "moonshot", name: "Moonshot (Kimi)", kind: "openai_compatible", base_url: "https://api.moonshot.cn/v1", default_model: "kimi-k2-0905-preview", needsKey: true },
  { id: "dashscope", name: "DashScope (Qwen)", kind: "openai_compatible", base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1", default_model: "qwen-plus", needsKey: true },
  { id: "zhipu", name: "Zhipu (GLM)", kind: "openai_compatible", base_url: "https://open.bigmodel.cn/api/paas/v4", default_model: "glm-4.5", needsKey: true },
  { id: "ollama", name: "Ollama", kind: "ollama", base_url: "http://127.0.0.1:11434", default_model: "", needsKey: false },
];

const KINDS: ProviderKind[] = ["openai_compatible", "anthropic", "ollama"];

export function ProvidersPage() {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<Provider | null | "new">(null);
  const [testResults, setTestResults] = useState<Record<string, ProviderTestResult | "testing">>({});

  const providers = useQuery({ queryKey: queryKeys.providers, queryFn: () => api.providers.list() });

  const remove = useMutation({
    mutationFn: (id: string) => api.providers.remove(id),
    onSuccess: () => {
      toast.show(t("providers.deleted"), "ok");
      void queryClient.invalidateQueries({ queryKey: queryKeys.providers });
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });

  const test = useMutation({
    mutationFn: (id: string) => api.providers.test(id),
    onMutate: (id) => setTestResults((r) => ({ ...r, [id]: "testing" })),
    onSuccess: (result, id) => {
      setTestResults((r) => ({ ...r, [id]: result }));
      void queryClient.invalidateQueries({ queryKey: queryKeys.providers });
    },
    onError: (err, id) => setTestResults((r) => ({ ...r, [id]: { ok: false, error: describeError(err, t) } })),
  });

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("providers.title")}</h1>
        <button type="button" className="btn primary" onClick={() => setEditing("new")}>
          {t("providers.create")}
        </button>
      </div>
      <p style={{ color: "var(--text-2)", marginTop: 0 }}>{t("providers.intro")}</p>
      <ErrorBanner error={providers.error} />
      {providers.isPending ? <Spinner /> : null}
      {providers.data && providers.data.length === 0 ? <Empty>{t("providers.empty")}</Empty> : null}
      {providers.data?.map((p) => {
        const result = testResults[p.id];
        return (
          <div className="card" key={p.id}>
            <div className="card-title">
              <span>{p.name}</span>
              <Badge>{t(`providers.kind_${p.kind}`)}</Badge>
              <Badge tone={p.enabled ? "ok" : undefined}>{p.enabled ? t("common.enabled") : t("common.disabled")}</Badge>
              <Badge tone={p.has_secret ? "accent" : "warn"}>{p.has_secret ? t("providers.hasSecret") : t("providers.noSecret")}</Badge>
              <span className="spacer" />
              <button type="button" className="btn sm" disabled={result === "testing"} onClick={() => test.mutate(p.id)}>
                {result === "testing" ? t("providers.testing") : t("providers.test")}
              </button>
              <button type="button" className="btn sm" onClick={() => setEditing(p)}>
                {t("common.edit")}
              </button>
              <button
                type="button"
                className="btn sm danger"
                disabled={remove.isPending}
                onClick={() => {
                  if (window.confirm(t("providers.confirmDelete", { name: p.name }))) remove.mutate(p.id);
                }}
              >
                {t("common.delete")}
              </button>
            </div>
            {(result && result !== "testing" && result.ok ? result.models : p.models)?.length ? (
              <details style={{ marginTop: 8 }}>
                <summary>{t("providers.models")}</summary>
                <div className="meta">{(result && result !== "testing" && result.ok ? result.models : p.models)?.map((model) => <div key={model}><code>{model}</code></div>)}</div>
              </details>
            ) : null}
            <div className="meta">
              {p.base_url ? <code>{p.base_url}</code> : null}
              {p.default_model ? ` · ${t("providers.defaultModel")}: ${p.default_model}` : ""}
              {p.models && p.models.length > 0 ? ` · ${t("providers.models")}: ${p.models.length}` : ""}
            </div>
            {result && result !== "testing" ? (
              <div className={`banner ${result.ok ? "ok" : "error"}`} style={{ marginTop: 8, marginBottom: 0 }}>
                {result.ok
                  ? t("providers.testOk", { ms: result.latency_ms ?? 0, n: result.models?.length ?? 0 })
                  : `${t("providers.testFail")}${result.error ? `: ${result.error}` : ""}`}
              </div>
            ) : null}
          </div>
        );
      })}
      {editing !== null ? (
        <ProviderForm
          provider={editing === "new" ? null : editing}
          discoveredModels={editing !== "new" && testResults[editing.id] !== "testing" ? (testResults[editing.id] as ProviderTestResult | undefined)?.models : undefined}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            toast.show(t("providers.saved"), "ok");
            void queryClient.invalidateQueries({ queryKey: queryKeys.providers });
          }}
        />
      ) : null}
    </div>
  );
}

export interface ProviderFormState {
  preset: string;
  name: string;
  kind: ProviderKind;
  base_url: string;
  api_key: string;
  default_model: string;
  enabled: boolean;
}

/** 密钥仅随写入请求发送；空值不覆盖已保存的密钥。 */
export function buildProviderInput(form: ProviderFormState): ProviderInput {
  const input: ProviderInput = {
    name: form.name.trim(),
    kind: form.kind,
    base_url: form.base_url.trim(),
    default_model: form.default_model.trim(),
    enabled: form.enabled,
  };
  const key = form.api_key.trim();
  if (key) input.api_key = key;
  return input;
}

/** 与供应商服务的地址限制一致；Anthropic 与 Ollama 支持留空使用默认地址。 */
export function validProviderBaseUrl(kind: ProviderKind, value: string): boolean {
  if (!value.trim()) return kind !== "openai_compatible";
  try {
    const url = new URL(value.trim());
    return ["http:", "https:"].includes(url.protocol) && Boolean(url.hostname) && !url.username && !url.password && !url.search && !url.hash;
  } catch {
    return false;
  }
}

function ProviderForm({ provider, discoveredModels, onClose, onSaved }: { provider: Provider | null; discoveredModels?: string[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const [form, setForm] = useState<ProviderFormState>(() => ({
    preset: provider ? "custom" : (PRESETS[0]?.id ?? "custom"),
    name: provider?.name ?? PRESETS[0]?.name ?? "",
    kind: provider?.kind ?? PRESETS[0]?.kind ?? "openai_compatible",
    base_url: provider?.base_url ?? PRESETS[0]?.base_url ?? "",
    api_key: "",
    default_model: provider?.default_model ?? PRESETS[0]?.default_model ?? "",
    enabled: provider?.enabled ?? true,
  }));
  const [error, setError] = useState<unknown>(null);
  const [models, setModels] = useState<string[]>(() => discoveredModels ?? provider?.models ?? []);
  const [testResult, setTestResult] = useState<ProviderTestResult | null>(null);
  const saveInFlight = useRef(false);
  const discoveryInFlight = useRef(false);
  const baseUrlValid = validProviderBaseUrl(form.kind, form.base_url);
  const valid = Boolean(form.name.trim() && baseUrlValid);
  // /test 检测服务端已保存的配置。地址、类型或密钥有未保存变更时必须先保存。
  const connectionChanged = Boolean(provider && (form.kind !== provider.kind || form.base_url.trim() !== (provider.base_url ?? "") || form.api_key.trim()));

  const discover = useMutation({
    mutationFn: () => api.providers.test(provider!.id),
    onMutate: () => setTestResult(null),
    onSuccess: (result) => {
      setTestResult(result);
      if (!result.ok) return;
      setModels(result.models ?? []);
      if (result.models?.length) setForm((current) => ({ ...current, default_model: current.default_model || result.models![0]! }));
      void queryClient.invalidateQueries({ queryKey: queryKeys.providers });
    },
    onError: (err) => setTestResult({ ok: false, error: describeError(err, t) }),
    onSettled: () => { discoveryInFlight.current = false; },
  });

  const applyPreset = (id: string) => {
    const preset = PRESETS.find((p) => p.id === id);
    if (!preset) {
      setForm((f) => ({ ...f, preset: "custom" }));
      return;
    }
    setForm((f) => ({
      ...f,
      preset: id,
      name: f.name && f.preset === "custom" ? f.name : preset.name,
      kind: preset.kind,
      base_url: preset.base_url,
      default_model: preset.default_model,
    }));
  };

  const save = useMutation({
    mutationFn: (input: ProviderInput) => (provider ? api.providers.update(provider.id, input) : api.providers.create(input)),
    onSuccess: onSaved,
    onError: setError,
    onSettled: () => { saveInFlight.current = false; },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!valid || saveInFlight.current || discoveryInFlight.current) return;
    saveInFlight.current = true;
    setError(null);
    save.mutate(buildProviderInput(form));
  };

  return (
    <Dialog
      open
      title={provider ? t("providers.edit") : t("providers.create")}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button type="submit" form="provider-form" className="btn primary" disabled={save.isPending || discover.isPending || !valid}>
            {t("common.save")}
          </button>
        </>
      }
    >
      <form id="provider-form" onSubmit={onSubmit} autoComplete="off">
        <ErrorBanner error={error} />
        {!provider ? (
          <Field label={t("providers.preset")}>
            <select className="select" value={form.preset} onChange={(e) => applyPreset(e.target.value)}>
              {PRESETS.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
              <option value="custom">{t("providers.presetCustom")}</option>
            </select>
          </Field>
        ) : null}
        <div className="grid-2">
          <Field label={t("providers.name")}>
            <input className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value, preset: "custom" })} required />
          </Field>
          <Field label={t("providers.kind")}>
            <select className="select" value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value as ProviderKind, preset: "custom" })}>
              {KINDS.map((k) => (
                <option key={k} value={k}>
                  {t(`providers.kind_${k}`)}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Field label={t("providers.baseUrl")}>
          <input className="input" value={form.base_url} aria-label={t("providers.baseUrl")} aria-invalid={!baseUrlValid} onChange={(e) => setForm({ ...form, base_url: e.target.value, preset: "custom" })} spellCheck={false} />
          {!baseUrlValid ? <span className="hint" role="alert">{t("providers.baseUrlInvalid")}</span> : null}
        </Field>
        <Field label={t("providers.apiKey")} hint={provider?.has_secret ? t("providers.apiKeyKeep") : t("providers.apiKeyHint")}>
          <input className="input" type="password" aria-label={t("providers.apiKey")} value={form.api_key} onChange={(e) => setForm({ ...form, api_key: e.target.value })} autoComplete="new-password" />
        </Field>
        <Field label={t("providers.defaultModel")} hint={t("providers.modelHint")}>
          <input className="input" list="provider-model-options" aria-label={t("providers.defaultModel")} value={form.default_model} onChange={(e) => setForm({ ...form, default_model: e.target.value })} />
          <datalist id="provider-model-options">{[...new Set([...models, ...(provider?.default_model ? [provider.default_model] : [])])].map((model) => <option key={model} value={model} />)}</datalist>
          {provider ? (
            <button type="button" className="btn sm" disabled={discover.isPending || save.isPending || connectionChanged} onClick={() => {
              if (discoveryInFlight.current || saveInFlight.current || connectionChanged) return;
              discoveryInFlight.current = true;
              discover.mutate();
            }}>
              {discover.isPending ? t("providers.testing") : t("providers.discoverModels")}
            </button>
          ) : null}
          {!provider || connectionChanged ? <span className="hint">{t("providers.discoverSaveFirst")}</span> : null}
          {testResult ? (
            <div className={`banner ${testResult.ok ? "ok" : "error"}`} role="status">
              {testResult.ok ? t("providers.testOk", { ms: testResult.latency_ms ?? 0, n: testResult.models?.length ?? 0 }) : `${t("providers.testFail")}${testResult.error ? `: ${testResult.error}` : ""}`}
            </div>
          ) : null}
        </Field>
        <label className="check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
          {t("common.enabled")}
        </label>
      </form>
    </Dialog>
  );
}
