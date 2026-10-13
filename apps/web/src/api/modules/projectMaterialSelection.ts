// 新任务仅保留第一页候选资料和显式引用；向后查找有硬上限，不能把扫描上限当成资料不存在。
import type { ProjectMaterial } from "../types";
import type { WorkbenchApi } from "./workbench";

export const MATERIAL_SELECTION_LIMIT = 32;
export const MATERIAL_LOOKUP_PAGE_SIZE = 200;
export const MATERIAL_LOOKUP_SCAN_LIMIT = 5000;

export interface ProjectMaterialSelection {
  items: ProjectMaterial[];
  missingIds: string[];
  unresolvedIds: string[];
  issue?: "too_many" | "scan_limit";
}

/** 按需读取所选资料元数据；取消后停止翻页，不缓存中间页或读取文件正文。 */
export async function readProjectMaterialSelection(api: Pick<WorkbenchApi, "listMaterials">, projectId: string, selectedIds: readonly string[], signal?: AbortSignal): Promise<ProjectMaterialSelection> {
  const wanted = new Set(selectedIds);
  if (wanted.size > MATERIAL_SELECTION_LIMIT) return { items: [], missingIds: [], unresolvedIds: [], issue: "too_many" };
  const kept = new Map<string, ProjectMaterial>();
  for (let offset = 0; offset < MATERIAL_LOOKUP_SCAN_LIMIT; offset += MATERIAL_LOOKUP_PAGE_SIZE) {
    signal?.throwIfAborted();
    const page = await api.listMaterials(projectId, { offset, limit: MATERIAL_LOOKUP_PAGE_SIZE }, signal);
    signal?.throwIfAborted();
    for (const item of page) {
      if (offset === 0 || wanted.has(item.id)) kept.set(item.id, item);
      wanted.delete(item.id);
    }
    if (!wanted.size) return { items: [...kept.values()], missingIds: [], unresolvedIds: [] };
    if (page.length < MATERIAL_LOOKUP_PAGE_SIZE) return { items: [...kept.values()], missingIds: [...wanted], unresolvedIds: [] };
  }
  return { items: [...kept.values()], missingIds: [], unresolvedIds: [...wanted], issue: "scan_limit" };
}
