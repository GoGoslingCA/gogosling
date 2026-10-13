import type { ApiClient } from "../client";
import type {
  Attachment,
  ListResponse,
  Message,
  PageQuery,
  Run,
  SendMessageInput,
  Session,
  SessionCreateInput,
  SessionPatchInput,
  SessionStatus,
} from "../types";

export interface SessionListQuery extends PageQuery {
  agent_id?: string;
  project_id?: string;
  status?: SessionStatus;
}

/** 单页消息上限（契约 limit 最大 500）。 */
export const MESSAGE_PAGE_LIMIT = 200;

/** 一次会话最多向前拉取的消息数，防止异常会话把内存撑爆。 */
export const MESSAGE_FETCH_CAP = 5000;

/** /api/v1/sessions 及其消息、附件。 */
export function sessionsApi(client: ApiClient) {
  const base = "/api/v1/sessions";
  const listMessages = async (id: string, afterSeq: number, limit = MESSAGE_PAGE_LIMIT): Promise<Message[]> =>
    (
      await client.get<ListResponse<Message>>(`${base}/${encodeURIComponent(id)}/messages`, {
        query: { after_seq: afterSeq, limit },
      })
    ).items ?? [];

  return {
    list: async (query: SessionListQuery = {}, signal?: AbortSignal): Promise<Session[]> =>
      (await client.get<ListResponse<Session>>(base, { query: { ...query }, signal })).items ?? [],
    get: (id: string): Promise<Session> => client.get<Session>(`${base}/${encodeURIComponent(id)}`),
    create: (input: SessionCreateInput): Promise<Session> => client.post<Session>(base, input),
    update: (id: string, input: SessionPatchInput): Promise<Session> => client.patch<Session>(`${base}/${encodeURIComponent(id)}`, input),
    remove: (id: string): Promise<void> => client.delete(`${base}/${encodeURIComponent(id)}`),
    /** 读取 seq 大于 afterSeq 的一页消息（升序）。 */
    listMessages,
    /** 从 afterSeq 之后持续翻页直到读完（或触及上限）。 */
    listMessagesAfter: async (id: string, afterSeq: number): Promise<Message[]> => {
      const out: Message[] = [];
      let cursor = afterSeq;
      while (out.length < MESSAGE_FETCH_CAP) {
        const page = await listMessages(id, cursor);
        if (page.length === 0) break;
        out.push(...page);
        const last = page[page.length - 1];
        if (!last || page.length < MESSAGE_PAGE_LIMIT) break;
        cursor = last.seq;
      }
      return out;
    },
    /** 发送用户消息并发起一次运行；同一会话有未结束运行时服务端返回 conflict。 */
    send: (id: string, input: SendMessageInput): Promise<Run> => client.post<Run>(`${base}/${encodeURIComponent(id)}/messages`, input),
    /** 上传附件到工作区 inbound/ 目录。 */
    upload: (id: string, file: File): Promise<Attachment> => {
      const form = new FormData();
      form.append("file", file, file.name);
      return client.post<Attachment>(`${base}/${encodeURIComponent(id)}/attachments`, form);
    },
  };
}

export type SessionsApi = ReturnType<typeof sessionsApi>;
