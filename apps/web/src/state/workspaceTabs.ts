// 标签只保存当前连接内的打开位置；正文仍由受控预览接口读取。
export const WORKSPACE_TAB_LIMIT = 12;
export type WorkspaceTab =
  | { id: string; kind: "file"; agentId: string; path: string; title: string }
  | { id: string; kind: "web"; url: string; title: string };
export interface WorkspaceTabsSnapshot { tabs: readonly WorkspaceTab[]; activeId: string | null; visible: boolean }
export type OpenTabResult = "opened" | "existing" | "limit" | "invalid";
export const emptyWorkspaceTabs: WorkspaceTabsSnapshot = { tabs: [], activeId: null, visible: false };
export type WorkspaceTabsAction = { type: "restoreWeb"; tab: Extract<WorkspaceTab, { kind: "web" }> } | { type: "open"; tab: WorkspaceTab } | { type: "activate"; id: string } | { type: "close"; id: string } | { type: "hide" } | { type: "web"; id: string; url: string; title?: string };

/** 客户端只接受普通网页地址；桌面主进程另行执行网络与协议限制。 */
export function workspaceWebURL(value: string): string | null {
  if (!value.trim() || value.length > 4096) return null;
  try {
    const url = new URL(value.trim());
    if (!["https:", "http:"].includes(url.protocol) || url.username || url.password) return null;
    return url.href;
  } catch { return null; }
}
/** 纯状态转换，不发请求，也不把客户端打开位置写入磁盘。 */
export function workspaceTabsReducer(state: WorkspaceTabsSnapshot, action: WorkspaceTabsAction): WorkspaceTabsSnapshot {
  if (action.type === "restoreWeb") {
    const url = workspaceWebURL(action.tab.url);
    if (!url || !action.tab.id || action.tab.id.length > 128 || state.tabs.length >= WORKSPACE_TAB_LIMIT || state.tabs.some(tab => tab.id === action.tab.id)) return state;
    return { ...state, tabs: [...state.tabs, { ...action.tab, url, title: action.tab.title.trim().slice(0, 200) || new URL(url).hostname }] };
  }
  if (action.type === "hide") return state.visible ? { ...state, visible: false } : state;
  if (action.type === "activate") return state.tabs.some(tab => tab.id === action.id) ? { ...state, activeId: action.id, visible: true } : state;
  if (action.type === "open") {
    const tab = action.tab;
    const existing = state.tabs.find(item => item.id === tab.id || (item.kind === "file" && tab.kind === "file" && item.agentId === tab.agentId && item.path === tab.path) || (item.kind === "web" && tab.kind === "web" && item.url === tab.url));
    if (existing) return { ...state, activeId: existing.id, visible: true };
    return state.tabs.length >= WORKSPACE_TAB_LIMIT ? state : { tabs: [...state.tabs, tab], activeId: tab.id, visible: true };
  }
  if (action.type === "close") {
    const index = state.tabs.findIndex(tab => tab.id === action.id); if (index < 0) return state;
    const tabs = state.tabs.filter(tab => tab.id !== action.id);
    const activeId = state.activeId === action.id ? (tabs[Math.min(index, tabs.length - 1)]?.id ?? null) : state.activeId;
    return { tabs, activeId, visible: state.visible && !!activeId };
  }
  const safe = workspaceWebURL(action.url); if (!safe) return state;
  const tab = state.tabs.find(item => item.id === action.id); if (tab?.kind !== "web") return state;
  const title = action.title?.trim().slice(0, 200) || new URL(safe).hostname;
  if (tab.url === safe && tab.title === title) return state;
  return { ...state, tabs: state.tabs.map(item => item.id === action.id ? { ...tab, url: safe, title } : item) };
}
