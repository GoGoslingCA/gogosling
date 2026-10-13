import { describe, expect, it } from "vitest";
import { emptyWorkspaceTabs, workspaceTabsReducer, workspaceWebURL, WORKSPACE_TAB_LIMIT, type WorkspaceTabsSnapshot } from "./workspaceTabs";
const file = (id: string, agentId = "a", path = "notes.md") => ({ type: "open" as const, tab: { id, kind: "file" as const, agentId, path, title: path } });
describe("阅读标签状态", () => {
  it("同一助手的相同路径去重，不同助手的同名文件分开", () => {
    let state = workspaceTabsReducer(emptyWorkspaceTabs, file("one"));
    state = workspaceTabsReducer(state, file("other-id"));
    expect(state.tabs).toHaveLength(1); expect(state.activeId).toBe("one");
    state = workspaceTabsReducer(state, file("two", "b"));
    expect(state.tabs).toHaveLength(2); expect(state.activeId).toBe("two");
  });
  it("上限内只保留描述符，满额不淘汰现有项且仍可打开已有文件", () => {
    let state: WorkspaceTabsSnapshot = emptyWorkspaceTabs;
    for (let i = 0; i < WORKSPACE_TAB_LIMIT; i++) state = workspaceTabsReducer(state, file(String(i), "a", `${i}.md`));
    expect(workspaceTabsReducer(state, file("overflow"))).toBe(state);
    state = workspaceTabsReducer(state, file("duplicate", "a", "0.md"));
    expect(state.activeId).toBe("0"); expect(state.tabs).toHaveLength(12);
    expect(Object.keys(state.tabs[0]!).sort()).toEqual(["id", "kind", "agentId", "path", "title"].sort());
  });
  it("关闭当前项选中相邻项，收起不关闭文件，关闭最后一项退出阅读", () => {
    let state = workspaceTabsReducer(workspaceTabsReducer(emptyWorkspaceTabs, file("one")), file("two", "b"));
    state = workspaceTabsReducer(state, { type: "hide" }); expect(state.visible).toBe(false); expect(state.tabs).toHaveLength(2);
    state = workspaceTabsReducer(state, { type: "activate", id: "two" }); expect(state.visible).toBe(true);
    state = workspaceTabsReducer(state, { type: "close", id: "two" }); expect(state.activeId).toBe("one");
    state = workspaceTabsReducer(state, { type: "close", id: "one" }); expect(state).toEqual(emptyWorkspaceTabs);
  });
  it.each(["javascript:alert(1)", "file:///etc/passwd", "data:text/html,test", "app://gogosling", "https://name:password@example.com", "relative", "https://example.com/" + "x".repeat(4096)])("拒绝无效或含凭据的网页地址 %s", value => expect(workspaceWebURL(value)).toBeNull());
  it("规范化普通地址，限制网页标题长度且不接收无效导航结果", () => {
    expect(workspaceWebURL(" https://example.com ")).toBe("https://example.com/");
    const state = workspaceTabsReducer(emptyWorkspaceTabs, { type: "open", tab: { id: "web", kind: "web", url: "https://example.com/", title: "Example" } });
    const updated = workspaceTabsReducer(state, { type: "web", id: "web", url: "https://example.com/next", title: "x".repeat(500) });
    expect(updated.tabs[0]?.title).toHaveLength(200);
    expect(workspaceTabsReducer(updated, { type: "web", id: "web", url: "javascript:alert(1)" })).toBe(updated);
  });
});
it("原生网页恢复只补描述符，不自动激活、重复添加或突破上限", () => {
  const action = { type: "restoreWeb" as const, tab: { id: "live", kind: "web" as const, url: "https://example.com/form", title: "Form" } };
  const restored = workspaceTabsReducer(emptyWorkspaceTabs, action); expect(restored.activeId).toBeNull(); expect(restored.visible).toBe(false); expect(restored.tabs).toHaveLength(1); expect(workspaceTabsReducer(restored, action)).toBe(restored);
  let full = restored; for (let i = 0; i < 11; i++) full = workspaceTabsReducer(full, file(String(i), "a", `${i}.md`));
  expect(workspaceTabsReducer(full, { ...action, tab: { ...action.tab, id: "other" } })).toBe(full);
});
