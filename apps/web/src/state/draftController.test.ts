import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Draft, DraftInput, DraftScope, DraftUpdateInput } from "../api/types";
import { DraftController } from "./draftController";

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
function create(initial = { text: "" }, customScope = scope) { const controller = new DraftController(customScope, initial); controllers.push(controller); return controller; }

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
});
