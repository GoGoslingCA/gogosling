// 已建立成果的编辑区：不可变版本与可恢复草稿分别保存，不覆盖工作区原文件。
import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../../state/ConnectionContext";
import { queryKeys } from "../../state/queryKeys";
import { usePersistentDraft } from "../../state/usePersistentDraft";
import { useWorkbenchCapabilities } from "../../state/useWorkbenchCapabilities";
import { DraftSaveStatus } from "../../components/DraftSaveStatus";
import { ErrorBanner, Spinner } from "../../components/ui";
import { useToast } from "../../components/Toast";
import { ApiError } from "../../api/client";
import type { Artifact } from "../../api/types";
import { diffArtifactLines, highlightArtifactCode, safeArtifactFilename } from "../../lib/artifacts";
import { describeError } from "../../lib/errorMessage";
import { Markdown } from "./Markdown";
import { FileChangePanel } from "./FileChangePanel";
import { Icon } from "../../components/Icon";
import "./artifact.css";

export function SavedArtifactEditor({ artifactId, sessionId, visible, onClose, onDirtyChange }: { artifactId: string; sessionId: string; visible: boolean; onClose: () => void; onDirtyChange: (dirty: boolean) => void }) {
  const { t } = useTranslation(); const api = useApi();
  const artifact = useQuery({ queryKey: queryKeys.artifact(artifactId), queryFn: () => api.workbench.getArtifact(artifactId) });
  const opening = useRef<{ artifactId: string; revisionId?: string }>({ artifactId });
  if (opening.current.artifactId !== artifactId) opening.current = { artifactId };
  if (!opening.current.revisionId && artifact.data?.latest_revision_id) opening.current.revisionId = artifact.data.latest_revision_id;
  const initialRevision = opening.current.revisionId;
  const initial = useQuery({ queryKey: [...queryKeys.revisions(artifactId), initialRevision, "content"], queryFn: () => api.workbench.readRevision(artifactId, initialRevision!), enabled: !!initialRevision && artifact.data?.session_id === sessionId });
  if (artifact.isPending || initial.isPending && !artifact.error && artifact.data?.session_id === sessionId) return <section className="artifact-panel"><div className="artifact-head"><Spinner /><button className="btn ghost" onClick={onClose}>{t("common.close")}</button></div></section>;
  if (artifact.error || initial.error || !artifact.data || artifact.data.session_id !== sessionId || initial.data === undefined) return <section className="artifact-panel"><div className="artifact-head"><h2>{t("artifact.title")}</h2><button className="btn ghost" onClick={onClose}>{t("common.close")}</button></div><ErrorBanner error={artifact.error ?? initial.error} /><p className="artifact-notice">{t("versions.unavailable")}</p><button className="btn" onClick={() => { void artifact.refetch(); void initial.refetch(); }}>{t("chat.retryLoad")}</button></section>;
  return <SavedArtifactBody key={artifactId} artifact={artifact.data} initialRevision={initialRevision!} initialContent={initial.data} visible={visible} onClose={onClose} onDirtyChange={onDirtyChange} />;
}

