import type { ApiClient } from "../client";
import type { ApprovalRequest, ApprovalStatus, Decision, ListResponse, PageQuery } from "../types";

/** /api/v1/approvals */
export function approvalsApi(client: ApiClient) {
  return {
    list: async (status: ApprovalStatus = "pending", page: PageQuery = {}): Promise<ApprovalRequest[]> =>
      (await client.get<ListResponse<ApprovalRequest>>("/api/v1/approvals", { query: { status, ...page } })).items ?? [],
    decide: (id: string, decision: Decision): Promise<ApprovalRequest> =>
      client.post<ApprovalRequest>(`/api/v1/approvals/${encodeURIComponent(id)}/decision`, decision),
  };
}

export type ApprovalsApi = ReturnType<typeof approvalsApi>;
