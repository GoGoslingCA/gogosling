import { describe, expect, it } from "vitest";
import type { Message } from "../api/types";
import { lastSeq, mergeMessages } from "./messages";

function msg(id: string, seq: number): Message {
  return { id, session_id: "s", seq, role: "user", parts: [], created_at: "2026-10-10T00:00:00Z" };
}

describe("mergeMessages", () => {
  it("按 id 去重、按 seq 升序，新数据覆盖旧数据", () => {
    const prev = [msg("a", 1), msg("b", 2)];
    const fresh = [msg("b", 2), msg("c", 3)];
    expect(mergeMessages(prev, fresh).map((m) => m.id)).toEqual(["a", "b", "c"]);
    expect(lastSeq(mergeMessages(prev, fresh))).toBe(3);
  });

  it("没有新数据时返回原数组引用（避免无谓重渲染）", () => {
    const prev = [msg("a", 1)];
    expect(mergeMessages(prev, [])).toBe(prev);
    expect(lastSeq([])).toBe(0);
  });
});
