import { describe, expect, it } from "vitest";
import type { Session } from "../../api/types";
import { filterSessions, mergeSessionPages } from "./sessionSearch";

const session = (id: string, title: string): Session => ({ id, title, agent_id: "agent", key: `${id}:desktop:local`, origin: "desktop", status: "active", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" });

describe("会话检索和分页", () => {
  it("跨分页按 ID 去重，后返回的会话状态覆盖先前页面", () => {
    const merged = mergeSessionPages([[session("a", "Old")], [session("a", "New"), session("b", "Second")]]);
    expect(merged.map((item) => item.id)).toEqual(["a", "b"]);
    expect(merged[0]?.title).toBe("New");
  });
  it("搜索忽略大小写和空白，覆盖标题、会话键及智能体名称", () => {
    const items = [session("a", "Report"), session("b", "Sales")];
    expect(filterSessions(items, "  REPORT ", () => "Work").map((item) => item.id)).toEqual(["a"]);
    expect(filterSessions(items, "b:desktop", () => "Work").map((item) => item.id)).toEqual(["b"]);
    expect(filterSessions(items, "work", () => "Work")).toEqual(items);
    expect(filterSessions(items, "  ", () => "Work")).toBe(items);
  });
});
