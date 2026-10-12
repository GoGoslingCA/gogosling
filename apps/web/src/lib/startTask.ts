// 开始任务的客户端编排。每次启动保留已创建会话与已上传文件，失败后不重复创建。
import type { Attachment, DraftContent, Run, Session } from "../api/types";
import type { SessionsApi } from "../api/modules/sessions";
import { ApiError } from "../api/client";

export type StartStage = "creating" | "uploading" | "sending";
export interface StartTaskInput { agentId: string; text: string; files: File[]; projectId?: string; materialIds?: string[]; attachments?: Attachment[] }
export interface StartTaskResult { session: Session; sent: boolean; run?: Run }

/** 发送结果未知时进入已有任务核对，不通过重复发送来猜测成功与否。 */
export class StartOutcomeUnknown extends Error {
  constructor(public readonly session: Session | null) { super("start_outcome_unknown"); }
}

/** 一次开始操作；实例由界面持有，重复调用复用进度，不持久化正文或凭据。 */
export class TaskStarter {
  private session: Session | null = null;
  private uploaded = new Map<File, { attachment: Attachment; materialId?: string }>();
  private running = false;
  private sent = false;
  private run: Run | undefined;
  private unknown = false;

  constructor(private readonly sessions: Pick<SessionsApi, "create" | "upload" | "send">, private readonly uploadMaterial?: (projectId: string, file: File) => Promise<{ id: string; attachment: Attachment }>, private readonly checkpoint?: (session: Session, content: DraftContent) => Promise<void>) {}

  /** 若有已创建的任务，失败后可直接打开它。 */
  get createdSession(): Session | null { return this.session; }

  /** 串行执行创建、上传与发送；只在明确未受理的失败后允许继续发送。 */
  async start(input: StartTaskInput, onStage: (stage: StartStage) => void): Promise<StartTaskResult> {
    if (this.running) throw new Error("start_in_progress");
    if (this.session && (this.session.agent_id !== input.agentId || (this.session.project_id ?? "") !== (input.projectId ?? ""))) throw new Error("start_agent_changed");
    if (this.unknown) throw new StartOutcomeUnknown(this.session);
    if (this.session && this.sent) return { session: this.session, sent: true, run: this.run };
    this.running = true;
    try {
      if (!this.session) {
        onStage("creating");
        try {
          this.session = await this.sessions.create({ agent_id: input.agentId, title: taskTitle(input.text) || input.files[0]?.name || undefined, ...(input.projectId ? { project_id: input.projectId } : {}) });
        } catch (error) {
          if (!ApiError.is(error) || ["network", "timeout", "aborted", "internal", "unavailable"].includes(error.code)) {
            this.unknown = true;
            throw new StartOutcomeUnknown(null);
          }
          throw error;
        }
      }
      const session = this.session;
      if (!input.text.trim() && !input.files.length && !input.materialIds?.length && !input.attachments?.length) return { session, sent: false };
      const attachments: Attachment[] = [...input.attachments ?? []];
      const materialIds = new Set(input.materialIds ?? []);
      const checkpoint = () => this.checkpoint?.(session, { text: input.text.trim(), attachments, material_ids: [...materialIds] });
      await checkpoint();
      for (const file of input.files) {
        let stored = this.uploaded.get(file);
        if (!stored) {
          onStage("uploading");
          if (input.projectId && this.uploadMaterial) {
            const material = await this.uploadMaterial(input.projectId, file);
            stored = { attachment: material.attachment, materialId: material.id };
          } else stored = { attachment: await this.sessions.upload(session.id, file) };
          this.uploaded.set(file, stored);
        }
        if (stored.materialId) materialIds.add(stored.materialId);
        else attachments.push(stored.attachment);
        await checkpoint();
      }
      onStage("sending");
      try {
        this.run = await this.sessions.send(session.id, { text: input.text.trim(), attachments: attachments.length ? attachments : undefined, ...(materialIds.size ? { material_ids: [...materialIds] } : {}) });
      } catch (error) {
        // 网关超时或连接断开不能证明内核没有受理，必须由任务历史核对。
        if (!ApiError.is(error) || ["network", "timeout", "aborted", "internal", "unavailable"].includes(error.code)) {
          this.unknown = true;
          throw new StartOutcomeUnknown(session);
        }
        throw error;
      }
      this.sent = true;
      return { session, sent: true, run: this.run };
    } finally { this.running = false; }
  }
}

/** 标题取首个非空行并限定可读长度；完整目标保留在原始用户消息。 */
export function taskTitle(text: string): string {
  const first = text.split(/\r?\n/).map((line) => line.trim()).find(Boolean) ?? "";
  return Array.from(first).slice(0, 40).join("");
}
