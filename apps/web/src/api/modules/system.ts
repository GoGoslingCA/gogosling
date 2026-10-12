import type { ApiClient } from "../client";
import type { VersionInfo, ResourceState } from "../types";

/** 系统接口：版本与运行状态、存活检查。 */
export function systemApi(client: ApiClient) {
  return {
    version: (): Promise<VersionInfo> => client.get<VersionInfo>("/api/v1/version"),
    resources: (): Promise<ResourceState> => client.get<ResourceState>("/api/v1/resources"),
    setResources: (max_parallel: number): Promise<ResourceState> => client.request("PUT", "/api/v1/resources", { body: { max_parallel } }),
    /** 免鉴权的存活检查；用于连接页在输入令牌前确认地址可达。 */
    health: (): Promise<{ status: string }> => client.get<{ status: string }>("/healthz"),
  };
}

export type SystemApi = ReturnType<typeof systemApi>;
