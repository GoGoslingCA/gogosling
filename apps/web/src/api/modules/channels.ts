// IM 通道管理：字段按 api/openapi/openapi.yaml 的 Channel / ChannelInput 手写。

import type { ApiClient } from "../client";
import type { Channel, ChannelInput, ChannelPatch, ListResponse } from "../types";

/** /api/v1/channels。secret 只写，读取结果只含 has_secret。 */
export function channelsApi(client: ApiClient) {
  const base = "/api/v1/channels";
  const path = (id: string) => `${base}/${encodeURIComponent(id)}`;
  return {
    list: async (agentId?: string, signal?: AbortSignal): Promise<Channel[]> =>
      (await client.get<ListResponse<Channel>>(base, { query: { agent_id: agentId }, signal })).items ?? [],
    get: (id: string): Promise<Channel> => client.get<Channel>(path(id)),
    create: (input: ChannelInput): Promise<Channel> => client.post<Channel>(base, input),
    update: (id: string, input: ChannelPatch): Promise<Channel> => client.patch<Channel>(path(id), input),
    remove: (id: string): Promise<void> => client.delete(path(id)),
    /** 重启适配器，并返回包含运行状态的通道。 */
    reload: (id: string): Promise<Channel> => client.post<Channel>(`${path(id)}/reload`),
  };
}

export type ChannelsApi = ReturnType<typeof channelsApi>;
