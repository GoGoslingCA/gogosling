// 旧守护进程省略 capabilities 时保持会话与附件流程，不试探不存在的写接口。
import { useQuery } from "@tanstack/react-query";
import { useApi } from "./ConnectionContext";
import { queryKeys } from "./queryKeys";

/** 只接受服务端声明的完整 P3A 能力；查询失败时也不启用写入口。 */
export function useWorkbenchCapabilities() {
  const api = useApi();
  const version = useQuery({ queryKey: queryKeys.version, queryFn: () => api.system.version(), staleTime: 60_000, retry: false });
  const capabilities = version.error ? undefined : version.data?.capabilities;
  return {
    readDocument: capabilities?.["tools.read_document"] === 1,
    preview: capabilities?.["workbench.preview"] === 1,
    previewLayout: capabilities?.["workbench.preview_layout"] === 1,
    enhancePrompt: capabilities?.["prompt.enhance"] === 1,
    projects: capabilities?.["workbench.projects"] === 1,
    materials: capabilities?.["workbench.materials"] === 1,
    drafts: capabilities?.["workbench.drafts"] === 1,
    artifacts: capabilities?.["workbench.artifacts"] === 1,
    changes: capabilities?.["workbench.changesets"] === 1,
    textBytes: capabilities?.["workbench.limits"]?.text_bytes ?? 524288,
    loading: version.isPending,
    error: version.error,
  };
}
