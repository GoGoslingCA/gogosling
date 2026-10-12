// 共享任务输入外观与键盘、拖放交互；不创建会话、不上传文件、不保存业务状态。
import { lazy, Suspense, useEffect, useRef, useState, type ReactNode, type Ref } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/Icon";
import "./task-input.css";

// 点击后才下载优化界面；内部模型指令始终由内核持有。
const PromptEnhancementControls = lazy(() => import("./PromptEnhancementControls")
  .then(module => ({ default: module.PromptEnhancementControls }))
  .catch(() => ({default: PromptEnhancementUnavailable})));
function PromptEnhancementUnavailable({onDismiss}: {onDismiss: () => void}) {
  const {t} = useTranslation();
  return <><span role="alert" className="composer-hint">{t("enhance.loadFailed")}</span><button type="button" className="btn ghost sm" onClick={onDismiss}>{t("common.close")}</button></>;
}

/** 输入区只展示选中资料的名称；id 由持有本地文件或已上传附件的调用方定义。 */
export interface TaskInputMaterial {
  id: string;
  name: string;
  title?: string;
}

/** 所有状态均由调用方持有，开始页和任务工作区可共用相同交互。 */
export interface TaskInputProps {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  materials: readonly TaskInputMaterial[];
  onRemoveMaterial: (id: string) => void;
  onFiles: (files: File[]) => void;
  disabled: boolean;
  filesDisabled: boolean;
  removeDisabled?: boolean;
  submitDisabled: boolean;
  submitOnEnter?: boolean;
  submitLabel: string;
  attachLabel?: string;
  label: string;
  placeholder: string;
  className?: string;
  inputKey?: string;
  textareaRef?: Ref<HTMLTextAreaElement>;
  actions?: ReactNode;
  contextActions?: ReactNode;
  secondaryAction?: ReactNode;
  status?: ReactNode;
  footer?: ReactNode;
  enhanceAgentId?: string;
  enhanceDisabled?: boolean;
}

