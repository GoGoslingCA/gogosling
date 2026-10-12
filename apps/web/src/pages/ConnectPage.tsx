// 连接页：输入守护进程地址与令牌，用 /api/v1/version 验证后进入应用。令牌只存内存。

import { useState, type FormEvent } from "react";
import { Navigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ApiClient, ApiError } from "../api/client";
import { DEFAULT_ENDPOINT } from "../connection";
import { useConnection } from "../state/ConnectionContext";
import { currentLocale } from "../i18n";
import { Field } from "../components/ui";
import { describeError } from "../lib/errorMessage";

export function ConnectPage() {
  const { t } = useTranslation();
  const { connection, connect, reason } = useConnection();
  const [endpoint, setEndpoint] = useState(import.meta.env.DEV ? "" : DEFAULT_ENDPOINT);
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (connection) return <Navigate to="/chat" replace />;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const tok = token.trim();
    if (!tok) {
      setError(t("connect.tokenRequired"));
      return;
    }
    setBusy(true);
    setError(null);
    const client = new ApiClient({ baseUrl: endpoint, token: tok, locale: currentLocale });
    try {
      await client.get("/api/v1/version");
      connect({ endpoint: client.baseUrl, token: tok, source: "manual" });
    } catch (err) {
      if (ApiError.is(err, "network")) setError(t("connect.unreachable"));
      else if (ApiError.is(err, "unauthorized")) setError(t("connect.invalidToken"));
      else setError(describeError(err, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page" style={{ maxWidth: 520, margin: "48px auto" }}>
      <div className="page-head">
        <h1>{t("connect.title")}</h1>
      </div>
      <p style={{ color: "var(--text-2)" }}>{t("connect.intro")}</p>
      {reason === "unauthorized" ? <div className="banner warn">{t("connect.reasonUnauthorized")}</div> : null}
      {error ? (
        <div className="banner error" role="alert">
          {error}
        </div>
      ) : null}
      <form onSubmit={(e) => void onSubmit(e)}>
        <Field label={t("connect.endpoint")} hint={t("connect.endpointHint", { def: DEFAULT_ENDPOINT })}>
          <input className="input" value={endpoint} placeholder={DEFAULT_ENDPOINT} onChange={(e) => setEndpoint(e.target.value)} autoComplete="off" spellCheck={false} />
        </Field>
        <Field label={t("connect.token")} hint={t("connect.tokenHint")}>
          <input className="input" type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" />
        </Field>
        <button type="submit" className="btn primary" disabled={busy}>
          {busy ? t("connect.checking") : t("connect.submit")}
        </button>
      </form>
    </div>
  );
}
