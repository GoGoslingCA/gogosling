// 每次发送都绑定用户看过的预览；撤销请求与销毁预览是两种独立操作。
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { cancelDiagnostics, cancelDiagnosticsPreparation, previewTaskDiagnostics, sendDiagnostics, type DiagnosticsPreview, type DiagnosticsReceipt, type DiagnosticsResult } from "../api/diagnostics";
import "./diagnostics.css";

type Failure = Extract<DiagnosticsResult<never>, { ok: false }>;

export function DiagnosticsDialog({ sessionId, runId, onClose }: { sessionId: string; runId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const dialog = useRef<HTMLDialogElement>(null);
  const epoch = useRef(0);
  const current = useRef<DiagnosticsPreview | null>(null);
  const sending = useRef(false);
  const [generation, setGeneration] = useState(0);
  const [preview, setPreview] = useState<DiagnosticsPreview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [consent, setConsent] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [receipt, setReceipt] = useState<DiagnosticsReceipt | null>(null);

  useEffect(() => { if (dialog.current && !dialog.current.open) dialog.current.showModal(); }, []);
  useEffect(() => {
    const request = ++epoch.current;
    const requestId = crypto.randomUUID();
    sending.current = false; setBusy(false); setCancelling(false);
    setPreview(null); setConsent(false); setFailure(null); setReceipt(null); setLoading(true);
    void previewTaskDiagnostics({ kind: "task", requestId, sessionId, runId }).then(result => {
      if (request !== epoch.current) {
        if (result.ok) void cancelDiagnostics(result.value.id, true);
        return;
      }
      setLoading(false);
      if (result.ok) { current.current = result.value; setPreview(result.value); }
      else setFailure(result);
    });
    return () => {
      ++epoch.current;
      void cancelDiagnosticsPreparation(requestId);
      const old = current.current;
      current.current = null;
      if (old) void cancelDiagnostics(old.id, true);
    };
  }, [sessionId, runId, generation]);

  const send = async () => {
    const reviewed = current.current;
    if (!reviewed || !consent || sending.current) return;
    const request = ++epoch.current;
    sending.current = true; setBusy(true); setFailure(null);
    const result = await sendDiagnostics(reviewed.id);
    if (request !== epoch.current) return;
    sending.current = false; setBusy(false);
    if (result.ok) {
      current.current = null;
      setPreview(null); setConsent(false); setReceipt(result.value);
    } else setFailure(result);
  };
  const cancel = async () => {
    const reviewed = current.current;
    if (!reviewed || !sending.current || cancelling) return;
    const request = ++epoch.current;
    setCancelling(true);
    await cancelDiagnostics(reviewed.id, false);
    if (request !== epoch.current) return;
    sending.current = false; setBusy(false); setCancelling(false);
    setFailure({ ok: false, code: "cancelled" });
  };
  const expired = failure?.code === "stale_preview";
  const terminalFailure = failure?.code === "no_error" || failure?.code === "unsupported_report" || failure?.code === "disabled";
  return <dialog ref={dialog} className="dialog diagnostics-dialog" aria-labelledby="diagnostics-title" aria-describedby="diagnostics-explanation" onClose={onClose} onCancel={event => { event.preventDefault(); onClose(); }}>
    <div className="dialog-head"><span id="diagnostics-title">{t("diagnostics.title")}</span><span className="spacer" /><button type="button" className="btn ghost sm" onClick={onClose} aria-label={t("common.close")}>×</button></div>
    <div className="dialog-body">
      <p id="diagnostics-explanation" className="diagnostics-hint">{t("diagnostics.summary")}</p>
      {receipt ? <div role="status" className="diagnostics-success"><p>{t(receipt.duplicate ? "diagnostics.alreadySent" : "diagnostics.sent")}</p><p>{t("diagnostics.receipt", { id: receipt.eventId })}</p></div> : <>
        {loading ? <p role="status">{t("diagnostics.preparing")}</p> : null}
        {preview ? <>
          <p className="diagnostics-destination">{t("diagnostics.destination", { destination: preview.destination, days: preview.retentionDays })}</p>
          <details className="diagnostics-preview" key={preview.id}><summary>{t("diagnostics.review", { bytes: preview.byteLength })}</summary><pre tabIndex={0} aria-label={t("diagnostics.payload")}>{preview.body}</pre></details>
          <label className="diagnostics-consent"><input type="checkbox" checked={consent} disabled={busy || expired || terminalFailure} onChange={event => setConsent(event.target.checked)} /><span>{t("diagnostics.consent")}</span></label>
        </> : null}
        {failure ? <div className="banner error" role="alert"><span>{t(`diagnostics.errors.${failure.code}`)}</span>{failure.retryAfterSeconds ? <span> {t("diagnostics.retryAfter", { seconds: failure.retryAfterSeconds })}</span> : null}</div> : null}
        {busy ? <p role="status">{t(cancelling ? "diagnostics.cancelling" : "diagnostics.sending")}</p> : null}
      </>}
    </div>
    <div className="dialog-foot">
      {receipt ? <button type="button" className="btn primary" onClick={onClose}>{t("diagnostics.done")}</button> : <>
        <button type="button" className="btn" onClick={busy ? () => { void cancel(); } : onClose} disabled={cancelling}>{t(busy ? "diagnostics.cancelSend" : "common.close")}</button>
        {terminalFailure ? null : expired || (!preview && !loading) ? <button type="button" className="btn primary" onClick={() => setGeneration(value => value + 1)}>{t("diagnostics.refresh")}</button> : <button type="button" className="btn primary" disabled={!preview || !consent || busy || loading} onClick={() => { void send(); }}>{t(failure ? "diagnostics.retry" : "diagnostics.send")}</button>}
      </>}
    </div>
  </dialog>;
}
