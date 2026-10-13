import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Draft, DraftContent, DraftInput, DraftScope, DraftUpdateInput } from "../api/types";
import { DraftController, normalizeDraft, sameDraftContent } from "./draftController";

const scope: DraftScope = { kind: "composer", agent_id: "agent-a", session_id: "session-a" };
const stamp = "2026-10-12T01:00:00Z";
function makeDraft(input: Partial<Draft> = {}): Draft { return { ...scope, id: "draft-a", owner_id: "local", text: "远端草稿", version: 1, created_at: stamp, updated_at: stamp, ...input }; }
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function fixture(initial: Draft | null = null) {
  let remote = initial;
  return {
    get remote() { return remote; },
    change(value: Draft) { remote = value; },
    api: {
      listDrafts: vi.fn(async () => remote ? [remote] : []),
      getDraft: vi.fn(async (_id: string) => { if (!remote) throw new ApiError("not_found", ""); return remote; }),
      createDraft: vi.fn(async (input: DraftInput) => { if (remote) throw new ApiError("conflict", "", { details: { existing_draft_id: remote.id } }); remote = makeDraft({ ...input, text: input.text }); return remote; }),
      updateDraft: vi.fn(async (_id: string, input: DraftUpdateInput) => { if (!remote || input.expected_version !== remote.version) throw new ApiError("conflict", "", { details: { reason: "stale_version" } }); remote = makeDraft({ ...input, version: remote.version + 1 }); return remote; }),
      deleteDraft: vi.fn(async (_id: string, version: number) => { if (remote && remote.version !== version) throw new ApiError("conflict", ""); remote = null; }),
    },
  };
}
let controllers: DraftController[];
beforeEach(() => { vi.useFakeTimers(); controllers = []; });
afterEach(() => { for (const controller of controllers) controller.dispose(); vi.useRealTimers(); vi.restoreAllMocks(); });
function create(initial: DraftContent = { text: "" }, customScope = scope) { const controller = new DraftController(customScope, initial); controllers.push(controller); return controller; }

