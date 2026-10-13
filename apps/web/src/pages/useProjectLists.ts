// 每次只读取当前项目标签；分页缓存挂在既有失效键下，切换页面立即取消未完成的读取。
import { useEffect } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";

export const PROJECT_PAGE_SIZE = 50;
export type ProjectTab = "materials" | "tasks" | "artifacts";

/** 去除翻页时由新增或删除造成的重复项，保持服务端返回顺序。 */
export function projectPageItems<T extends { id: string }>(pages: T[][] | undefined): T[] {
  const items = new Map<string, T>();
  for (const page of pages ?? []) for (const item of page) if (!items.has(item.id)) items.set(item.id, item);
  return [...items.values()];
}

/** 标签页使用专属分页键，不改变其他调用方单页缓存的数据形状。 */
export function useProjectLists(projectId: string | undefined, tab: ProjectTab, caps: { projects: boolean; materials: boolean; artifacts: boolean }) {
  const api = useApi();
  const client = useQueryClient();
  const id = projectId ?? "";
  const keys = {
    materials: [...queryKeys.materials(id), "pages"],
    tasks: [...queryKeys.sessionsRoot, "project", id, "pages"],
    artifacts: [...queryKeys.artifacts, "project", id, "pages"],
  };
  const nextPage = <T,>(last: T[], pages: T[][]) => last.length === PROJECT_PAGE_SIZE ? pages.length * PROJECT_PAGE_SIZE : undefined;
  const materials = useInfiniteQuery({
    queryKey: keys.materials, initialPageParam: 0, getNextPageParam: nextPage,
    queryFn: ({ pageParam, signal }) => api.workbench.listMaterials(id, { limit: PROJECT_PAGE_SIZE, offset: pageParam }, signal),
    enabled: caps.materials && !!id && tab === "materials", staleTime: 30_000,
  });
  const sessions = useInfiniteQuery({
    queryKey: keys.tasks, initialPageParam: 0, getNextPageParam: nextPage,
    queryFn: ({ pageParam, signal }) => api.sessions.list({ project_id: id, limit: PROJECT_PAGE_SIZE, offset: pageParam }, signal),
    enabled: caps.projects && !!id && tab === "tasks", staleTime: 30_000,
  });
  const artifacts = useInfiniteQuery({
    queryKey: keys.artifacts, initialPageParam: 0, getNextPageParam: nextPage,
    queryFn: ({ pageParam, signal }) => api.workbench.listArtifacts({ project_id: id, limit: PROJECT_PAGE_SIZE, offset: pageParam }, signal),
    enabled: caps.artifacts && !!id && tab === "artifacts", staleTime: 30_000,
  });
  useEffect(() => {
    const key = tab === "materials" ? [...queryKeys.materials(id), "pages"] : tab === "tasks" ? [...queryKeys.sessionsRoot, "project", id, "pages"] : [...queryKeys.artifacts, "project", id, "pages"];
    return () => { void client.cancelQueries({ queryKey: key, exact: true }); };
  }, [client, id, tab]);
  return { materials, sessions, artifacts };
}
