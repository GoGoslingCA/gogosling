// 文件修改只引用已保存成果版本；预览、申请审批和最终落账是三个不同阶段。
import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../../state/ConnectionContext";
import { queryKeys } from "../../state/queryKeys";
import { ApiError } from "../../api/client";
import type { ChangeSet, ChangeStatus } from "../../api/types";
import { diffArtifactLines, safeArtifactFilename, type ArtifactDiffLine } from "../../lib/artifacts";
import { ErrorBanner, Spinner } from "../../components/ui";
import { Icon } from "../../components/Icon";
import "./file-change.css";

const DIFF_PAGE_SIZE = 600;
const pending = (status?: ChangeStatus) => status === "awaiting_approval" || status === "applying";
const uncertainError = (error: unknown) => !ApiError.is(error) || ["network", "timeout", "aborted", "internal", "unavailable"].includes(error.code);

/** 幂等键只由已持久化 change ID 推导，重试和重新打开面板都不会换键。 */
export function changeApplyKey(id: string): string { return `gogosling:apply:${id}`; }

/** 空文件没有旧行，避免把新建文件错误计为删除一行再新增。 */
export function fileChangeLines(before: string, after: string): ArtifactDiffLine[] {
  if (!before) return after ? after.split("\n").map((text, index) => ({ kind: "added", text, newLine: index + 1 })) : [];
  if (!after) return before.split("\n").map((text, index) => ({ kind: "removed", text, oldLine: index + 1 }));
  return diffArtifactLines(before, after);
}

