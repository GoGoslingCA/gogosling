// 审计只读 API：过滤字段与 api/openapi/openapi.yaml 的 GET /audit 一致。

import type { ApiClient } from "../client";
import type { AuditEntry, ListResponse, PageQuery } from "../types";

export interface AuditQuery extends PageQuery {
  agent_id?: string;
  session_id?: string;
  run_id?: string;
  actor?: string;
  action?: string;
  /** RFC 3339 时间；服务端按此时间之后的记录过滤。 */
  since?: string;
}

/** /api/v1/audit，按时间降序返回；无修改或删除操作。 */
export function auditApi(client: ApiClient) {
  return {
    list: async (query: AuditQuery = {}, signal?: AbortSignal): Promise<AuditEntry[]> =>
      (await client.get<ListResponse<AuditEntry>>("/api/v1/audit", { query: { ...query }, signal })).items ?? [],
  };
}

export type AuditApi = ReturnType<typeof auditApi>;
