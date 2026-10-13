// 主进程与 preload 之间的 IPC 契约：通道名与跨进程传递的数据形状。
//
// 沙箱 preload 不能 require 本地文件（Electron 的 require 只是受限的 polyfill），
// 所以 preload 只 `import type` 本文件：通道名在 preload 里以字面量重写，并用
// `satisfies IpcChannelMap` 让编译器保证两边完全一致。
//
// 这里的任何对象都可能被发送到渲染进程，因此 DaemonStatus 不得携带令牌；
// 打包后 ConnectionInfo 也不带令牌（proxy 模式），只有开发模式（direct）才把令牌交给页面。

import type { DiagnosticCategory, DiagnosticPreview, DiagnosticReceipt, DiagnosticResult, DiagnosticStatus, DiagnosticTaskRequest } from "./diagnostics.js";
import type { WorkspaceBrowserBridge } from "./workspace-browser.js";

/** IPC 通道名。前缀 gogosling: 避免与第三方 preload（无）或 Electron 内部通道冲突。 */
export const IPC = {
  /** invoke：渲染进程索取连接信息（见 ConnectionInfo）；守护进程未就绪时返回 null。 */
  getConnection: "gogosling:connection:get",
  /** invoke：壳与运行时版本。 */
  getVersion: "gogosling:version:get",
  /** invoke：用系统浏览器打开 http(s)/mailto 链接。 */
  openExternal: "gogosling:shell:open-external",
  /** 主进程 → 渲染进程：守护进程状态变化。 */
  daemonStatus: "gogosling:daemon:status",
  /** 渲染进程 → 主进程：订阅后立即补发一次当前状态。 */
  daemonStatusSubscribe: "gogosling:daemon:status:subscribe",
  diagnosticsStatus: "gogosling:diagnostics:status",
  diagnosticsPreview: "gogosling:diagnostics:preview",
  diagnosticsTaskPreview: "gogosling:diagnostics:task-preview",
  diagnosticsCancelPreparation: "gogosling:diagnostics:cancel-preparation",
  diagnosticsSend: "gogosling:diagnostics:send",
  diagnosticsCancel: "gogosling:diagnostics:cancel",
  workspaceBrowserShow: "gogosling:workspace-browser:show",
  workspaceBrowserHide: "gogosling:workspace-browser:hide",
  workspaceBrowserClose: "gogosling:workspace-browser:close",
  workspaceBrowserNavigate: "gogosling:workspace-browser:navigate",
  workspaceBrowserAction: "gogosling:workspace-browser:action",
  workspaceBrowserState: "gogosling:workspace-browser:state",
  workspaceBrowserCurrent: "gogosling:workspace-browser:current",
} as const;

/** 每个通道的字面量类型；preload 用它校验自己重写的通道名。 */
export type IpcChannelMap = { readonly [K in keyof typeof IPC]: (typeof IPC)[K] };

/** 任一通道名。 */
export type IpcChannel = IpcChannelMap[keyof IpcChannelMap];

/**
 * 页面如何访问守护进程：
 *   - proxy：页面只访问同源 app://gogosling/api/...，由主进程代理到守护进程并注入令牌（打包后的默认）；
 *   - direct：页面直连守护进程并自带令牌（只用于 GOGOSLING_WEB_URL 指向 vite 的开发模式）。
 */
export type ConnectionMode = "proxy" | "direct";

/** 交给渲染进程的连接信息。只在内存中传递，不写磁盘、不进日志。 */
export interface ConnectionInfo {
  /** proxy 模式为 "app://gogosling"；direct 模式为守护进程地址，例如 http://127.0.0.1:46750（无尾部斜杠）。 */
  endpoint: string;
  /** proxy 模式为空串（令牌留在主进程）；direct 模式为 daemon.json 中的 Bearer 令牌。 */
  token: string;
  mode: ConnectionMode;
}

/** 守护进程管理器的状态机取值。 */
export type DaemonState =
  /** 尚未开始。 */
  | "idle"
  /** 正在读取 daemon.json 并探测是否已有守护进程在跑。 */
  | "attaching"
  /** 正在定位 gogoslingd 可执行文件。 */
  | "locating"
  /** 已 spawn，等待 /healthz 就绪。 */
  | "starting"
  /** 可用；getConnection() 返回非 null。 */
  | "ready"
  /** 子进程意外退出，正在退避等待后重启。 */
  | "restarting"
  /** 正在终止由本壳启动的守护进程。 */
  | "stopping"
  /** 已停止（主动停止或壳退出）。 */
  | "stopped"
  /** 放弃：见 error 字段。 */
  | "failed";

/** 状态为 failed 时的原因代码；文案由各前端按自己的语言包映射。 */
export type DaemonErrorCode =
  /** 环境变量、资源目录与 PATH 都没有找到 gogoslingd。 */
  | "executable_not_found"
  /** spawn 系统调用失败（权限、架构不匹配等）。 */
  | "spawn_failed"
  /** 启动后在超时时间内没有写出句柄或 /healthz 未就绪。 */
  | "ready_timeout"
  /** 子进程退出。 */
  | "exited"
  /** daemon.json 指向的守护进程在跑，但拒绝了句柄里的令牌：句柄陈旧，且不能再拉起第二个实例。 */
  | "stale_handle"
  /** 连续重启达到上限。 */
  | "restart_limit";

/** 发送给渲染进程与托盘的守护进程状态（不含令牌）。 */
export interface DaemonStatus {
  state: DaemonState;
  /** 守护进程是否由本壳启动；决定退出时是否终止它、托盘"重启"是否可用。 */
  owned: boolean;
  /** 本轮连续重启次数（崩溃前为 0）。 */
  attempt: number;
  /** 重启上限。 */
  maxAttempts: number;
  pid?: number;
  endpoint?: string;
  error?: DaemonErrorCode;
  /** 最近一次子进程退出信息，用于诊断。 */
  lastExit?: { code: number | null; signal: string | null };
}

/** 壳的版本信息。 */
export interface ShellVersion {
  /** 桌面壳自身版本（package.json）。 */
  app: string;
  electron: string;
  chromium: string;
  node: string;
  platform: string;
  arch: string;
}

/**
 * preload 通过 contextBridge 暴露给渲染进程的 window.gogosling。
 * 基础连接与可选诊断成员；apps/web 的 global.d.ts 应与之保持一致。
 */
export interface GogoslingBridge {
  workspaceBrowser?: WorkspaceBrowserBridge;
  /** 守护进程就绪时返回连接信息（打包后为 proxy 模式、不含令牌），否则 null。 */
  getConnection(): Promise<ConnectionInfo | null>;
  /** 订阅守护进程状态；订阅后会立即收到一次当前状态。返回取消订阅函数。 */
  onDaemonStatus(callback: (status: DaemonStatus) => void): () => void;
  /** 只接受 http / https / mailto；其他协议会被拒绝（Promise reject）。 */
  openExternal(url: string): Promise<void>;
  getVersion(): Promise<ShellVersion>;
  diagnosticsStatus(): Promise<DiagnosticStatus>;
  diagnosticsPreview(category: DiagnosticCategory): Promise<DiagnosticResult<DiagnosticPreview>>;
  diagnosticsTaskPreview(request: DiagnosticTaskRequest): Promise<DiagnosticResult<DiagnosticPreview>>;
  diagnosticsCancelPreparation(requestId: string): Promise<void>;
  diagnosticsSend(id: string): Promise<DiagnosticResult<DiagnosticReceipt>>;
  diagnosticsCancel(id: string, discard: boolean): Promise<void>;
}
