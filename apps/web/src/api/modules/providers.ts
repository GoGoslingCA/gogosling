import type { ApiClient } from "../client";
import type { ListResponse, Provider, ProviderInput, ProviderTestResult } from "../types";

/** /api/v1/providers。api_key 只写：创建/更新时传入，响应里永远只有 has_secret。 */
export function providersApi(client: ApiClient) {
  return {
    list: async (): Promise<Provider[]> => (await client.get<ListResponse<Provider>>("/api/v1/providers")).items ?? [],
    get: (id: string): Promise<Provider> => client.get<Provider>(`/api/v1/providers/${encodeURIComponent(id)}`),
    create: (input: ProviderInput): Promise<Provider> => client.post<Provider>("/api/v1/providers", input),
    update: (id: string, input: ProviderInput): Promise<Provider> =>
      client.patch<Provider>(`/api/v1/providers/${encodeURIComponent(id)}`, input),
    remove: (id: string): Promise<void> => client.delete(`/api/v1/providers/${encodeURIComponent(id)}`),
    /** 连通性测试；成功时服务端会刷新 models。 */
    test: (id: string): Promise<ProviderTestResult> => client.post<ProviderTestResult>(`/api/v1/providers/${encodeURIComponent(id)}/test`),
  };
}

export type ProvidersApi = ReturnType<typeof providersApi>;
