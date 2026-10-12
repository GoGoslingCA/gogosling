// 保存反馈与跨窗口冲突选择；只有内核确认后才显示已保存。
import { useTranslation } from "react-i18next";
import type { usePersistentDraft } from "../state/usePersistentDraft";
import { Icon } from "./Icon";
import "./compact-workbench.css";

/** 未上传的本地 File 只能保留在当前窗口，需要单独说明恢复边界。 */
export function DraftSaveStatus({ draft, localFiles = false, kind = "composer" }: { kind?: "composer" | "artifact"; draft: ReturnType<typeof usePersistentDraft>; localFiles?: boolean }) {
  const { t } = useTranslation();
  const needsDetails = draft.status === "unsupported" || draft.status === "error" || draft.restored || localFiles || !!draft.conflict;
  const statusKey = draft.status === "unsupported" ? "compact.memoryOnly" : draft.status === "idle" ? draft.dirty ? "compact.draftUnsaved" : null : `p3draft.${draft.status}`;
  return <div className="draft-save-state" data-state={draft.status}>
    {statusKey || needsDetails ? <div className="draft-state-line" role="status">
      <span className="draft-state-icon"><Icon name={draft.status === "saved" ? "check" : draft.status === "error" || draft.conflict ? "close" : "clock"} size={13} /></span>
      {statusKey ? <span>{t(statusKey)}</span> : null}
      {draft.restored ? <span className="draft-state-tag">{t("compact.draftRestored")}</span> : null}
      {localFiles ? <span className="draft-state-tag">{t("compact.localFiles")}</span> : null}
      {draft.status === "error" ? <button type="button" className="btn ghost sm" onClick={() => void draft.saveNow().catch(() => undefined)}>{t("p3draft.retry")}</button> : null}
      {needsDetails ? <details className="draft-explainer"><summary>{t("compact.details")}<Icon name="chevron" size={12} /></summary>
        {draft.status === "unsupported" ? <p>{t("p3draft.unsupported")}</p> : null}
        {draft.status === "error" ? <p>{t("compact.draftFailureDetails")}</p> : null}
        {draft.restored ? <p>{t(kind === "artifact" ? "p3draft.artifactRestored" : "p3draft.restored")}</p> : null}
        {localFiles ? <p>{t("p3draft.localFiles")}</p> : null}
        {draft.conflict ? <p>{t("p3draft.conflictHint")}</p> : null}
      </details> : null}
    </div> : null}
    {draft.conflict ? <div className="draft-conflict" role="alert">
      <details><summary>{t("p3draft.remoteContent")}</summary><pre>{draft.conflict.text}</pre>
        {draft.conflict.attachments?.map((attachment) => <div key={attachment.path}>{attachment.name}</div>)}
        {draft.conflict.material_ids?.map((id) => <div key={id}>{t("p3draft.materialReference", { id })}</div>)}
      </details>
      <div className="row">
        <button type="button" className="btn sm" onClick={() => void draft.resolveConflict("local").catch(() => undefined)}>{t("p3draft.keepLocal")}</button>
        <button type="button" className="btn sm" onClick={() => void draft.resolveConflict("remote").catch(() => undefined)}>{t("p3draft.useRemote")}</button>
      </div>
    </div> : null}
  </div>;
}
