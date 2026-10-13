// 桌面壳（apps/desktop）的 preload 通过 contextBridge 注入的最小接口，与 apps/desktop/src/shared/ipc.ts 一致。
// 打包后 getConnection 返回 { endpoint: "app://gogosling", token: "", mode: "proxy" }：页面同源访问，
// 主进程代理并注入令牌，令牌不进入页面；开发模式（mode: "direct"）才给出守护进程地址与令牌（只在内存中）。

import type { TaskDiagnosticsRequest, DiagnosticsCategory, DiagnosticsPreview, DiagnosticsReceipt, DiagnosticsResult } from "./api/diagnostics";

declare global {
  interface GogoslingBridge {
    /** 返回守护进程的连接信息；壳尚未发现守护进程时返回 null。 */
    getConnection?: () => Promise<{ endpoint: string; token: string; mode?: "proxy" | "direct" } | null>;
    /** 发行版可选诊断 IPC；源码版和旧桌面壳可不提供。 */
    diagnosticsStatus?: () => Promise<{ available: boolean; taskReports?: boolean }>;
    diagnosticsPreview?: (category: DiagnosticsCategory) => Promise<DiagnosticsResult<DiagnosticsPreview>>;
    diagnosticsTaskPreview?: (request: TaskDiagnosticsRequest) => Promise<DiagnosticsResult<DiagnosticsPreview>>;
    diagnosticsCancelPreparation?: (requestId: string) => Promise<void>;
    diagnosticsSend?: (id: string) => Promise<DiagnosticsResult<DiagnosticsReceipt>>;
    diagnosticsCancel?: (id: string, discard: boolean) => Promise<void>;
  }

  interface Window {
    gogosling?: GogoslingBridge;
  }
}
