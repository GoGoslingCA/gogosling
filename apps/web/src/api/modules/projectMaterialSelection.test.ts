import { describe, expect, it, vi } from "vitest";
import type { ProjectMaterial } from "../types";
import { MATERIAL_LOOKUP_SCAN_LIMIT, readProjectMaterialSelection } from "./projectMaterialSelection";

function material(index: number): ProjectMaterial { return { id: `m${index}`, project_id: "p", agent_id: "a", name: `${index}.md`, size: 1, sha256: "digest", attachment: { name: `${index}.md`, path: `inbound/${index}.md`, size: 1 }, created_at: "" }; }
function fixture(count: number) {
  return { listMaterials: vi.fn(async (_id: string, page = { offset: 0, limit: 200 }, _signal?: AbortSignal) => Array.from({ length: Math.max(0, Math.min(page.limit, count - page.offset)) }, (_, index) => material(page.offset + index + 1))) };
}

describe("项目所选资料的有界元数据查找", () => {
  it("未选择资料只读第一页，所选第201份资料额外读取一页且不保留其余候选", async () => {
    const api = fixture(800);
    expect((await readProjectMaterialSelection(api, "p", [])).items).toHaveLength(200);
    expect(api.listMaterials).toHaveBeenCalledTimes(1);
    api.listMaterials.mockClear();
    const result = await readProjectMaterialSelection(api, "p", ["m201"]);
    expect(api.listMaterials).toHaveBeenCalledTimes(2);
    expect(result.items).toHaveLength(201);
    expect(result.items.at(-1)?.id).toBe("m201");
    expect(result.items.some((item) => item.id === "m202")).toBe(false);
    expect(result.missingIds).toEqual([]);
  });
  it("只有遍历到真实列表末尾才判定失效引用，重复引用不重复计数", async () => {
    const api = fixture(201);
    const result = await readProjectMaterialSelection(api, "p", ["m201", "missing", "m201"]);
    expect(result.missingIds).toEqual(["missing"]);
    expect(result.issue).toBeUndefined();
    expect(result.items.filter((item) => item.id === "m201")).toHaveLength(1);
  });
  it("最多检查5000份资料，上限不能被错误显示为资料已删除", async () => {
    const api = fixture(8000);
    const result = await readProjectMaterialSelection(api, "p", ["m7999"]);
    expect(api.listMaterials).toHaveBeenCalledTimes(MATERIAL_LOOKUP_SCAN_LIMIT / 200);
    expect(result).toMatchObject({ issue: "scan_limit", missingIds: [], unresolvedIds: ["m7999"] });
    expect(result.items).toHaveLength(200);
  });
  it("超过32份引用不访问网络，也不静默截掉用户选择", async () => {
    const api = fixture(100);
    const result = await readProjectMaterialSelection(api, "p", Array.from({ length: 33 }, (_, index) => `m${index}`));
    expect(result.issue).toBe("too_many");
    expect(api.listMaterials).not.toHaveBeenCalled();
  });
  it("取消首个请求后不继续翻页或返回过期资料", async () => {
    const controller = new AbortController();
    const api = fixture(500);
    api.listMaterials.mockImplementationOnce(async () => { controller.abort(); return Array.from({ length: 200 }, (_, index) => material(index + 1)); });
    await expect(readProjectMaterialSelection(api, "p", ["m201"], controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(api.listMaterials).toHaveBeenCalledTimes(1);
  });
});
