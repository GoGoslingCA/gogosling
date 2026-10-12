// 扩展：列表、启用/停用、从本地路径安装、注册 MCP 服务（含 http 请求头）、卸载；
// MCP 服务显示运行时状态（是否连上、工具数、最近错误），工具声明变化（tools_changed）时提供"确认工具变化"。

import { useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { Badge, Dialog, Empty, ErrorBanner, Field, Spinner } from "../components/ui";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import type { Extension, ExtensionInstallInput, ExtensionKind, MCPServerConfig, MCPTransport } from "../api/types";

const KINDS: ExtensionKind[] = ["skill", "mcp_server", "plugin"];

export function ExtensionsPage() {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [kind, setKind] = useState<ExtensionKind | "">("");
  const [path, setPath] = useState("");
  const [mcpOpen, setMcpOpen] = useState(false);
  const installInFlight = useRef(false);

  const extensions = useQuery({ queryKey: queryKeys.extensions(kind || undefined), queryFn: () => api.extensions.list(kind || undefined) });
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.extensionsRoot });
    // 扩展变化会影响智能体可用工具目录。
    void queryClient.invalidateQueries({ queryKey: queryKeys.agents });
  };

  const install = useMutation({
    mutationFn: (input: ExtensionInstallInput) => api.extensions.install(input),
    onSuccess: () => {
      toast.show(t("extensions.installed"), "ok");
      setPath("");
      setMcpOpen(false);
      invalidate();
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
    onSettled: () => { installInFlight.current = false; },
  });
  // React Query 的 pending 状态可能在下一帧才到达；同步门闩阻止连续提交产生重复安装。
  const startInstall = (input: ExtensionInstallInput) => {
    if (installInFlight.current) return;
    installInFlight.current = true;
    install.mutate(input);
  };
  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => api.extensions.setEnabled(id, enabled),
    onSuccess: invalidate,
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const uninstall = useMutation({
    mutationFn: (id: string) => api.extensions.uninstall(id),
    onSuccess: () => {
      toast.show(t("extensions.uninstalled"), "ok");
      invalidate();
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });

  const onInstallPath = (e: FormEvent) => {
    e.preventDefault();
    const p = path.trim();
    if (p && !install.isPending) startInstall({ path: p });
  };

  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("extensions.title")}</h1>
        <select className="select" style={{ width: 160 }} value={kind} onChange={(e) => setKind(e.target.value as ExtensionKind | "")}>
          <option value="">{t("extensions.filterAll")}</option>
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`extensions.kind_${k}`)}
            </option>
          ))}
        </select>
        <button type="button" className="btn primary" disabled={install.isPending} onClick={() => { install.reset(); setMcpOpen(true); }}>
          {t("extensions.registerMcp")}
        </button>
      </div>
      <form className="card" onSubmit={onInstallPath}>
        {!mcpOpen ? <ErrorBanner error={install.error} /> : null}
        <Field label={t("extensions.installPath")} hint={t("extensions.installPathHint")}>
          <div className="row">
            <input className="input" style={{ flex: 1 }} value={path} onChange={(e) => setPath(e.target.value)} placeholder="/path/to/skill-or-plugin" spellCheck={false} />
            <button type="submit" className="btn" disabled={install.isPending || !path.trim()}>
              {t("extensions.install")}
            </button>
          </div>
        </Field>
      </form>
      <ErrorBanner error={extensions.error} />
      {extensions.isPending ? <Spinner /> : null}
      {extensions.data && extensions.data.length === 0 ? <Empty>{t("extensions.empty")}</Empty> : null}
      {extensions.data?.map((x) => (
        <div className="card" key={x.id}>
          <div className="card-title">
            <span>{x.name}</span>
            <Badge tone="accent">{t(`extensions.kind_${x.kind}`)}</Badge>
            {x.version ? <Badge>{x.version}</Badge> : null}
            <Badge tone={x.enabled ? "ok" : undefined}>{x.enabled ? t("common.enabled") : t("common.disabled")}</Badge>
            <span className="spacer" />
            <button type="button" className="btn sm" disabled={toggle.isPending} onClick={() => toggle.mutate({ id: x.id, enabled: !x.enabled })}>
              {x.enabled ? t("common.disable") : t("common.enable")}
            </button>
            <button
              type="button"
              className="btn sm danger"
              disabled={uninstall.isPending}
              onClick={() => {
                if (window.confirm(t("extensions.confirmUninstall", { name: x.name }))) uninstall.mutate(x.id);
              }}
            >
              {t("extensions.uninstall")}
            </button>
          </div>
          <div className="meta">
            {t("extensions.source")}: <code>{x.source}</code>
            {x.mcp ? (
              <>
                {" · "}
                {x.mcp.transport === "stdio" ? <code>{[x.mcp.command, ...(x.mcp.args ?? [])].join(" ")}</code> : <code>{x.mcp.url}</code>}
                {x.mcp.headers && Object.keys(x.mcp.headers).length > 0 ? (
                  <>
                    {" · "}
                    {t("extensions.headers")}: <code>{Object.keys(x.mcp.headers).join(", ")}</code>
                  </>
                ) : null}
              </>
            ) : null}
          </div>
          {x.kind === "mcp_server" && x.enabled ? <ExtensionStatusLine extension={x} /> : null}
        </div>
      ))}
      {mcpOpen ? <McpForm busy={install.isPending} error={install.error} onClose={() => setMcpOpen(false)} onSubmit={(name, mcp) => startInstall({ name, mcp })} /> : null}
    </div>
  );
}

