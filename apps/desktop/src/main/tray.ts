// 托盘菜单与提示文字的构造（纯逻辑）。只 import electron 的类型，运行时不依赖它，便于测试。
//
// 托盘统一提供右键/点击菜单：
// 打开、重启守护进程、开机自启动、退出；左键点击在 Windows/Linux 上直接打开窗口。

import type { MenuItemConstructorOptions } from "electron";
import type { DaemonStatus } from "../shared/ipc.js";
import { canRestart } from "./daemon.js";
import { t, type MessageKey, type ShellLocale } from "./i18n.js";

export interface TrayActions {
  open(): void;
  restartDaemon(): void;
  quit(): void;
  setAutostart(enabled: boolean): void;
}

export interface AutostartModel {
  /** 平台是否支持（macOS / Windows）。 */
  supported: boolean;
  /** 是否允许切换（开发态不允许，否则会把 electron 二进制注册为登录项）。 */
  enabled: boolean;
  checked: boolean;
}

export interface TrayModel {
  locale: ShellLocale;
  status: DaemonStatus;
  autostart: AutostartModel;
}

/** 守护进程状态的一句话描述。 */
export function describeDaemonStatus(locale: ShellLocale, status: DaemonStatus): string {
  const key = `daemon.state.${status.state}` as MessageKey;
  return t(locale, key, { attempt: status.attempt, max: status.maxAttempts });
}

/** 托盘悬停提示。 */
export function trayTooltip(locale: ShellLocale, status: DaemonStatus): string {
  return t(locale, "tray.tooltip", { status: describeDaemonStatus(locale, status) });
}

/** 构造托盘菜单模板。 */
export function buildTrayMenuTemplate(model: TrayModel, actions: TrayActions): MenuItemConstructorOptions[] {
  const { locale, status, autostart } = model;
  const items: MenuItemConstructorOptions[] = [
    { label: t(locale, "tray.open"), click: () => actions.open() },
    { label: t(locale, "tray.restartDaemon"), enabled: canRestart(status.state), click: () => actions.restartDaemon() },
    { type: "separator" },
  ];
  if (autostart.supported) {
    items.push({
      label: t(locale, "tray.autostart"),
      type: "checkbox",
      checked: autostart.checked,
      enabled: autostart.enabled,
      click: (item) => actions.setAutostart(item.checked),
    });
    items.push({ type: "separator" });
  }
  items.push({ label: t(locale, "tray.quit"), click: () => actions.quit() });
  return items;
}

/** 平台是否支持开机自启动开关（Electron 的 setLoginItemSettings 只实现了 macOS 与 Windows）。 */
export function autostartSupported(platform: NodeJS.Platform): boolean {
  return platform === "darwin" || platform === "win32";
}
