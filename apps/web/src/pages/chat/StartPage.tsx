// 开始页仅收集目标和本地资料；创建、上传、发送及失败恢复由宿主统一编排。
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/Icon";
import { ErrorBanner } from "../../components/ui";
import type { Agent, Attachment, Session } from "../../api/types";
import { SessionList } from "./SessionList";
import { TaskInput } from "./TaskInput";
import { TaskContextBar } from "./TaskContextBar";
import { usePersistentDraft } from "../../state/usePersistentDraft";
import type { DraftSaveToken } from "../../state/draftController";
import { DraftSaveStatus } from "../../components/DraftSaveStatus";
import { ApiError } from "../../api/client";
import "../../components/compact-workbench.css";

/** 开始页恢复的已上传引用与其已确认草稿版本，不包含本地文件二进制。 */
export interface StartReferences { attachments: Attachment[]; material_ids: string[]; draft?: DraftSaveToken }

export function StartPage({ enhancementEnabled = false, agents, sessions, loading, error, busy, timeZone, onStart, onSelect, onHistory, startStage = null, startError, startLocked = false, startBlocked = false, onDraftDirtyChange, projectId, projectMaterialIds, onProjectMaterialIdsChange }: {
  enhancementEnabled?: boolean;
  agents: Agent[]; sessions: Session[]; loading: boolean; error: unknown; busy: boolean;
  timeZone: string | undefined;
  onStart: (agentId: string, text: string, files: File[], references?: StartReferences) => Promise<unknown>;
  onSelect: (id: string) => void;
  onHistory: () => void;
  startStage?: "creating" | "uploading" | "sending" | null;
  startError?: unknown;
  startLocked?: boolean;
  startBlocked?: boolean;
  onDraftDirtyChange?: (dirty: boolean) => void;
  projectId?: string;
  projectMaterialIds?: string[];
  onProjectMaterialIdsChange?: (ids: string[]) => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [references, setReferences] = useState<StartReferences>({ attachments: [], material_ids: [] });
  const [chosenAgent, setChosenAgent] = useState("");
  const [inProgress, setInProgress] = useState(false);
  const [submissionError, setSubmissionError] = useState<unknown>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const submitting = useRef(false);
  const dirtyCallback = useRef(onDraftDirtyChange);
  dirtyCallback.current = onDraftDirtyChange;
  const inFlight = busy || inProgress;
  const materialIds = projectMaterialIds ?? references.material_ids;
  const hasContent = Boolean(text.trim() || files.length || references.attachments.length || materialIds.length);
  const available = agents.filter((agent) => agent.enabled);
  const agentId = startLocked && chosenAgent ? chosenAgent : available.some((agent) => agent.id === chosenAgent) ? chosenAgent : available[0]?.id ?? "";
  const persistent = usePersistentDraft(agentId ? { kind: "composer", agent_id: agentId, project_id: projectId } : null,
    { text, attachments: references.attachments, material_ids: materialIds },
    (restored) => {
      setText(restored.text);
      const ids = restored.material_ids ?? [];
      setReferences({ attachments: restored.attachments ?? [], material_ids: ids });
      onProjectMaterialIdsChange?.([...ids]);
    });
  const unsaved = persistent.anyDirty || files.length > 0;
  useEffect(() => { dirtyCallback.current?.(unsaved); }, [unsaved]);
  useEffect(() => () => { dirtyCallback.current?.(false); }, []);
  useEffect(() => {
    if (!unsaved) return;
    const beforeUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [unsaved]);

  const submit = async (empty = false) => {
    if (!agentId || inFlight || startBlocked || submitting.current || (!hasContent && !empty) || persistent.conflict || !persistent.hydrated) return;
    submitting.current = true;
    setChosenAgent(agentId);
    setInProgress(true);
    setSubmissionError(null);
    let draftToken = persistent.capture();
    try {
      try { draftToken = await persistent.saveNow(); } catch (error) { if (ApiError.is(error, "conflict")) return; }
      if (references.attachments.length || materialIds.length || draftToken.draftId) await onStart(agentId, text.trim(), [...files], { attachments: references.attachments, material_ids: [...materialIds], draft: draftToken });
      else await onStart(agentId, text.trim(), [...files]);
      setText("");
      setFiles([]);
      setReferences({ attachments: [], material_ids: [] });
      void persistent.clearAfterSend(draftToken).catch(() => undefined);
    } catch (err) {
      // 任一阶段失败都保留正文和 File 对象，重试仍由宿主决定是否复用已建会话。
      setSubmissionError(err);
    } finally {
      submitting.current = false;
      setInProgress(false);
    }
  };
  return <div className="start-page">
    <div className="welcome">
      <div className="compact-welcome-heading"><img className="welcome-gosli" src={`${import.meta.env.BASE_URL}brand/gosli.png`} alt="" /><div><h1>{t("work.welcome")}</h1><p>{t("work.welcomeHint")}</p></div></div>
      <div className="quick-starts">
        {(["research", "write", "code"] as const).map((kind) => <button type="button" key={kind} disabled={inFlight} onClick={() => { setText(t(`work.prompt_${kind}`)); input.current?.focus(); }}>
          <Icon name={kind === "research" ? "folder" : kind === "write" ? "edit" : "code"} /><span>{t(`work.entry_${kind}`)}</span>
        </button>)}
      </div>
      <TaskInput
        className="start-composer"
        inputKey={`start:${agentId}:${projectId ?? "personal"}`}
        enhanceAgentId={enhancementEnabled ? agentId : undefined}
        enhanceDisabled={inFlight || !!persistent.conflict || !persistent.hydrated}
        textareaRef={input}
        value={text}
        onChange={setText}
        label={t("work.taskGoal")}
        placeholder={t("work.startPlaceholder")}
        disabled={inFlight}
        filesDisabled={inFlight}
        removeDisabled={inFlight}
        submitDisabled={!agentId || inFlight || startBlocked || !hasContent || !!persistent.conflict || !persistent.hydrated}
        submitLabel={t(inFlight ? "phase2.starting" : "phase2.startTask")}
        materials={[...files.map((file, index) => ({ id: String(index), name: file.name })), ...references.attachments.map((attachment) => ({ id: `uploaded:${attachment.path}`, name: attachment.name })), ...(projectMaterialIds === undefined ? references.material_ids.map((id) => ({ id: `material:${id}`, name: t("p3draft.materialReference", { id }) })) : [])]}
        onFiles={(added) => { if (!inFlight) setFiles((current) => [...current, ...added.filter((file) => !current.includes(file))]); }}
        onRemoveMaterial={(id) => { if (!inFlight) { setFiles((current) => current.filter((_, index) => String(index) !== id)); setReferences((current) => ({ attachments: current.attachments.filter((item) => `uploaded:${item.path}` !== id), material_ids: current.material_ids.filter((item) => `material:${item}` !== id) })); } }}
        onSubmit={() => void submit()}
        actions={<>
          <label className="sr-only" htmlFor="start-agent">{t("chat.agent")}</label>
          <select id="start-agent" title={available.find((agent) => agent.id === agentId)?.name ?? ""} value={agentId} onChange={(event) => {
            const next = event.target.value;
            if (!next || next === agentId || inFlight || startLocked) return;
            void (async () => {
              try { await persistent.saveNow(); } catch { return; }
              setText("");
              setFiles([]);
              setReferences({ attachments: [], material_ids: [] });
              onProjectMaterialIdsChange?.([]);
              setChosenAgent(next);
            })();
          }} disabled={!available.length || inFlight || startLocked}>
            {startLocked && agentId && !available.some((agent) => agent.id === agentId) ? <option value={agentId}>{agents.find((agent) => agent.id === agentId)?.name ?? agentId}</option> : null}
            {!available.length ? <option value="">{t("work.chooseAssistant")}</option> : available.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
          </select>
        </>}
        footer={<DraftSaveStatus draft={persistent} localFiles={files.length > 0} />}
        contextActions={available.find((agent) => agent.id === agentId) ? <TaskContextBar key={agentId} agent={available.find((agent) => agent.id === agentId)!} messages={[]} disabled={inFlight} onAddMaterial={() => undefined} /> : null}
        secondaryAction={!hasContent && !startLocked ? <button type="button" className="btn ghost sm" disabled={!agentId || inFlight || startBlocked || !persistent.hydrated} onClick={() => void submit(true)}>{t("phase2.emptyTask")}</button> : null}
        status={inFlight ? t(startStage ? `phase2.stage_${startStage}` : "phase2.starting") : startLocked ? t("phase2.retrySameTask") : t("compose.keyboardHint")}
      />
      <ErrorBanner error={startError ?? submissionError} />
      {!loading && !available.length ? <div className="setup-note">{t("work.setupHint")} <Link to="/providers">{t("nav.providers")}</Link><span> · </span><Link to="/agents">{t("work.configureAssistant")}</Link></div> : null}
      <ErrorBanner error={error} />
    </div>
    <section className="recent-tasks">
      <div className="section-heading"><h2><Icon name="clock" />{t("work.continueTasks")}</h2><button type="button" className="btn ghost sm" disabled={inFlight} onClick={onHistory}>{t("work.allTasks")}<Icon name="arrow" size={14} /></button></div>
      {sessions.length || loading ? <SessionList sessions={sessions.slice(0, 4)} loading={loading} error={null} activeId={null} onSelect={(id) => { if (!inFlight) onSelect(id); }} agentName={(id) => agents.find((agent) => agent.id === id)?.name ?? id} timeZone={timeZone} /> : <div className="recent-empty"><Icon name="chat" size={24} /><span>{t("work.noRecent")}</span></div>}
    </section>
    <div className="start-footer"><span>{t("work.shortcutHint")}</span></div>
  </div>;
}
