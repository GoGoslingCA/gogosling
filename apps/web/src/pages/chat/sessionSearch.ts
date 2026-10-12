// 会话列表的本地检索；服务端契约不提供全文搜索，因此只搜索已加载分页。
import type { Session } from "../../api/types";

export const SESSION_PAGE_LIMIT = 50;

/** offset 分页期间发生新增或删除时，展示层按 ID 去重。 */
export function mergeSessionPages(pages: Session[][]): Session[] {
  return [...new Map(pages.flatMap((page) => page.map((session) => [session.id, session] as const))).values()];
}

/** 忽略首尾空白和大小写，检索标题、会话键和智能体名称。 */
export function filterSessions(sessions: Session[], search: string, agentName: (id: string) => string): Session[] {
  const needle = search.trim().toLocaleLowerCase();
  if (!needle) return sessions;
  return sessions.filter((session) => [session.title ?? "", session.key, agentName(session.agent_id)].some((value) => value.toLocaleLowerCase().includes(needle)));
}
