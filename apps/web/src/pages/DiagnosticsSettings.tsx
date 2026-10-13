// 只探测本地发行能力；预览和上传模块在用户打开后才加载。
import { lazy, Suspense, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { diagnosticsAvailable } from "../api/diagnostics";
import { Spinner } from "../components/ui";

const DiagnosticsDialog = lazy(() => import("./DiagnosticsDialog").then(module => ({ default: module.DiagnosticsDialog })));

export function DiagnosticsSettings() {
  const { t } = useTranslation();
  const [available, setAvailable] = useState(false);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    let active = true;
    void diagnosticsAvailable().then(value => { if (active) setAvailable(value); });
    return () => { active = false; };
  }, []);
  if (!available) return null;
  return <>
    <div className="settings-row">
      <div className="settings-row-copy"><span className="settings-row-title">{t("diagnostics.title")}</span><p>{t("diagnostics.entryHint")}</p></div>
      <div className="settings-row-control"><button type="button" className="btn sm" onClick={() => setOpen(true)}>{t("diagnostics.open")}</button></div>
    </div>
    {open ? <Suspense fallback={<Spinner />}><DiagnosticsDialog onClose={() => setOpen(false)} /></Suspense> : null}
  </>;
}
