// 阅读标签属于连接，不属于聊天正文；只保留轻量描述符。
import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useConnection } from "./ConnectionContext";
import { workspaceBrowser, type WorkspaceBrowserState } from "../api/workspaceBrowser";
import { emptyWorkspaceTabs, workspaceTabsReducer, workspaceWebURL, WORKSPACE_TAB_LIMIT, type OpenTabResult, type WorkspaceTabsAction, type WorkspaceTabsSnapshot } from "./workspaceTabs";
interface WorkspaceTabsActions {
  getSnapshot: () => WorkspaceTabsSnapshot;
  restoreWeb: (state: WorkspaceBrowserState) => void;
  openFile: (input: { agentId: string; path: string; title?: string }) => OpenTabResult;
  openWeb: (value: string) => OpenTabResult;
  activate: (id: string) => void; hide: () => void; close: (id: string) => void;
  updateWeb: (id: string, url: string, title?: string) => void;
}
const WorkspaceTabsContext = createContext<(WorkspaceTabsSnapshot & { store: WorkspaceTabsActions }) | null>(null);
export function WorkspaceTabsProvider({ children }: { children: ReactNode }) {
  const { api } = useConnection();
  const [scoped, setScoped] = useState({ api, state: emptyWorkspaceTabs });
  // 网页是独立的临时阅读内容。重连清除文件位置，保留可返回的网页，避免遗失表单。
  if (scoped.api !== api) {
    const web = scoped.state.tabs.filter(tab => tab.kind === "web");
    setScoped({ api, state: { tabs: web, activeId: web.some(tab => tab.id === scoped.state.activeId) ? scoped.state.activeId : web[0]?.id ?? null, visible: false } });
  }
  const state = scoped.state; const current = useRef(state); current.current = state;
  const store = useMemo<WorkspaceTabsActions>(() => {
    const dispatch = (action: WorkspaceTabsAction) => { const next = workspaceTabsReducer(current.current, action); current.current = next; setScoped(previous => ({ ...previous, state: next })); };
    return {
      getSnapshot: () => current.current,
      restoreWeb: value => dispatch({ type: "restoreWeb", tab: { id: value.tabId, kind: "web", url: value.url, title: value.title } }),
      openFile: ({ agentId, path, title }) => {
        if (!agentId || !path || path.length > 4096) return "invalid";
        const found = current.current.tabs.find(tab => tab.kind === "file" && tab.agentId === agentId && tab.path === path);
        if (found) { dispatch({ type: "activate", id: found.id }); return "existing"; }
        if (current.current.tabs.length >= WORKSPACE_TAB_LIMIT) return "limit";
        dispatch({ type: "open", tab: { id: crypto.randomUUID(), kind: "file", agentId, path, title: (title || path.split(/[\\/]/).at(-1) || path).slice(0, 200) } }); return "opened";
      },
      openWeb: value => {
        const url = workspaceWebURL(value); if (!url) return "invalid";
        const found = current.current.tabs.find(tab => tab.kind === "web" && tab.url === url);
        if (found) { dispatch({ type: "activate", id: found.id }); return "existing"; }
        if (current.current.tabs.length >= WORKSPACE_TAB_LIMIT) return "limit";
        dispatch({ type: "open", tab: { id: crypto.randomUUID(), kind: "web", url, title: new URL(url).hostname } }); return "opened";
      },
      activate: id => dispatch({ type: "activate", id }), hide: () => dispatch({ type: "hide" }), close: id => dispatch({ type: "close", id }), updateWeb: (id, url, title) => dispatch({ type: "web", id, url, title }),
    };
  }, []);
  useEffect(() => { void workspaceBrowser()?.hide().catch(() => undefined); }, [api]);
  const recovery = useRef<Promise<WorkspaceBrowserState | null> | null>(null);
  useEffect(() => {
    let disposed = false;
    recovery.current ??= workspaceBrowser()?.current?.() ?? Promise.resolve(null);
    void recovery.current.then(value => { if (!disposed && value) store.restoreWeb(value); }).catch(() => undefined);
    return () => { disposed = true; };
  }, [store]);
  return <WorkspaceTabsContext.Provider value={{ ...state, store }}>{children}</WorkspaceTabsContext.Provider>;
}
/** 独立页面测试和旧宿主未挂载 Provider 时保留原预览流程。 */
export function useWorkspaceTabs() { return useContext(WorkspaceTabsContext); }
