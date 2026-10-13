// 聊天页：会话搜索、分页与归档；持久化消息和事件流共同恢复运行状态。

import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useBlocker, useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useApi } from "../../state/ConnectionContext";
import { useLiveRunsDispatch, useSessionLiveRuns } from "../../state/EventStreamProvider";
import { queryKeys } from "../../state/queryKeys";
import { useTimeZone } from "../../state/useSettings";
import { Dialog, ErrorBanner } from "../../components/ui";
import { useToast } from "../../components/Toast";
import { ApiError } from "../../api/client";
import { describeError } from "../../lib/errorMessage";
import { lastSeq, mergeMessages } from "../../lib/messages";
import { fallbackRunId, resolveActiveRun } from "../../lib/activeRun";
import { MESSAGE_FETCH_CAP } from "../../api/modules/sessions";
import type { Attachment, Draft, Message, Session, SessionStatus } from "../../api/types";
import { SessionList } from "./SessionList";
import { MessageList } from "./MessageList";
import { Composer } from "./Composer";
import { StartPage, type StartReferences } from "./StartPage";
import { TaskFailureBanner } from "./TaskFailureBanner";
import { latestTaskRunId, resolveTaskFailure } from "./taskFailure";
import { TaskProgress } from "./TaskProgress";
import { SavedArtifactEditor } from "./SavedArtifactEditor";
import { extractArtifacts, preferredArtifact, artifactExtension, safeArtifactFilename } from "../../lib/artifacts";
import { ArtifactPanel } from "./ArtifactPanel";
import { Icon } from "../../components/Icon";
import { clampArtifactWidth, readUiPreferences, writeUiPreferences } from "../../lib/uiPreferences";
import { TaskStarter, StartOutcomeUnknown, type StartStage } from "../../lib/startTask";
import { useWorkbenchCapabilities } from "../../state/useWorkbenchCapabilities";
import { SessionProjectControl } from "./SessionProjectControl";
import { TaskContextBar } from "./TaskContextBar";
import { isTerminal } from "../../state/liveRuns";
import { filterSessions, mergeSessionPages, SESSION_PAGE_LIMIT } from "./sessionSearch";

const ContentPreview = lazy(() => import("../../content/ContentPreview").then(m => ({default:m.ContentPreview})));
const FileBrowser = lazy(() => import("../../content/FileBrowser").then(m => ({default:m.FileBrowser})));

export function ChatPage() {
  const { sessionId } = useParams<{ sessionId: string }>();
  const [search] = useSearchParams();
  // 新建任务的文件、重试进度与检查点归属当前项目；切换项目后使用独立生命周期。
  // 已有会话仍复用工作区，保留既有会话切换与成果编辑保护流程。
  return <ChatWorkspace key={sessionId ? "conversation" : `start:${search.get("project") ?? ""}`} />;
}

