import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ChangeDiff, ChangeInput, ChangeSet } from "../../api/types";
import { ApiError } from "../../api/client";
import { queryKeys } from "../../state/queryKeys";
import { FileChangePanel } from "./FileChangePanel";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => api }));
const date = "2026-10-12T00:00:00Z";
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let props: Parameters<typeof FileChangePanel>[0];
let api: ReturnType<typeof fixture>;
let changes: Map<string, ChangeSet>;
let bodies: Map<string, { before: string; after: string }>;
let sequence: number;
function change(fields: Partial<ChangeSet> = {}): ChangeSet {
  return { id: "change-1", owner_id: "local", agent_id: "agent-a", session_id: "session-a", artifact_id: "artifact-a", revision_id: "revision-2", target_path: "src/example.ts", operation: "create", status: "prepared", version: 1, preview_digest: "preview-digest", base_exists: false, base_sha256: "before", proposed_sha256: "after", proposed_size: 8, created_at: date, expires_at: new Date(Date.now() + 900_000).toISOString(), ...fields };
}
function seed(value: ChangeSet, before = "", after = "new line") { changes.set(value.id, value); bodies.set(value.id, { before, after }); return value; }
function transition(value: ChangeSet, status: ChangeSet["status"]) { const next = { ...value, status, version: value.version + 1 }; changes.set(next.id, next); return next; }
function fixture() {
  return { workbench: {
    listChanges: vi.fn(async (_id: string) => [...changes.values()].map((item) => ({ ...item }))),
    getChange: vi.fn(async (id: string) => ({ ...changes.get(id)! })),
    changeDiff: vi.fn(async (id: string): Promise<ChangeDiff> => ({ change: { ...changes.get(id)! }, ...bodies.get(id)! })),
    prepareChange: vi.fn(async (sessionId: string, input: ChangeInput) => seed(change({ ...input, session_id: sessionId, id: `change-${++sequence}` }))),
    applyChange: vi.fn(async (value: ChangeSet, _key: string) => transition({ ...value, operation_id: `operation-${value.id}` }, "awaiting_approval")),
    undoChange: vi.fn(async (value: ChangeSet) => seed(change({ id: `undo-${++sequence}`, operation: value.operation === "create" ? "delete" : "replace", undo_of: value.id, target_path: value.target_path, base_exists: true }), bodies.get(value.id)!.after, bodies.get(value.id)!.before)),
    cancelChange: vi.fn(async (value: ChangeSet) => transition(value, "cancelled")),
    reconcileChange: vi.fn(async (value: ChangeSet) => transition(value, "applied")),
  } };
}
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  changes = new Map(); bodies = new Map(); sequence = 0; api = fixture();
  props = { artifactId: "artifact-a", sessionId: "session-a", revisionId: "revision-2", revisionNumber: 2, modified: false, visible: true, onEdit: vi.fn() };
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); vi.restoreAllMocks(); });
async function ready(assertion: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { interval: 10, timeout: 2500 }); }
async function render() { await act(async () => root.render(<QueryClientProvider client={client}><FileChangePanel {...props} /></QueryClientProvider>)); }
function button(label: string) { const found = [...host.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === label); if (!found) throw new Error(`未找到按钮 ${label}`); return found; }
function status() { return host.querySelector(".file-change-status strong")?.textContent; }
async function typeTarget(value: string) { await act(async () => { const input = host.querySelector<HTMLInputElement>('.file-change-form input:not([type="radio"])')!; Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value); input.dispatchEvent(new Event("input", { bubbles: true })); }); }
async function preview() { await render(); await typeTarget("src/example.ts"); await act(async () => button("changes.prepare").click()); await ready(() => expect(button("changes.requestApply").disabled).toBe(false)); }

