import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { workspaceBrowser, type WorkspaceBrowserState } from "../api/workspaceBrowser";
import { Icon } from "../components/Icon";
import { useWorkspaceTabs } from "../state/WorkspaceTabsProvider";
import { workspaceWebURL, type WorkspaceTab } from "../state/workspaceTabs";

export function WorkspaceWebView({ tab }: { tab: Extract<WorkspaceTab, { kind: "web" }> }) {
  const { t } = useTranslation(); const tabs = useWorkspaceTabs();
  const host = useRef<HTMLDivElement>(null); const [state, setState] = useState<WorkspaceBrowserState | null>(null);
  const [address, setAddress] = useState(tab.url); const [error, setError] = useState<WorkspaceBrowserState["error"]>();
  const bridge = workspaceBrowser(); const editingAddress = useRef(false); const active = useRef(true); const mountedURL = useRef(tab.url);
  const retryShow = useRef<() => void>(() => undefined); const cancelRetry = useRef<() => void>(() => undefined);
  const accept = useRef<(next: WorkspaceBrowserState) => void>(() => undefined);
  accept.current = next => {
    if (!active.current) return;
    if (next.tabId !== tab.id) {
      if (next.error === "cancelled") { setError("cancelled"); tabs?.store.activate(next.tabId); }
      return;
    }
    setState(next); setError(next.error); if (!editingAddress.current) setAddress(next.url);
    if (workspaceWebURL(next.url)) { mountedURL.current = next.url; tabs?.store.updateWeb(tab.id, next.url, next.title); }
  };
  useEffect(() => {
    active.current = true;
    if (!bridge || !host.current) return () => { active.current = false; };
    let disposed = false; let frame = 0; let sequence = 0; let occluded = false; let lastLayout = ""; let busyAttempts = 0; let retryTimer: number | undefined;
    cancelRetry.current = () => { window.clearTimeout(retryTimer); retryTimer = undefined; };
    const unsubscribe = bridge.onState(next => { if (!disposed && (next.tabId === tab.id || next.error === "cancelled")) accept.current(next); });
    const update = () => {
      frame = 0;
      const el = host.current; if (!el || disposed) return;
      // 原生网页在 DOM 上方；审批、菜单与对话框展开时先隐藏，避免遮住必要操作。
      const blocked = document.visibilityState === "hidden" || [...document.querySelectorAll('dialog[open], [role="dialog"], [role="alertdialog"], [role="menu"], details[data-task-popover][open], details.task-menu[open], details.connection-details[open]')].some(element => element.getClientRects().length > 0);
      const rect = el.getBoundingClientRect();
      const layout = `${blocked}:${Math.round(rect.x)}:${Math.round(rect.y)}:${Math.round(rect.width)}:${Math.round(rect.height)}`;
      if (layout === lastLayout) return;
      lastLayout = layout; const request = ++sequence; cancelRetry.current();
      if (blocked || rect.width < 40 || rect.height < 40) { if (!occluded) void bridge.hide().catch(() => undefined); occluded = true; return; }
      occluded = false;
      void bridge.show({ tabId: tab.id, url: mountedURL.current, bounds: { x: rect.x, y: rect.y, width: rect.width, height: rect.height } }).then(next => {
        if (disposed) return;
        if (request === sequence || next.error === "cancelled") accept.current(next);
        if (request === sequence && next.error === "busy" && busyAttempts < 4) {
          const delay = [120, 250, 500, 1000][busyAttempts++]!;
          retryTimer = window.setTimeout(() => { if (!disposed) { lastLayout = ""; schedule(); } }, delay);
        } else if (request === sequence && next.error !== "busy") busyAttempts = 0;
      }).catch(() => { if (!disposed && request === sequence) setError("unavailable"); });
    };
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update); };
    retryShow.current = () => { busyAttempts = 0; lastLayout = ""; cancelRetry.current(); schedule(); };
    const observer = new ResizeObserver(schedule); observer.observe(host.current);
    const modals = new MutationObserver(schedule); modals.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["open", "role"] });
    window.addEventListener("resize", schedule); window.addEventListener("scroll", schedule, true); document.addEventListener("visibilitychange", schedule);
    // 先撤下旧页面，换页被 beforeunload 拒绝时不把旧网页显示在新标题下。
    void bridge.hide().catch(() => undefined).then(() => { if (!disposed) schedule(); });
    return () => { disposed = true; active.current = false; cancelRetry.current(); cancelRetry.current = () => undefined; retryShow.current = () => undefined; cancelAnimationFrame(frame); observer.disconnect(); modals.disconnect(); unsubscribe(); window.removeEventListener("resize", schedule); window.removeEventListener("scroll", schedule, true); document.removeEventListener("visibilitychange", schedule); void bridge.hide().catch(() => undefined); };
  }, [bridge, tab.id]);
  const action = async (value: "back" | "forward" | "reload" | "stop") => {
    if (!bridge) return;
    editingAddress.current = false;
    try { accept.current(await bridge.action({ tabId: tab.id, action: value })); } catch { if (active.current) setError("unavailable"); }
  };
  return <div className="workspace-web-view">
    <form className="workspace-web-toolbar" onSubmit={event => {
      event.preventDefault(); const url = workspaceWebURL(address); if (!url) { setError("invalid_url"); return; }
      editingAddress.current = false; mountedURL.current = url; cancelRetry.current(); setError(undefined);
      if (!bridge) { tabs?.store.updateWeb(tab.id, url); return; }
      void bridge.navigate({ tabId: tab.id, url }).then(next => accept.current(next)).catch(() => { if (active.current) setError("unavailable"); });
    }}>
      {bridge ? <><button type="button" className="btn ghost sm" disabled={!state?.canGoBack} aria-label={t("workspaceTabs.back")} onClick={() => void action("back")}><Icon name="chevron" size={15} style={{ transform: "rotate(180deg)" }} /></button><button type="button" className="btn ghost sm" disabled={!state?.canGoForward} aria-label={t("workspaceTabs.forward")} onClick={() => void action("forward")}><Icon name="chevron" size={15} /></button><button type="button" className="btn ghost sm" onClick={() => void action(state?.loading ? "stop" : "reload")}>{t(state?.loading ? "workspaceTabs.stop" : "preview.refresh")}</button></> : null}
      <input type="url" required maxLength={4096} aria-label={t("workspaceTabs.address")} value={address} onChange={event => { editingAddress.current = true; setAddress(event.target.value); }} onKeyDown={event => { if (event.key === "Escape") { event.preventDefault(); editingAddress.current = false; setAddress(state?.url ?? tab.url); } }} />
      <button className="btn ghost sm" type="submit">{t("workspaceTabs.go")}</button>
      {bridge ? <a className="btn ghost sm" href={workspaceWebURL(state?.url ?? tab.url) ?? tab.url} target="_blank" rel="noopener noreferrer" title={t("workspaceTabs.external")} aria-label={t("workspaceTabs.external")}><Icon name="arrow" size={16} style={{ transform: "rotate(-45deg)" }} /></a> : null}
    </form>
    {error ? <div className="workspace-web-error" role="alert">{t(`workspaceTabs.${error}`)}{["busy", "unavailable", "load_failed"].includes(error) ? <button className="btn ghost sm" onClick={() => { if (error === "load_failed") void action("reload"); else retryShow.current(); }}>{t("chat.retryLoad")}</button> : null}</div> : null}
    <div className="workspace-web-host" ref={host}>
      {!bridge ? <div className="workspace-web-fallback"><Icon name="globe" size={30} /><h2>{tab.title}</h2><p>{t("workspaceTabs.externalHint")}</p><a className="btn" href={tab.url} target="_blank" rel="noopener noreferrer">{t("workspaceTabs.external")}</a></div> : <p className="workspace-web-placeholder">{t(state?.loading ? "common.loading" : "workspaceTabs.webHint")}</p>}
    </div>
  </div>;
}