function SavedArtifactBody({ artifact, initialRevision, initialContent, visible, onClose, onDirtyChange }: { artifact: Artifact; initialRevision: string; initialContent: string; visible: boolean; onClose: () => void; onDirtyChange: (dirty: boolean) => void }) {
  const { t } = useTranslation(); const api = useApi(); const client = useQueryClient(); const toast = useToast();
  const caps = useWorkbenchCapabilities();
  const [text, setText] = useState(initialContent); const [filename, setFilename] = useState(artifact.filename);
  const [baseRevision, setBaseRevision] = useState(initialRevision);
  const [baseFilename, setBaseFilename] = useState(artifact.filename);
  const [mode, setMode] = useState<"edit" | "preview" | "diff" | "files">("edit");
  const [historyId, setHistoryId] = useState(""); const [conflict, setConflict] = useState<Artifact | null>(null);
  const [adopting, setAdopting] = useState(false); const adoptLock = useRef(false);
  const [copyBusy, setCopyBusy] = useState(false); const saving = useRef(false);
  const persistent = usePersistentDraft({ kind: "artifact", agent_id: artifact.agent_id, session_id: artifact.session_id, artifact_id: artifact.id, project_id: artifact.project_id, base_revision_id: baseRevision }, { text, filename }, (restored) => { setText(restored.text); if (restored.filename) setFilename(restored.filename); });
  useEffect(() => { if (persistent.baseRevisionId) setBaseRevision(persistent.baseRevisionId); }, [persistent.baseRevisionId]);
  const revisions = useQuery({ queryKey: queryKeys.revisions(artifact.id), queryFn: () => api.workbench.listRevisions(artifact.id, { limit: 200 }) });
  const base = useQuery({ queryKey: [...queryKeys.revisions(artifact.id), baseRevision, "content"], queryFn: () => api.workbench.readRevision(artifact.id, baseRevision), initialData: baseRevision === initialRevision ? initialContent : undefined });
  const history = useQuery({ queryKey: [...queryKeys.revisions(artifact.id), historyId, "content"], queryFn: () => api.workbench.readRevision(artifact.id, historyId), enabled: !!historyId });
  const selectedRevision = revisions.data?.find((revision) => revision.id === baseRevision);
  useEffect(() => { if (selectedRevision) setBaseFilename(selectedRevision.filename); }, [selectedRevision]);
  const modified = text !== base.data || filename !== baseFilename;
  const save = useMutation({
    mutationFn: async () => {
      const snapshot = { content: text, filename: safeArtifactFilename(filename, artifact.filename), expected_latest_revision_id: baseRevision, media_type: artifact.media_type };
      const releaseDraft = persistent.retainOperation();
      try {
        const draftToken = await persistent.saveNow();
        const revision = await api.workbench.createRevision(artifact.id, snapshot);
        return { revision, snapshot, draftToken, releaseDraft };
      } catch (error) { releaseDraft(); throw error; }
    },
    onSuccess: async ({ revision, snapshot, draftToken, releaseDraft }) => {
      try {
        client.setQueryData([...queryKeys.revisions(artifact.id), revision.id, "content"], snapshot.content);
        setBaseRevision(revision.id); setBaseFilename(snapshot.filename);
        await persistent.clearAfterSend(draftToken).catch(() => undefined);
        persistent.setBaseRevision(revision.id);
        void client.invalidateQueries({ queryKey: queryKeys.artifacts });
        toast.show(t("versions.saved", { n: revision.number }), "ok");
      } finally { releaseDraft(); }
    },
    onError: async (error) => {
      if (ApiError.is(error, "conflict")) {
        try { setConflict(await api.workbench.getArtifact(artifact.id)); } catch { /* 原错误留在面板，不改变当前草稿。 */ }
      } else toast.show(describeError(error, t), "error");
    },
    onSettled: () => { saving.current = false; },
  });
  const saveCurrent = () => { if (historyId || mode === "files" || saving.current || !modified || !persistent.hydrated || persistent.conflict || conflict || !base.data && base.data !== "") return; saving.current = true; save.mutate(); };
  useEffect(() => {
    const dirty = persistent.dirty || save.isPending || copyBusy;
    onDirtyChange(dirty);
    const prevent = (event: BeforeUnloadEvent) => { if (dirty) { event.preventDefault(); event.returnValue = ""; } };
    window.addEventListener("beforeunload", prevent);
    return () => { window.removeEventListener("beforeunload", prevent); onDirtyChange(false); };
  }, [persistent.dirty, save.isPending, copyBusy, onDirtyChange]);
  useEffect(() => {
    if (!visible) return;
    const handle = (event: KeyboardEvent) => { if ((event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey && !event.isComposing && event.key.toLowerCase() === "s" && !event.defaultPrevented && !document.querySelector("dialog[open]")) { event.preventDefault(); saveCurrent(); } };
    window.addEventListener("keydown", handle); return () => window.removeEventListener("keydown", handle);
  });
  const adoptLatest = async (useRemote: boolean) => {
    if (!conflict?.latest_revision_id || adoptLock.current) return;
    adoptLock.current = true; setAdopting(true);
    try {
      const content = await api.workbench.readRevision(artifact.id, conflict.latest_revision_id);
      client.setQueryData([...queryKeys.revisions(artifact.id), conflict.latest_revision_id, "content"], content);
      setBaseRevision(conflict.latest_revision_id); setBaseFilename(conflict.filename); persistent.setBaseRevision(conflict.latest_revision_id);
      if (useRemote) { setText(content); setFilename(conflict.filename); }
      setConflict(null); save.reset();
    } catch (error) { toast.show(describeError(error, t), "error"); } finally { adoptLock.current = false; setAdopting(false); }
  };
  const exportText = () => {
    const blob = new Blob([historyId && history.data !== undefined ? history.data : text], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob); const link = document.createElement("a"); link.href = url; link.download = safeArtifactFilename(historyId ? revisions.data?.find((revision) => revision.id === historyId)?.filename ?? filename : filename, artifact.filename); document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const saveCopy = async () => {
    if (copyBusy) return; setCopyBusy(true);
    try { const attachment = await api.sessions.upload(artifact.session_id, new File([text], safeArtifactFilename(filename, artifact.filename), { type: artifact.media_type })); toast.show(t("versions.copySaved", { path: attachment.path }), "ok"); }
    catch (error) { toast.show(describeError(error, t), "error"); } finally { setCopyBusy(false); }
  };
  const diff = useMemo(() => mode === "diff" ? diffArtifactLines(base.data ?? "", text) : [], [mode, base.data, text]);
  const tokens = useMemo(() => mode === "preview" && artifact.kind === "code" ? highlightArtifactCode(text, filename.split(".").at(-1) ?? "") : [], [mode, text, filename, artifact.kind]);
  return <section className="artifact-panel" aria-label={t("versions.editor")}>
    <header className="artifact-head"><div className="artifact-heading"><h2>{artifact.title}</h2><span className="artifact-source">{t("versions.source", { n: artifact.source.message_seq })}</span></div><button className="btn ghost sm" onClick={onClose} aria-label={t("artifact.close")}>✕</button></header>
    <div className="artifact-file-controls" hidden={mode === "files" && !historyId}><div className="artifact-file-row"><label className="artifact-label" htmlFor="version-filename">{t("versions.filename")}</label><input id="version-filename" className="input" disabled={adopting} value={filename} onChange={(event) => setFilename(event.target.value)} /></div><DraftSaveStatus kind="artifact" draft={persistent} /><p className="artifact-notice">{t(modified ? "versions.draftVsVersion" : "versions.currentVersion", { n: selectedRevision?.number ?? artifact.version })}</p></div>
    <div className="artifact-view-controls" role="group" aria-label={t("artifact.viewMode")}>{(["edit", "preview", "diff"] as const).map((value) => <button key={value} className={`artifact-view-button ${mode === value ? "active" : ""}`} aria-pressed={mode === value} onClick={() => { setMode(value); setHistoryId(""); }}>{t(`artifact.${value}`)}</button>)}{caps.changes ? <button type="button" className={`artifact-view-button ${mode === "files" && !historyId ? "active" : ""}`} aria-pressed={mode === "files" && !historyId} disabled={!!historyId} title={historyId ? t("changes.historyReadOnly") : undefined} onClick={() => setMode("files")}><Icon name="folder" size={14} />{t("changes.tab")}</button> : null}<label className="sr-only" htmlFor="version-history">{t("versions.history")}</label><select id="version-history" className="select version-history" value={historyId} onChange={(event) => setHistoryId(event.target.value)}><option value="">{t("versions.history")}</option>{revisions.data?.map((revision) => <option key={revision.id} value={revision.id}>{t("projects.version", { n: revision.number })}</option>)}</select></div>
    <ErrorBanner error={save.error ?? base.error ?? revisions.error} />
    {conflict ? <div className="banner warn"><p>{t("versions.conflict")}</p><button className="btn sm" disabled={adopting} onClick={() => void adoptLatest(true)}>{t("versions.loadLatest")}</button><button className="btn sm" disabled={adopting} onClick={() => void adoptLatest(false)}>{t("versions.keepMine")}</button></div> : null}
    <div className="artifact-content">
      {historyId ? <div className="artifact-document"><ErrorBanner error={history.error} />{history.isPending ? <Spinner /> : <><p className="artifact-notice">{t("versions.historyPreview")}</p><Markdown text={history.data ?? ""} /><button className="btn" disabled={history.data === undefined || save.isPending} onClick={() => { if (window.confirm(t("versions.confirmRestore"))) { setText(history.data!); setHistoryId(""); setMode("edit"); } }}>{t("versions.restoreAsNew")}</button></>}</div> : mode === "edit" ? <textarea className={`artifact-editor ${artifact.kind === "code" ? "code" : ""}`} disabled={!persistent.hydrated || adopting} value={text} onChange={(event) => setText(event.target.value)} aria-label={t("artifact.editor")} /> : mode === "preview" ? artifact.kind === "code" ? <pre className="artifact-code"><code>{tokens.map((token, index) => <span key={index} className={`artifact-token-${token.kind}`}>{token.text}</span>)}</code></pre> : <div className="artifact-document"><Markdown text={text} /></div> : mode === "diff" ? <div className="artifact-diff">{diff.map((line, index) => <div key={index} className={`artifact-diff-line ${line.kind}`}><span>{line.oldLine}</span><span>{line.newLine}</span><span>{line.kind === "added" ? "+" : line.kind === "removed" ? "−" : " "}</span><code>{line.text || " "}</code></div>)}</div> : null}
      {caps.changes ? <div className="file-change-host" hidden={mode !== "files" || !!historyId}><FileChangePanel artifactId={artifact.id} sessionId={artifact.session_id} revisionId={baseRevision} revisionNumber={selectedRevision?.number} modified={modified || !!persistent.conflict || !!conflict || !persistent.hydrated || base.data === undefined} visible={visible && mode === "files" && !historyId} onEdit={() => { setMode("edit"); setHistoryId(""); }} /></div> : null}
    </div>
    <footer className="artifact-footer" hidden={mode === "files" && !historyId}><details className="compact-explainer"><summary>{t("changes.versionDetails")}</summary><p className="artifact-notice">{t("versions.boundary")}</p></details><div className="artifact-actions"><button className="btn sm" onClick={exportText}>{t("artifact.export")}</button><button className="btn sm" disabled={!!historyId || mode === "files" || adopting || copyBusy || !persistent.hydrated} onClick={() => void saveCopy()}>{t("artifact.saveCopy")}</button><span className="spacer" /><button className="btn primary" disabled={!!historyId || mode === "files" || save.isPending || !modified || !persistent.hydrated || !!persistent.conflict || !!conflict || base.data === undefined} onClick={saveCurrent}>{t(save.isPending ? "versions.saving" : "versions.save")}</button></div></footer>
  </section>;
}
