// 失败提示保留在源码版；报告入口只在发行壳明确支持任务报告时出现。
import { lazy, Suspense, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { diagnosticsAvailable } from "../../api/diagnostics";
import { Icon } from "../../components/Icon";
import { APP_RELEASE_LABEL } from "../../lib/release";
import { formatDateTime } from "../../lib/time";
import type { TaskFailure } from "./taskFailure";
import "./task-failure.css";

const DiagnosticsDialog = lazy(() => import("../DiagnosticsDialog").then(module => ({ default: module.DiagnosticsDialog })));

export function TaskFailureBanner({ failure, timeZone }: { failure: TaskFailure; timeZone?: string }) {
  const { t, i18n } = useTranslation();
  const [available, setAvailable] = useState(false);
  const [open, setOpen] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  useEffect(() => {
    let active = true;
    void diagnosticsAvailable().then(value => { if (active) setAvailable(value); });
    return () => { active = false; };
  }, []);
  if (dismissed) return null;
  const date = failure.occurredAt ? new Date(failure.occurredAt) : null;
  const timestamp = date && Number.isFinite(date.getTime()) ? formatDateTime(date, { locale: i18n?.language, timeZone }) : null;
  return <section className="task-failure" aria-label={t("taskFailure.title")}>
    <div className="task-failure-heading"><span role="status">{t("taskFailure.message")}</span><span className="spacer" />{available ? <button type="button" className="btn ghost sm" onClick={() => setOpen(true)}>{t("taskFailure.report")}</button> : null}<button type="button" className="btn ghost sm" onClick={() => setDismissed(true)} aria-label={t("taskFailure.dismiss")}><Icon name="close" size={14} /></button></div>
    <details className="task-failure-details"><summary>{t("taskFailure.details")}</summary>
      <p className="muted">{t("taskFailure.localOnly")}</p>
      {failure.detail ? <pre>{failure.detail}</pre> : null}
      <dl><dt>{t("taskFailure.code")}</dt><dd>run_failed</dd><dt>{t("taskFailure.runId")}</dt><dd>{failure.runId}</dd><dt>{t("taskFailure.sessionId")}</dt><dd>{failure.sessionId}</dd>{timestamp ? <><dt>{t("taskFailure.time")}</dt><dd><time dateTime={failure.occurredAt}>{timestamp}</time></dd></> : null}<dt>{t("taskFailure.version")}</dt><dd>{APP_RELEASE_LABEL}</dd></dl>
    </details>
    {open ? <Suspense fallback={<p role="status">{t("diagnostics.preparing")}</p>}><DiagnosticsDialog sessionId={failure.sessionId} runId={failure.runId} onClose={() => setOpen(false)} /></Suspense> : null}
  </section>;
}
