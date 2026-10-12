// 成果面板只编辑消息副本。保存通过既有附件 API 另存文件，不覆盖工作区源文件。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Attachment, Message } from "../../api/types";
import { useApi } from "../../state/ConnectionContext";
import { useToast } from "../../components/Toast";
import { describeError } from "../../lib/errorMessage";
import { artifactExtension, artifactLanguageLabel, diffArtifactLines, extractArtifacts, highlightArtifactCode, preferredArtifact, safeArtifactFilename, type Artifact } from "../../lib/artifacts";
import { Markdown } from "./Markdown";
import "./artifact.css";

/** 宿主在切换会话时使用 sessionId 作为 key，并在路由离开前检查 onDirtyChange。 */
export interface ArtifactPanelProps {
  sessionId: string;
  messages: readonly Message[];
  /** 隐藏面板并保留挂载；真正离开任务时由宿主拦截未保存内容。 */
  onClose: () => void;
  onUseAsContext?: (attachment: Attachment) => void;
  onDirtyChange?: (dirty: boolean) => void;
  /** 面板隐藏时保留草稿，但不接管保存快捷键。 */
  visible?: boolean;
  /** 从指定回复打开成果；优先选完整文档，其次选择首个代码片段。 */
  requestedMessageId?: string;
  /** 重复点击同一回复时递增，使显式打开请求与普通消息刷新可区分。 */
  requestedVersion?: number;
}

interface Draft {
  source: Artifact;
  content: string;
  filename: string;
  initialFilename: string;
  savedContent?: string;
  savedFilename?: string;
  attachment?: Attachment;
}

interface SaveInput {
  sessionId: string;
  artifact: Artifact;
  content: string;
  filename: string;
}

