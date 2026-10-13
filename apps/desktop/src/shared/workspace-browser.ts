/** 网页工作台仅传递瞬时 UI 状态，不持久化网址、页面内容或凭据。 */
export interface WorkspaceBrowserBounds { x: number; y: number; width: number; height: number }

/** 主进程返回错误码；界面使用已有语言包显示提示。 */
export type WorkspaceBrowserError = "invalid_url" | "blocked_url" | "load_failed" | "cancelled" | "unavailable" | "busy";

/** 一个活动网页的导航状态；非活动标签由界面保留描述符。 */
export interface WorkspaceBrowserState {
  tabId: string;
  url: string;
  title: string;
  loading: boolean;
  canGoBack: boolean;
  canGoForward: boolean;
  error?: WorkspaceBrowserError;
}

/** 最小网页宿主接口；不会给远程页面安装此桥接。 */
export interface WorkspaceBrowserBridge {
  current(): Promise<WorkspaceBrowserState | null>;
  show(request: { tabId: string; url: string; bounds: WorkspaceBrowserBounds }): Promise<WorkspaceBrowserState>;
  hide(): Promise<void>;
  close(tabId: string): Promise<WorkspaceBrowserState | null>;
  navigate(request: { tabId: string; url: string }): Promise<WorkspaceBrowserState>;
  action(request: { tabId: string; action: "back" | "forward" | "reload" | "stop" }): Promise<WorkspaceBrowserState>;
  onState(callback: (state: WorkspaceBrowserState) => void): () => void;
}
