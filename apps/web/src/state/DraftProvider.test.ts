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
