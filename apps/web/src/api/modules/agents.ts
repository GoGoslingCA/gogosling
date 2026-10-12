import type { ApiClient } from "../client";
import type { Agent, AgentInput, ListResponse, ToolSpec } from "../types";

/** /api/v1/agents */
export function agentsApi(client: ApiClient) {
  return {
    enhancePrompt: (id: string, text: string, signal?: AbortSignal, revision?: { draft: string; feedback: string }): Promise<{ text: string }> => client.post(`/api/v1/agents/${encodeURIComponent(id)}/prompt-enhancement`, { text, ...revision }, { signal }),
    list: async (): Promise<Agent[]> => (await client.get<ListResponse<Agent>>("/api/v1/agents")).items ?? [],
    get: (id: string): Promise<Agent> => client.get<Agent>(`/api/v1/agents/${encodeURIComponent(id)}`),
    create: (input: AgentInput): Promise<Agent> => client.post<Agent>("/api/v1/agents", input),
    update: (id: string, input: AgentInput): Promise<Agent> => client.patch<Agent>(`/api/v1/agents/${encodeURIComponent(id)}`, input),
    remove: (id: string): Promise<void> => client.delete(`/api/v1/agents/${encodeURIComponent(id)}`),
    /** 该智能体当前可用的工具声明（内置 + 已启用扩展）。 */
    tools: async (id: string): Promise<ToolSpec[]> =>
      (await client.get<ListResponse<ToolSpec>>(`/api/v1/agents/${encodeURIComponent(id)}/tools`)).items ?? [],
  };
}

export type AgentsApi = ReturnType<typeof agentsApi>;
