// 输入框：每个会话独立保留草稿，上传和发送完成后只更新发起操作的会话。

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useToast } from "../../components/Toast";
import { describeError } from "../../lib/errorMessage";
import { ApiError } from "../../api/client";
import type { Attachment, DraftContent } from "../../api/types";
import { TaskInput } from "./TaskInput";
import { usePersistentDraft } from "../../state/usePersistentDraft";
import { DraftSaveStatus } from "../../components/DraftSaveStatus";
import { sameDraftContent } from "../../state/draftController";

interface Draft {
  text: string;
  textVersion: number;
  attachments: Attachment[];
  materialIds: string[];
  uploading: number;
  sending: boolean;
}

const emptyDraft = (): Draft => ({ text: "", textVersion: 0, attachments: [], materialIds: [], uploading: 0, sending: false });

export function Composer({
  draftKey,
  enhancementEnabled = false,
  contextActions,
  modelAction,
  agentId,
  projectId,
  initialText,
  pendingAttachment,
  onAttachmentConsumed,
  draftRequest,
  onDraftRequestConsumed,
  onDraftDirtyChange,
  disabled,
  busy,
  cancelBusy = false,
  activeRunId,
  onSend,
  onCancel,
  upload,
}: {
  draftKey: string;
  enhancementEnabled?: boolean;
  contextActions?: ReactNode;
  modelAction?: ReactNode;
  agentId?: string;
  projectId?: string;
  initialText?: string;
  pendingAttachment?: Attachment | null;
  onAttachmentConsumed?: () => void;
  draftRequest?: { id: string; text: string } | null;
  onDraftRequestConsumed?: () => void;
  onDraftDirtyChange?: (dirty: boolean) => void;
  disabled: boolean;
  busy: boolean;
  cancelBusy?: boolean;
  activeRunId: string | null;
  onSend: (text: string, attachments: Attachment[], materialIds?: string[]) => Promise<unknown>;
  onCancel: () => void;
  upload: (file: File) => Promise<Attachment>;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  // 同步引用阻止同一个事件循环中的重复点击，并保护异步回调中的新编辑。
  const currentDrafts = useRef(drafts);
  const activeDraftKey = useRef(draftKey);
  useLayoutEffect(() => { activeDraftKey.current = draftKey; }, [draftKey]);
  const textInput = useRef<HTMLTextAreaElement>(null);
  const initialized = useRef(new Set<string>());
  const consumedDraftRequests = useRef(new Set<string>());
  const consumedAttachment = useRef<{ key: string; attachment: Attachment } | null>(null);
  const dirtyCallback = useRef(onDraftDirtyChange);
  dirtyCallback.current = onDraftDirtyChange;
  const draft = drafts[draftKey] ?? emptyDraft();
  const running = activeRunId !== null;

  const update = useCallback((key: string, change: (value: Draft) => Draft, sync?: (content: DraftContent) => void) => {
    const value = change(currentDrafts.current[key] ?? emptyDraft());
    const next = { ...currentDrafts.current, [key]: value };
    currentDrafts.current = next;
    // 明确输入在事件内进入来源控制器，避免同一事件中的上传响应恢复旧文字。
    sync?.({ text: value.text, attachments: value.attachments, material_ids: value.materialIds });
    setDrafts(next);
  }, []);

  const persistent = usePersistentDraft(agentId && draftKey ? { kind: "composer", agent_id: agentId, session_id: draftKey, project_id: projectId } : null,
    { text: draft.text, attachments: draft.attachments, material_ids: draft.materialIds },
    (restored) => update(draftKey, (value) => ({ ...value, text: restored.text, attachments: restored.attachments ?? [], materialIds: restored.material_ids ?? [], textVersion: value.textVersion + 1 })));
  useEffect(() => persistent.subscribeReleased((scope, saved) => {
    const key = scope.kind === "composer" ? scope.session_id : undefined;
    if (!key || key === activeDraftKey.current) return;
    const cached = currentDrafts.current[key];
    if (!cached || cached.uploading || cached.sending || !sameDraftContent({ text: cached.text, attachments: cached.attachments, material_ids: cached.materialIds }, saved)) return;
    const next = { ...currentDrafts.current };
    delete next[key];
    currentDrafts.current = next;
    setDrafts(next);
  }), [persistent.subscribeReleased]);
  const hasDrafts = persistent.anyDirty || Object.values(drafts).some((value) => value.uploading || value.sending || (persistent.status === "unsupported" && (value.text.trim() || value.attachments.length || value.materialIds.length)));

  useEffect(() => {
    if (!initialText || initialized.current.has(draftKey)) return;
    initialized.current.add(draftKey);
    update(draftKey, (value) => value.textVersion > 0 ? value : { ...value, text: initialText, textVersion: value.textVersion + 1 }, persistent.setContent);
  }, [draftKey, initialText, update]);

  useEffect(() => {
    if (!draftRequest || consumedDraftRequests.current.has(draftRequest.id)) return;
    consumedDraftRequests.current.add(draftRequest.id);
    if (draftRequest.text) update(draftKey, (value) => ({
      ...value,
      text: value.text.trim() ? `${value.text}\n\n${draftRequest.text}` : draftRequest.text,
      textVersion: value.textVersion + 1,
    }), persistent.setContent);
    textInput.current?.focus();
    onDraftRequestConsumed?.();
  }, [draftKey, draftRequest, onDraftRequestConsumed, update]);

  useEffect(() => { dirtyCallback.current?.(hasDrafts); }, [hasDrafts]);
  useEffect(() => () => { dirtyCallback.current?.(false); }, []);

  useEffect(() => {
    if (!hasDrafts) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [hasDrafts]);

  useEffect(() => {
    if (!pendingAttachment) {
      consumedAttachment.current = null;
      return;
    }
    if (consumedAttachment.current?.key === draftKey && consumedAttachment.current.attachment === pendingAttachment) return;
    consumedAttachment.current = { key: draftKey, attachment: pendingAttachment };
    update(draftKey, (value) => value.attachments.some((attachment) => attachment.path === pendingAttachment.path)
      ? value
      : { ...value, attachments: [...value.attachments, pendingAttachment] }, persistent.setContent);
    onAttachmentConsumed?.();
  }, [draftKey, pendingAttachment, onAttachmentConsumed, update]);

  const submit = async () => {
    const key = draftKey;
    const sent = currentDrafts.current[key] ?? emptyDraft();
    const trimmed = sent.text.trim();
    if ((!trimmed && sent.attachments.length === 0 && !sent.materialIds.length) || running || busy || disabled || sent.sending || sent.uploading > 0 || persistent.conflict || !persistent.hydrated) return;
    update(key, (value) => ({ ...value, sending: true }));
    const releaseDraft = persistent.retainOperation();
    let draftToken = persistent.capture();
    try {
      try { draftToken = await persistent.saveNow(); } catch (error) { if (ApiError.is(error, "conflict")) return; }
      if (sent.materialIds.length) await onSend(trimmed, [...sent.attachments], [...sent.materialIds]);
      else await onSend(trimmed, [...sent.attachments]);
      const sentPaths = new Set(sent.attachments.map((attachment) => attachment.path));
      update(key, (value) => ({
        ...value,
        text: value.textVersion === sent.textVersion ? "" : value.text,
        attachments: value.attachments.filter((attachment) => !sentPaths.has(attachment.path)),
        materialIds: value.materialIds.filter((id) => !sent.materialIds.includes(id)),
      }));
      void persistent.clearAfterSend(draftToken).catch(() => undefined);
    } catch (err) {
      // 失败保留正文和附件，允许用户修正或重试。
      toast.show(ApiError.is(err, "conflict") ? t("chat.conflict") : describeError(err, t), "error");
    } finally {
      releaseDraft();
      update(key, (value) => ({ ...value, sending: false }));
    }
  };

  const onFiles = async (files: FileList | readonly File[] | null) => {
    if (!files || files.length === 0 || disabled || busy) return;
    const key = draftKey;
    const current = currentDrafts.current[key] ?? emptyDraft();
    if (current.uploading > 0 || current.sending) return;
    const selected = Array.from(files);
    const releaseDraft = persistent.retainOperation();
    persistent.setContent({ text: current.text, attachments: current.attachments, material_ids: current.materialIds });
    update(key, (value) => ({ ...value, uploading: value.uploading + selected.length }));
    try {
      for (const file of selected) {
        try {
          const attachment = await upload(file);
          // 来源控制器持续存在，即使整个编辑页已卸载，也能保存附件与最新输入。
          persistent.addAttachment(attachment);
          update(key, (value) => value.attachments.some((item) => item.path === attachment.path)
            ? value
            : { ...value, attachments: [...value.attachments, attachment] });
        } catch (err) {
          toast.show(describeError(err, t), "error");
        } finally {
          update(key, (value) => ({ ...value, uploading: value.uploading - 1 }));
        }
      }
    } finally { releaseDraft(); }
  };

  return <TaskInput
    className="composer"
    contextActions={contextActions}
    modelAction={modelAction}
    enhanceAgentId={enhancementEnabled ? agentId : undefined}
    enhanceDisabled={busy || draft.sending || draft.uploading > 0 || !!persistent.conflict || !persistent.hydrated}
    inputKey={draftKey}
    textareaRef={textInput}
    value={draft.text}
    onChange={(text) => update(draftKey, (value) => ({ ...value, text, textVersion: value.textVersion + 1 }), persistent.setContent)}
    label={t("chat.messageInput")}
    placeholder={running ? t("compose.runningPlaceholder") : t("chat.placeholder")}
    disabled={disabled}
    filesDisabled={disabled || busy || draft.sending || draft.uploading > 0}
    removeDisabled={disabled || draft.sending}
    submitDisabled={disabled || busy || draft.sending || draft.uploading > 0 || running || !!persistent.conflict || !persistent.hydrated || (!draft.text.trim() && draft.attachments.length === 0 && !draft.materialIds.length)}
    submitOnEnter={!running}
    submitLabel={t(draft.sending ? "chat.sending" : "chat.send")}
    attachLabel={t(draft.uploading > 0 ? "chat.uploading" : "chat.attach")}
    materials={[...draft.attachments.map((attachment) => ({ id: attachment.path, name: attachment.name, title: attachment.path })), ...draft.materialIds.map((id) => ({ id: `material:${id}`, name: t("p3draft.materialReference", { id }) }))]}
    onRemoveMaterial={(id) => update(draftKey, (value) => ({ ...value, attachments: value.attachments.filter((item) => item.path !== id), materialIds: value.materialIds.filter((item) => `material:${item}` !== id) }), persistent.setContent)}
    onFiles={(files) => void onFiles(files)}
    onSubmit={() => void submit()}
    secondaryAction={running ? <button type="button" className="btn danger sm" disabled={cancelBusy} onClick={onCancel}>{t("chat.cancel")}</button> : null}
    status={draft.uploading > 0 ? t("compose.uploading", { count: draft.uploading }) : running ? t("compose.runningHint") : t("compose.keyboardHint")}
    footer={<DraftSaveStatus draft={persistent} />}
  />;
}