function ChatWorkspace() {
  const { t } = useTranslation();
  const api = useApi();
  const caps = useWorkbenchCapabilities();
  const location = useLocation();
  const currentLocationKey = useRef(location.key);
  const mounted = useRef(true);
  useLayoutEffect(() => { currentLocationKey.current = location.key; }, [location.key]);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const queryClient = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const { sessionId = null } = useParams<{ sessionId: string }>();
  const [search, setSearch] = useSearchParams();
  const agentFilter = search.get("agent") ?? "";
  const projectId = search.get("project") ?? "";
  const [materialSelection, setMaterialSelection] = useState<Record<string, string[]>>({});
  const project = useQuery({ queryKey: queryKeys.project(projectId), queryFn: () => api.workbench.getProject(projectId), enabled: caps.projects && !!projectId && !sessionId });
  const projectMaterials = useQuery({ queryKey: queryKeys.materials(projectId), queryFn: () => api.workbench.listMaterials(projectId, { limit: 200 }), enabled: caps.materials && !!projectId && !sessionId });
  const selectedMaterials = materialSelection[projectId] ?? (search.get("materials") ?? "").split(",").filter(Boolean);
  const missingMaterials = projectMaterials.data ? selectedMaterials.filter((id) => !projectMaterials.data.some((material) => material.id === id)) : [];
  const sessionStatus: SessionStatus = search.get("status") === "archived" ? "archived" : "active";
  const [sessionSearch, setSessionSearch] = useState("");
  const timeZone = useTimeZone();
  const dispatch = useLiveRunsDispatch();
  const [historyOpen, setHistoryOpen] = useState(false);
  const [previewFile,setPreviewFile] = useState<string | null>(null);
  const [filesOpen,setFilesOpen] = useState(false);
  const [artifactOpen, setArtifactOpen] = useState(false);
  const [narrow, setNarrow] = useState(() => window.matchMedia("(max-width: 1250px)").matches);
  useEffect(() => {
    const media = window.matchMedia("(max-width: 1250px)");
    const update = () => setNarrow(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  const [artifactDirty, setArtifactDirty] = useState(false);
  const [composerDirty, setComposerDirty] = useState(false);
  const artifactIntent = useRef(0);
  const [savedArtifactId, setSavedArtifactId] = useState<string | null>(null);
  const activeSessionRef = useRef(sessionId); activeSessionRef.current = sessionId;
  const [requestedResult, setRequestedResult] = useState<{messageId: string; version: number} | null>(null);
  const [retryDraft, setRetryDraft] = useState<{sessionId: string; id: string; text: string} | null>(null);
  const [tab, setTab] = useState<"conversation" | "artifact">("conversation");
  const [artifactWidth, setArtifactWidth] = useState(() => readUiPreferences().artifactWidth);
  const [homeDirty, setHomeDirty] = useState(false);
  const [startStage, setStartStage] = useState<StartStage | null>(null);
  const [startError, setStartError] = useState<string | null>(null);
  const [startOutcomeUnknown, setStartOutcomeUnknown] = useState(false);
  const taskStarter = useRef<TaskStarter | null>(null);
  const checkpointDraft = useRef<Draft | null>(null);
  const allowStartNavigation = useRef(false);
  const [pendingContext, setPendingContext] = useState<{ sessionId: string; attachment: Attachment } | null>(null);
  const [renaming, setRenaming] = useState<Session | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const searchInput = useRef<HTMLInputElement>(null);
  const resizeStart = useRef<{ x: number; width: number } | null>(null);

  const blocker = useBlocker(({ currentLocation, nextLocation }) => {
    const pathChanged = currentLocation.pathname !== nextLocation.pathname;
    const startScopeChanged = !sessionId && (new URLSearchParams(currentLocation.search).get("project") ?? "") !== (new URLSearchParams(nextLocation.search).get("project") ?? "");
    return !allowStartNavigation.current && ((artifactDirty && pathChanged) || (homeDirty && (pathChanged || startScopeChanged)) || (composerDirty && !nextLocation.pathname.startsWith("/chat")));
  });
  useEffect(() => {
    if (blocker.state === "blocked") {
      if (window.confirm(t(artifactDirty ? "work.leaveUnsaved" : "work.leaveDraft"))) { setArtifactDirty(false); blocker.proceed(); }
      else blocker.reset();
    }
  }, [blocker, t]);
  useEffect(() => {
    if (search.get("history") === "1") { setHistoryOpen(true); requestAnimationFrame(() => searchInput.current?.focus()); }
  }, [search]);
  useEffect(() => { setArtifactDirty(false); setPreviewFile(null); setFilesOpen(false); setTab("conversation"); setPendingContext(null); setRequestedResult(null); setRetryDraft(null); setArtifactOpen(false); setSavedArtifactId(null); artifactIntent.current += 1; allowStartNavigation.current = false; }, [sessionId]);
  const [messageMore, setMessageMore] = useState<Record<string, boolean>>({});
  const detailKey = queryKeys.session(sessionId ?? "");

  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list() });
  const sessions = useInfiniteQuery({
    queryKey: [...queryKeys.sessions(agentFilter || undefined), "list", sessionStatus],
    initialPageParam: 0,
    enabled: historyOpen,
    queryFn: ({ pageParam }) => api.sessions.list({ agent_id: agentFilter || undefined, status: sessionStatus, limit: SESSION_PAGE_LIMIT, offset: pageParam }),
    getNextPageParam: (lastPage, _pages, lastOffset) => lastPage.length === SESSION_PAGE_LIMIT ? lastOffset + lastPage.length : undefined,
  });
  // 当前会话独立查询，切换过滤条件或列表还未翻到它时也能恢复标题和在途运行。
  const selectedSession = useQuery({
    queryKey: detailKey,
    queryFn: () => api.sessions.get(sessionId ?? ""),
    enabled: sessionId !== null,
  });
  const recentSessions = useQuery({ queryKey: [...queryKeys.sessionsRoot, "recent"], queryFn: () => api.sessions.list({ status: "active", limit: 4 }), enabled: !sessionId });
  const allSessions = mergeSessionPages(sessions.data?.pages ?? []);
  const session = selectedSession.data ?? allSessions.find((item) => item.id === sessionId);
  useEffect(() => {
    if (session) writeUiPreferences({ lastSessionId: session.id, lastArtifactId: artifactOpen ? savedArtifactId : null });
    else if (ApiError.is(selectedSession.error, "not_found")) { writeUiPreferences({ lastSessionId: null, lastArtifactId: null }); void navigate("/chat", { replace: true }); }
  }, [session, savedArtifactId, artifactOpen, selectedSession.error, navigate]);
  const agentOfSession = agents.data?.find((agent) => agent.id === session?.agent_id);
  const agentName = useCallback((id: string) => agents.data?.find((agent) => agent.id === id)?.name ?? id, [agents.data]);
  const visibleSessions = filterSessions(allSessions, sessionSearch, agentName);

  // 每次增量读取有上限；达到上限时提供继续加载入口，避免静默丢掉长会话后半部分。
  const messages = useQuery<Message[]>({
    queryKey: queryKeys.messages(sessionId ?? ""),
    queryFn: async () => {
      const id = sessionId ?? "";
      const prev = queryClient.getQueryData<Message[]>(queryKeys.messages(id)) ?? [];
      const fresh = await api.sessions.listMessagesAfter(id, lastSeq(prev));
      setMessageMore((value) => ({ ...value, [id]: fresh.length >= MESSAGE_FETCH_CAP }));
      return mergeMessages(prev, fresh);
    },
    enabled: sessionId !== null,
    staleTime: 15_000,
  });

  const { runs: liveRuns, active } = useSessionLiveRuns(sessionId);
  const terminalSeen = useRef(new Set<string>());
  for (const run of liveRuns) if (isTerminal(run.status)) terminalSeen.current.add(run.runId);
  const activeInput = {
    live: active,
    liveRunIds: [...liveRuns.map((run) => run.runId), ...terminalSeen.current],
    sessionActiveRunId: session?.active_run_id,
  };
  const recoverId = fallbackRunId(activeInput);
  const recoveredRun = useQuery({
    queryKey: queryKeys.run(recoverId ?? ""),
    queryFn: () => api.runs.get(recoverId ?? ""),
    enabled: recoverId !== null,
    refetchInterval: (query) => (query.state.data && !isTerminal(query.state.data.status) ? 5_000 : false),
  });
  const activeRun = resolveActiveRun(activeInput, { data: recoveredRun.data, error: recoveredRun.error });
  const activeRunId = activeRun?.runId ?? null;

  useEffect(() => {
    if (!messages.data) return;
    for (const run of liveRuns) {
      if (!isTerminal(run.status)) continue;
      const persisted = messages.data.some((message) => message.run_id === run.runId && message.role === "assistant");
      if (persisted || (run.status === "cancelled" && !run.text)) dispatch({ type: "forget", runId: run.runId });
    }
  }, [messages.data, liveRuns, dispatch]);

  const send = useMutation({
    mutationFn: ({ id, text, attachments, materialIds }: { id: string; text: string; attachments: Attachment[]; materialIds?: string[] }) =>
      api.sessions.send(id, { text, attachments: attachments.length > 0 ? attachments : undefined, ...(materialIds?.length ? { material_ids: materialIds } : {}) }),
    onSuccess: (run, { id }) => {
      queryClient.setQueryData(queryKeys.run(run.id), run);
      queryClient.setQueryData<Session>(queryKeys.session(id), (value) => value ? { ...value, active_run_id: run.id } : value);
      void queryClient.invalidateQueries({ queryKey: queryKeys.messages(id) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionRoot });
    },
    // Composer 统一展示错误并保留草稿。id 必须随请求传递，导航后不污染新会话。
  });
  const cancel = useMutation({
    mutationFn: (runId: string) => api.runs.cancel(runId),
    onSuccess: (_, runId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.run(runId) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionRoot });
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const upload = useCallback((file: File) => api.sessions.upload(sessionId ?? "", file), [api, sessionId]);
  const chatUrl = (id: string | null, params = search) => {
    const next = new URLSearchParams(params); next.delete("artifact"); next.delete("materials");
    if (id) next.delete("project");
    return `/chat${id ? `/${encodeURIComponent(id)}` : ""}${next.size ? `?${next.toString()}` : ""}`;
  };

  const startTask = async (agentId: string, text: string, files: File[], references?: StartReferences) => {
    taskStarter.current ??= new TaskStarter(api.sessions, caps.materials ? api.workbench.uploadMaterial : undefined, caps.drafts ? async (created, content) => {
      const input = { kind: "composer" as const, agent_id: created.agent_id, session_id: created.id, project_id: created.project_id, ...content };
      checkpointDraft.current = checkpointDraft.current ? await api.workbench.updateDraft(checkpointDraft.current.id, { ...input, expected_version: checkpointDraft.current.version }) : await api.workbench.createDraft(input);
    } : undefined);
    const originLocationKey = location.key;
    setStartError(null);
    try {
      const result = await taskStarter.current.start({ agentId, text, files, projectId: caps.projects ? project.data?.id : undefined, materialIds: caps.materials ? [...new Set([...selectedMaterials, ...references?.material_ids ?? []])] : undefined, attachments: references?.attachments }, setStartStage);
      queryClient.setQueryData(queryKeys.session(result.session.id), { ...result.session, active_run_id: result.run && !isTerminal(result.run.status) ? result.run.id : undefined });
      if (result.run) queryClient.setQueryData(queryKeys.run(result.run.id), result.run);
      await queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
      void queryClient.invalidateQueries({ queryKey: queryKeys.messages(result.session.id) });
      const params = new URLSearchParams(search); params.delete("status"); params.delete("history");
      taskStarter.current = null;
      if (checkpointDraft.current) {
        const checkpoint = checkpointDraft.current;
        checkpointDraft.current = null;
        await api.workbench.deleteDraft(checkpoint.id, checkpoint.version).catch(() => toast.show(t("projects.sentDraftRetained"), "error"));
      }
      if (mounted.current && currentLocationKey.current === originLocationKey) {
        allowStartNavigation.current = true;
        setHomeDirty(false);
        await navigate(chatUrl(result.session.id, params));
      }
    } catch (error) {
      const message = error instanceof StartOutcomeUnknown ? t("phase2.startUnknown") : describeError(error, t);
      setStartOutcomeUnknown(error instanceof StartOutcomeUnknown);
      setStartError(message);
      throw new Error(message);
    } finally {
      setStartStage(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
    }
  };
  const deleteSession = useMutation({
    mutationFn: (id: string) => api.sessions.remove(id),
    onSuccess: (_, id) => {
      queryClient.removeQueries({ queryKey: queryKeys.messages(id) });
      queryClient.removeQueries({ queryKey: queryKeys.session(id) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionRoot });
      if (id === sessionId) void navigate(chatUrl(null));
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const renameSession = useMutation({
    mutationFn: ({ id, title }: { id: string; title: string }) => api.sessions.update(id, { title }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionRoot });
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const archiveSession = useMutation({
    mutationFn: ({ id, status }: { id: string; status: SessionStatus }) => api.sessions.update(id, { status }),
    onSuccess: (updated) => {
      queryClient.setQueryData(queryKeys.session(updated.id), updated);
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionsRoot });
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessionRoot });
      toast.show(t(updated.status === "archived" ? "chat.archivedSuccess" : "chat.restoredSuccess"), "ok");
    },
    onError: (err) => toast.show(describeError(err, t), "error"),
  });
  const actionBusy = renameSession.isPending || archiveSession.isPending || deleteSession.isPending;
  const archived = session?.status === "archived";
  const setListFilter = (key: string, value: string) => {
    const params = new URLSearchParams(search);
    if (value) params.set(key, value);
    else params.delete(key);
    setSearch(params);
  };

  const savedArtifacts = useQuery({ queryKey: [...queryKeys.artifacts, "session", sessionId], queryFn: () => api.workbench.listArtifacts({ session_id: sessionId!, limit: 200 }), enabled: caps.artifacts && !!sessionId });
  useEffect(() => {
    const id = search.get("artifact");
    if (caps.artifacts && id && sessionId) { setSavedArtifactId(id); setArtifactOpen(true); setTab("artifact"); }
  }, [caps.artifacts, search, sessionId]);
  const openPersistent = (id: string) => { setPreviewFile(null); setFilesOpen(false); artifactIntent.current += 1; setSavedArtifactId(id); setArtifactOpen(true); setTab("artifact"); const params = new URLSearchParams(search); params.set("artifact", id); setSearch(params, { replace: true }); };
  const createArtifact = useMutation({
    mutationFn: async ({ messageId, codeIndex }: { messageId: string; codeIndex?: number; intent: number }) => {
      const source = extractArtifacts(messages.data ?? []);
      const item = codeIndex === undefined ? preferredArtifact(source, messageId) : source.find((candidate) => candidate.messageId === messageId && candidate.kind === "code" && candidate.index === codeIndex);
      if (!item || !sessionId) throw new Error(t("versions.unavailable"));
      const fragment = codeIndex === undefined ? "document" : `code:${codeIndex}`;
      const records = await api.workbench.listArtifacts({ session_id: sessionId, limit: 200 });
      const existing = records.find((record) => record.source.message_id === messageId && record.source.fragment === fragment);
      if (existing) return existing;
      const title = item.title ?? (item.kind === "code" ? t("artifact.codeName", { language: item.language, index: item.index }) : t("artifact.untitledDocument"));
      return api.workbench.createArtifact(sessionId, { title, kind: item.kind, filename: safeArtifactFilename(`${title}.${artifactExtension(item)}`, `result.${artifactExtension(item)}`), media_type: item.kind === "document" ? "text/markdown" : "text/plain", source_message_id: messageId, fragment, content: item.content }, `source:${messageId}:${fragment}`);
    },
    onSuccess: (saved, input) => { void queryClient.invalidateQueries({ queryKey: queryKeys.artifacts }); if (mounted.current && input.intent === artifactIntent.current && saved.session_id === activeSessionRef.current) openPersistent(saved.id); },
    onError: (error) => toast.show(describeError(error, t), "error"),
  });
  const lastRunId = latestTaskRunId(sessionId, messages.data ?? [], liveRuns);
  const latestRun = useQuery({ queryKey: queryKeys.run(lastRunId ?? ""), queryFn: () => api.runs.get(lastRunId ?? ""), enabled: !!lastRunId && !activeRunId });
  const taskFailure = resolveTaskFailure(sessionId, lastRunId, liveRuns, latestRun.data, activeRunId);
  const selectSession = (id: string) => { setHistoryOpen(false); void navigate(chatUrl(id)); };
  const beginRename = (item: Session) => { setRenaming(item); setRenameValue(item.title ?? ""); };
  const showHistory = () => { setHistoryOpen(true); requestAnimationFrame(() => searchInput.current?.focus()); };
  const closeHistory = () => { setHistoryOpen(false); const params = new URLSearchParams(search); params.delete("history"); setSearch(params, { replace: true }); };

  return <div className={`workbench ${artifactOpen && sessionId ? "has-artifact" : ""} showing-${tab}`} style={{ "--artifact-width": `${artifactWidth}px` } as CSSProperties}>
    {historyOpen ? <button className="history-scrim" aria-label={t("common.close")} onClick={closeHistory} /> : null}
    <aside hidden={!historyOpen} className={`chat-side history-drawer ${historyOpen ? "open" : ""}`} inert={!historyOpen} aria-label={t("work.history")}>
      <div className="side-head"><h2>{t("work.history")}</h2><button type="button" className="btn ghost sm" onClick={closeHistory} aria-label={t("common.close")}><Icon name="close" /></button></div>
      <div className="history-filters">
        <input ref={searchInput} className="input" type="search" aria-label={t("chat.searchSessions")} placeholder={t("chat.searchSessions")} value={sessionSearch} onChange={(event) => setSessionSearch(event.target.value)} />
        <select className="select" aria-label={t("chat.agent")} value={agentFilter} onChange={(event) => setListFilter("agent", event.target.value)}>
          <option value="">{t("chat.allAgents")}</option>{agents.data?.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
        </select>
        <select className="select" aria-label={t("chat.sessionStatus")} value={sessionStatus} onChange={(event) => setListFilter("status", event.target.value === "archived" ? "archived" : "")}>
          <option value="active">{t("chat.activeSessions")}</option><option value="archived">{t("chat.archivedSessions")}</option>
        </select>
        {sessionSearch && sessions.hasNextPage ? <span className="muted">{t("chat.searchLoadedHint")}</span> : null}
      </div>
      <SessionList sessions={visibleSessions} loading={sessions.isPending} error={sessions.error} activeId={sessionId} onSelect={selectSession} onRename={beginRename} agentName={agentName} timeZone={timeZone} />
      {sessions.error ? <button type="button" className="btn sm" disabled={sessions.isFetching} onClick={() => void sessions.refetch()}>{t("chat.retryLoad")}</button> : null}
      {sessions.hasNextPage ? <button type="button" className="btn sm" disabled={sessions.isFetching} onClick={() => void sessions.fetchNextPage()}>{sessions.isFetchingNextPage ? t("common.loading") : t("chat.loadMoreSessions")}</button> : null}
    </aside>
    <section className="chat-main">
      {sessionId || projectId ? <div className="chat-head">
        <div className="workspace-title"><span className="eyebrow">{t("work.workspace")}</span><h1>{sessionId ? session?.title || t("chat.untitled") : t("work.startPage")}</h1></div>
        <button type="button" className="btn ghost sm" onClick={showHistory} title={t("work.searchShortcut")}><Icon name="clock" /><span className="optional-label">{t("work.history")}</span></button>
        {sessionId ? <>
          {caps.preview ? <button type="button" className="btn ghost sm" onClick={() => {setPreviewFile(null);setFilesOpen(true);setArtifactOpen(true);setTab("artifact");}}><Icon name="folder"/><span className="optional-label">{t("preview.files")}</span></button> : null}
          <button type="button" className={`btn sm ${artifactOpen ? "selected" : "ghost"}`} onClick={() => { setPreviewFile(null); setFilesOpen(false); setArtifactOpen(true); setTab("artifact"); }} aria-label={t("work.openArtifact")}><Icon name="panel" /><span className="optional-label">{t("work.artifact")}</span></button>
          <details className="task-menu"><summary className="btn ghost sm" aria-label={t("work.taskActions")}><Icon name="more" /></summary><div className="task-menu-items">
            <button type="button" className="btn ghost sm" disabled={!session || actionBusy} onClick={() => { if (session) beginRename(session); }}>{t("chat.rename")}</button>
            <button type="button" className="btn ghost sm" disabled={!session || actionBusy || activeRunId !== null || send.isPending} onClick={() => archiveSession.mutate({ id: sessionId, status: archived ? "active" : "archived" })}>{t(archived ? "chat.restore" : "chat.archive")}</button>
            <button type="button" className="btn ghost sm danger" disabled={!session || actionBusy || artifactDirty || activeRunId !== null || send.isPending} title={artifactDirty ? t("work.saveBeforeDelete") : undefined} onClick={() => { if (window.confirm(t("chat.confirmDelete"))) deleteSession.mutate(sessionId); }}>{t("common.delete")}</button>
          </div></details>
        </> : null}
      </div> : null}
      {!sessionId && projectId && !caps.loading && !caps.projects ? <div className="project-context" role="alert"><p>{t("projects.unavailable")}</p><Link to="/chat">{t("work.newTask")}</Link></div> : null}
      {!sessionId && projectId && caps.projects ? <div className="project-context">
        <ErrorBanner error={project.error ?? projectMaterials.error} />
        <strong>{t("projects.current")}: {project.data?.name ?? t("common.loading")}</strong> <Link to={`/projects/${projectId}`}>{t("projects.manage")}</Link>
        <p>{t("projects.materialSelectionHint")}</p>
        {project.data?.status === "archived" ? <p role="alert">{t("projects.archivedStart")}</p> : null}
        {missingMaterials.length ? <p role="alert">{t("projects.invalidSelection")}</p> : null}
        {missingMaterials.map((id, index) => <button key={id} className="btn sm" disabled={startStage !== null} onClick={() => setMaterialSelection((previous) => ({ ...previous, [projectId]: selectedMaterials.filter((selected) => selected !== id) }))}>{t("projects.removeUnavailable", { n: index + 1 })}</button>)}
        {projectMaterials.data?.map((material) => <label key={material.id}><input type="checkbox" checked={selectedMaterials.includes(material.id)} disabled={startStage !== null} onChange={(event) => setMaterialSelection((previous) => ({ ...previous, [projectId]: event.target.checked ? [...selectedMaterials, material.id] : selectedMaterials.filter((id) => id !== material.id) }))} />{material.name}</label>)}
      </div> : null}
      {sessionId ? <>
        <div className="workspace-tabs" role="tablist" aria-label={t("work.workspaceViews")} onKeyDown={(event) => {
          if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
          event.preventDefault();
          const next = event.key === "Home" ? "conversation" : event.key === "End" ? "artifact" : tab === "conversation" ? "artifact" : "conversation";
          setTab(next); if (next === "artifact") setArtifactOpen(true);
          document.getElementById(`${next}-tab`)?.focus();
        }}>
          <button role="tab" tabIndex={tab === "conversation" ? 0 : -1} aria-selected={tab === "conversation"} aria-controls="conversation-view" id="conversation-tab" onClick={() => setTab("conversation")}>{t("work.conversation")}</button>
          <button role="tab" tabIndex={tab === "artifact" ? 0 : -1} aria-selected={tab === "artifact"} aria-controls="artifact-view" id="artifact-tab" onClick={() => { setArtifactOpen(true); setTab("artifact"); }}>{t("work.artifact")}</button>
        </div>
        {savedArtifacts.data?.length ? <div className="saved-results-bar"><span>{t("versions.savedResults")}</span>{savedArtifacts.data.map((item) => <button key={item.id} className="btn ghost sm" onClick={() => openPersistent(item.id)}>{item.title}<span className="badge">v{item.version}</span></button>)}</div> : null}
        <div id="conversation-view" className="conversation-view">
          <ErrorBanner error={selectedSession.error ?? messages.error ?? recoveredRun.error} />
          {selectedSession.error || messages.error || recoveredRun.error ? <button type="button" className="btn sm" disabled={selectedSession.isFetching || messages.isFetching || recoveredRun.isFetching} onClick={() => { void selectedSession.refetch(); void messages.refetch(); if (recoverId) void recoveredRun.refetch(); }}>{t("chat.retryLoad")}</button> : null}
          <TaskProgress statusPending={!!lastRunId && latestRun.isPending && !activeRunId} statusFailed={!!latestRun.error} activeRunId={activeRunId} latestRunId={lastRunId} messages={messages.data ?? []} liveRuns={liveRuns} activeStatus={activeRun?.status} latestStatus={latestRun.data?.status} onRetry={() => {
            const previous = (messages.data ? [...messages.data].reverse() : []).find((message) => message.role === "user")?.parts.filter((part) => part.type === "text").map((part) => part.text ?? "").join("\n") ?? "";
            setRetryDraft({ sessionId, id: crypto.randomUUID(), text: t("work.retryPrompt", { goal: previous }) });
          }} />
          <MessageList onOpenFile={caps.preview ? attachment => {setPreviewFile(attachment.path);setFilesOpen(false);setArtifactOpen(true);setTab("artifact");} : undefined} key={sessionId} messages={messages.data ?? []} liveRuns={liveRuns} recovered={activeRun?.source === "session" ? { runId: activeRun.runId, status: activeRun.status } : null} loading={messages.isPending} timeZone={timeZone} persistResults={caps.artifacts} onOpenArtifact={(messageId, codeIndex) => { setPreviewFile(null);setFilesOpen(false);
            if (caps.artifacts) { if (!createArtifact.isPending) createArtifact.mutate({ messageId, codeIndex, intent: ++artifactIntent.current }); }
            else { setRequestedResult((current) => ({ messageId, version: (current?.version ?? 0) + 1 })); setArtifactOpen(true); setTab("artifact"); }
          }} />
          {messageMore[sessionId] ? <button type="button" className="btn sm" disabled={messages.isFetching} onClick={() => void messages.refetch()}>{messages.isFetching ? t("common.loading") : t("chat.loadMoreMessages")}</button> : null}
          {archived ? <div className="banner warn">{t("chat.archivedHint")}</div> : null}
        </div>
      </> : <StartPage enhancementEnabled={caps.enhancePrompt} projectMaterials={caps.materials && projectId && project.data?.status !== "archived" ? projectMaterials.data : undefined} projectMaterialIds={caps.materials && projectId ? selectedMaterials : undefined} onProjectMaterialIdsChange={(ids) => setMaterialSelection((previous) => ({ ...previous, [projectId]: ids }))} projectId={caps.projects ? project.data?.id : undefined} agents={projectId && caps.projects ? (agents.data ?? []).filter((agent) => agent.id === project.data?.agent_id) : agents.data ?? []} sessions={recentSessions.data ?? []} loading={recentSessions.isPending || agents.isPending} error={agents.error ?? recentSessions.error} busy={startStage !== null} startBlocked={startOutcomeUnknown || (!!projectId && (!caps.projects || project.isPending || projectMaterials.isPending || !!project.error || !!projectMaterials.error || project.data?.status === "archived" || !!missingMaterials.length))} startStage={startStage} startLocked={!!taskStarter.current?.createdSession} onDraftDirtyChange={setHomeDirty} timeZone={timeZone}
        onStart={startTask} onSelect={selectSession} onHistory={showHistory} />}
      {!sessionId && startError ? <div className="start-feedback" role="alert"><p>{startError}</p>{taskStarter.current?.createdSession ? <button className="btn" onClick={() => {
        const created = taskStarter.current?.createdSession;
        if (created) void navigate(chatUrl(created.id));
      }}>{t("phase2.openCreatedTask")}</button> : <button className="btn" onClick={showHistory}>{t("work.history")}</button>}{startOutcomeUnknown ? <button className="btn ghost" onClick={() => { if (window.confirm(t("phase2.confirmStartOver"))) { taskStarter.current = null; checkpointDraft.current = null; setStartOutcomeUnknown(false); setStartError(null); } }}>{t("phase2.startOver")}</button> : null}</div> : null}
      <div className="workspace-composer" hidden={!sessionId}>
        {taskFailure ? <TaskFailureBanner key={`${taskFailure.sessionId}:${taskFailure.runId}`} failure={taskFailure} timeZone={timeZone} /> : null}
        {session ? <SessionProjectControl session={session} running={!!activeRunId || send.isPending} /> : null}
        <Composer contextActions={agentOfSession ? <TaskContextBar part="context" key={sessionId} usage={active?.usage ?? latestRun.data?.usage ?? null} agent={agentOfSession} messages={messages.data ?? []} disabled={!!archived} onAddMaterial={(attachment) => setPendingContext({ sessionId: sessionId ?? "", attachment })} /> : null} modelAction={agentOfSession ? <TaskContextBar part="model" key={sessionId} usage={active?.usage ?? latestRun.data?.usage ?? null} agent={agentOfSession} messages={messages.data ?? []} disabled={!!archived} onAddMaterial={(attachment) => setPendingContext({ sessionId: sessionId ?? "", attachment })} /> : null} enhancementEnabled={caps.enhancePrompt} agentId={agentOfSession?.id} projectId={session?.project_id} onDraftDirtyChange={setComposerDirty} draftRequest={retryDraft?.sessionId === sessionId ? retryDraft : undefined} onDraftRequestConsumed={() => setRetryDraft(null)} draftKey={sessionId ?? ""} pendingAttachment={pendingContext?.sessionId === sessionId ? pendingContext.attachment : null} onAttachmentConsumed={() => setPendingContext(null)} disabled={!session || archived || actionBusy} busy={send.isPending} cancelBusy={cancel.isPending} activeRunId={activeRunId}
          onSend={(text, attachments, materialIds) => send.mutateAsync({ id: sessionId ?? "", text, attachments, materialIds })} onCancel={() => { if (activeRunId && !cancel.isPending) cancel.mutate(activeRunId); }} upload={upload} />
      </div>
    </section>
    {sessionId ? <aside id="artifact-view" className="artifact-container" hidden={!artifactOpen} aria-label={t("work.artifact")}>
      <div className="panel-resize" role="separator" aria-label={t("work.resizeArtifact")} aria-orientation="vertical" aria-valuenow={artifactWidth} aria-valuemin={420} aria-valuemax={640} tabIndex={0}
        onPointerDown={(event) => { resizeStart.current = { x: event.clientX, width: artifactWidth }; event.currentTarget.setPointerCapture(event.pointerId); }}
        onPointerMove={(event) => { if (resizeStart.current && event.currentTarget.hasPointerCapture(event.pointerId)) setArtifactWidth(clampArtifactWidth(resizeStart.current.width + resizeStart.current.x - event.clientX)); }}
        onPointerUp={(event) => { resizeStart.current = null; event.currentTarget.releasePointerCapture(event.pointerId); writeUiPreferences({ artifactWidth }); }}
        onPointerCancel={() => { resizeStart.current = null; }}
        onKeyDown={(event) => { const next = event.key === "ArrowLeft" ? artifactWidth + 20 : event.key === "ArrowRight" ? artifactWidth - 20 : event.key === "Home" ? 420 : event.key === "End" ? 640 : null; if (next !== null) { event.preventDefault(); const width = clampArtifactWidth(next); setArtifactWidth(width); writeUiPreferences({ artifactWidth: width }); } }} />
      <div className="artifact-editor-preserved" hidden={previewFile !== null || filesOpen}>{caps.artifacts && savedArtifactId ? <SavedArtifactEditor artifactId={savedArtifactId} sessionId={sessionId} visible={artifactOpen && !previewFile && !filesOpen && (!narrow || tab === "artifact")} onClose={() => { artifactIntent.current += 1; setArtifactOpen(false); setTab("conversation"); const params = new URLSearchParams(search); params.delete("artifact"); setSearch(params, { replace: true }); }} onDirtyChange={setArtifactDirty} /> : caps.artifacts ? <section className="artifact-panel"><header className="artifact-head"><h2>{t("versions.savedResults")}</h2><button className="btn ghost" onClick={() => { setArtifactOpen(false); setTab("conversation"); }}>{t("common.close")}</button></header><p className="artifact-notice">{t("versions.chooseHint")}</p>{savedArtifacts.data?.map((item) => <button className="btn ghost" key={item.id} onClick={() => openPersistent(item.id)}>{item.title}</button>)}</section> : <ArtifactPanel requestedMessageId={requestedResult?.messageId} requestedVersion={requestedResult?.version} visible={artifactOpen && !previewFile && !filesOpen && (!narrow || tab === "artifact")} key={sessionId} sessionId={sessionId} messages={messages.data ?? []} onDirtyChange={setArtifactDirty} onClose={() => { artifactIntent.current += 1; setArtifactOpen(false); setTab("conversation"); }} onUseAsContext={(attachment) => { setPendingContext({ sessionId, attachment }); setTab("conversation"); }} />}</div>
      {caps.preview && artifactOpen && (!narrow || tab === "artifact") && agentOfSession ? <Suspense fallback={<p className="muted">{t("common.loading")}</p>}>{previewFile ? <ContentPreview agentId={agentOfSession.id} path={previewFile} layoutAvailable={caps.previewLayout} onClose={() => {setPreviewFile(null);setArtifactOpen(false);setTab("conversation");}}/> : filesOpen ? <FileBrowser key={agentOfSession.id} agentId={agentOfSession.id} layoutAvailable={caps.previewLayout} onClose={() => {setFilesOpen(false);setArtifactOpen(false);setTab("conversation");}}/> : null}</Suspense> : null}
    </aside> : null}
    {renaming ? <Dialog open title={t("chat.rename")} onClose={() => setRenaming(null)} dismissible={!renameSession.isPending} footer={<><button className="btn" disabled={renameSession.isPending} onClick={() => setRenaming(null)}>{t("common.cancel")}</button><button className="btn primary" disabled={!renameValue.trim() || renameSession.isPending} onClick={() => renameSession.mutate({ id: renaming.id, title: renameValue.trim() }, { onSuccess: () => setRenaming(null) })}>{t("common.save")}</button></>}>
      <label htmlFor="task-title">{t("work.taskName")}</label><input autoFocus id="task-title" className="input" value={renameValue} onChange={(event) => setRenameValue(event.target.value)} />
    </Dialog> : null}
  </div>;
}
