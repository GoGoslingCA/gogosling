// 工具 ID 仅在一次运行中唯一；旧消息缺少 run_id 时，只在归属唯一时补用已知运行。
import type { Message, ToolCall, ToolResult } from "../../api/types";

interface ToolRun { runId: string; toolCalls: readonly { call: ToolCall }[] }

export interface ToolResultIndex {
  key: (callId: string, runId?: string) => string;
  results: Map<string, ToolResult>;
}

export function indexToolResults(messages: readonly Message[], liveRuns: readonly ToolRun[] = []): ToolResultIndex {
  const owners = new Map<string, string | null>();
  const remember = (id: string, runId: string | undefined) => {
    if (!runId) return;
    const previous = owners.get(id);
    if (previous === undefined) owners.set(id, runId);
    else if (previous !== runId) owners.set(id, null);
  };
  for (const message of messages) for (const part of message.parts) {
    if (part.tool_call) remember(part.tool_call.id, message.run_id);
    if (part.tool_result) remember(part.tool_result.call_id, message.run_id);
  }
  for (const run of liveRuns) for (const entry of run.toolCalls) remember(entry.call.id, run.runId);
  const key = (callId: string, runId?: string) => {
    const owner = runId || owners.get(callId) || "";
    return `${owner.length}:${owner}${callId}`;
  };
  const results = new Map<string, ToolResult>();
  for (const message of messages) for (const part of message.parts) {
    if (part.tool_result) results.set(key(part.tool_result.call_id, message.run_id), part.tool_result);
  }
  return { key, results };
}
