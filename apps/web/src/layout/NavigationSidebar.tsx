// 导航中的最近任务常驻；固定与折叠仅为设备上的展示偏好，不改变会话业务状态。
import { useCallback, useEffect, useRef, useState, type MouseEvent } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { NavLink, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { Icon } from "../components/Icon";
import { Dialog, ErrorBanner } from "../components/ui";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import { readUiPreferences, writeUiPreferences } from "../lib/uiPreferences";
import { useWorkbenchCapabilities } from "../state/useWorkbenchCapabilities";
import type { Session, VersionInfo } from "../api/types";
import { AvatarSettingsMenu } from "./AvatarSettingsMenu";
import { WorkspaceSidebar } from "../workspace/WorkspaceSidebar";
import { SidebarProjects } from "./SidebarProjects";

export function NavigationSidebar({ openSearch, modifier }: { openSearch: () => void; modifier: string }) {
  const { t } = useTranslation();
  const api = useApi();
  const caps = useWorkbenchCapabilities();
  const client = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const location = useLocation();
  const [prefs, setPrefs] = useState(readUiPreferences);
  const [menu, setMenu] = useState<{ session: Session; x: number; y: number } | null>(null);
  const [avatarOpen, setAvatarOpen] = useState(false);
  const changeAvatarOpen = useCallback((open: boolean) => { if (open) setMenu(null); setAvatarOpen(open); }, []);
  const [rename, setRename] = useState<Session | null>(null);
  const [title, setTitle] = useState("");
  const menuRef = useRef<HTMLDivElement>(null);
  const menuTrigger = useRef<HTMLElement | null>(null);
  const recent = useQuery({ queryKey: [...queryKeys.sessionsRoot, "sidebar"], queryFn: () => api.sessions.list({ status: "active", limit: 12 }) });
  const currentSessionId = /^\/chat\/([^/]+)$/.exec(location.pathname)?.[1];
  const currentSession = useQuery({ queryKey: queryKeys.session(currentSessionId ?? ""), queryFn: () => api.sessions.get(currentSessionId!), enabled: caps.projects && !!currentSessionId, staleTime: 30_000 });
  const activeProjectId = location.pathname.startsWith("/projects/") ? location.pathname.split("/")[2] : new URLSearchParams(location.search).get("project") ?? (currentSession.data ? currentSession.data.project_id : recent.data?.find(session => session.id === currentSessionId)?.project_id);
  const pinQueries = useQueries({ queries: prefs.pinnedSessionIds.map((id) => ({ queryKey: queryKeys.session(id), queryFn: () => api.sessions.get(id), retry: false })) });
  const pinned = pinQueries.flatMap((query, index) => {
    const session = query.data ?? recent.data?.find((item) => item.id === prefs.pinnedSessionIds[index]);
    return session ? [session] : [];
  });
  const missingPins = prefs.pinnedSessionIds.filter((id, index) => pinQueries[index]?.isError && !pinned.some((item) => item.id === id));
  const unpinned = (recent.data ?? []).filter((session) => !prefs.pinnedSessionIds.includes(session.id));
  const update = (patch: Partial<typeof prefs>) => { setPrefs((previous) => ({ ...previous, ...patch })); writeUiPreferences(patch); };
  const mutation = useMutation({
    mutationFn: ({ session, title: nextTitle, status }: { session: Session; title?: string; status?: "active" | "archived" }) => api.sessions.update(session.id, { title: nextTitle, status }),
    onSuccess: (saved, input) => { client.setQueryData(queryKeys.session(saved.id), saved); void client.invalidateQueries({ queryKey: queryKeys.sessionsRoot }); if (input.title !== undefined) setRename((current) => current?.id === saved.id ? null : current); },
    onError: (error) => toast.show(describeError(error, t), "error"),
  });
  const closeMenu = () => { setMenu(null); menuTrigger.current?.focus(); };
  useEffect(() => {
    if (!menu) return;
    menuRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
    const close = (event: PointerEvent) => { if (!menuRef.current?.contains(event.target as Node)) setMenu(null); };
    window.addEventListener("pointerdown", close);
    return () => window.removeEventListener("pointerdown", close);
  }, [menu]);
  useEffect(() => { setMenu(null); }, [location.pathname]);
  const showMenu = (session: Session, event: MouseEvent<HTMLElement>) => {
    event.preventDefault();
    setAvatarOpen(false);
    menuTrigger.current = event.currentTarget;
    const rect = event.currentTarget.getBoundingClientRect();
    setMenu({ session, x: Math.min(event.type === "contextmenu" ? event.clientX : rect.left, window.innerWidth - 200), y: Math.min(rect.bottom + 4, window.innerHeight - 180) });
  };
  const row = (session: Session, isPinned: boolean) => <div className="nav-task-row" key={session.id}>
    <NavLink to={`/chat/${session.id}`} className={({ isActive }) => isActive ? "nav-task active" : "nav-task"} onContextMenu={(event) => showMenu(session, event)} title={session.title || t("chat.untitled")}>
      <span className={`nav-task-indicator ${session.active_run_id ? "running" : ""}`} aria-label={session.active_run_id ? t("chat.running") : undefined}>{isPinned ? "·" : ""}</span>
      <span>{session.title || t("chat.untitled")}</span>
      {session.status === "archived" ? <span className="nav-task-archived">{t("phase2.archived")}</span> : null}
    </NavLink>
    <button className="btn ghost sm nav-task-actions" aria-label={t("phase2.taskMenu", { title: session.title || t("chat.untitled") })} onClick={(event) => showMenu(session, event)}><Icon name="more" size={15} /></button>
  </div>;
  return <>
    <nav className="app-nav" aria-label={t("nav.label")}>
      <button type="button" className="btn primary new-task" onClick={() => void navigate("/chat")}><Icon name="plus" />{t("work.newTask")}<kbd>{modifier} N</kbd></button>
      <button className="nav-search" onClick={openSearch}><Icon name="search" />{t("work.searchTasks")}<kbd>{modifier} K</kbd></button>
      <NavLink to="/jobs"><Icon name="clock" />{t("nav.jobs")}</NavLink>
      <section className="nav-task-section" aria-label={t("phase2.recentTasks")}>
        <div className="nav-section-head"><button aria-expanded={prefs.taskListVisible} onClick={() => update({ taskListVisible: !prefs.taskListVisible })}><Icon name="chevron" size={13} />{t("phase2.recentTasks")}</button><button className="btn ghost sm" onClick={openSearch}>{t("phase2.all")}</button></div>
        {prefs.taskListVisible ? <div className="nav-task-list">
          <ErrorBanner error={recent.error} />
          {pinned.length ? <><span className="nav-subheading">{t("phase2.pinned")}</span>{pinned.map((session) => row(session, true))}</> : null}
          {missingPins.map((id, index) => <div className="nav-missing-pin" key={id}><span>{t("phase2.unavailablePinned", { n: index + 1 })}</span><button className="btn ghost sm" onClick={() => update({ pinnedSessionIds: prefs.pinnedSessionIds.filter((pin) => pin !== id) })}>{t("phase2.unpin")}</button></div>)}
          {unpinned.map((session) => row(session, false))}
          {!recent.isPending && !recent.error && !pinned.length && !unpinned.length ? <p className="nav-task-empty">{t("phase2.noTasks")}</p> : null}
        </div> : null}
      </section>
      {caps.projects ? <SidebarProjects activeProjectId={activeProjectId} onTaskMenu={showMenu} /> : null}
      <WorkspaceSidebar />
      <div className="nav-bottom"><AvatarSettingsMenu open={avatarOpen} onOpenChange={changeAvatarOpen} resourcesAvailable={!caps.error && client.getQueryData<VersionInfo>(queryKeys.version)?.capabilities?.["runtime.resources"] === 1} /></div>
    </nav>
    {menu ? createPortal(<div ref={menuRef} className="session-context-menu" role="menu" style={{ left: menu.x, top: menu.y }} aria-label={t("work.taskActions")} onKeyDown={(event) => {
      if (event.key === "Escape") { event.preventDefault(); closeMenu(); }
      if (["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) {
        event.preventDefault(); const items = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")); const index = items.indexOf(document.activeElement as HTMLButtonElement);
        const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
        items[next]?.focus();
      }
    }}>
      <button role="menuitem" onClick={() => { void navigate(`/chat/${menu.session.id}`); closeMenu(); }}>{t("phase2.openTask")}</button>
      <button role="menuitem" onClick={() => { const id = menu.session.id; update({ pinnedSessionIds: prefs.pinnedSessionIds.includes(id) ? prefs.pinnedSessionIds.filter((pin) => pin !== id) : [id, ...prefs.pinnedSessionIds].slice(0, 20) }); closeMenu(); }}>{t(prefs.pinnedSessionIds.includes(menu.session.id) ? "phase2.unpin" : "phase2.pin")}</button>
      <button role="menuitem" onClick={() => { setRename(menu.session); setTitle(menu.session.title ?? ""); setMenu(null); }}>{t("chat.rename")}</button>
      <button role="menuitem" disabled={!!menu.session.active_run_id || mutation.isPending} onClick={() => { mutation.mutate({ session: menu.session, status: menu.session.status === "archived" ? "active" : "archived" }); closeMenu(); }}>{t(menu.session.status === "archived" ? "chat.restore" : "chat.archive")}</button>
    </div>, document.body) : null}
    {rename ? <Dialog open title={t("chat.rename")} dismissible={!mutation.isPending} onClose={() => setRename(null)} footer={<><button className="btn" disabled={mutation.isPending} onClick={() => setRename(null)}>{t("common.cancel")}</button><button className="btn primary" disabled={!title.trim() || mutation.isPending} onClick={() => mutation.mutate({ session: rename, title: title.trim() })}>{t("common.save")}</button></>}><label htmlFor="sidebar-task-name">{t("work.taskName")}</label><input autoFocus id="sidebar-task-name" className="input" value={title} onChange={(event) => setTitle(event.target.value)} /></Dialog> : null}
  </>;
}
