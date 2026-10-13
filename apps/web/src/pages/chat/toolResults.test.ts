import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types";
import { indexToolResults } from "./toolResults";

const call = { id: "same", name: "read_file", arguments: {} };
function message(id: string, runId?: string, result?: string): Message {
  return { id, run_id: runId, session_id: "session", seq: 1, role: result ? "tool" : "assistant", created_at: "", parts: result ? [{ type: "tool_result", tool_result: { call_id: call.id, content: result } }] : [{ type: "tool_call", tool_call: call }] };
}

describe("历史工具结果的运行归属", () => {
  it("旧无运行 ID 结果在唯一已知归属下仍可恢复", () => {
    const index = indexToolResults([message("call", "run"), message("result", undefined, "legacy")]);
    expect(index.results.get(index.key(call.id, "run"))?.content).toBe("legacy");
  });
  it("运行 ID 缺失且存在多个候选运行时，不把旧结果安到任一新工具上", () => {
    const index = indexToolResults([message("old", "old"), message("new", "new"), message("legacy", undefined, "unknown-owner")]);
    expect(index.results.get(index.key(call.id, "old"))).toBeUndefined();
    expect(index.results.get(index.key(call.id, "new"))).toBeUndefined();
    expect(index.results.get(index.key(call.id))?.content).toBe("unknown-owner");
  });
  it("已知运行、工具 ID 包含分隔符也不会产生组合键碰撞", () => {
    const { key } = indexToolResults([]);
    expect(key("b:c", "a")).not.toBe(key("c", "a:b"));
  });
});
