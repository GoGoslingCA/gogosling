// 项目只组织同一助手的任务与资料；文件访问边界仍由守护进程决定。
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { useWorkbenchCapabilities } from "../state/useWorkbenchCapabilities";
import { queryKeys } from "../state/queryKeys";
import { ErrorBanner, Spinner } from "../components/ui";
import { CreateProjectDialog, ProjectForm } from "../components/ProjectForm";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import type { Project } from "../api/types";
import { MATERIAL_SELECTION_LIMIT } from "../api/modules/projectMaterialSelection";
import { PROJECT_PAGE_SIZE, projectPageItems, useProjectLists } from "./useProjectLists";
import "./projects.css";
import "../components/compact-workbench.css";

const ContentPreview = lazy(() => import("../content/ContentPreview").then(m => ({default:m.ContentPreview})));
export function ProjectsPage() {
 const [previewPath,setPreviewPath] = useState<string | null>(null);
  const { t } = useTranslation(); const api = useApi(); const caps = useWorkbenchCapabilities();
  const client = useQueryClient(); const toast = useToast(); const navigate = useNavigate();
  const { projectId } = useParams<{ projectId: string }>();
 useEffect(() => {setPreviewPath(null);},[projectId]);
  const [archived, setArchived] = useState(false); const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Project | null>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedTab = searchParams.get("view");
  const tab = requestedTab === "tasks" || requestedTab === "artifacts" ? requestedTab : "materials";
  const setTab = (nextTab: "materials" | "tasks" | "artifacts") => setSearchParams((previous) => {
    const next = new URLSearchParams(previous);
    if (nextTab === "materials") next.delete("view"); else next.set("view", nextTab);
    return next;
  }, { replace: true });
  const [filter, setFilter] = useState("");
  useEffect(() => { setFilter(""); }, [projectId, tab]);
  const [selection, setSelection] = useState<Record<string, string[]>>({});
  const [uploading, setUploading] = useState(false); const uploadLock = useRef(false); const fileInput = useRef<HTMLInputElement>(null);
  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list() });
  const projects = useInfiniteQuery({ queryKey: [...queryKeys.projects, "list", archived, "pages"], initialPageParam: 0, queryFn: ({ pageParam, signal }) => api.workbench.listProjects({ status: archived ? "archived" : "active", limit: PROJECT_PAGE_SIZE, offset: pageParam }, signal), getNextPageParam: (last, pages) => last.length === PROJECT_PAGE_SIZE ? pages.length * PROJECT_PAGE_SIZE : undefined, enabled: caps.projects && !projectId, staleTime: 30_000 });
  useEffect(() => () => { void client.cancelQueries({ queryKey: [...queryKeys.projects, "list", archived, "pages"], exact: true }); }, [client, archived, projectId]);
  const project = useQuery({ queryKey: queryKeys.project(projectId ?? ""), queryFn: () => api.workbench.getProject(projectId!), enabled: caps.projects && !!projectId });
  const { materials, sessions, artifacts } = useProjectLists(projectId, tab, caps);
  const projectItems = projectPageItems(projects.data?.pages);
  const materialItems = projectPageItems(materials.data?.pages);
  const sessionItems = projectPageItems(sessions.data?.pages);
  const artifactItems = projectPageItems(artifacts.data?.pages);
  const selected = (selection[projectId ?? ""] ?? []).filter((id) => materialItems.some((material) => material.id === id));
  const matches = (...values: (string | undefined)[]) => !filter.trim() || values.some((value) => value?.toLocaleLowerCase().includes(filter.trim().toLocaleLowerCase()));
  const visibleProjects = projectItems.filter((item) => matches(item.name, item.description));
  const visibleMaterials = materialItems.filter((item) => matches(item.name));
  const visibleSessions = sessionItems.filter((item) => matches(item.title || t("chat.untitled")));
  const visibleArtifacts = artifactItems.filter((item) => matches(item.title, item.filename));
  const currentList = !projectId ? projects : tab === "materials" ? materials : tab === "tasks" ? sessions : artifacts;
  const currentItems = !projectId ? projectItems : tab === "materials" ? materialItems : tab === "tasks" ? sessionItems : artifactItems;
  const visibleCount = !projectId ? visibleProjects.length : tab === "materials" ? visibleMaterials.length : tab === "tasks" ? visibleSessions.length : visibleArtifacts.length;
  const filterControl = <div className="project-list-filter"><input className="input" type="search" aria-label={t("projectList.search")} placeholder={t("projectList.search")} value={filter} onChange={(event) => setFilter(event.target.value)} /><span className="muted">{t("projectList.loaded", { n: currentItems.length })}{currentList.hasNextPage ? ` · ${t("projectList.loadedOnly")}` : ""}</span></div>;
  const moreControl = <div className="project-list-more">{filter.trim() && !visibleCount && currentItems.length ? <p className="muted">{t("projectList.noMatch")}</p> : null}{currentList.hasNextPage ? <button className="btn ghost" disabled={currentList.isFetching} onClick={() => void currentList.fetchNextPage()}>{t(currentList.isFetchingNextPage ? "common.loading" : "projectList.loadMore")}</button> : null}</div>;
  const currentAgent = agents.data?.find((agent) => agent.id === project.data?.agent_id);
  const tabs = [
    { id: "materials", icon: "folder", count: materials.data ? materialItems.length : undefined },
    { id: "tasks", icon: "chat", count: sessions.data ? sessionItems.length : undefined },
    { id: "artifacts", icon: "document", count: artifacts.data ? artifactItems.length : undefined },
  ] as const;
  const refresh = () => { void client.invalidateQueries({ queryKey: queryKeys.projects }); };
  const saveProject = useMutation({
    mutationFn: ({ project: target, name, description }: { project: Project; name: string; description: string }) => api.workbench.updateProject(target.id, { expected_version: target.version, name, description }),
    onSuccess: (saved) => { client.setQueryData(queryKeys.project(saved.id), saved); refresh(); setCreating(false); setEditing(null); void navigate(`/projects/${saved.id}`); },
    onError: (error) => toast.show(describeError(error, t), "error"),
  });
  const archive = useMutation({
    mutationFn: (item: Project) => api.workbench.updateProject(item.id, { expected_version: item.version, status: item.status === "active" ? "archived" : "active" }),
    onSuccess: (saved) => { client.setQueryData(queryKeys.project(saved.id), saved); refresh(); },
    onError: (error) => { toast.show(describeError(error, t), "error"); refresh(); },
  });
  const remove = useMutation({
    mutationFn: ({ projectId: target, materialId }: { projectId: string; materialId: string }) => api.workbench.removeMaterial(target, materialId),
    onSuccess: (_, input) => void client.invalidateQueries({ queryKey: queryKeys.materials(input.projectId) }),
    onError: (error) => toast.show(describeError(error, t), "error"),
  });
  const upload = async (files: File[]) => {
    if (!projectId || !files.length || uploadLock.current || project.data?.status !== "active") return;
    const target = projectId; uploadLock.current = true; setUploading(true);
    try {
      for (const file of files) {
        try { await api.workbench.uploadMaterial(target, file); }
        catch (error) { toast.show(`${file.name}: ${describeError(error, t)}`, "error"); }
      }
      await client.invalidateQueries({ queryKey: queryKeys.materials(target) });
    } finally { uploadLock.current = false; setUploading(false); }
  };
  if (caps.loading) return <div className="page"><Spinner /></div>;
  if (!caps.projects) return <div className="page"><h1>{t("projects.title")}</h1><ErrorBanner error={caps.error} /><p>{t("projects.unavailable")}</p><Link to="/chat">{t("work.startPage")}</Link></div>;
  return <div className="projects-page page">
    <div className="page-head"><h1>{projectId ? project.data?.name ?? t("common.loading") : t("projects.title")}</h1><button className="btn primary" onClick={() => { setEditing(null); setCreating(true); }}><Icon name="plus" />{t("projects.create")}</button></div>
    <ErrorBanner error={projectId ? project.error : projects.error} />
    {!projectId ? <>
      <div className="project-intro"><p>{t("projects.intro")}</p><label className="check"><input type="checkbox" checked={archived} onChange={(event) => setArchived(event.target.checked)} />{t("projects.showArchived")}</label></div>
      {projects.isPending ? <Spinner /> : null}
      {filterControl}
      <div className="project-grid">{visibleProjects.map((item) => <Link className="project-card" key={item.id} to={`/projects/${item.id}`}><Icon name="folder" size={24} /><h2>{item.name}</h2><p>{item.description || t("projects.openHint")}</p><span>{agents.data?.find((agent) => agent.id === item.agent_id)?.name ?? item.agent_id}</span></Link>)}</div>
      {!projects.isPending && !projects.error && !projectItems.length ? <p className="empty">{t("projects.empty")}</p> : null}
      {moreControl}
    </> : project.data ? <>
      <div className="project-details-head"><Link to="/projects">← {t("projects.all")}</Link><span>{currentAgent?.name}</span><span className="badge">{t(project.data.status === "active" ? "projects.active" : "projects.archived")}</span><button className="btn ghost sm" onClick={() => { setEditing(project.data!); setCreating(true); }}>{t("common.edit")}</button><button className="btn ghost sm" disabled={archive.isPending} onClick={() => archive.mutate(project.data!)}>{t(project.data.status === "active" ? "chat.archive" : "chat.restore")}</button></div>
      <details className="project-overview compact-explainer"><summary><Icon name="shield" size={14} />{t("compact.projectDetails")}<Icon name="chevron" size={12} /></summary>{project.data.description ? <p>{project.data.description}</p> : null}<p className="project-scope">{t("projects.scopeHint")} <code>{currentAgent?.workspace_dir}</code></p></details>
      <div className="project-tabs" role="tablist" aria-label={t("projects.views")} onKeyDown={(event) => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const index = tabs.findIndex((item) => item.id === tab);
        const next = event.key === "Home" ? tabs[0] : event.key === "End" ? tabs[2] : tabs[(index + (event.key === "ArrowRight" ? 1 : 2)) % tabs.length]!;
        setTab(next.id);
        event.currentTarget.querySelector<HTMLButtonElement>(`#project-tab-${next.id}`)?.focus();
      }}>{tabs.map((item) => <button type="button" role="tab" key={item.id} id={`project-tab-${item.id}`} aria-controls={`project-panel-${item.id}`} aria-label={t(`projects.${item.id}`)} tabIndex={tab === item.id ? 0 : -1} aria-selected={tab === item.id} className={tab === item.id ? "btn selected" : "btn ghost"} onClick={() => setTab(item.id)}><Icon name={item.icon} size={17} /><span>{t(`projects.${item.id}`)}</span>{item.count !== undefined ? <span className="project-tab-count" title={t("compact.loadedCount", { n: item.count })}>{item.count}</span> : null}</button>)}</div>
      {filterControl}
      {tab === "materials" ? <section role="tabpanel" id="project-panel-materials" aria-labelledby="project-tab-materials" tabIndex={0} onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = project.data?.status === "active" ? "copy" : "none"; }} onDrop={(event) => { event.preventDefault(); void upload(Array.from(event.dataTransfer.files)); }}>
        <div className="project-action-row"><details className="compact-explainer"><summary><Icon name="check" size={14} />{t("compact.selectMaterials")}<Icon name="chevron" size={12} /></summary><p>{t("projects.materialSelectionHint")}</p></details><input ref={fileInput} type="file" multiple hidden onChange={(event) => { const files = Array.from(event.target.files ?? []); event.target.value = ""; void upload(files); }} /><button className="btn" disabled={!caps.materials || uploading || project.data.status !== "active"} onClick={() => fileInput.current?.click()}><Icon name="plus" size={15} />{t(uploading ? "chat.uploading" : "projects.addMaterials")}</button></div>
        <ErrorBanner error={materials.error} />
        {materials.isPending ? <Spinner /> : null}
        <div className="project-material-list">{visibleMaterials.map((material) => <div className="project-material-row" key={material.id}><label><input type="checkbox" checked={selected.includes(material.id)} disabled={!selected.includes(material.id) && selected.length >= MATERIAL_SELECTION_LIMIT} onChange={(event) => setSelection((previous) => ({ ...previous, [projectId]: event.target.checked ? [...(previous[projectId] ?? []), material.id] : (previous[projectId] ?? []).filter((id) => id !== material.id) }))} /><Icon name="document" /><span>{material.name}<small>{Math.ceil(material.size / 1024)} KB</small></span></label>{caps.preview ? <button className="btn ghost sm" onClick={() => setPreviewPath(material.attachment.path)}>{t("preview.open")}</button> : null}<button className="btn ghost sm" disabled={remove.isPending || project.data?.status !== "active"} onClick={() => { if (window.confirm(t("projects.confirmRemove"))) remove.mutate({ projectId, materialId: material.id }); }}>{t("projects.removeReference")}</button></div>)}</div>
        {!materials.isPending && !materials.error && !materialItems.length ? <p className="empty">{t("projects.noMaterials")}</p> : null}
        <div className="project-start"><span><Icon name="document" size={15} />{t("projects.selected", { n: selected.length })}</span><button className="btn primary" disabled={project.data.status !== "active" || uploading} onClick={() => void navigate(`/chat?project=${encodeURIComponent(projectId)}${selected.length ? `&materials=${selected.map(encodeURIComponent).join(",")}` : ""}`)}>{t("projects.startTask")}<Icon name="arrow" /></button></div>
      </section> : null}
      {tab === "tasks" ? <section role="tabpanel" id="project-panel-tasks" aria-labelledby="project-tab-tasks" tabIndex={0}><ErrorBanner error={sessions.error} />{sessions.isPending ? <Spinner /> : null}<div className="project-task-list">{visibleSessions.map((item) => <Link key={item.id} to={`/chat/${item.id}`}><Icon name="chat" /><span>{item.title || t("chat.untitled")}</span><span className="badge">{t(item.status === "archived" ? "projects.archived" : item.active_run_id ? "chat.running" : "compact.taskOpen")}</span></Link>)}</div>{!sessions.isPending && !sessions.error && !sessionItems.length ? <p className="empty">{t("projects.noTasks")}</p> : null}</section> : null}
      {tab === "artifacts" ? <section role="tabpanel" id="project-panel-artifacts" aria-labelledby="project-tab-artifacts" tabIndex={0}><ErrorBanner error={artifacts.error} />{artifacts.isPending ? <Spinner /> : null}<div className="project-task-list">{visibleArtifacts.map((item) => <Link key={item.id} to={`/chat/${item.session_id}?artifact=${item.id}`}><Icon name={item.kind === "code" ? "code" : "document"} /><span>{item.title}<small>{item.filename}</small></span><span className="badge">{t("projects.version", { n: item.version })}</span></Link>)}</div>{!artifacts.isPending && !artifacts.error && !artifactItems.length ? <p className="empty">{t("projects.noArtifacts")}</p> : null}</section> : null}
      {moreControl}
    </> : null}
    {previewPath && project.data && caps.preview ? <div className="project-preview-container"><Suspense fallback={<Spinner/>}><ContentPreview agentId={project.data.agent_id} path={previewPath} layoutAvailable={caps.previewLayout} onClose={() => setPreviewPath(null)}/></Suspense></div> : null}
    {creating ? editing ? <ProjectForm key={editing.id} project={editing} agents={agents.data ?? []} busy={saveProject.isPending} loadingAgents={agents.isFetching} agentError={agents.error} onRetryAgents={() => { void agents.refetch(); }} onClose={() => setCreating(false)} onSubmit={(_agentId, name, description) => saveProject.mutate({ project: editing, name, description })} /> : <CreateProjectDialog onClose={() => setCreating(false)} onCreated={(saved) => { setCreating(false); void navigate(`/projects/${saved.id}`); }} /> : null}
  </div>;
}
