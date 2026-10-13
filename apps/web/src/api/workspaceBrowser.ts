// 与桌面壳的可选阅读桥接一致；远程网页不能调用该接口。
export interface WorkspaceBrowserState {
  tabId: string; url: string; title: string; loading: boolean; canGoBack: boolean; canGoForward: boolean;
  error?: "invalid_url" | "blocked_url" | "load_failed" | "cancelled" | "unavailable" | "busy";
}
export interface WorkspaceBrowserBridge {
  show(input: { tabId: string; url: string; bounds: { x: number; y: number; width: number; height: number } }): Promise<WorkspaceBrowserState>;
  hide(): Promise<void>;
  current?: () => Promise<WorkspaceBrowserState | null>;
  close(tabId: string): Promise<WorkspaceBrowserState | null>;
  navigate(input: { tabId: string; url: string }): Promise<WorkspaceBrowserState>;
  action(input: { tabId: string; action: "back" | "forward" | "reload" | "stop" }): Promise<WorkspaceBrowserState>;
  onState(listener: (state: WorkspaceBrowserState) => void): () => void;
}
export function workspaceBrowser(): WorkspaceBrowserBridge | undefined { return window.gogosling?.workspaceBrowser; }
