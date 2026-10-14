// 草稿的本地编辑状态与服务端 CAS 分离；串行保存，迟到响应不得覆盖新的输入。
import { ApiError } from "../api/client";
import type { WorkbenchApi } from "../api/modules/workbench";
import type { Attachment, Draft, DraftContent, DraftScope } from "../api/types";

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

/** 标准化省略字段并复制附件元数据；固定字段顺序让发送令牌不受 API 对象键顺序影响。 */
export function normalizeDraft(value: DraftContent): DraftContent {
  return {
    text: value.text,
    filename: value.filename ?? "",
    attachments: (value.attachments ?? []).map(({ name, path, mime, size }) => ({ name, path, mime, size })),
    material_ids: [...value.material_ids ?? []],
  };
}
/** 按契约字段比较，省略的空数组等同空数组；正文不复制成 JSON 字符串。 */
export function sameDraftContent(left: DraftContent, right: DraftContent): boolean {
  if (left === right) return true;
  if (left.text !== right.text || (left.filename ?? "") !== (right.filename ?? "")) return false;
  const leftMaterials = left.material_ids ?? [], rightMaterials = right.material_ids ?? [];
  if (leftMaterials.length !== rightMaterials.length || leftMaterials.some((id, index) => id !== rightMaterials[index])) return false;
  const leftAttachments = left.attachments ?? [], rightAttachments = right.attachments ?? [];
  return leftAttachments.length === rightAttachments.length && leftAttachments.every((attachment, index) => {
    const other = rightAttachments[index]!;
    return attachment.name === other.name && attachment.path === other.path && attachment.mime === other.mime && attachment.size === other.size;
  });
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
  private confirmed: DraftContent;
  // 编辑状态只比较字段；仅发送/保存令牌需要完整且无碰撞的序列化正文。
  private currentFingerprint: string | undefined;
  private editVersion = 0;
  private api: DraftApi | null = null;
  private available = false;
  private loading: Promise<void> | null = null;
  private queue: Promise<unknown> = Promise.resolve();
  private timer: ReturnType<typeof setTimeout> | undefined;
  private disposed = false;
  private operations = 0;
  private heldOperations = 0;

  constructor(private scope: DraftScope, initial: DraftContent, private readonly debounceMs = 600) {
    const content = normalizeDraft(initial);
    this.confirmed = scope.kind === "artifact" ? content : { text: "" };
    this.editVersion = scope.kind === "composer" && contentPresent(content) ? 1 : 0;
    this.snapshot = { content, hydrated: false, status: "loading", dirty: !sameDraftContent(content, this.confirmed), restored: false, error: null, conflict: null, restoreEpoch: 0, baseRevisionId: scope.base_revision_id };
  }
  /** 供 React 外部状态订阅使用，快照在未变化时保持同一引用。 */
  getSnapshot = (): DraftSnapshot => this.snapshot;
  /** 所有订阅都由调用方清理；控制器不持有组件函数。 */
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private emit(update: Partial<DraftSnapshot>) {
    if (this.disposed || !Object.entries(update).some(([key, value]) => this.snapshot[key as keyof DraftSnapshot] !== value)) return;
    if (update.content && update.content !== this.snapshot.content) this.currentFingerprint = undefined;
    this.snapshot = { ...this.snapshot, ...update };
    for (const listener of this.listeners) listener();
  }
  /** 只回收内核已有副本的空闲状态；旧内核、未保存内容和所有操作都必须保留。 */
  canRelease(): boolean {
    return !this.disposed && this.available && this.snapshot.hydrated && !this.snapshot.dirty
      && (this.snapshot.status === "saved" || this.snapshot.status === "idle")
      && !this.snapshot.conflict && !this.snapshot.error && !this.loading && !this.timer
      && this.operations === 0 && this.heldOperations === 0
      && (this.acknowledged !== null || !contentPresent(this.snapshot.content));
  }
  /** 显式用户操作尚未结束时，离开编辑页仍须保护窗口关闭。 */
  hasPendingOperation(): boolean { return this.heldOperations > 0; }
  /** 上传完成后合并来源作用域的最新内容，不用旧界面闭包覆盖重新打开后的输入。 */
  addAttachment(attachment: Attachment) {
    if (this.disposed || this.snapshot.content.attachments?.some((item) => item.path === attachment.path)) return;
    this.setContent({ ...this.snapshot.content, attachments: [...this.snapshot.content.attachments ?? [], attachment] });
    this.emit({ restoreEpoch: this.snapshot.restoreEpoch + 1 });
  }
  /** 发送/保存成果与草稿 API 之间的等待也占有控制器，释放函数可重复调用。 */
  retainOperation(): () => void {
    this.ensureActive();
    this.heldOperations += 1;
    for (const listener of this.listeners) listener();
    let released = false;
    return () => {
      if (released) return;
      released = true;
      this.heldOperations -= 1;
      for (const listener of this.listeners) listener();
    };
  }
  private ensureActive() { if (this.disposed) throw new DOMException("", "AbortError"); }
  private isDirty(content: DraftContent) {
    return !sameDraftContent(content, this.confirmed) || Boolean(this.acknowledged && this.acknowledged.base_revision_id !== this.snapshot.baseRevisionId);
  }
  /** 新版本来源变化不改变成果草稿的唯一作用域。 */
  setScope(scope: DraftScope) { this.scope = scope.kind === "artifact" && this.snapshot.baseRevisionId ? { ...scope, base_revision_id: this.snapshot.baseRevisionId } : scope; }
  /** 仅在用户显式载入新基线或保存新成果版本成功后调用，不能由 latest 查询自动推进。 */
  setBaseRevision(id: string) {
    if (this.disposed) return;
    this.scope = { ...this.scope, base_revision_id: id };
    const dirty = !sameDraftContent(this.snapshot.content, this.confirmed) || Boolean(this.acknowledged && this.acknowledged.base_revision_id !== id);
    this.emit({ baseRevisionId: id, dirty, status: this.available ? "idle" : this.snapshot.status });
    this.schedule();
  }
  /** 能力发现完成前不写草稿，旧 daemon 只保留内存编辑。 */
  configure(api: DraftApi | null, supported: boolean, checking: boolean) {
    if (this.disposed) return;
    this.api = api;
    this.available = supported && api !== null;
    if (checking) return;
    if (!this.available) { this.emit({ hydrated: true, status: "unsupported", dirty: this.isDirty(this.snapshot.content) }); return; }
    if (!this.snapshot.hydrated || this.snapshot.status === "unsupported") void this.hydrate().catch(() => undefined);
  }
  /** 只接受明确的新编辑；服务端响应从不通过本入口回写旧正文。 */
  setContent(input: DraftContent) {
    if (this.disposed || sameDraftContent(input, this.snapshot.content)) return;
    const content = normalizeDraft(input);
    this.editVersion += 1;
    const dirty = this.isDirty(content);
    this.emit({ content, dirty, status: this.snapshot.conflict ? "conflict" : !this.available ? this.snapshot.status : this.snapshot.status === "saving" || !this.snapshot.hydrated ? this.snapshot.status : "idle" });
    this.schedule();
  }
  private schedule() {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
    if (this.disposed || !this.available || !this.snapshot.hydrated || !this.snapshot.dirty || this.snapshot.conflict || this.snapshot.status === "error") return;
    this.timer = setTimeout(() => { this.timer = undefined; void this.save(false).catch(() => undefined); }, this.debounceMs);
  }
  private async findRemote(): Promise<Draft | null> {
    if (!this.api) return null;
    const { kind, agent_id, project_id, session_id, artifact_id } = this.scope;
    for (let offset = 0; ; offset += 200) {
      this.ensureActive();
      const drafts = await this.api.listDrafts({ kind, agent_id, project_id, session_id, artifact_id, limit: 200, offset });
      this.ensureActive();
      const found = drafts.find((draft) => sameScope(draft, this.scope));
      if (found) return found;
      if (drafts.length < 200) return null;
    }
  }
  /** 迟到水合遇到已输入内容时保留双方，要求用户明确选择。 */
  hydrate(): Promise<void> {
    if (this.loading) return this.loading;
    if (this.disposed) return Promise.reject(new DOMException("", "AbortError"));
    if (!this.api || !this.available) return Promise.resolve();
    this.emit({ status: "loading", hydrated: false, error: null });
    this.loading = (async () => {
      try {
        const remote = await this.findRemote();
        this.acknowledged = remote;
        if (remote) {
          const content = normalizeDraft(remote);
          this.confirmed = content;
          if (this.editVersion > 0 && !sameDraftContent(this.snapshot.content, this.confirmed)) {
            this.emit({ hydrated: true, conflict: remote, status: "conflict", dirty: true });
          } else {
            if (remote.base_revision_id) this.scope = { ...this.scope, base_revision_id: remote.base_revision_id };
            this.emit({ content, hydrated: true, status: "saved", dirty: false, restored: contentPresent(content), restoreEpoch: this.snapshot.restoreEpoch + 1, baseRevisionId: remote.base_revision_id ?? this.snapshot.baseRevisionId });
          }
        } else {
          const dirty = !sameDraftContent(this.snapshot.content, this.confirmed);
          this.emit({ hydrated: true, status: "idle", dirty });
          this.schedule();
        }
      } catch (error) { this.emit({ status: "error", error, dirty: contentPresent(this.snapshot.content) }); throw error; }
      finally { this.loading = null; for (const listener of this.listeners) listener(); }
    })();
    return this.loading;
  }
  /** 捕获发送时的已确认版本；调用方应将此令牌与实际发送内容一起保留。 */
  capture(): DraftSaveToken {
    this.currentFingerprint ??= fingerprint(this.snapshot.content);
    return { draftId: this.acknowledged?.id, version: this.acknowledged?.version, editVersion: this.editVersion, fingerprint: this.currentFingerprint };
  }
  private serial<T>(operation: () => Promise<T>): Promise<T> {
    this.operations += 1;
    const next = this.queue.then(() => { this.ensureActive(); return operation(); }).finally(() => {
      this.operations -= 1;
      for (const listener of this.listeners) listener();
    });
    this.queue = next.catch(() => undefined);
    return next;
  }
  private async conflict(error: unknown) {
    if (this.disposed || !this.api) return;
    try {
      const id = ApiError.is(error) ? error.details.existing_draft_id ?? error.details.draft_id : undefined;
      const remote = this.acknowledged ? await this.api.getDraft(this.acknowledged.id) : typeof id === "string" ? await this.api.getDraft(id) : await this.findRemote();
      if (!remote) throw error;
      this.emit({ status: "conflict", error, conflict: remote, dirty: true });
    } catch (readError) { this.emit({ status: "error", error: readError, dirty: true }); }
  }
  /** 立即保存调用瞬间的内容，返回其服务端版本；之后的编辑另行排队。 */
  saveNow(): Promise<DraftSaveToken> { return this.save(true); }
  private save(includeToken: true): Promise<DraftSaveToken>;
  private save(includeToken: false): Promise<void>;
  private save(includeToken: boolean): Promise<DraftSaveToken | void> {
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined; }
    const content = this.snapshot.content;
    // 自动保存没有发送令牌消费者，不额外序列化整段正文或在已完成队列中保留副本。
    const contentFingerprint = includeToken ? this.capture().fingerprint : undefined;
    const editVersion = this.editVersion;
    const scope = { ...this.scope };
    return this.serial(async () => {
      if (!this.available || !this.api) return includeToken ? { draftId: this.acknowledged?.id, version: this.acknowledged?.version, editVersion, fingerprint: contentFingerprint! } : undefined;
      if (!this.snapshot.hydrated) {
        await this.hydrate();
        this.ensureActive();
        if (editVersion === 0 && this.snapshot.restored) return includeToken ? this.capture() : undefined;
      }
      if (this.snapshot.conflict) throw new ApiError("conflict", "", { details: { reason: "stale_version" } });
      if ((sameDraftContent(content, this.confirmed) && (!this.acknowledged || this.acknowledged.base_revision_id === scope.base_revision_id)) || (!this.acknowledged && !contentPresent(content))) {
        return includeToken ? { draftId: this.acknowledged?.id, version: this.acknowledged?.version, editVersion, fingerprint: contentFingerprint! } : undefined;
      }
      this.emit({ status: "saving", error: null });
      try {
        const saved = this.acknowledged
          ? await this.api.updateDraft(this.acknowledged.id, { ...scope, ...content, expected_version: this.acknowledged.version })
          : await this.api.createDraft({ ...scope, ...content });
        this.ensureActive();
        // 已解码响应可能持有另一份等值大字符串；等值时复用提交正文，保留其余服务端字段。
        this.acknowledged = saved.text === content.text ? { ...saved, text: content.text } : saved;
        this.confirmed = content;
        const dirty = !sameDraftContent(this.snapshot.content, this.confirmed) || this.snapshot.baseRevisionId !== saved.base_revision_id;
        this.emit({ status: dirty ? "idle" : "saved", dirty, error: null });
        this.schedule();
        return includeToken ? { draftId: saved.id, version: saved.version, editVersion, fingerprint: contentFingerprint! } : undefined;
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
      this.ensureActive();
      this.acknowledged = null;
      const unchanged = this.editVersion === token.editVersion;
      const content = unchanged && this.scope.kind === "composer" ? normalizeDraft({ text: "" }) : this.snapshot.content;
      this.confirmed = unchanged ? content : { text: "" };
      this.emit({ content, status: this.available ? "idle" : "unsupported", dirty: !unchanged && contentPresent(content), restored: false, error: null, restoreEpoch: unchanged && this.scope.kind === "composer" ? this.snapshot.restoreEpoch + 1 : this.snapshot.restoreEpoch });
      this.schedule();
    });
  }
  /** 用户明确选择当前版本或远端版本；继续使用当前版本时仍以远端 version 做 CAS。 */
  async resolveConflict(choice: "local" | "remote"): Promise<void> {
    this.ensureActive();
    const remote = this.snapshot.conflict;
    if (!remote) return;
    this.acknowledged = remote;
    this.confirmed = normalizeDraft(remote);
    if (choice === "remote") {
      this.editVersion += 1;
      if (remote.base_revision_id) this.scope = { ...this.scope, base_revision_id: remote.base_revision_id };
      this.emit({ content: this.confirmed, status: "saved", conflict: null, error: null, dirty: false, restoreEpoch: this.snapshot.restoreEpoch + 1, baseRevisionId: remote.base_revision_id ?? this.snapshot.baseRevisionId });
    } else {
      this.emit({ conflict: null, error: null, status: "idle", dirty: true });
      await this.save(false);
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
      this.ensureActive();
      this.acknowledged = null;
      const changed = this.editVersion !== discardedVersion;
      this.confirmed = { text: "" };
      if (!changed) this.editVersion += 1;
      this.emit({ content: changed ? this.snapshot.content : normalizeDraft({ text: "" }), status: this.available ? "idle" : "unsupported", dirty: changed && contentPresent(this.snapshot.content), restored: false, error: null, conflict: null, restoreEpoch: changed ? this.snapshot.restoreEpoch : this.snapshot.restoreEpoch + 1 });
      this.schedule();
    });
  }
  /** 连接或组件树销毁时释放计时器，不把内容写入客户端存储。 */
  dispose() { this.disposed = true; if (this.timer) clearTimeout(this.timer); this.timer = undefined; this.api = null; this.available = false; this.listeners.clear(); }
}