/** MCP 服务的运行时状态与"确认工具变化"。状态只对 MCP 服务有意义（连接与工具声明）。 */
function ExtensionStatusLine({ extension }: { extension: Extension }) {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const status = useQuery({
    queryKey: queryKeys.extensionStatus(extension.id),
    queryFn: () => api.extensions.status(extension.id),
    // 连接在后台建立；未连上时稍后再看一次。
    refetchInterval: (q) => (q.state.data && !q.state.data.connected ? 10_000 : false),
  });
  const accept = useMutation({
    mutationFn: () => api.extensions.acceptTools(extension.id),
    onSuccess: (next) => {
      queryClient.setQueryData(queryKeys.extensionStatus(extension.id), next);
      toast.show(t("extensions.toolsAccepted"), "ok");
      void queryClient.invalidateQueries({ queryKey: queryKeys.extensionsRoot });
      void queryClient.invalidateQueries({ queryKey: queryKeys.agents });
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });

  if (status.isPending) return null;
  if (status.error) return <ErrorBanner error={status.error} />;
  const st = status.data;
  return (
    <div className="meta">
      <Badge tone={st.connected ? "ok" : "warn"}>{st.connected ? t("extensions.connected") : t("extensions.disconnected")}</Badge>
      {typeof st.tool_count === "number" ? ` ${t("extensions.toolCount", { n: st.tool_count })}` : null}
      {st.last_error ? (
        <>
          {" · "}
          {t("extensions.lastError")}: <code>{st.last_error}</code>
        </>
      ) : null}
      {st.tools_changed ? (
        <div className="banner warn" style={{ marginTop: 6, marginBottom: 0 }}>
          {t("extensions.toolsChanged")}{" "}
          <button type="button" className="btn sm" disabled={accept.isPending} onClick={() => accept.mutate()}>
            {t("extensions.acceptTools")}
          </button>
        </div>
      ) : null}
    </div>
  );
}

/** 把 KEY=VALUE 行解析为 env 对象；空行与无等号的行忽略。 */
export function parseEnvLines(text: string): Record<string, string> {
  const env: Record<string, string> = Object.create(null) as Record<string, string>;
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const eq = trimmed.indexOf("=");
    if (eq <= 0) continue;
    env[trimmed.slice(0, eq).trim()] = trimmed.slice(eq + 1).trim();
  }
  return env;
}

/** HTTP 头名必须是 RFC 9110 的 token。 */
const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

/**
 * 解析请求头文本：每行 "Name: value" 或 "Name=value"（以先出现的 ':' 或 '=' 为分隔，
 * 所以 "Authorization=secret:tok" 与 "Authorization: Bearer a=b" 都能正确拆分）。
 * 空行与 # 注释忽略；名称不合法或没有分隔符的行进入 invalid，由表单提示，不静默丢弃。
 */
