import type { ApiClient } from "../client";
import type { Run } from "../types";

/** /api/v1/runs */
export function runsApi(client: ApiClient) {
  return {
    get: (id: string): Promise<Run> => client.get<Run>(`/api/v1/runs/${encodeURIComponent(id)}`),
    /** 取消运行（幂等；已终态时返回当前状态）。 */
    cancel: (id: string): Promise<Run> => client.post<Run>(`/api/v1/runs/${encodeURIComponent(id)}/cancel`),
  };
}

export type RunsApi = ReturnType<typeof runsApi>;