/** Enter 提交、Shift+Enter 换行，输入法合成期间不提交；文件仅交回调用方。 */
export function TaskInput({ value, onChange, onSubmit, materials, onRemoveMaterial, onFiles, disabled, filesDisabled, removeDisabled = disabled, submitDisabled, submitOnEnter = true, submitLabel, attachLabel, label, placeholder, className = "", inputKey, textareaRef, actions, contextActions, secondaryAction, status, footer, enhanceAgentId, enhanceDisabled = false }: TaskInputProps) {
  const { t } = useTranslation();
  const fileInput = useRef<HTMLInputElement>(null);
  const focusInput = () => queueMicrotask(() => fileInput.current?.form?.querySelector<HTMLTextAreaElement>("textarea")?.focus());
  const composing = useRef(false);
  const dragDepth = useRef(0);
  const [dragging, setDragging] = useState(false);
  const [enhancementStartedAt, setEnhancementStartedAt] = useState<number | null>(null);
  const revision = useRef({ value, scope: inputKey, agentId: enhanceAgentId, number: 0 });
  if (revision.current.value !== value || revision.current.scope !== inputKey || revision.current.agentId !== enhanceAgentId) {
    revision.current = { value, scope: inputKey, agentId: enhanceAgentId, number: revision.current.number + 1 };
  }
  useEffect(() => { setEnhancementStartedAt(null); }, [inputKey, enhanceAgentId]);
  const submit = () => { if (!submitDisabled && !disabled) { setEnhancementStartedAt(null); onSubmit(); } };
  useEffect(() => {
    dragDepth.current = 0;
    setDragging(false);
  }, [inputKey, filesDisabled]);
  useEffect(() => { composing.current = false; }, [inputKey]);

  const addFiles = (files: FileList | File[] | null) => {
    const selected = files ? Array.from(files) : [];
    // 先重置选择器，让失败后再次选择同一个文件仍能触发 change。
    if (fileInput.current) fileInput.current.value = "";
    if (!filesDisabled && selected.length) onFiles(selected);
  };

  return <form
    className={`task-input composer-dropzone ${className}${dragging ? " is-dragging" : ""}`}
    onSubmit={(event) => { event.preventDefault(); submit(); }}
    onDragEnter={(event) => {
      if (!event.dataTransfer.types.includes("Files")) return;
      event.preventDefault();
      dragDepth.current += 1;
      if (!filesDisabled) setDragging(true);
    }}
    onDragLeave={(event) => {
      if (!event.dataTransfer.types.includes("Files")) return;
      event.preventDefault();
      dragDepth.current = Math.max(0, dragDepth.current - 1);
      if (!dragDepth.current) setDragging(false);
    }}
    onDragOver={(event) => {
      if (!event.dataTransfer.types.includes("Files")) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = filesDisabled ? "none" : "copy";
    }}
    onDrop={(event) => {
      if (!event.dataTransfer.types.includes("Files")) return;
      event.preventDefault();
      dragDepth.current = 0;
      setDragging(false);
      addFiles(event.dataTransfer.files);
    }}
  >
    <div className={dragging ? "composer-caption" : "sr-only"}>{t(dragging ? "compose.dropFiles" : "compose.materialHint")}</div>
    {materials.length ? <div className="composer-materials" aria-label={t("compose.selectedMaterials", { count: materials.length })}>
      <span className="composer-material-count">{t("compose.selectedMaterials", { count: materials.length })}</span>
      {materials.map((material) => <span key={material.id} className="attachment-chip" title={material.title ?? material.name}>
        <Icon name="document" size={14} /><span className="attachment-name">{material.name}</span>
        <button type="button" className="btn ghost sm" disabled={removeDisabled} aria-label={t("chat.removeAttachment", { name: material.name })} onClick={() => onRemoveMaterial(material.id)}><Icon name="close" size={13} /></button>
      </span>)}
    </div> : null}
    <textarea ref={textareaRef} className="textarea" value={value} placeholder={placeholder} aria-label={label} disabled={disabled}
      onChange={(event) => { if (!disabled) onChange(event.target.value); }}
      onCompositionStart={() => { composing.current = true; }}
      onCompositionEnd={() => { composing.current = false; }}
      onKeyDown={(event) => {
        if (event.key !== "Enter" || event.shiftKey || composing.current || event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229 || !submitOnEnter) return;
        event.preventDefault();
        submit();
      }}
    />
    <div className="row composer-actions task-input-actions">
      <input ref={fileInput} type="file" multiple aria-label={t("chat.attach")} disabled={filesDisabled} hidden onChange={(event) => addFiles(event.target.files)} />
      <button type="button" className="btn ghost sm composer-icon" aria-label={attachLabel ?? t("chat.attach")} title={attachLabel ?? t("chat.attach")} disabled={filesDisabled} onClick={() => fileInput.current?.click()}><Icon name="plus" size={19}/><span className="sr-only">{attachLabel ?? t("chat.attach")}</span></button>
      {enhanceAgentId ? enhancementStartedAt !== null ? <Suspense fallback={<button type="button" className="btn ghost sm" disabled>{t("common.loading")}</button>}><PromptEnhancementControls agentId={enhanceAgentId} scope={inputKey} value={value} revision={revision.current.number} initialRevision={enhancementStartedAt} disabled={disabled || enhanceDisabled} onChange={onChange} onFocus={focusInput} onDismiss={() => setEnhancementStartedAt(null)}/></Suspense> : <button type="button" className="btn ghost sm prompt-enhance-button" disabled={disabled || enhanceDisabled || !value.trim()} title={t("enhance.action")} aria-label={t("enhance.action")} onClick={() => setEnhancementStartedAt(revision.current.number)}><Icon name="spark" size={17}/><span className="sr-only">{t("enhance.action")}</span></button> : null}
      {actions}
      {contextActions}
      {!contextActions ? <span className="spacer"/> : null}
      {secondaryAction}
      <button type="submit" className="btn primary sm composer-send" aria-label={submitLabel} title={submitLabel} disabled={submitDisabled || disabled}><Icon name="arrow" size={18} style={{transform:"rotate(-90deg)"}}/><span className="sr-only">{submitLabel}</span></button>
    </div>
    <div className={!status || status === t("compose.keyboardHint") ? "sr-only" : "composer-hint"} role="status">{status ?? t("compose.keyboardHint")}</div>
    {footer ? <div className="task-input-footer">{footer}</div> : null}

  </form>;
}
