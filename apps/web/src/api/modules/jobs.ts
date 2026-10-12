import type { ApiClient } from "../client";
import type { Job, JobInput, ListResponse } from "../types";

/** /api/v1/jobs */
export function jobsApi(client: ApiClient) {
  return {
    list: async (agentId?: string): Promise<Job[]> =>
      (await client.get<ListResponse<Job>>("/api/v1/jobs", { query: { agent_id: agentId } })).items ?? [],
    get: (id: string): Promise<Job> => client.get<Job>(`/api/v1/jobs/${encodeURIComponent(id)}`),
    create: (input: JobInput): Promise<Job> => client.post<Job>("/api/v1/jobs", input),
    update: (id: string, input: JobInput): Promise<Job> => client.patch<Job>(`/api/v1/jobs/${encodeURIComponent(id)}`, input),
    remove: (id: string): Promise<void> => client.delete(`/api/v1/jobs/${encodeURIComponent(id)}`),
    /** 立即触发一次（202，无响应体）。 */
    runNow: (id: string): Promise<void> => client.post<void>(`/api/v1/jobs/${encodeURIComponent(id)}/run`),
  };
}

export type JobsApi = ReturnType<typeof jobsApi>;
