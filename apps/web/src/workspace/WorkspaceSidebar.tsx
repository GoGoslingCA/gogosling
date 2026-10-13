import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../components/Icon";
import { Dialog } from "../components/ui";
import { useWorkspaceActions } from "./useWorkspaceActions";
import { WORKSPACE_TAB_LIMIT } from "../state/workspaceTabs";

export function WorkspaceSidebar() {
  const { t } = useTranslation(); const { tabs, close } = useWorkspaceActions();
  const rows = useRef(new Map<string, HTMLButtonElement>()); const addButton = useRef<HTMLButtonElement>(null);
  const [adding, setAdding] = useState(false); const [url, setURL] = useState(""); const [error, setError] = useState<string | null>(null);
  if (!tabs) return null;
  return <section className="workspace-open-items" aria-label={t("workspaceTabs.opened")}>
    <div className="nav-section-head"><span>{t("workspaceTabs.opened")}{tabs.tabs.length ? <small>{tabs.tabs.length}</small> : null}</span><button type="button" className="btn ghost sm" ref={addButton} aria-label={t("workspaceTabs.addWeb")} title={t("workspaceTabs.addWeb")} onClick={() => { setAdding(true); setURL(""); setError(null); }}><Icon name="plus" size={15} /></button></div>
    {tabs.tabs.map(tab => <div className={`workspace-open-row ${tabs.visible && tabs.activeId === tab.id ? "active" : ""}`} key={tab.id}>
      <button type="button" className="workspace-open-link" ref={element => { if (element) rows.current.set(tab.id, element); else rows.current.delete(tab.id); }} aria-pressed={tabs.visible && tabs.activeId === tab.id} title={tab.kind === "file" ? tab.path : tab.url} onClick={() => tabs.store.activate(tab.id)}><Icon name={tab.kind === "file" ? "document" : "globe"} size={16} /><span>{tab.title}</span></button>
      <button type="button" className="workspace-close-tab" aria-label={t("workspaceTabs.closeTab", { name: tab.title })} onClick={event => { const keyboard = event.detail === 0; void close(tab.id).then(() => { if (!keyboard || tabs.store.getSnapshot().tabs.some(item => item.id === tab.id)) return; requestAnimationFrame(() => { const active = tabs.store.getSnapshot().activeId; (active ? rows.current.get(active) : addButton.current)?.focus(); }); }); }}><Icon name="close" size={13} /></button>
    </div>)}
    {!tabs.tabs.length ? <p className="workspace-open-empty">{t("workspaceTabs.empty")}</p> : null}
    {adding ? <Dialog open title={t("workspaceTabs.addWeb")} onClose={() => setAdding(false)} footer={<><button className="btn" onClick={() => setAdding(false)}>{t("common.cancel")}</button><button className="btn primary" type="submit" form="workspace-add-web">{t("workspaceTabs.open")}</button></>}><form id="workspace-add-web" onSubmit={event => { event.preventDefault(); const result = tabs.store.openWeb(url); if (result === "limit") setError(t("workspaceTabs.limit", { n: WORKSPACE_TAB_LIMIT })); else if (result === "invalid") setError(t("workspaceTabs.invalid_url")); else setAdding(false); }}><label htmlFor="workspace-web-url">{t("workspaceTabs.address")}</label><input autoFocus id="workspace-web-url" className="input" type="url" maxLength={4096} required placeholder="https://" value={url} onChange={event => { setURL(event.target.value); setError(null); }} />{error ? <p role="alert">{error}</p> : null}<p className="muted">{t("workspaceTabs.webHint")}</p></form></Dialog> : null}
  </section>;
}
