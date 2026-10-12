// 一键回填、按需补充意见；所有异步结果都绑定点击时的草稿版本。
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useApi } from "../../state/ConnectionContext";
import { Dialog, ErrorBanner } from "../../components/ui";
import { Icon } from "../../components/Icon";
import "./prompt-enhancement.css";

export interface PromptEnhancementProps {
  agentId: string; scope?: string; value: string; revision: number; initialRevision: number;
  disabled: boolean; onChange: (value: string) => void; onFocus: () => void; onDismiss: () => void;
}
interface Applied { original: string; text: string; revision: number }
interface Pending { controller: AbortController; revision: number; scope?: string; agentId: string }

export function PromptEnhancementControls(props: PromptEnhancementProps) {
  const api = useApi(); const {t} = useTranslation(); const latest = useRef(props); latest.current = props;
  const [busy,setBusy] = useState(false); const [error,setError] = useState<unknown>(null);
  const [applied,setApplied] = useState<Applied | null>(null);
  const [adjusting,setAdjusting] = useState(false); const [feedback,setFeedback] = useState("");
  const feedbackInput = useRef<HTMLTextAreaElement>(null);
  useEffect(()=>{if(adjusting)feedbackInput.current?.focus();},[adjusting]);
  const request = useRef<Pending | null>(null); const initialRequested = useRef(false);
  const unchanged = applied?.text === props.value && applied.revision === props.revision;
  const cancel = () => { request.current?.controller.abort(); request.current = null; setBusy(false); setError(null); };
  const run = (notes = "") => {
    const current = latest.current;
    if (current.disabled || !current.value.trim() || request.current) return;
    const pending: Pending = {controller: new AbortController(), revision: current.revision, scope: current.scope, agentId: current.agentId};
    request.current = pending; setBusy(true); setError(null);
    const validPrevious = applied?.text === current.value && applied.revision === current.revision;
    const original = validPrevious ? applied.original : current.value;
    const source = notes ? original : current.value;
    void api.agents.enhancePrompt(current.agentId, source, pending.controller.signal, notes ? {draft:current.value, feedback:notes} : undefined).then(result => {
      const now = latest.current;
      if (request.current !== pending || pending.controller.signal.aborted || now.disabled || now.revision !== pending.revision || now.scope !== pending.scope || now.agentId !== pending.agentId) return;
      request.current = null; setBusy(false); setAdjusting(false); setFeedback("");
      setApplied({original, text:result.text, revision:now.revision + 1});
      now.onChange(result.text); now.onFocus();
    }).catch(err => { if (request.current === pending && !pending.controller.signal.aborted) setError(err); })
      .finally(() => { if (request.current === pending) { request.current = null; setBusy(false); } });
  };
  useEffect(() => {
    let disposed = false;
    queueMicrotask(() => {
      if (!disposed && !initialRequested.current) {
        initialRequested.current = true;
        if (latest.current.revision === latest.current.initialRevision) run();
      }
    });
    return () => { disposed = true; request.current?.controller.abort(); request.current = null; };
  }, []);
  useEffect(() => {
    const pending = request.current;
    if (pending && (pending.revision !== props.revision || pending.scope !== props.scope || pending.agentId !== props.agentId || props.disabled)) {
      cancel(); setAdjusting(false);
    }
  }, [props.revision, props.scope, props.agentId, props.disabled]);
  const close = () => { cancel(); setAdjusting(false); props.onFocus(); };
  return <>
    <button type="button" className="btn ghost sm prompt-enhance-button" disabled={props.disabled || !props.value.trim() || busy} title={t(busy ? "enhance.loading" : "enhance.action")} aria-label={t(busy ? "enhance.loading" : "enhance.action")} onClick={() => run()}><Icon name="spark" size={17}/><span className={busy ? undefined : "sr-only"}>{t(busy ? "enhance.loading" : "enhance.action")}</span></button>
    {busy && !adjusting ? <button type="button" className="btn ghost sm" onClick={cancel}>{t("common.cancel")}</button> : null}
    {unchanged && !busy ? <>
      <button type="button" className="btn ghost sm" disabled={props.disabled} onClick={() => { props.onChange(applied.original); setApplied(null); props.onFocus(); }}>{t("enhance.restore")}</button>
      <button type="button" className="btn ghost sm" disabled={props.disabled} onClick={() => { setFeedback(""); setError(null); setAdjusting(true); }}>{t("enhance.adjust")}</button>
    </> : null}
    {!adjusting && error ? <div className="prompt-enhancement-inline-error"><ErrorBanner error={error}/></div> : null}
    {adjusting ? <Dialog open title={t("enhance.adjustTitle")} onClose={close} footer={<>
      <button type="button" className="btn" onClick={close}>{t("common.cancel")}</button>
      <button type="button" className="btn primary" disabled={busy || props.disabled || !feedback.trim() || !unchanged} onClick={() => run(feedback.trim())}>{t(busy ? "enhance.adjusting" : "enhance.applyFeedback")}</button>
    </>}><div className="prompt-enhancement">
      <label className="field">{t("enhance.feedback")}<textarea ref={feedbackInput} autoFocus className="textarea prompt-enhancement-feedback" aria-label={t("enhance.feedback")} placeholder={t("enhance.feedbackPlaceholder")} value={feedback} maxLength={1000} disabled={busy} onChange={event => setFeedback(event.target.value)}/></label>
      <ErrorBanner error={error}/>
      <details><summary>{t("enhance.current")}</summary><p className="prompt-enhancement-original">{props.value}</p></details>
    </div></Dialog> : null}
  </>;
}