/** 宿主保持挂载以保留目标和选择；visible 只控制读取与交互，不授予文件权限。 */
export function FileChangePanel({ artifactId, sessionId, revisionId, revisionNumber, modified, visible, onEdit }: {
  artifactId: string;
  sessionId: string;
  revisionId: string;
  revisionNumber?: number;
  modified: boolean;
  visible: boolean;
  onEdit: () => void;
}) {
  const { t } = useTranslation(); const api = useApi(); const client = useQueryClient();
  const [target, setTarget] = useState("");
  const [operation, setOperation] = useState<"create" | "replace">("create");
  const [currentId, setCurrentId] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const [expanded, setExpanded] = useState(false);
  const [clock, setClock] = useState(() => Date.now());
  const [unknownIds, setUnknownIds] = useState<Set<string>>(new Set());
  const [verifyingIds, setVerifyingIds] = useState<Set<string>>(new Set());
  const [conflictIds, setConflictIds] = useState<Set<string>>(new Set());
  const restored = useRef(false); const fieldEdited = useRef(false); const mutationLock = useRef(false);
  const applyRequests = useRef(new Map<string, ChangeSet>());
  const list = useQuery({ queryKey: queryKeys.sessionChanges(sessionId), queryFn: () => api.workbench.listChanges(sessionId), enabled: visible });
  const choices = useMemo(() => (list.data ?? []).filter((item) => item.artifact_id === artifactId && item.session_id === sessionId).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || b.id.localeCompare(a.id)), [list.data, artifactId, sessionId]);
  const remember = (change: ChangeSet) => {
    client.setQueryData(queryKeys.change(change.id), change);
    void client.invalidateQueries({ queryKey: queryKeys.sessionChanges(change.session_id) });
  };
  const select = (change: ChangeSet) => {
    if (change.artifact_id !== artifactId || change.session_id !== sessionId) return;
    remember(change); setCurrentId(change.id); setTarget(change.target_path);
    if (change.operation !== "delete") setOperation(change.operation);
    setPage(0); setExpanded(false); fieldEdited.current = false;
  };
  useEffect(() => {
    if (restored.current || !list.isSuccess) return;
    restored.current = true;
    if (!fieldEdited.current && choices[0]) select(choices[0]);
  }, [list.isSuccess, choices]);

  const changeQuery = useQuery({
    queryKey: queryKeys.change(currentId ?? ""),
    queryFn: async () => {
      const id = currentId!;
      const change = await api.workbench.getChange(id);
      setUnknownIds((previous) => { if (!previous.has(id)) return previous; const next = new Set(previous); next.delete(id); return next; });
      setVerifyingIds((previous) => { if (!previous.has(id)) return previous; const next = new Set(previous); next.delete(id); return next; });
      return change;
    },
    enabled: !!currentId,
    retry: false,
    refetchInterval: (query) => pending(query.state.data?.status) || (currentId && (unknownIds.has(currentId) || verifyingIds.has(currentId))) ? 1500 : false,
  });
  const current = changeQuery.data?.artifact_id === artifactId && changeQuery.data.session_id === sessionId ? changeQuery.data : undefined;
  useEffect(() => {
    const now = Date.now(); setClock(now);
    if (!visible || current?.status !== "prepared") return;
    const remaining = Date.parse(current.expires_at) - now;
    if (!Number.isFinite(remaining) || remaining <= 0) return;
    const timer = setTimeout(() => setClock(Date.now()), Math.min(remaining + 1, 2_147_483_647));
    return () => clearTimeout(timer);
  }, [current?.id, current?.status, current?.expires_at, visible]);
  const expired = current?.status === "prepared" && Date.parse(current.expires_at) <= clock;
  const diff = useQuery({ queryKey: [...queryKeys.change(currentId ?? ""), "diff", current?.preview_digest], queryFn: () => api.workbench.changeDiff(currentId!), enabled: !!current && visible, retry: false });
  const frozen = diff.data?.change.id === current?.id && diff.data?.change.preview_digest === current?.preview_digest ? diff.data : undefined;
  const lines = useMemo(() => frozen ? fileChangeLines(frozen.before, frozen.after) : [], [frozen]);
  const added = lines.filter((line) => line.kind === "added").length;
  const removed = lines.filter((line) => line.kind === "removed").length;
  const totalChanged = added + removed;
  const pageCount = Math.max(1, Math.ceil(lines.length / DIFF_PAGE_SIZE));
  const visibleLines = lines.slice(page * DIFF_PAGE_SIZE, (page + 1) * DIFF_PAGE_SIZE);
  const unknown = !!currentId && unknownIds.has(currentId);
  const verifying = !!currentId && verifyingIds.has(currentId);
  const sourceChanged = !!current && !current.undo_of && current.revision_id !== revisionId;
  const inputChanged = !!current && !current.undo_of && (target.trim() !== current.target_path || operation !== current.operation);
  const retryPreview = !!current && (["conflict", "expired", "denied", "failed", "cancelled"].includes(current.status) || expired || conflictIds.has(current.id) || sourceChanged || inputChanged);

  const prepare = useMutation({
    mutationFn: () => api.workbench.prepareChange(sessionId, { artifact_id: artifactId, revision_id: revisionId, target_path: target.trim(), operation }),
    onSuccess: select,
    onSettled: () => { mutationLock.current = false; },
  });
  const apply = useMutation({
    mutationFn: (change: ChangeSet) => api.workbench.applyChange(change, changeApplyKey(change.id)),
    onSuccess: (change) => {
      // POST 受理不作为最终成功证据，马上通过 GET 核对落账状态。
      setVerifyingIds((previous) => new Set(previous).add(change.id));
      remember(change);
      void client.invalidateQueries({ queryKey: queryKeys.change(change.id) });
    },
    onError: (error, change) => {
      if (uncertainError(error)) setUnknownIds((previous) => new Set(previous).add(change.id));
      else if (ApiError.is(error, "conflict")) setConflictIds((previous) => new Set(previous).add(change.id));
      void client.invalidateQueries({ queryKey: queryKeys.change(change.id) });
    },
    onSettled: () => { mutationLock.current = false; },
  });
  const undo = useMutation({ mutationFn: (change: ChangeSet) => api.workbench.undoChange(change), onSuccess: select, onSettled: () => { mutationLock.current = false; } });
  const renewUndo = useMutation({ mutationFn: async (change: ChangeSet) => {
    const original = await api.workbench.getChange(change.undo_of!);
    if (original.artifact_id !== artifactId || original.session_id !== sessionId) throw new ApiError("conflict", "", {});
    return api.workbench.undoChange(original);
  }, onSuccess: select, onSettled: () => { mutationLock.current = false; } });
  const cancel = useMutation({ mutationFn: (change: ChangeSet) => api.workbench.cancelChange(change), onSuccess: remember, onSettled: () => { mutationLock.current = false; } });
  const reconcile = useMutation({ mutationFn: (change: ChangeSet) => api.workbench.reconcileChange(change), onSuccess: remember, onSettled: () => { mutationLock.current = false; } });
  const busy = prepare.isPending || apply.isPending || undo.isPending || renewUndo.isPending || cancel.isPending || reconcile.isPending;
  const blocked = modified || !visible || busy || unknown || verifying;
  const resetErrors = () => { prepare.reset(); apply.reset(); undo.reset(); renewUndo.reset(); cancel.reset(); reconcile.reset(); };
  const generate = () => {
    if (blocked || mutationLock.current || !target.trim() || pending(current?.status) || current?.status === "recovery_required") return;
    mutationLock.current = true; resetErrors(); prepare.mutate();
  };
  const requestApply = () => {
    if (!current || current.status !== "prepared" || blocked || mutationLock.current || retryPreview || !frozen || diff.error) return;
    mutationLock.current = true;
    const request = applyRequests.current.get(current.id) ?? { ...current };
    applyRequests.current.set(current.id, request);
    apply.mutate(request);
  };
  const checkStatus = () => { if (currentId) void changeQuery.refetch(); else void list.refetch(); };
  const exportDiff = () => {
    if (!current || !frozen) return;
    const body = [`--- ${current.target_path}`, `+++ ${current.target_path}`, ...lines.map((line) => `${line.kind === "added" ? "+" : line.kind === "removed" ? "-" : " "}${line.text}`)].join("\n");
    const url = URL.createObjectURL(new Blob([body], { type: "text/plain;charset=utf-8" }));
    const link = document.createElement("a"); link.href = url; link.download = `${safeArtifactFilename(current.target_path, "file-change")}.diff`; document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const applied = current?.status === "applied" && !verifying && !unknown;
  const approvalPassed = current?.status === "applying" || applied;
  const statusKey = unknown ? "changes.status_unknown" : verifying && current?.status === "applied" ? "changes.status_checking" : expired ? "changes.status_expired" : current?.reason === "change_busy" ? "changes.status_busy" : current ? `changes.status_${current.status}` : "changes.notPrepared";

  return <section className="file-change-panel" aria-label={t("changes.title")}>
    {modified ? <div className="file-change-save-first"><Icon name="edit" size={15} /><span>{t("changes.saveFirst")}</span><button type="button" className="btn ghost sm" onClick={onEdit}>{t("changes.backToEdit")}</button></div> : null}
    {choices.length ? <div className="file-change-history"><label htmlFor={`change-history-${artifactId}`}>{t("changes.history")}</label><select id={`change-history-${artifactId}`} className="select" value={currentId ?? ""} disabled={busy} onChange={(event) => {
      restored.current = true; resetErrors(); const next = choices.find((item) => item.id === event.target.value);
      if (next) select(next); else { setCurrentId(null); fieldEdited.current = true; }
    }}><option value="">{t("changes.newPreview")}</option>{choices.map((item) => <option key={item.id} value={item.id}>{item.target_path} · {t(`changes.status_${item.status}`)}</option>)}</select></div> : null}
    <ErrorBanner error={list.error ?? changeQuery.error ?? prepare.error ?? apply.error ?? undo.error ?? renewUndo.error ?? cancel.error ?? reconcile.error} />
    {list.isFetching && !list.data ? <Spinner /> : null}
    {!modified && !current?.undo_of ? <div className="file-change-form">
      <label htmlFor={`change-target-${artifactId}`}>{t("changes.target")}</label>
      <input id={`change-target-${artifactId}`} className="input" value={target} disabled={busy || pending(current?.status) || unknown || verifying} placeholder={t("changes.pathPlaceholder")} autoComplete="off" spellCheck={false} onChange={(event) => { fieldEdited.current = true; setTarget(event.target.value); }} />
      <div className="file-change-operation" role="group" aria-label={t("changes.operation")}>{(["create", "replace"] as const).map((value) => <label key={value}><input type="radio" name={`change-operation-${artifactId}`} checked={operation === value} disabled={busy || pending(current?.status) || unknown || verifying} onChange={() => { fieldEdited.current = true; setOperation(value); }} /><span>{t(`changes.operation_${value}`)}</span></label>)}<span>{t("changes.savedVersion", { n: revisionNumber ?? "—" })}</span></div>
      <button type="button" className="btn sm" disabled={blocked || !target.trim() || pending(current?.status) || current?.status === "recovery_required"} onClick={generate}>{t(prepare.isPending ? "changes.preparing" : retryPreview || current ? "changes.previewAgain" : "changes.prepare")}</button>
    </div> : null}
    {current ? <>
      <div className="file-change-target"><Icon name={current.operation === "delete" ? "close" : "document"} size={15} /><code>{current.target_path}</code><span className="badge">{t(`changes.operation_${current.operation}`)}</span>{current.undo_of ? <span className="badge warn">{t("changes.undoPreview")}</span> : null}</div>
      <div className="file-change-status" role="status"><strong>{t(statusKey)}</strong>{apply.isPending ? <span>{t("changes.requesting")}</span> : null}<button type="button" className="btn ghost sm" disabled={changeQuery.isFetching} onClick={checkStatus}>{t("changes.refresh")}</button></div>
      <ol className="file-change-trail" aria-label={t("changes.stages")}>
        <li data-state="done"><Icon name="check" size={14} /><span>{t("changes.stepPreview")}</span></li>
        <li data-state={approvalPassed ? "done" : current.status === "awaiting_approval" ? "active" : current.status === "denied" || current.status === "cancelled" ? "blocked" : "pending"}><Icon name={approvalPassed ? "check" : "shield"} size={14} /><span>{t("changes.stepApproval")}</span></li>
        <li data-state={applied ? "done" : current.status === "applying" ? "active" : "pending"}><Icon name={applied ? "check" : "document"} size={14} /><span>{t("changes.stepApply")}</span></li>
      </ol>
      {current.operation === "delete" ? <p className="file-change-warning" role="alert">{t("changes.deleteWarning")}</p> : null}
      {unknown ? <p className="file-change-warning">{t("changes.unknownHint")}</p> : null}
      {sourceChanged || retryPreview ? <p className="file-change-warning">{t(sourceChanged ? "changes.versionChanged" : "changes.previewRequired")}</p> : null}
      {current.status === "recovery_required" ? <p className="file-change-warning">{t("changes.recoveryHint")}</p> : null}
      <ErrorBanner error={diff.error} />
      {diff.isFetching && !frozen ? <Spinner /> : frozen ? <>
        <div className="file-change-diff-summary" aria-label={t("changes.diffSummary", { added, removed })}><span className="change-added">+{added}</span><span className="change-removed">−{removed}</span><div className="file-change-count-bar" aria-hidden="true"><span className="added" style={{ width: `${totalChanged ? added / totalChanged * 100 : 0}%` }} /><span className="removed" style={{ width: `${totalChanged ? removed / totalChanged * 100 : 0}%` }} /></div><button type="button" className="btn ghost sm" onClick={exportDiff}>{t("changes.exportDiff")}</button></div>
        <div className="file-change-diff" tabIndex={0} aria-label={t("changes.diff")}><div className="file-change-diff-labels"><span>{t("changes.before")}</span><span>{t("changes.after")}</span></div>{visibleLines.length ? visibleLines.map((line, index) => <div key={page * DIFF_PAGE_SIZE + index} className={`artifact-diff-line ${line.kind}`}><span className="artifact-line-number">{line.oldLine}</span><span className="artifact-line-number">{line.newLine}</span><span aria-hidden="true">{line.kind === "added" ? "+" : line.kind === "removed" ? "−" : " "}</span><code>{line.text || " "}</code></div>) : <p>{t("changes.noDifferences")}</p>}</div>
        {pageCount > 1 ? <div className="file-change-pagination"><span>{t("changes.diffWindow", { start: page * DIFF_PAGE_SIZE + 1, end: Math.min((page + 1) * DIFF_PAGE_SIZE, lines.length), total: lines.length })}</span>{!expanded ? <button type="button" className="btn ghost sm" onClick={() => { setExpanded(true); setPage(1); }}>{t("changes.showMore")}</button> : <><button type="button" className="btn ghost sm" disabled={page === 0} onClick={() => setPage((value) => value - 1)}>{t("changes.previous")}</button><button type="button" className="btn ghost sm" disabled={page + 1 >= pageCount} onClick={() => setPage((value) => value + 1)}>{t("changes.next")}</button></>}</div> : null}
      </> : null}
      <div className="file-change-actions">
        {current.undo_of && retryPreview ? <button type="button" className="btn sm" disabled={blocked} onClick={() => { if (!mutationLock.current) { mutationLock.current = true; resetErrors(); renewUndo.mutate(current); } }}>{t("changes.previewAgain")}</button> : null}
        {current.status === "prepared" ? <button type="button" className={`btn ${current.operation === "delete" ? "danger" : "primary"}`} disabled={blocked || retryPreview || !frozen || !!diff.error} onClick={requestApply}>{t(current.operation === "delete" ? "changes.requestDelete" : "changes.requestApply")}</button> : null}
        {current.status === "awaiting_approval" ? <button type="button" className="btn sm" disabled={!visible || busy} onClick={() => { if (!mutationLock.current) { mutationLock.current = true; cancel.mutate(current); } }}>{t("changes.cancel")}</button> : null}
        {applied ? <button type="button" className="btn sm" disabled={blocked} onClick={() => { if (!mutationLock.current) { mutationLock.current = true; undo.mutate(current); } }}>{t("changes.prepareUndo")}</button> : null}
        {current.status === "recovery_required" ? <button type="button" className="btn sm" disabled={!visible || busy} onClick={() => { if (!mutationLock.current) { mutationLock.current = true; reconcile.mutate(current); } }}>{t("changes.reconcile")}</button> : null}
      </div>
    </> : null}
    <details className="file-change-boundary"><summary><Icon name="shield" size={13} />{t("changes.boundaryTitle")}</summary><p>{t("changes.boundary")}</p>{current?.reason ? <p>{t("changes.reason")}: <code>{current.reason}</code></p> : null}</details>
  </section>;
}