export function parseHeaderLines(text: string): { headers: Record<string, string>; invalid: string[] } {
  const headers: Record<string, string> = Object.create(null) as Record<string, string>;
  const invalid: string[] = [];
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const colon = trimmed.indexOf(":");
    const eq = trimmed.indexOf("=");
    const sep = colon === -1 ? eq : eq === -1 ? colon : Math.min(colon, eq);
    const name = sep > 0 ? trimmed.slice(0, sep).trim() : "";
    if (!name || !HEADER_NAME.test(name)) {
      invalid.push(trimmed);
      continue;
    }
    headers[name] = trimmed.slice(sep + 1).trim();
  }
  return { headers, invalid };
}

function McpForm({ busy, error, onClose, onSubmit }: { busy: boolean; error: unknown; onClose: () => void; onSubmit: (name: string, mcp: MCPServerConfig) => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [transport, setTransport] = useState<MCPTransport>("stdio");
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [env, setEnv] = useState("");
  const [url, setUrl] = useState("");
  const [headers, setHeaders] = useState("");

  const parsedHeaders = parseHeaderLines(headers);
  const headersOk = transport !== "http" || parsedHeaders.invalid.length === 0;
  const valid = name.trim() && (transport === "stdio" ? command.trim() : url.trim()) && headersOk;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!valid || busy) return;
    const mcp: MCPServerConfig = { transport };
    if (transport === "stdio") {
      mcp.command = command.trim();
      const list = args
        .split(/\r?\n/)
        .map((s) => s.trim())
        .filter(Boolean);
      if (list.length > 0) mcp.args = list;
      const envObj = parseEnvLines(env);
      if (Object.keys(envObj).length > 0) mcp.env = envObj;
    } else {
      mcp.url = url.trim();
      if (Object.keys(parsedHeaders.headers).length > 0) mcp.headers = parsedHeaders.headers;
    }
    onSubmit(name.trim(), mcp);
  };

  return (
    <Dialog
      open
      title={t("extensions.registerMcp")}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button type="submit" form="mcp-form" className="btn primary" disabled={busy || !valid}>
            {t("common.save")}
          </button>
        </>
      }
    >
      <form id="mcp-form" onSubmit={submit}>
        <ErrorBanner error={error} />
        <Field label={t("extensions.name")}>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field label={t("extensions.transport")}>
          <select className="select" value={transport} onChange={(e) => setTransport(e.target.value as MCPTransport)}>
            <option value="stdio">stdio</option>
            <option value="http">http</option>
          </select>
        </Field>
        {transport === "stdio" ? (
          <>
            <Field label={t("extensions.command")}>
              <input className="input" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npx" spellCheck={false} />
            </Field>
            <Field label={t("extensions.args")} hint={t("extensions.argsHint")}>
              <textarea className="textarea" rows={3} value={args} onChange={(e) => setArgs(e.target.value)} spellCheck={false} />
            </Field>
            <Field label={t("extensions.env")} hint={t("extensions.envHint")}>
              <textarea className="textarea" rows={3} value={env} onChange={(e) => setEnv(e.target.value)} spellCheck={false} placeholder="API_TOKEN=secret:my-token" />
            </Field>
          </>
        ) : (
          <>
            <Field label={t("extensions.url")}>
              <input className="input" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://127.0.0.1:8000/mcp" spellCheck={false} />
            </Field>
            <Field label={t("extensions.headers")} hint={t("extensions.headersHint")}>
              <textarea
                className="textarea"
                rows={3}
                value={headers}
                onChange={(e) => setHeaders(e.target.value)}
                spellCheck={false}
                placeholder="Authorization: secret:my-token"
              />
            </Field>
            {parsedHeaders.invalid.length > 0 ? (
              <div className="banner error">{t("extensions.headersInvalid", { lines: parsedHeaders.invalid.join(" / ") })}</div>
            ) : null}
          </>
        )}
      </form>
    </Dialog>
  );
}