/** 已持久化助手正文的检查、编辑、差异和副本保存界面。 */
export function ArtifactPanel({ sessionId, messages, onClose, onUseAsContext, onDirtyChange, visible = true, requestedMessageId, requestedVersion = 0 }: ArtifactPanelProps) {
  const { t } = useTranslation();
  const api = useApi();
  const toast = useToast();
  const artifacts = useMemo(() => extractArtifacts(messages), [messages]);
  const [selection, setSelection] = useState<string | null>(null);
  const appliedRequest = useRef<{ messageId: string; version: number } | null>(null);
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [mode, setMode] = useState<"edit" | "preview" | "diff">("edit");
  // 已打开的版本以快照为准，消息刷新或后续回复都不能替换正在检查的原稿。
  const available = useMemo(() => {
    const byId = new Map(artifacts.map((artifact) => [artifact.id, artifact]));
    for (const edited of Object.values(drafts)) byId.set(edited.source.id, edited.source);
    return [...byId.values()].sort((a, b) => a.seq - b.seq || a.index - b.index);
  }, [artifacts, drafts]);
  const artifactName = useCallback((artifact: Artifact) => artifact.kind === "document"
    ? artifact.title ?? t("artifact.untitledDocument")
    : t("artifact.codeName", { language: artifactLanguageLabel(artifact.language) || t("artifact.plainText"), index: artifact.index }), [t]);
  const defaultFilename = useCallback((artifact: Artifact) => {
    const name = artifactName(artifact).replace(/[\\/]/g, "-").slice(0, 80);
    return safeArtifactFilename(`${name}-${t("artifact.copyFileSuffix")}-${artifact.seq}.${artifactExtension(artifact)}`, `${t("artifact.filenameBase")}-${artifact.seq}.${artifactExtension(artifact)}`);
  }, [t, artifactName]);
  const newDraft = useCallback((artifact: Artifact): Draft => {
    const name = defaultFilename(artifact);
    return { source: artifact, content: artifact.content, filename: name, initialFilename: name };
  }, [defaultFilename]);
  const selectArtifact = useCallback((artifact: Artifact) => {
    setDrafts((previous) => previous[artifact.id] ? previous : { ...previous, [artifact.id]: newDraft(artifact) });
    setSelection(artifact.id);
  }, [newDraft]);
  const selected = available.find((artifact) => artifact.id === selection) ?? preferredArtifact(available, requestedMessageId) ?? preferredArtifact(available);
  useEffect(() => {
    if (visible && selection === null && selected) selectArtifact(selected);
  }, [visible, selection, selected, selectArtifact]);
  useEffect(() => {
    if (!requestedMessageId || (appliedRequest.current?.messageId === requestedMessageId && appliedRequest.current.version === requestedVersion)) return;
    const target = preferredArtifact(available, requestedMessageId);
    if (!target) return;
    appliedRequest.current = { messageId: requestedMessageId, version: requestedVersion };
    selectArtifact(target);
  }, [requestedMessageId, requestedVersion, available, selectArtifact]);
  const draft = selected ? drafts[selected.id] : undefined;
  const content = draft?.content ?? selected?.content ?? "";
  const filename = draft?.filename ?? (selected ? defaultFilename(selected) : "");
  const mounted = useRef(true);
  const currentSession = useRef(sessionId);
  const saveLock = useRef(false);
  currentSession.current = sessionId;
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  const save = useMutation({
    mutationFn: ({ sessionId: targetSession, artifact, content: body, filename: name }: SaveInput) => api.sessions.upload(targetSession,
      new File([body], name, { type: artifact.kind === "document" ? "text/markdown;charset=utf-8" : "text/plain;charset=utf-8" })),
    onSuccess: (attachment, saved) => {
      if (!mounted.current || saved.sessionId !== currentSession.current) return;
      setDrafts((previous) => ({
        ...previous,
        [saved.artifact.id]: {
          ...(previous[saved.artifact.id] ?? { ...newDraft(saved.artifact), content: saved.content, filename: saved.filename }),
          savedContent: saved.content,
          savedFilename: saved.filename,
          attachment,
        },
      }));
      toast.show(t("artifact.saved"), "ok");
    },
    onSettled: () => { saveLock.current = false; },
  });

  const dirty = Object.values(drafts).some((edited) => edited.content !== (edited.savedContent ?? edited.source.content) || edited.filename !== (edited.savedFilename ?? edited.initialFilename));
  const pendingChanges = dirty || save.isPending;
  const dirtyCallback = useRef(onDirtyChange);
  dirtyCallback.current = onDirtyChange;
  useEffect(() => { dirtyCallback.current?.(pendingChanges); }, [pendingChanges]);
  useEffect(() => () => { dirtyCallback.current?.(false); }, []);

  useEffect(() => {
    if (!pendingChanges) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [pendingChanges]);

  const saveCurrent = useCallback(() => {
    if (!selected || saveLock.current || save.isPending || (draft?.attachment && content === draft.savedContent && filename === draft.savedFilename)) return;
    saveLock.current = true;
    const name = safeArtifactFilename(filename, defaultFilename(selected));
    setDrafts((previous) => ({ ...previous, [selected.id]: { ...(previous[selected.id] ?? newDraft(selected)), content, filename: name } }));
    save.mutate({ sessionId, artifact: selected, content, filename: name });
  }, [sessionId, selected, draft, save.isPending, save.mutate, content, filename, defaultFilename, newDraft]);

  useEffect(() => {
    if (!visible) return;
    const saveShortcut = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || !selected || document.querySelector("dialog[open], [role=dialog][aria-modal=true]")) return;
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === "s") {
        event.preventDefault();
        saveCurrent();
      }
    };
    window.addEventListener("keydown", saveShortcut);
    return () => window.removeEventListener("keydown", saveShortcut);
  }, [saveCurrent, selected, visible]);

  const updateDraft = (patch: Partial<Pick<Draft, "content" | "filename">>) => {
    if (!selected) return;
    setDrafts((previous) => ({ ...previous, [selected.id]: { ...(previous[selected.id] ?? newDraft(selected)), ...patch } }));
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(content);
      toast.show(t("artifact.copied"), "ok");
    } catch {
      toast.show(t("artifact.copyFailed"), "error");
    }
  };
  const download = () => {
    if (!selected) return;
    const url = URL.createObjectURL(new Blob([content], { type: "text/plain;charset=utf-8" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = safeArtifactFilename(filename, defaultFilename(selected));
    document.body.append(link);
    link.click();
    link.remove();
    // 下载交给浏览器处理后再释放，兼容桌面壳内的下载事件。
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const changed = Boolean(selected && content !== selected.content);
  const unsaved = Boolean(selected && (content !== (draft?.savedContent ?? selected.content) || filename !== (draft?.savedFilename ?? draft?.initialFilename ?? defaultFilename(selected))));
  const savedCurrent = Boolean(draft?.attachment && !unsaved);
  const savingSelected = save.isPending && save.variables?.artifact.id === selected?.id;
  const lines = useMemo(() => mode === "diff" && selected ? diffArtifactLines(selected.content, content) : [], [mode, selected, content]);
  const codeTokens = useMemo(() => selected?.kind === "code" && mode === "preview" ? highlightArtifactCode(content, selected.language) : [], [selected, mode, content]);
  const currentSaveError = save.variables?.artifact.id === selected?.id ? save.error : null;

  return (
    <section className="artifact-panel" aria-label={t("artifact.title")}>
      <header className="artifact-head">
        <div className="artifact-heading"><h2>{t("artifact.title")}</h2>{selected ? <span className="artifact-source">{t("artifact.sourceReply", { seq: selected.seq })}</span> : null}</div>
        <button type="button" className="btn ghost sm" onClick={onClose} aria-label={t("artifact.close")} title={t("artifact.close")}>✕</button>
      </header>
      {!selected ? <div className="artifact-empty"><span aria-hidden="true">▤</span><h3>{t("artifact.emptyTitle")}</h3><p>{t("artifact.emptyHint")}</p></div> : <>
        <div className="artifact-file-controls">
          <label className="sr-only" htmlFor="artifact-selection">{t("artifact.select")}</label>
          <select id="artifact-selection" className="select artifact-selection" value={selected.id} onChange={(event) => { const target = available.find((artifact) => artifact.id === event.target.value); if (target) selectArtifact(target); }}>
            {[...available].sort((a, b) => b.seq - a.seq || a.index - b.index).map((artifact) => <option key={artifact.id} value={artifact.id}>
              {t("artifact.namedOption", { name: artifactName(artifact), seq: artifact.seq })}
            </option>)}
          </select>
          <div className="artifact-file-row"><label className="artifact-label" htmlFor="artifact-filename">{t("artifact.filename")}</label>
          <input id="artifact-filename" className="input" value={filename} onChange={(event) => updateDraft({ filename: event.target.value })} autoComplete="off" spellCheck={false} />
          </div>
          <div className="artifact-status" role="status">
            <span className={`artifact-status-dot${unsaved ? " unsaved" : ""}`} aria-hidden="true" />
            {savingSelected ? t("artifact.saving") : unsaved ? t("artifact.unsaved") : savedCurrent ? t("artifact.saved") : t("artifact.ready")}
            {selected.partial ? <span className="badge warn">{t("artifact.partial")}</span> : null}
          </div>
        </div>
        <div className="artifact-view-controls" role="group" aria-label={t("artifact.viewMode")}>
          {(["edit", "preview", "diff"] as const).map((view) => <button key={view} type="button" aria-pressed={mode === view} className={`artifact-view-button${mode === view ? " active" : ""}`} onClick={() => setMode(view)}>{t(`artifact.${view}`)}</button>)}
          <span className="spacer" />
          <button type="button" className="btn ghost sm" disabled={!changed || save.isPending} onClick={() => { if (window.confirm(t("artifact.confirmUndo"))) updateDraft({ content: selected.content }); }}>{t("artifact.undo")}</button>
        </div>
        {selected.partial ? <p className="artifact-notice">{t("artifact.partialHint")}</p> : null}
        <div className="artifact-content">
          {mode === "edit" ? <textarea className={`artifact-editor${selected.kind === "code" ? " code" : ""}`} aria-label={t("artifact.editor")} value={content} onChange={(event) => updateDraft({ content: event.target.value })} spellCheck={selected.kind !== "code"} /> : null}
          {mode === "preview" ? selected.kind === "code" ? <pre className="artifact-code" tabIndex={0} aria-label={t("artifact.codePreview")}><code>{codeTokens.map((token, index) => <span key={index} className={`artifact-token-${token.kind}`}>{token.text}</span>)}</code></pre> : <div className="artifact-document" tabIndex={0}><Markdown text={content} /></div> : null}
          {mode === "diff" ? <div className="artifact-diff" tabIndex={0} aria-label={t("artifact.diffLabel")}>
            <div className="artifact-diff-legend"><span>{t("artifact.original")}</span><span>{t("artifact.edited")}</span><span>{t("artifact.diffHint")}</span></div>
            {!changed ? <p className="artifact-notice">{t("artifact.noChanges")}</p> : lines.map((line, index) => <div key={index} className={`artifact-diff-line ${line.kind}`}>
              <span className="artifact-line-number" aria-label={t("artifact.original")}>{line.oldLine ?? ""}</span><span className="artifact-line-number" aria-label={t("artifact.edited")}>{line.newLine ?? ""}</span>
              <span aria-hidden="true">{line.kind === "added" ? "+" : line.kind === "removed" ? "−" : " "}</span><code>{line.text || " "}</code>
            </div>)}
          </div> : null}
        </div>
        {currentSaveError ? <div className="artifact-save-error" role="alert"><strong>{t("artifact.saveFailed")}</strong><span>{describeError(currentSaveError, t)}</span></div> : null}
        <footer className="artifact-footer">
          <p className="artifact-notice">{t("artifact.copyHint")}</p>
          {draft?.attachment ? <div className="artifact-saved-path"><span>{t("artifact.savedAt")}</span><code title={draft.attachment.path}>{draft.attachment.path}</code>{onUseAsContext ? <button type="button" className="btn ghost sm" disabled={unsaved || save.isPending} onClick={() => { if (draft.attachment && !unsaved && !save.isPending) onUseAsContext(draft.attachment); }}>{t("artifact.useAsContext")}</button> : null}</div> : null}
          <div className="artifact-actions"><button type="button" className="btn sm" onClick={() => void copy()}>{t("artifact.copy")}</button><button type="button" className="btn sm" onClick={download}>{t("artifact.export")}</button><span className="spacer" /><button type="button" className="btn primary" disabled={save.isPending || savedCurrent} title={save.isPending && !savingSelected ? t("artifact.savingOther") : undefined} onClick={saveCurrent}>{t(savingSelected ? "artifact.saving" : savedCurrent ? "artifact.savedAction" : "artifact.saveCopy")}</button></div>
        </footer>
      </>}
    </section>
  );
}
