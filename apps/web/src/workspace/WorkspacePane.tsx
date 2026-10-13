import { lazy, Suspense, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../components/Icon";
import { Spinner } from "../components/ui";
import { useWorkbenchCapabilities } from "../state/useWorkbenchCapabilities";
import { clampArtifactWidth } from "../lib/uiPreferences";
import { useWorkspaceActions } from "./useWorkspaceActions";
const ContentPreview = lazy(() => import("../content/ContentPreview").then(module => ({ default: module.ContentPreview })));
const WorkspaceWebView = lazy(() => import("./WorkspaceWebView").then(module => ({ default: module.WorkspaceWebView })));

export function WorkspacePane({ width, onResize, onResizeEnd }: { width: number; onResize: (width: number) => void; onResizeEnd?: (width: number) => void }) {
  const { t } = useTranslation(); const { tabs, close } = useWorkspaceActions(); const caps = useWorkbenchCapabilities();
  const resize = useRef<{ x: number; width: number } | null>(null);
  const tab = tabs?.tabs.find(item => item.id === tabs.activeId);
  if (!tabs?.visible || !tab) return null;
  return <aside className="workspace-pane" aria-label={t("workspaceTabs.reading")}>
    <div className="panel-resize" role="separator" tabIndex={0} aria-label={t("work.resizeArtifact")} aria-orientation="vertical" aria-valuenow={width} aria-valuemin={420} aria-valuemax={640}
      onPointerDown={event => { resize.current = { x: event.clientX, width }; event.currentTarget.setPointerCapture(event.pointerId); }}
      onPointerMove={event => { if (resize.current && event.currentTarget.hasPointerCapture(event.pointerId)) onResize(clampArtifactWidth(resize.current.width + resize.current.x - event.clientX)); }}
      onPointerUp={event => { resize.current = null; event.currentTarget.releasePointerCapture(event.pointerId); onResizeEnd?.(width); }} onPointerCancel={() => { resize.current = null; }}
      onKeyDown={event => { const next = event.key === "ArrowLeft" ? width + 20 : event.key === "ArrowRight" ? width - 20 : event.key === "Home" ? 420 : event.key === "End" ? 640 : null; if (next !== null) { event.preventDefault(); onResize(clampArtifactWidth(next)); onResizeEnd?.(clampArtifactWidth(next)); } }} />
    <header className="workspace-pane-head"><button className="btn ghost sm" onClick={() => tabs.store.hide()} title={t("workspaceTabs.return")}><Icon name="panel" size={16} /><span>{t("workspaceTabs.return")}</span></button><span className="spacer" /><span className="workspace-open-count">{t("workspaceTabs.count", { n: tabs.tabs.length })}</span>{tab.kind === "web" ? <button className="btn ghost sm" aria-label={t("workspaceTabs.closeTab", { name: tab.title })} onClick={() => void close(tab.id)}><Icon name="close" size={16} /></button> : null}</header>
    <div className="workspace-pane-content"><Suspense fallback={<Spinner />}>
      {tab.kind === "file" ? caps.preview ? <ContentPreview key={tab.id} agentId={tab.agentId} path={tab.path} layoutAvailable={caps.previewLayout} onClose={() => void close(tab.id)} /> : <p className="empty">{t(caps.loading ? "common.loading" : "workspaceTabs.previewUnavailable")}</p> : <WorkspaceWebView key={tab.id} tab={tab} />}
    </Suspense></div>
  </aside>;
}
