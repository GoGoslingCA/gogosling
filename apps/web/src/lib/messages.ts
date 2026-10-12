// 消息列表的增量合并：按 id 去重、按 seq 升序。

import type { Message } from "../api/types";

export function mergeMessages(prev: Message[], fresh: Message[]): Message[] {
  if (fresh.length === 0) return prev;
  const byId = new Map<string, Message>();
  for (const m of prev) byId.set(m.id, m);
  for (const m of fresh) byId.set(m.id, m);
  return [...byId.values()].sort((a, b) => a.seq - b.seq);
}

/** 最后一条消息的 seq；空列表为 0。 */
export function lastSeq(messages: Message[]): number {
  const last = messages[messages.length - 1];
  return last ? last.seq : 0;
}
