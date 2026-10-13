// 渲染进程与主进程之间的最小桥；诊断通道只传递脱敏预览和枚举参数。
// getConnection 打包后返回 { endpoint: "app://gogosling", token: "", mode: "proxy" }：页面同源访问、
// 主进程代理并注入令牌，令牌不进入页面；只有开发模式（mode: "direct"）才把令牌交给页面内存。
//
// 沙箱 preload 不能 require 本地文件，所以通道名在这里用字面量声明，
// 并用 `satisfies IpcChannelMap` 让编译器保证与 src/shared/ipc.ts 完全一致。
// 本文件不得 import 任何运行时模块（除 electron）。

import { contextBridge, ipcRenderer } from "electron";
import type { ConnectionInfo, DaemonStatus, GogoslingBridge, IpcChannelMap, ShellVersion } from "../shared/ipc.js";
import type { DiagnosticPreview, DiagnosticReceipt, DiagnosticResult, DiagnosticStatus } from "../shared/diagnostics.js";
import type { WorkspaceBrowserState } from "../shared/workspace-browser.js";

const CHANNELS = {
  getConnection: "gogosling:connection:get",
  getVersion: "gogosling:version:get",
  openExternal: "gogosling:shell:open-external",
  daemonStatus: "gogosling:daemon:status",
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
} as const satisfies IpcChannelMap;

const bridge: GogoslingBridge = {
  workspaceBrowser: {
    current: () => ipcRenderer.invoke(CHANNELS.workspaceBrowserCurrent) as Promise<WorkspaceBrowserState | null>,
    show: request => ipcRenderer.invoke(CHANNELS.workspaceBrowserShow, request) as Promise<WorkspaceBrowserState>,
    hide: () => ipcRenderer.invoke(CHANNELS.workspaceBrowserHide) as Promise<void>,
    close: tabId => ipcRenderer.invoke(CHANNELS.workspaceBrowserClose, tabId) as Promise<WorkspaceBrowserState | null>,
    navigate: request => ipcRenderer.invoke(CHANNELS.workspaceBrowserNavigate, request) as Promise<WorkspaceBrowserState>,
    action: request => ipcRenderer.invoke(CHANNELS.workspaceBrowserAction, request) as Promise<WorkspaceBrowserState>,
    onState: callback => {
      const listener = (_event: Electron.IpcRendererEvent, state: WorkspaceBrowserState) => callback(state);
      ipcRenderer.on(CHANNELS.workspaceBrowserState, listener);
      return () => ipcRenderer.removeListener(CHANNELS.workspaceBrowserState, listener);
    },
  },
  getConnection: () => ipcRenderer.invoke(CHANNELS.getConnection) as Promise<ConnectionInfo | null>,

  onDaemonStatus: (callback) => {
    const listener = (_event: Electron.IpcRendererEvent, status: DaemonStatus) => {
      callback(status);
    };
    ipcRenderer.on(CHANNELS.daemonStatus, listener);
    ipcRenderer.send(CHANNELS.daemonStatusSubscribe);
    return () => {
      ipcRenderer.removeListener(CHANNELS.daemonStatus, listener);
    };
  },

  openExternal: (url) => ipcRenderer.invoke(CHANNELS.openExternal, String(url)) as Promise<void>,

  getVersion: () => ipcRenderer.invoke(CHANNELS.getVersion) as Promise<ShellVersion>,
  diagnosticsStatus: () => ipcRenderer.invoke(CHANNELS.diagnosticsStatus) as Promise<DiagnosticStatus>,
  diagnosticsPreview: category => ipcRenderer.invoke(CHANNELS.diagnosticsPreview, category) as Promise<DiagnosticResult<DiagnosticPreview>>,
  diagnosticsTaskPreview: request => ipcRenderer.invoke(CHANNELS.diagnosticsTaskPreview, request) as Promise<DiagnosticResult<DiagnosticPreview>>,
  diagnosticsCancelPreparation: requestId => ipcRenderer.invoke(CHANNELS.diagnosticsCancelPreparation, requestId) as Promise<void>,
  diagnosticsSend: id => ipcRenderer.invoke(CHANNELS.diagnosticsSend, id) as Promise<DiagnosticResult<DiagnosticReceipt>>,
  diagnosticsCancel: (id, discard) => ipcRenderer.invoke(CHANNELS.diagnosticsCancel, id, discard) as Promise<void>,
};

contextBridge.exposeInMainWorld("gogosling", bridge);
