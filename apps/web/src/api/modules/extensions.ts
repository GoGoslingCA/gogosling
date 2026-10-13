import type { ApiClient } from "../client";
import type { Extension, ExtensionInstallInput, ExtensionKind, ExtensionStatus, ListResponse } from "../types";

/** /api/v1/extensions */
export function extensionsApi(client: ApiClient) {
  const path = (id: string) => `/api/v1/extensions/${encodeURIComponent(id)}`;
  return {
    list: async (kind?: ExtensionKind, signal?: AbortSignal): Promise<Extension[]> =>
      (await client.get<ListResponse<Extension>>("/api/v1/extensions", { query: { kind }, signal })).items ?? [],
    get: (id: string): Promise<Extension> => client.get<Extension>(path(id)),
    /** 从本地目录安装技能/插件，或注册一个 MCP 服务。 */
    install: (input: ExtensionInstallInput): Promise<Extension> => client.post<Extension>("/api/v1/extensions", input),
    setEnabled: (id: string, enabled: boolean): Promise<Extension> => client.patch<Extension>(path(id), { enabled }),
    uninstall: (id: string): Promise<void> => client.delete(path(id)),
    /** 运行时状态：MCP 是否连上、工具数、工具声明是否变化（tools_changed）、最近一次错误。 */
    status: (id: string, signal?: AbortSignal): Promise<ExtensionStatus> => client.get<ExtensionStatus>(`${path(id)}/status`, { signal }),
    /** 确认并重新固定工具声明（tools_changed 之后调用），返回新的状态。 */
    acceptTools: (id: string): Promise<ExtensionStatus> => client.post<ExtensionStatus>(`${path(id)}/accept-tools`),
  };
}

export type ExtensionsApi = ReturnType<typeof extensionsApi>;
