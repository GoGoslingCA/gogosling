// 契约来源：api/openapi/openapi.yaml，文件预览与原始下载均经鉴权客户端。
import type { ApiClient } from "../client";
import type { FileEntry, PreviewDocument } from "../../content/types";
export function previewApi(client: ApiClient) {
  const root = (id: string) => `/api/v1/agents/${encodeURIComponent(id)}`;
  return {
    list: (id: string, path: string, signal?: AbortSignal) => client.get<{items: FileEntry[]; truncated: boolean}>(`${root(id)}/files`, {query:{path},signal}),
    read: (id: string, path: string, signal?: AbortSignal) => client.get<PreviewDocument>(`${root(id)}/file-preview`, {query:{path},signal}),
    content: (id: string, path: string, version?: string, signal?: AbortSignal, layout = false) => client.get<Blob>(`${root(id)}/${layout ? "file-layout" : "file-content"}`, {query:{path,version},signal,responseType:"blob"}),
  };
}
