// 草稿的本地编辑状态与服务端 CAS 分离；串行保存，迟到响应不得覆盖新的输入。
import { ApiError } from "../api/client";
import type { WorkbenchApi } from "../api/modules/workbench";
import type { Draft, DraftContent, DraftScope } from "../api/types";

export type DraftStatus = "loading" | "unsupported" | "idle" | "saving" | "saved" | "error" | "conflict";
export interface DraftSaveToken { draftId?: string; version?: number; editVersion: number; fingerprint: string }
export interface DraftSnapshot {
  content: DraftContent;
  hydrated: boolean;
  status: DraftStatus;
  dirty: boolean;
  restored: boolean;
  error: unknown;
  conflict: Draft | null;
  restoreEpoch: number;
  baseRevisionId?: string;
}
export type DraftApi = Pick<WorkbenchApi, "listDrafts" | "getDraft" | "createDraft" | "updateDraft" | "deleteDraft">;

/** 标准化省略字段，避免服务端省略空数组触发无意义的重复写入。 */
export function normalizeDraft(value: DraftContent): DraftContent {
  return { text: value.text, filename: value.filename ?? "", attachments: [...value.attachments ?? []], material_ids: [...value.material_ids ?? []] };
}
/** 内存作用域键不作为 API 字段提交，服务端仍独立推导 owner 与唯一作用域。 */
export function draftScopeKey(scope: DraftScope): string {
  return JSON.stringify(scope.kind === "artifact" ? [scope.kind, scope.artifact_id] : scope.session_id ? [scope.kind, scope.session_id] : [scope.kind, scope.agent_id, scope.project_id ?? ""]);
}
function fingerprint(value: DraftContent) { return JSON.stringify(normalizeDraft(value)); }
function contentPresent(value: DraftContent) { return Boolean(value.text || value.attachments?.length || value.material_ids?.length); }
function sameScope(value: Draft, scope: DraftScope) {
  return value.kind === scope.kind && value.agent_id === scope.agent_id
    && (scope.kind === "artifact" ? value.artifact_id === scope.artifact_id : scope.session_id ? value.session_id === scope.session_id : !value.session_id && (value.project_id ?? "") === (scope.project_id ?? ""));
}

/** 一个作用域的编辑事务；网络操作仅通过注入的 API，正文只驻留内存与内核。 */
export class DraftController {
  private listeners = new Set<() => void>();
  private snapshot: DraftSnapshot;
  private acknowledged: Draft | null = null;
  private confirmed: string;
  private editVersion = 0;
  private api: DraftApi | null = null;
  private available = false;
  private loading: Promise<void> | null = null;
  private queue: Promise<unknown> = Promise.resolve();
  private timer: ReturnType<typeof setTimeout> | undefined;
  private disposed = false;

