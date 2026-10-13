import { describe, expect, it, vi } from "vitest";
import { DraftRegistry } from "./DraftProvider";

describe("草稿集合的增量未保存状态", () => {
  it("只在整体未保存状态改变时通知，单个输入不扫描其他草稿", async () => {
    const registry = new DraftRegistry(); registry.configure(null, false, false);
    const controllers = Array.from({ length: 200 }, (_, index) => registry.get({ kind: "composer", agent_id: "agent-a", session_id: `session-${index}` }, { text: "" }));
    for (const controller of controllers) registry.activate(controller);
    const listener = vi.fn(); registry.subscribe(listener);
    const dormantReads = controllers.slice(1).map((controller) => vi.spyOn(controller, "getSnapshot"));
    const active = controllers[0]!;
    active.setContent({ text: "第一版" }); active.setContent({ text: "第二版" });
    await active.saveNow();
    expect(registry.getSnapshot()).toBe(true); expect(listener).toHaveBeenCalledTimes(1);
    expect(dormantReads.every((read) => read.mock.calls.length === 0)).toBe(true);
    active.setContent({ text: "" });
    expect(registry.getSnapshot()).toBe(false); expect(listener).toHaveBeenCalledTimes(2);
    registry.dispose();
  });

  it("创建草稿时不在渲染阶段通知，激活后才公布初始未保存内容", () => {
    const registry = new DraftRegistry(); registry.configure(null, false, false);
    const listener = vi.fn(); registry.subscribe(listener);
    const first = registry.get({ kind: "composer", agent_id: "agent-a", session_id: "first" }, { text: "初始输入" });
    expect(listener).not.toHaveBeenCalled();
    registry.activate(first);
    expect(listener).toHaveBeenCalledTimes(1); expect(registry.getSnapshot()).toBe(true);
    const second = registry.get({ kind: "composer", agent_id: "agent-a", session_id: "second" }, { text: "第二份" });
    registry.activate(second); first.setContent({ text: "" });
    expect(registry.getSnapshot()).toBe(true); expect(listener).toHaveBeenCalledTimes(1);
    second.setContent({ text: "" });
    expect(registry.getSnapshot()).toBe(false); expect(listener).toHaveBeenCalledTimes(2);
    registry.dispose();
  });

  it("StrictMode 重挂载保留原控制器，真正卸载后清理且不同会话草稿互不覆盖", async () => {
    const registry = new DraftRegistry(); registry.configure(null, false, false);
    const scope = { kind: "composer" as const, agent_id: "agent-a", session_id: "first" };
    const first = registry.get(scope, { text: "第一份" }); registry.activate(first);
    const release = registry.retain(); release(); const releaseAgain = registry.retain();
    await Promise.resolve();
    expect(registry.get(scope, { text: "" })).toBe(first);
    const second = registry.get({ ...scope, session_id: "second" }, { text: "第二份" }); registry.activate(second);
    second.setContent({ text: "第二份的新编辑" });
    expect(first.getSnapshot().content.text).toBe("第一份");
    releaseAgain(); await Promise.resolve();
    expect(registry.getSnapshot()).toBe(false);
    await expect(first.saveNow()).rejects.toMatchObject({ name: "AbortError" });
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((yes) => { resolve = yes; });
  return { promise, resolve };
}

async function persistentRegistry() {
  const { ApiError } = await import("../api/client");
  const scope = { kind: "composer" as const, agent_id: "agent-a", session_id: "first" };
  let remote: import("../api/types").Draft | null = { ...scope, id: "draft-a", text: "已保存", owner_id: "local", version: 1, created_at: "2026-10-13T00:00:00Z", updated_at: "2026-10-13T00:00:00Z" };
  const api: import("./draftController").DraftApi = {
    listDrafts: vi.fn(async () => remote ? [remote] : []),
    getDraft: vi.fn(async () => remote!),
    createDraft: vi.fn(async (input) => { remote = { ...remote!, ...input, id: "draft-new", version: 1 }; return remote!; }),
    updateDraft: vi.fn(async (_id, input) => {
      if (!remote || input.expected_version !== remote.version) throw new ApiError("conflict", "");
      remote = { ...remote, ...input, version: input.expected_version + 1 }; return remote!;
    }),
    deleteDraft: vi.fn(async (_id, version) => {
      if (!remote || remote.version !== version) throw new ApiError("conflict", "");
      remote = null;
    }),
  };
  const registry = new DraftRegistry(); registry.configure(api, true, false);
  const controller = registry.get(scope, { text: "" });
  const release = registry.subscribeDraft(controller, () => undefined);
  registry.activate(controller); await controller.hydrate();
  return { registry, controller, scope, api, release, remote: () => remote, replace: (text: string, version: number) => { remote = { ...remote!, text, version }; } };
}

async function drain() { for (let index = 0; index < 6; index += 1) await Promise.resolve(); }

describe("只回收内核可恢复的空闲草稿", () => {
  it("最后一个 UI 离开才回收，重新进入读取最新版本而不是用旧 CAS", async () => {
    const f = await persistentRegistry();
    const releaseSecond = f.registry.subscribeDraft(f.controller, () => undefined);
    f.release(); await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    releaseSecond(); await drain();
    f.replace("另一窗口的新稿", 8);
    const next = f.registry.get(f.scope, { text: "" });
    expect(next).not.toBe(f.controller);
    const releaseNext = f.registry.subscribeDraft(next, () => undefined);
    f.registry.activate(next); await next.hydrate();
    expect(next.getSnapshot().content.text).toBe("另一窗口的新稿");
    next.setContent({ text: "继续修改" }); await next.saveNow();
    expect(f.api.updateDraft).toHaveBeenCalledWith("draft-a", expect.objectContaining({ expected_version: 8 }));
    releaseNext(); f.registry.dispose();
  });

  it("StrictMode 重订阅撤销回收，重复清理不能删除另一个读者", async () => {
    const f = await persistentRegistry();
    f.release();
    const releaseAgain = f.registry.subscribeDraft(f.controller, () => undefined);
    f.release(); await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    releaseAgain(); await drain();
    expect(f.registry.get(f.scope, { text: "" })).not.toBe(f.controller);
    f.registry.dispose();
  });

  it("读取后提交前被回收时快照失效，迟到激活不能启动旧实例", async () => {
    const f = await persistentRegistry();
    const renderedSnapshot = f.registry.read(f.controller);
    f.release(); await drain();
    expect(f.registry.read(f.controller)).not.toBe(renderedSnapshot);
    f.registry.activate(f.controller);
    expect(f.api.listDrafts).toHaveBeenCalledTimes(1);
    await expect(f.controller.saveNow()).rejects.toMatchObject({ name: "AbortError" });
    f.registry.dispose();
  });

  it("加载、未保存、保存失败、冲突与旧内核都保留原实例", async () => {
    const f = await persistentRegistry();
    f.controller.setContent({ text: "未保存不能丢" }); f.release(); await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    vi.mocked(f.api.updateDraft).mockRejectedValueOnce(new Error("offline"));
    await expect(f.controller.saveNow()).rejects.toThrow("offline"); await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    f.replace("外部修改", 2);
    await expect(f.controller.saveNow()).rejects.toMatchObject({ code: "conflict" }); await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    expect(f.controller.getSnapshot().conflict?.text).toBe("外部修改");
    f.registry.dispose();
    const legacy = new DraftRegistry(); legacy.configure(null, false, false);
    const initial = legacy.get(f.scope, { text: "" }); legacy.activate(initial);
    const release = legacy.subscribeDraft(initial, () => undefined);
    release(); await drain();
    expect(legacy.get(f.scope, { text: "" })).toBe(initial); legacy.dispose();
  });

  it("保存与排队保存期间不回收，最后一次确认且无人读取后再释放", async () => {
    const f = await persistentRegistry();
    const firstSaved = deferred<import("../api/types").Draft>();
    vi.mocked(f.api.updateDraft).mockReturnValueOnce(firstSaved.promise);
    f.controller.setContent({ text: "第一稿" }); const first = f.controller.saveNow();
    f.controller.setContent({ text: "第二稿" }); const second = f.controller.saveNow();
    f.release(); await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    firstSaved.resolve({ ...f.remote()!, text: "第一稿", version: 2 });
    f.replace("第一稿", 2); await first; await second; await drain();
    expect(f.remote()?.text).toBe("第二稿");
    expect(f.registry.get(f.scope, { text: "" })).not.toBe(f.controller);
    f.registry.dispose();
  });

  it("发送令牌跨切页保留，失败不删远端内容，释放可重复调用", async () => {
    const f = await persistentRegistry();
    const finish = f.controller.retainOperation(); const token = await f.controller.saveNow();
    f.release(); await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    expect(f.remote()?.text).toBe("已保存");
    await f.controller.clearAfterSend(token); finish(); finish(); await drain();
    expect(f.remote()).toBeNull(); expect(f.api.deleteDraft).toHaveBeenCalledWith("draft-a", 1);
    expect(f.registry.get(f.scope, { text: "" })).not.toBe(f.controller); f.registry.dispose();
  });

  it("丢弃期间的新输入不会被回收或删除，恢复后继续自动保存", async () => {
    const f = await persistentRegistry(); const deletion = deferred<void>();
    vi.mocked(f.api.deleteDraft).mockReturnValueOnce(deletion.promise);
    const discard = f.controller.discard(); await Promise.resolve();
    f.controller.setContent({ text: "删除等待时的新内容" }); f.release();
    deletion.resolve(); await discard; await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    expect(f.controller.getSnapshot()).toMatchObject({ dirty: true, content: { text: "删除等待时的新内容" } });
    f.registry.dispose();
  });
});

describe("回收等待的边界", () => {
  it("加载期间最后读者离开不销毁请求，确认远端副本后才释放", async () => {
    const f = await persistentRegistry();
    const pending = deferred<import("../api/types").Draft[]>();
    vi.mocked(f.api.listDrafts).mockReturnValueOnce(pending.promise);
    const loading = f.controller.hydrate(); f.release(); await drain();
    expect(f.registry.read(f.controller).status).toBe("loading");
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    pending.resolve([f.remote()!]); await loading; await drain();
    expect(f.registry.get(f.scope, { text: "" })).not.toBe(f.controller);
    f.registry.dispose();
  });

  it("同一作用域双读者在保存期间一方离开不影响剩余读者和保存结果", async () => {
    const f = await persistentRegistry(), result = deferred<import("../api/types").Draft>();
    const other = f.registry.subscribeDraft(f.controller, () => undefined);
    vi.mocked(f.api.updateDraft).mockReturnValueOnce(result.promise);
    f.controller.setContent({ text: "仍在编辑" }); const saving = f.controller.saveNow();
    f.release(); await drain();
    result.resolve({ ...f.remote()!, text: "仍在编辑", version: 2 }); await saving; await drain();
    expect(f.registry.get(f.scope, { text: "" })).toBe(f.controller);
    expect(f.registry.read(f.controller)).toMatchObject({ status: "saved", content: { text: "仍在编辑" } });
    other(); await drain();
    expect(f.registry.get(f.scope, { text: "" })).not.toBe(f.controller);
    f.registry.dispose();
  });
});
