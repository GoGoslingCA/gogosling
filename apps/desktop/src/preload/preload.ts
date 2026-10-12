// 渲染进程与主进程之间的最小桥：只暴露四个函数。
// getConnection 打包后返回 { endpoint: "app://gogosling", token: "", mode: "proxy" }：页面同源访问、
// 主进程代理并注入令牌，令牌不进入页面；只有开发模式（mode: "direct"）才把令牌交给页面内存。
//
// 沙箱 preload 不能 require 本地文件，所以通道名在这里用字面量声明，
// 并用 `satisfies IpcChannelMap` 让编译器保证与 src/shared/ipc.ts 完全一致。
// 本文件不得 import 任何运行时模块（除 electron）。

import { contextBridge, ipcRenderer } from "electron";
import type { ConnectionInfo, DaemonStatus, GogoslingBridge, IpcChannelMap, ShellVersion } from "../shared/ipc.js";

const CHANNELS = {
  getConnection: "gogosling:connection:get",
  getVersion: "gogosling:version:get",
  openExternal: "gogosling:shell:open-external",
  daemonStatus: "gogosling:daemon:status",
  daemonStatusSubscribe: "gogosling:daemon:status:subscribe",
} as const satisfies IpcChannelMap;

const bridge: GogoslingBridge = {
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
};

contextBridge.exposeInMainWorld("gogosling", bridge);