  constructor(private scope: DraftScope, initial: DraftContent, private readonly debounceMs = 600) {
    const content = normalizeDraft(initial);
    this.confirmed = fingerprint(scope.kind === "artifact" ? content : { text: "" });
    this.editVersion = scope.kind === "composer" && contentPresent(content) ? 1 : 0;
    this.snapshot = { content, hydrated: false, status: "loading", dirty: fingerprint(content) !== this.confirmed, restored: false, error: null, conflict: null, restoreEpoch: 0, baseRevisionId: scope.base_revision_id };
  }
  /** 供 React 外部状态订阅使用，快照在未变化时保持同一引用。 */
  getSnapshot = (): DraftSnapshot => this.snapshot;
  /** 所有订阅都由调用方清理；控制器不持有组件函数。 */
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private emit(update: Partial<DraftSnapshot>) {
    this.snapshot = { ...this.snapshot, ...update };
    if (!this.disposed) for (const listener of this.listeners) listener();
  }
  /** 新版本来源变化不改变成果草稿的唯一作用域。 */
  setScope(scope: DraftScope) { this.scope = scope.kind === "artifact" && this.snapshot.baseRevisionId ? { ...scope, base_revision_id: this.snapshot.baseRevisionId } : scope; }
  /** 仅在用户显式载入新基线或保存新成果版本成功后调用，不能由 latest 查询自动推进。 */
  setBaseRevision(id: string) {
    this.scope = { ...this.scope, base_revision_id: id };
    const dirty = fingerprint(this.snapshot.content) !== this.confirmed || Boolean(this.acknowledged && this.acknowledged.base_revision_id !== id);
    this.emit({ baseRevisionId: id, dirty, status: this.available ? "idle" : this.snapshot.status });
    this.schedule();
  }
  /** 能力发现完成前不写草稿，旧 daemon 只保留内存编辑。 */
  configure(api: DraftApi | null, supported: boolean, checking: boolean) {
    this.api = api;
    this.available = supported && api !== null;
    if (checking) return;
    if (!this.available) { this.emit({ hydrated: true, status: "unsupported", dirty: fingerprint(this.snapshot.content) !== this.confirmed }); return; }
    if (!this.snapshot.hydrated || this.snapshot.status === "unsupported") void this.hydrate().catch(() => undefined);
  }
  /** 只接受明确的新编辑；服务端响应从不通过本入口回写旧正文。 */
  setContent(input: DraftContent) {
    const content = normalizeDraft(input);
    if (fingerprint(content) === fingerprint(this.snapshot.content)) return;
    this.editVersion += 1;
    const dirty = fingerprint(content) !== this.confirmed;
    this.emit({ content, dirty, status: this.snapshot.conflict ? "conflict" : !this.available ? this.snapshot.status : this.snapshot.status === "saving" || !this.snapshot.hydrated ? this.snapshot.status : "idle" });
    this.schedule();
  }
  private schedule() {
    if (this.timer) clearTimeout(this.timer);
    if (!this.available || !this.snapshot.hydrated || !this.snapshot.dirty || this.snapshot.conflict || this.snapshot.status === "error") return;
    this.timer = setTimeout(() => { this.timer = undefined; void this.saveNow().catch(() => undefined); }, this.debounceMs);
  }
  private async findRemote(): Promise<Draft | null> {
    if (!this.api) return null;
    const { kind, agent_id, project_id, session_id, artifact_id } = this.scope;
    for (let offset = 0; ; offset += 200) {
      const drafts = await this.api.listDrafts({ kind, agent_id, project_id, session_id, artifact_id, limit: 200, offset });
      const found = drafts.find((draft) => sameScope(draft, this.scope));
      if (found) return found;
      if (drafts.length < 200) return null;
    }
  }
  /** 迟到水合遇到已输入内容时保留双方，要求用户明确选择。 */
  hydrate(): Promise<void> {
    if (this.loading) return this.loading;
    if (!this.api || !this.available) return Promise.resolve();
    this.emit({ status: "loading", hydrated: false, error: null });
    this.loading = (async () => {
      try {
        const remote = await this.findRemote();
        this.acknowledged = remote;
        if (remote) {
          const content = normalizeDraft(remote);
          this.confirmed = fingerprint(content);
          if (this.editVersion > 0 && fingerprint(this.snapshot.content) !== this.confirmed) {
            this.emit({ hydrated: true, conflict: remote, status: "conflict", dirty: true });
          } else {
            if (remote.base_revision_id) this.scope = { ...this.scope, base_revision_id: remote.base_revision_id };
            this.emit({ content, hydrated: true, status: "saved", dirty: false, restored: contentPresent(content), restoreEpoch: this.snapshot.restoreEpoch + 1, baseRevisionId: remote.base_revision_id ?? this.snapshot.baseRevisionId });
          }
        } else {
          const dirty = fingerprint(this.snapshot.content) !== this.confirmed;
          this.emit({ hydrated: true, status: "idle", dirty });
          this.schedule();
        }
      } catch (error) { this.emit({ status: "error", error, dirty: contentPresent(this.snapshot.content) }); throw error; }
      finally { this.loading = null; }
    })();
    return this.loading;
  }
  /** 捕获发送时的已确认版本；调用方应将此令牌与实际发送内容一起保留。 */
  capture(): DraftSaveToken {
    return { draftId: this.acknowledged?.id, version: this.acknowledged?.version, editVersion: this.editVersion, fingerprint: fingerprint(this.snapshot.content) };
  }
  private serial<T>(operation: () => Promise<T>): Promise<T> {
    const next = this.queue.then(operation);
    this.queue = next.catch(() => undefined);
    return next;
  }
  private async conflict(error: unknown) {
    if (!this.api) return;
    try {
      const id = ApiError.is(error) ? error.details.existing_draft_id ?? error.details.draft_id : undefined;
      const remote = this.acknowledged ? await this.api.getDraft(this.acknowledged.id) : typeof id === "string" ? await this.api.getDraft(id) : await this.findRemote();
      if (!remote) throw error;
      this.emit({ status: "conflict", error, conflict: remote, dirty: true });
    } catch (readError) { this.emit({ status: "error", error: readError, dirty: true }); }
  }
  /** 立即保存调用瞬间的内容，返回其服务端版本；之后的编辑另行排队。 */
  saveNow(): Promise<DraftSaveToken> {
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined; }
    const content = normalizeDraft(this.snapshot.content);
    const editVersion = this.editVersion;
    const scope = { ...this.scope };
    return this.serial(async () => {
      if (!this.available || !this.api) return { ...this.capture(), editVersion, fingerprint: fingerprint(content) };
      if (!this.snapshot.hydrated) {
        await this.hydrate();
        if (editVersion === 0 && this.snapshot.restored) return this.capture();
      }
      if (this.snapshot.conflict) throw new ApiError("conflict", "", { details: { reason: "stale_version" } });
      if ((fingerprint(content) === this.confirmed && (!this.acknowledged || this.acknowledged.base_revision_id === scope.base_revision_id)) || (!this.acknowledged && !contentPresent(content))) {
        return { ...this.capture(), editVersion, fingerprint: fingerprint(content) };
      }
      this.emit({ status: "saving", error: null });
      try {
        const saved = this.acknowledged
          ? await this.api.updateDraft(this.acknowledged.id, { ...scope, ...content, expected_version: this.acknowledged.version })
          : await this.api.createDraft({ ...scope, ...content });
        this.acknowledged = saved;
        this.confirmed = fingerprint(content);
        const dirty = fingerprint(this.snapshot.content) !== this.confirmed || this.snapshot.baseRevisionId !== saved.base_revision_id;
        this.emit({ status: dirty ? "idle" : "saved", dirty, error: null });
        this.schedule();
        return { draftId: saved.id, version: saved.version, editVersion, fingerprint: fingerprint(content) };
      } catch (error) {
        if (ApiError.is(error, "conflict")) await this.conflict(error);
        else this.emit({ status: "error", error, dirty: true });
        throw error;
      }
    });
  }
  /** 成功发送后仅删除发送时版本；新输入或其他窗口的新版本永不无条件删除。 */
  clearAfterSend(token: DraftSaveToken): Promise<void> {
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined; }
    return this.serial(async () => {
      const acknowledged = this.acknowledged;
      if (acknowledged && token.draftId === acknowledged.id && token.version !== undefined && acknowledged.version > token.version && this.editVersion !== token.editVersion) { this.schedule(); return; }
      if (acknowledged && fingerprint(acknowledged) !== token.fingerprint) { this.schedule(); return; }
      if (this.available && this.api && acknowledged) {
        try { await this.api.deleteDraft(acknowledged.id, token.draftId === acknowledged.id && token.version !== undefined ? token.version : acknowledged.version); }
        catch (error) {
          if (ApiError.is(error, "conflict")) await this.conflict(error);
          else this.emit({ status: "error", error, dirty: true });
          throw error;
        }
      }
      this.acknowledged = null;
      const unchanged = this.editVersion === token.editVersion;
      const content = unchanged && this.scope.kind === "composer" ? normalizeDraft({ text: "" }) : this.snapshot.content;
      if (unchanged) this.confirmed = fingerprint(content);
      else this.confirmed = fingerprint({ text: "" });
      this.emit({ content, status: this.available ? "idle" : "unsupported", dirty: !unchanged && contentPresent(content), restored: false, error: null, restoreEpoch: unchanged && this.scope.kind === "composer" ? this.snapshot.restoreEpoch + 1 : this.snapshot.restoreEpoch });
      this.schedule();
    });
  }
  /** 用户明确选择当前版本或远端版本；继续使用当前版本时仍以远端 version 做 CAS。 */
  async resolveConflict(choice: "local" | "remote"): Promise<void> {
    const remote = this.snapshot.conflict;
    if (!remote) return;
    this.acknowledged = remote;
    this.confirmed = fingerprint(remote);
    if (choice === "remote") {
      this.editVersion += 1;
      if (remote.base_revision_id) this.scope = { ...this.scope, base_revision_id: remote.base_revision_id };
      this.emit({ content: normalizeDraft(remote), status: "saved", conflict: null, error: null, dirty: false, restoreEpoch: this.snapshot.restoreEpoch + 1, baseRevisionId: remote.base_revision_id ?? this.snapshot.baseRevisionId });
    } else {
      this.emit({ conflict: null, error: null, status: "idle", dirty: true });
      await this.saveNow();
    }
  }
  /** 明确丢弃草稿同样校验版本；失败前不清空本地内容。 */
  discard(): Promise<void> {
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined; }
    const discardedVersion = this.editVersion;
    return this.serial(async () => {
      if (this.available && this.api && this.acknowledged) {
        try { await this.api.deleteDraft(this.acknowledged.id, this.acknowledged.version); }
        catch (error) { if (ApiError.is(error, "conflict")) await this.conflict(error); else this.emit({ status: "error", error, dirty: true }); throw error; }
      }
      this.acknowledged = null;
      const changed = this.editVersion !== discardedVersion;
      this.confirmed = fingerprint({ text: "" });
      if (!changed) this.editVersion += 1;
      this.emit({ content: changed ? this.snapshot.content : normalizeDraft({ text: "" }), status: this.available ? "idle" : "unsupported", dirty: changed && contentPresent(this.snapshot.content), restored: false, error: null, conflict: null, restoreEpoch: changed ? this.snapshot.restoreEpoch : this.snapshot.restoreEpoch + 1 });
      this.schedule();
    });
  }
  /** 连接或组件树销毁时释放计时器，不把内容写入客户端存储。 */
  dispose() { this.disposed = true; if (this.timer) clearTimeout(this.timer); this.listeners.clear(); }
}
