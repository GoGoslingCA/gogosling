// 项目导航只读取元数据；仅展开的项目订阅任务列表，不加载消息、资料或成果正文。
import { lazy, Suspense, useEffect, useId, useState, type MouseEvent } from "react";
import { Link, NavLink, useLocation, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Project, Session } from "../api/types";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { readUiPreferences, writeUiPreferences, type UiPreferences } from "../lib/uiPreferences";
import { formatRelative } from "../lib/time";
import { Icon } from "../components/Icon";
import { ErrorBanner, Spinner } from "../components/ui";
import "./sidebar-projects.css";

const CreateProjectDialog = lazy(() => import("../components/ProjectForm").then(module => ({ default: module.CreateProjectDialog })));
type TaskMenu = (session: Session, event: MouseEvent<HTMLElement>) => void;

/** 项目与会话继续使用原有路由和权限；窄窗口保留项目总览入口。 */
export function SidebarProjects({ activeProjectId, onTaskMenu }: { activeProjectId?: string; onTaskMenu: TaskMenu }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const listId = useId();
  const [prefs, setPrefs] = useState(readUiPreferences);
  const [creating, setCreating] = useState(false);
  const [narrow, setNarrow] = useState(() => window.matchMedia?.("(max-width: 760px)").matches ?? false);
  const update = (patch: Partial<UiPreferences>) => { setPrefs(previous => ({ ...previous, ...patch })); writeUiPreferences(patch); };
  useEffect(() => {
    const media = window.matchMedia?.("(max-width: 760px)");
    if (!media) return;
    const change = () => setNarrow(media.matches);
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, []);
  useEffect(() => {
    if (activeProjectId) update({ expandedProjectId: activeProjectId, projectListVisible: true });
  }, [activeProjectId, location.pathname]);
  const created = (project: Project) => {
    setCreating(false);
    update({ expandedProjectId: project.id, projectListVisible: true });
    void navigate(`/projects/${project.id}`);
  };
  return <>
    {narrow ? <NavLink className="nav-projects-mobile" to="/projects"><Icon name="folder" />{t("projects.title")}</NavLink> : <section className="nav-project-section" aria-label={t("projects.title")}>
      <div className="nav-section-head">
        <button type="button" aria-expanded={prefs.projectListVisible} aria-controls={listId} onClick={() => update({ projectListVisible: !prefs.projectListVisible })}><Icon name="chevron" size={13} />{t("projects.title")}</button>
        <div className="nav-project-actions">
          <NavLink to="/projects" aria-label={t("projects.all")}>{t("phase2.all")}</NavLink>
          <button type="button" className="btn ghost sm" aria-label={t("projects.create")} title={t("projects.create")} onClick={() => setCreating(true)}><Icon name="plus" size={15} /></button>
        </div>
      </div>
      {prefs.projectListVisible ? <ProjectList listId={listId} activeProjectId={activeProjectId} expandedProjectId={prefs.expandedProjectId} onToggle={(id) => update({ expandedProjectId: prefs.expandedProjectId === id ? null : id })} onCreate={() => setCreating(true)} onTaskMenu={onTaskMenu} /> : null}
    </section>}
    {creating ? <Suspense fallback={<Spinner />}><CreateProjectDialog onClose={() => setCreating(false)} onCreated={created} /></Suspense> : null}
  </>;
}

// 隐藏时移除查询订阅；仅最后一个使用者退出时取消读取，不中断项目页的共享请求。
function ProjectList({ listId, activeProjectId, expandedProjectId, onToggle, onCreate, onTaskMenu }: { listId: string; activeProjectId?: string; expandedProjectId: string | null; onToggle: (id: string) => void; onCreate: () => void; onTaskMenu: TaskMenu }) {
  const { t } = useTranslation();
  const api = useApi();
  const projects = useQuery({
    queryKey: [...queryKeys.projects, "list", false],
    queryFn: ({ signal }) => api.workbench.listProjects({ status: "active", limit: 200 }, signal),
    staleTime: 30_000,
  });
  const active = (projects.data ?? []).filter(project => project.status === "active");
  const visible = active.slice(0, 8);
  const selected = active.find(project => project.id === activeProjectId);
  if (selected && !visible.some(project => project.id === selected.id)) visible.push(selected);
  return <div className="nav-project-list" id={listId}>
    {projects.isPending ? <p className="nav-task-empty" role="status">{t("common.loading")}</p> : null}
    {projects.error ? <><ErrorBanner error={projects.error} /><button className="btn ghost sm" onClick={() => void projects.refetch()}>{t("projects.retryLoading")}</button></> : null}
    {visible.map(project => <ProjectRow key={project.id} project={project} expanded={expandedProjectId === project.id} current={activeProjectId === project.id} onToggle={() => onToggle(project.id)} onTaskMenu={onTaskMenu} />)}
    {!projects.isPending && !projects.error && !active.length ? <button className="nav-project-empty" onClick={onCreate}><Icon name="plus" size={14} />{t("projects.create")}</button> : null}
  </div>;
}

function ProjectRow({ project, expanded, current, onToggle, onTaskMenu }: { project: Project; expanded: boolean; current: boolean; onToggle: () => void; onTaskMenu: TaskMenu }) {
  const { t } = useTranslation();
  const listId = useId();
  return <div className="nav-project-group">
    <div className={`nav-project-row${current ? " current" : ""}`}>
      <button className="nav-project-toggle" aria-label={t("projects.toggleTasks", { name: project.name })} aria-expanded={expanded} aria-controls={listId} onClick={onToggle}><Icon name="chevron" size={12} /></button>
      <NavLink className="nav-project-name" to={`/projects/${project.id}`} title={project.name}><Icon name="folder" size={15} /><span>{project.name}</span></NavLink>
    </div>
    {expanded ? <ProjectTasks project={project} listId={listId} onTaskMenu={onTaskMenu} /> : null}
  </div>;
}

function ProjectTasks({ project, listId, onTaskMenu }: { project: Project; listId: string; onTaskMenu: TaskMenu }) {
  const { t, i18n } = useTranslation();
  const api = useApi();
  const sessions = useQuery({
    queryKey: [...queryKeys.sessionsRoot, "sidebar-project", project.id],
    queryFn: ({ signal }) => api.sessions.list({ project_id: project.id, status: "active", limit: 6 }, signal),
    staleTime: 30_000,
  });
  const labels = { justNow: t("time.justNow"), minutes: (n: number) => t("time.minutesAgo", { n }), hours: (n: number) => t("time.hoursAgo", { n }), days: (n: number) => t("time.daysAgo", { n }) };
  return <div className="nav-project-tasks" id={listId} role="group" aria-label={project.name}>
    {sessions.isPending ? <p className="nav-task-empty" role="status">{t("common.loading")}</p> : null}
    {sessions.error ? <><ErrorBanner error={sessions.error} /><button className="btn ghost sm" onClick={() => void sessions.refetch()}>{t("projects.retryLoading")}</button></> : null}
    {(sessions.data ?? []).map(session => <div className="nav-project-task" key={session.id}>
      <NavLink to={`/chat/${session.id}`} title={session.title || t("chat.untitled")} onContextMenu={event => onTaskMenu(session, event)}>
        <span className="nav-project-task-title">{session.title || t("chat.untitled")}</span>
        {session.active_run_id ? <span className="nav-project-running" aria-label={t("chat.running")} /> : <time dateTime={session.updated_at}>{formatRelative(session.updated_at, labels, { locale: i18n?.language })}</time>}
      </NavLink>
      <button className="btn ghost sm nav-project-task-menu" aria-label={t("phase2.taskMenu", { title: session.title || t("chat.untitled") })} onClick={event => onTaskMenu(session, event)}><Icon name="more" size={14} /></button>
    </div>)}
    {!sessions.isPending && !sessions.error && !sessions.data?.length ? <p className="nav-task-empty">{t("projects.noTasks")}</p> : null}
    <div className="nav-project-task-links"><Link to={`/chat?project=${encodeURIComponent(project.id)}`}><Icon name="plus" size={12} />{t("work.newTask")}</Link><Link to={`/projects/${project.id}?view=tasks`} aria-label={t("projects.tasks")}>{t("phase2.all")}</Link></div>
  </div>;
}
