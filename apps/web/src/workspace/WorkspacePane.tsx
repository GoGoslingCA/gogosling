import { lazy, Suspense } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../components/Icon";
import { Spinner } from "../components/ui";
import { useWorkbenchCapabilities } from "../state/useWorkbenchCapabilities";
import { usePanelResize } from "../lib/usePanelResize";
import { useWorkspaceActions } from "./useWorkspaceActions";
import { ContentPreview } from "../content/ContentPreview";
const WorkspaceWebView = lazy(() => import("./WorkspaceWebView").then(module => ({ default: module.WorkspaceWebView })));

export function WorkspacePane({ width, onResize, onResizeEnd }: { width: number; onResize: (width: number) => void; onResizeEnd?: (width: number) => void }) {
  const { t } = useTranslation(); const { tabs, close } = useWorkspaceActions(); const caps = useWorkbenchCapabilities();
  const tab = tabs?.tabs.find(item => item.id === tabs.activeId);
  const resize = usePanelResize({ width, onResize, onResizeEnd, enabled: Boolean(tabs?.visible && tab) });
  if (!tabs?.visible || !tab) return null;
  return <aside className="workspace-pane" aria-label={t("workspaceTabs.reading")}>
    <div className="panel-resize" role="separator" tabIndex={0} aria-label={t("work.resizeArtifact")} aria-orientation="vertical" aria-valuenow={width} aria-valuemin={420} aria-valuemax={640}
      {...resize} />
    <header className="workspace-pane-head"><button className="btn ghost sm" onClick={() => tabs.store.hide()} title={t("workspaceTabs.return")}><Icon name="panel" size={16} /><span>{t("workspaceTabs.return")}</span></button><span className="spacer" /><span className="workspace-open-count">{t("workspaceTabs.count", { n: tabs.tabs.length })}</span>{tab.kind === "web" ? <button className="btn ghost sm" aria-label={t("workspaceTabs.closeTab", { name: tab.title })} onClick={() => void close(tab.id)}><Icon name="close" size={16} /></button> : null}</header>
    <div className="workspace-pane-content"><Suspense fallback={<Spinner />}>
      {tab.kind === "file" ? caps.preview ? <ContentPreview key={tab.id} agentId={tab.agentId} path={tab.path} layoutAvailable={caps.previewLayout} onClose={() => void close(tab.id)} /> : <p className="empty">{t(caps.loading ? "common.loading" : "workspaceTabs.previewUnavailable")}</p> : <WorkspaceWebView key={tab.id} tab={tab} />}
    </Suspense></div>
  </aside>;
}