describe("文件修改预览和审批状态", () => {
  it("过期撤销必须从原操作重新生成撤销预览，不能变成普通新建", async () => {
    const original = seed(change({ status: "applied", version: 5 }));
    seed(change({ id: "undo-old", created_at: "2026-10-12T01:00:00Z", undo_of: original.id, operation: "delete", expires_at: "2000-01-01T00:00:00Z" }));
    await render(); await ready(() => expect(status()).toBe("changes.status_expired"));
    expect(button("changes.requestDelete").disabled).toBe(true);
    await act(async () => button("changes.previewAgain").click());
    await ready(() => expect(api.workbench.undoChange).toHaveBeenCalledWith(original));
    expect(api.workbench.prepareChange).not.toHaveBeenCalled();
    expect(api.workbench.applyChange).not.toHaveBeenCalled();
  });
  it("预览只提交保存版本和目标，申请受理仍等待审批，只有GET终态才显示已应用", async () => {
    await preview();
    expect(api.workbench.prepareChange).toHaveBeenCalledWith("session-a", { artifact_id: "artifact-a", revision_id: "revision-2", target_path: "src/example.ts", operation: "create" });
    expect(api.workbench.applyChange).not.toHaveBeenCalled();
    expect(status()).toBe("changes.status_prepared");
    await act(async () => { button("changes.requestApply").click(); button("changes.requestApply").click(); });
    await ready(() => expect(status()).toBe("changes.status_awaiting_approval"));
    expect(api.workbench.applyChange).toHaveBeenCalledOnce();
    expect(host.querySelectorAll('.file-change-trail li[data-state="done"]')).toHaveLength(1);
    const current = changes.get("change-1")!; transition(current, "applied");
    await act(async () => { await client.invalidateQueries({ queryKey: queryKeys.change(current.id) }); });
    await ready(() => expect(status()).toBe("changes.status_applied"));
    expect(host.querySelectorAll('.file-change-trail li[data-state="done"]')).toHaveLength(3);
  });

  it("有未保存正文时不能预览或申请，界面只引导回到正式版本保存", async () => {
    props.modified = true; await render();
    expect(host.textContent).toContain("changes.saveFirst");
    expect(host.querySelector(".file-change-form")).toBeNull();
    await act(async () => button("changes.backToEdit").click());
    expect(props.onEdit).toHaveBeenCalledOnce();
    expect(api.workbench.prepareChange).not.toHaveBeenCalled(); expect(api.workbench.applyChange).not.toHaveBeenCalled();
  });

  it("即使申请响应附带applied，也要等GET核对后才显示成功", async () => {
    await preview();
    let confirm!: (value: ChangeSet) => void;
    api.workbench.getChange.mockImplementation(() => new Promise((resolve) => { confirm = resolve; }));
    api.workbench.applyChange.mockImplementation(async (value) => transition(value, "applied"));
    await act(async () => button("changes.requestApply").click());
    await ready(() => expect(status()).toBe("changes.status_checking"));
    expect(host.querySelectorAll('.file-change-trail li[data-state="done"]')).toHaveLength(1);
    expect([...host.querySelectorAll("button")].some((item) => item.textContent === "changes.prepareUndo")).toBe(false);
    await act(async () => confirm(changes.get("change-1")!));
    await ready(() => expect(status()).toBe("changes.status_applied"));
  });

  it("网络未知先核对，重新打开后明确重试仍使用同一个change的永久幂等键", async () => {
    seed(change()); await render(); await ready(() => expect(button("changes.requestApply").disabled).toBe(false));
    api.workbench.applyChange.mockRejectedValueOnce(new ApiError("network", ""));
    api.workbench.getChange.mockRejectedValue(new ApiError("network", ""));
    await act(async () => button("changes.requestApply").click());
    await ready(() => expect(status()).toBe("changes.status_unknown"));
    expect(button("changes.requestApply").disabled).toBe(true);
    expect(api.workbench.applyChange).toHaveBeenCalledOnce();
    const firstKey = api.workbench.applyChange.mock.calls[0]![1];
    await act(async () => root.unmount()); client.clear(); root = createRoot(host);
    api.workbench.getChange.mockImplementation(async (id) => ({ ...changes.get(id)! }));
    await render(); await ready(() => expect(button("changes.requestApply").disabled).toBe(false));
    await act(async () => button("changes.requestApply").click());
    await ready(() => expect(api.workbench.applyChange).toHaveBeenCalledTimes(2));
    expect(api.workbench.applyChange.mock.calls[1]![1]).toBe(firstKey);
    expect(api.workbench.applyChange.mock.calls[1]![0]).toMatchObject({ id: "change-1", version: 1, preview_digest: "preview-digest" });
  });

  it("外部冲突保留目标，只有用户重新预览才创建新变更", async () => {
    seed(change({ status: "conflict", version: 3 })); await render(); await ready(() => expect(status()).toBe("changes.status_conflict"));
    expect(host.querySelector<HTMLInputElement>('.file-change-form input:not([type="radio"])')?.value).toBe("src/example.ts");
    expect(api.workbench.prepareChange).not.toHaveBeenCalled(); expect(api.workbench.applyChange).not.toHaveBeenCalled();
    expect([...host.querySelectorAll("button")].some((item) => item.textContent === "changes.requestApply")).toBe(false);
    await act(async () => button("changes.previewAgain").click());
    await ready(() => expect(api.workbench.prepareChange).toHaveBeenCalledOnce());
    expect(api.workbench.applyChange).not.toHaveBeenCalled();
  });

  it("撤销新建只生成独立删除预览，明确告知删除后再次申请审批", async () => {
    seed(change({ status: "applied", version: 4, operation_id: "operation-a" })); await render(); await ready(() => expect(button("changes.prepareUndo").disabled).toBe(false));
    await act(async () => button("changes.prepareUndo").click());
    await ready(() => expect(button("changes.requestDelete").disabled).toBe(false));
    expect(host.textContent).toContain("changes.deleteWarning");
    expect(changes.get("change-1")?.status).toBe("applied");
    expect(api.workbench.applyChange).not.toHaveBeenCalled();
    await act(async () => button("changes.requestDelete").click());
    await ready(() => expect(api.workbench.applyChange).toHaveBeenCalledOnce());
    expect(api.workbench.applyChange.mock.calls[0]![0]).toMatchObject({ id: "undo-1", operation: "delete", undo_of: "change-1" });
  });

  it("取消仅在等待审批时可用，recovery只调用核对不重新写入", async () => {
    seed(change({ status: "awaiting_approval", version: 2 })); await render(); await ready(() => expect(status()).toBe("changes.status_awaiting_approval"));
    await act(async () => button("changes.cancel").click()); await ready(() => expect(status()).toBe("changes.status_cancelled"));
    expect(api.workbench.cancelChange).toHaveBeenCalledOnce();
    const current = changes.get("change-1")!; changes.set(current.id, { ...current, status: "recovery_required", version: 4 });
    await act(async () => { await client.invalidateQueries({ queryKey: queryKeys.change(current.id) }); });
    await ready(() => expect(status()).toBe("changes.status_recovery_required"));
    expect([...host.querySelectorAll("button")].some((item) => item.textContent === "changes.cancel")).toBe(false);
    await act(async () => button("changes.reconcile").click()); await ready(() => expect(status()).toBe("changes.status_applied"));
    expect(api.workbench.reconcileChange).toHaveBeenCalledOnce(); expect(api.workbench.applyChange).not.toHaveBeenCalled();
  });

  it("恢复只选当前成果最近的操作，切换可见性不丢目标与当前预览", async () => {
    seed(change({ id: "old", created_at: "2026-10-10T00:00:00Z", target_path: "old.ts" }));
    seed(change({ id: "new", created_at: "2026-10-11T00:00:00Z", target_path: "new.ts" }));
    seed(change({ id: "other", artifact_id: "artifact-other", created_at: "2026-10-12T00:00:00Z", target_path: "other.ts" }));
    await render(); await ready(() => expect(api.workbench.getChange).toHaveBeenCalledWith("new"));
    props.visible = false; await render(); props.visible = true; await render();
    expect(host.querySelector<HTMLInputElement>('.file-change-form input:not([type="radio"])')?.value).toBe("new.ts");
    expect(api.workbench.getChange).not.toHaveBeenCalledWith("other");
  });

  it("超过600行的真实差异分段展开且可导出全部，DOM始终不超过600行", async () => {
    const content = Array.from({ length: 725 }, (_, index) => `line-${index + 1}`).join("\n");
    seed(change(), "", content); await render(); await ready(() => expect(host.querySelectorAll(".file-change-diff .artifact-diff-line")).toHaveLength(600));
    expect(host.querySelector(".change-added")?.textContent).toBe("+725"); expect(host.querySelector(".change-removed")?.textContent).toBe("−0");
    await act(async () => button("changes.showMore").click());
    expect(host.querySelectorAll(".file-change-diff .artifact-diff-line")).toHaveLength(125);
    expect(host.querySelector(".file-change-diff")?.textContent).toContain("line-725");
    const create = vi.fn((_blob: Blob) => "blob:diff");
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    await act(async () => button("changes.exportDiff").click());
    expect(create).toHaveBeenCalledOnce(); expect(create.mock.calls[0]).toHaveLength(1);
    const exported = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = reject; reader.readAsText(create.mock.calls[0]![0]); });
    expect(exported).toContain("+line-1\n"); expect(exported).toContain("+line-725");
    expect(api.workbench.applyChange).not.toHaveBeenCalled();
  });
});