describe("草稿 CAS 与迟到响应", () => {
  it("旧守护进程不探测写接口，正文仅留在内存且继续标记未保存", async () => {
    const controller = create(); const api = fixture().api;
    controller.configure(api, false, false);
    controller.setContent({ text: "本地输入" });
    await controller.saveNow();
    expect(controller.getSnapshot()).toMatchObject({ status: "unsupported", dirty: true, content: { text: "本地输入" } });
    expect(api.listDrafts).not.toHaveBeenCalled();
    expect(api.createDraft).not.toHaveBeenCalled();
  });

  it("迟到水合不能覆盖已输入内容，明确选择恢复远端后才替换", async () => {
    const f = fixture(makeDraft()); const request = deferred<Draft[]>(); f.api.listDrafts.mockReturnValue(request.promise);
    const controller = create(); controller.configure(f.api, true, false);
    controller.setContent({ text: "等待期间的新输入" });
    request.resolve([f.remote!]); await controller.hydrate();
    expect(controller.getSnapshot()).toMatchObject({ status: "conflict", dirty: true, content: { text: "等待期间的新输入" }, conflict: { text: "远端草稿" } });
    expect(f.api.updateDraft).not.toHaveBeenCalled();
    await controller.resolveConflict("remote");
    expect(controller.getSnapshot()).toMatchObject({ status: "saved", dirty: false, content: { text: "远端草稿" } });
  });

  it("新草稿自动保存只有收到服务端成功响应后才显示已保存", async () => {
    const f = fixture(); const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    const request = deferred<Draft>(); f.api.createDraft.mockReturnValueOnce(request.promise);
    controller.setContent({ text: "待确认内容" });
    await vi.advanceTimersByTimeAsync(600);
    expect(controller.getSnapshot()).toMatchObject({ status: "saving", dirty: true });
    request.resolve(makeDraft({ text: "待确认内容" })); await Promise.resolve(); await Promise.resolve();
    expect(controller.getSnapshot()).toMatchObject({ status: "saved", dirty: false });
  });

  it("失败保留输入，显式重试成功后才解除未保存状态", async () => {
    const f = fixture(); const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    controller.setContent({ text: "不能丢失" }); f.api.createDraft.mockRejectedValueOnce(new ApiError("network", ""));
    await expect(controller.saveNow()).rejects.toBeInstanceOf(ApiError);
    expect(controller.getSnapshot()).toMatchObject({ status: "error", dirty: true, content: { text: "不能丢失" } });
    await controller.saveNow();
    expect(controller.getSnapshot()).toMatchObject({ status: "saved", dirty: false });
  });

  it("其他窗口更新后保留双方，用户选择当前输入才以最新版本再次 CAS", async () => {
    const f = fixture(makeDraft()); const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    controller.setContent({ text: "当前窗口编辑" }); f.change(makeDraft({ text: "另一窗口编辑", version: 2 }));
    await expect(controller.saveNow()).rejects.toBeInstanceOf(ApiError);
    expect(controller.getSnapshot()).toMatchObject({ status: "conflict", content: { text: "当前窗口编辑" }, conflict: { text: "另一窗口编辑", version: 2 } });
    expect(f.remote?.text).toBe("另一窗口编辑");
    await controller.resolveConflict("local");
    expect(f.api.updateDraft).toHaveBeenLastCalledWith("draft-a", expect.objectContaining({ text: "当前窗口编辑", expected_version: 2 }));
    expect(f.remote?.version).toBe(3);
  });

  it("保存期间的新输入不被旧保存响应清空，发送旧版本后新输入仍可保存", async () => {
    const f = fixture(); const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    controller.setContent({ text: "本次发送" }); const token = await controller.saveNow();
    controller.setContent({ text: "下一轮新输入" });
    await controller.clearAfterSend(token);
    expect(controller.getSnapshot().content.text).toBe("下一轮新输入");
    await vi.advanceTimersByTimeAsync(600);
    expect(f.remote?.text).toBe("下一轮新输入");
    expect(controller.getSnapshot().dirty).toBe(false);
  });

  it("新编辑已保存为较新版本时，旧发送响应不能删除较新草稿", async () => {
    const f = fixture(); const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    controller.setContent({ text: "发送版本" }); const sent = await controller.saveNow();
    controller.setContent({ text: "已经保存的新稿" }); await controller.saveNow();
    await controller.clearAfterSend(sent);
    expect(f.api.deleteDraft).not.toHaveBeenCalled();
    expect(f.remote?.text).toBe("已经保存的新稿");
  });

  it("成功发送后清理确认版本，重新挂载不会从内存恢复已发送的旧文字", async () => {
    const f = fixture(); const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    controller.setContent({ text: "已发送" }); const sent = await controller.saveNow(); await controller.clearAfterSend(sent);
    expect(f.api.deleteDraft).toHaveBeenCalledWith(sent.draftId, sent.version);
    expect(controller.getSnapshot()).toMatchObject({ content: { text: "" }, dirty: false });
    expect(f.remote).toBeNull();
  });

  it("删除遇到跨窗口版本冲突时不清空本地或远端新草稿", async () => {
    const f = fixture(makeDraft()); const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    const sent = controller.capture(); f.change(makeDraft({ version: 2, text: "另一窗口的新稿" }));
    await expect(controller.clearAfterSend(sent)).rejects.toBeInstanceOf(ApiError);
    expect(f.remote?.text).toBe("另一窗口的新稿");
    expect(controller.getSnapshot()).toMatchObject({ status: "conflict", content: { text: "远端草稿" }, conflict: { text: "另一窗口的新稿" } });
  });

  it("恢复旧成果草稿时保留真实基线，外部 latest 属性不能推进基线", async () => {
    const artifactScope: DraftScope = { kind: "artifact", agent_id: "agent-a", artifact_id: "artifact-a", base_revision_id: "revision-new" };
    const f = fixture(makeDraft({ ...artifactScope, base_revision_id: "revision-old", text: "基于旧版本编辑" }));
    const controller = create({ text: "最新版本正文" }, artifactScope); controller.configure(f.api, true, false); await controller.hydrate();
    expect(controller.getSnapshot().baseRevisionId).toBe("revision-old");
    controller.setScope(artifactScope); controller.setContent({ text: "继续修改旧草稿" }); await controller.saveNow();
    expect(f.api.updateDraft).toHaveBeenLastCalledWith("draft-a", expect.objectContaining({ base_revision_id: "revision-old" }));
    const previousVersion = f.remote!.version;
    controller.setBaseRevision("revision-chosen"); await controller.saveNow();
    expect(f.api.updateDraft).toHaveBeenLastCalledWith("draft-a", expect.objectContaining({ base_revision_id: "revision-chosen", expected_version: previousVersion }));
  });

  it("正文回退到原稿后仍会保存尚未确认的新成果基线", async () => {
    const artifactScope: DraftScope = { kind: "artifact", agent_id: "agent-a", artifact_id: "artifact-a", base_revision_id: "revision-old" };
    const f = fixture(makeDraft({ ...artifactScope, text: "原稿" }));
    const controller = create({ text: "原稿" }, artifactScope); controller.configure(f.api, true, false); await controller.hydrate();
    controller.setBaseRevision("revision-new");
    controller.setContent({ text: "临时编辑" }); controller.setContent({ text: "原稿" });
    expect(controller.getSnapshot().dirty).toBe(true);
    await vi.advanceTimersByTimeAsync(600);
    expect(f.api.updateDraft).toHaveBeenCalledWith("draft-a", expect.objectContaining({ text: "原稿", base_revision_id: "revision-new", expected_version: 1 }));
    expect(controller.getSnapshot().dirty).toBe(false);
  });

  it("附件字段排列不同不会阻止已发送草稿清理，顺序和元数据修改仍视为编辑", async () => {
    const attachment = { name: "notes.md", path: "inbound/notes.md", size: 10, mime: "text/markdown" };
    const f = fixture(makeDraft({ text: "附件", attachments: [{ size: 10, mime: "text/markdown", path: attachment.path, name: attachment.name }] }));
    const controller = create({ text: "附件", attachments: [attachment] }); controller.configure(f.api, true, false); await controller.hydrate();
    const token = controller.capture();
    await controller.clearAfterSend(token);
    expect(f.api.deleteDraft).toHaveBeenCalledWith("draft-a", 1);
    expect(controller.getSnapshot().content.text).toBe("");
    const second = { ...attachment, name: "second.md" };
    expect(sameDraftContent({ text: "", attachments: [attachment, second] }, { text: "", attachments: [second, attachment] })).toBe(false);
    for (const change of [{ name: "new" }, { path: "new" }, { mime: "new" }, { size: 11 }]) {
      expect(sameDraftContent({ text: "", attachments: [attachment] }, { text: "", attachments: [{ ...attachment, ...change }] })).toBe(false);
    }
  });

  it("相同内容保持快照引用并避免重复通知，省略字段与空数组一致", () => {
    const controller = create(); controller.configure(null, false, false);
    const snapshot = controller.getSnapshot(), listener = vi.fn(); controller.subscribe(listener);
    controller.setContent({ text: "", filename: "", attachments: [], material_ids: [] });
    controller.configure(null, false, false);
    expect(controller.getSnapshot()).toBe(snapshot); expect(listener).not.toHaveBeenCalled();
    expect(sameDraftContent({ text: "", material_ids: ["a", "b"] }, { text: "", material_ids: ["b", "a"] })).toBe(false);
    expect(sameDraftContent({ text: "", filename: "a" }, { text: "", filename: "b" })).toBe(false);
  });

  it("输入附件的后续修改不能悄悄改变已捕获的草稿和保存令牌", () => {
    const attachment = { name: "old.md", path: "inbound/old.md", size: 10 };
    const controller = create({ text: "原文", attachments: [attachment] });
    const token = controller.capture(); attachment.name = "new.md";
    expect(controller.getSnapshot().content.attachments?.[0]?.name).toBe("old.md");
    expect(controller.capture().fingerprint).toBe(token.fingerprint);
    expect(JSON.parse(token.fingerprint)).toEqual(JSON.parse(JSON.stringify(normalizeDraft({ text: "原文", attachments: [{ ...attachment, name: "old.md" }] }))));
  });

  it("销毁后迟到恢复不会翻页、替换草稿或重新启动自动保存", async () => {
    const f = fixture(), request = deferred<Draft[]>(); f.api.listDrafts.mockReturnValue(request.promise);
    const controller = create(); controller.configure(f.api, true, false); const hydration = controller.hydrate();
    controller.setContent({ text: "未保存输入" }); controller.dispose();
    const snapshot = controller.getSnapshot();
    request.resolve(Array.from({ length: 200 }, (_, index) => makeDraft({ session_id: `other-${index}` })));
    await expect(hydration).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(1200);
    expect(f.api.listDrafts).toHaveBeenCalledTimes(1); expect(f.api.createDraft).not.toHaveBeenCalled();
    expect(controller.getSnapshot()).toBe(snapshot);
  });

  it("销毁不撤回已经提交的保存，但拒绝排队写入且不启动后继保存", async () => {
    const f = fixture(), request = deferred<Draft>();
    const controller = create(); controller.configure(f.api, true, false); await controller.hydrate();
    f.api.createDraft.mockReturnValueOnce(request.promise);
    controller.setContent({ text: "已经提交" }); const first = controller.saveNow();
    await Promise.resolve(); expect(f.api.createDraft).toHaveBeenCalledTimes(1);
    controller.setContent({ text: "后继输入" }); const second = controller.saveNow();
    controller.dispose();
    const assertions = Promise.all([expect(first).rejects.toMatchObject({ name: "AbortError" }), expect(second).rejects.toMatchObject({ name: "AbortError" })]);
    request.resolve(makeDraft({ text: "已经提交" })); await assertions;
    await vi.advanceTimersByTimeAsync(1200);
    expect(f.api.createDraft).toHaveBeenCalledTimes(1); expect(f.api.updateDraft).not.toHaveBeenCalled();
    expect(controller.getSnapshot().content.text).toBe("后继输入");
  });
});
