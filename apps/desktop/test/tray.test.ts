import { describe, expect, it } from "vitest";
import type { KeyboardEvent as ElectronKeyboardEvent, MenuItem } from "electron";
import type { DaemonStatus } from "../src/shared/ipc.js";
import { autostartSupported, buildTrayMenuTemplate, describeDaemonStatus, trayTooltip, type TrayActions } from "../src/main/tray.js";

const ready: DaemonStatus = { state: "ready", owned: true, attempt: 0, maxAttempts: 5, pid: 1, endpoint: "http://127.0.0.1:46750" };

function actions(log: string[]): TrayActions {
  return {
    open: () => log.push("open"),
    restartDaemon: () => log.push("restart"),
    quit: () => log.push("quit"),
    setAutostart: (on) => log.push(`autostart:${on}`),
  };
}

describe("托盘菜单", () => {
  it("中文菜单：打开 / 重启 / 自启动 / 退出，并正确接线", () => {
    const log: string[] = [];
    const items = buildTrayMenuTemplate({ locale: "zh", status: ready, autostart: { supported: true, enabled: true, checked: true } }, actions(log));

    expect(items.map((i) => i.label ?? i.type)).toEqual(["打开 gogosling", "重启守护进程", "separator", "开机自启动", "separator", "退出"]);
    const fire = (index: number, checked = false) =>
      items[index]!.click?.({ checked } as unknown as MenuItem, undefined, {} as ElectronKeyboardEvent);
    fire(0);
    fire(1);
    fire(3, false);
    fire(5);
    expect(log).toEqual(["open", "restart", "autostart:false", "quit"]);
    expect(items[3]).toMatchObject({ type: "checkbox", checked: true, enabled: true });
  });

  it("不支持自启动的平台不显示该项", () => {
    const items = buildTrayMenuTemplate({ locale: "en", status: ready, autostart: { supported: false, enabled: false, checked: false } }, actions([]));
    expect(items.map((i) => i.label ?? i.type)).toEqual(["Open gogosling", "Restart daemon", "separator", "Quit"]);
  });

  it("启动中 / 停止中 / 重启中时禁用「重启」", () => {
    for (const state of ["starting", "stopping", "restarting", "attaching", "locating"] as const) {
      const items = buildTrayMenuTemplate({ locale: "zh", status: { ...ready, state }, autostart: { supported: false, enabled: false, checked: false } }, actions([]));
      expect(items[1]!.enabled).toBe(false);
    }
    const items = buildTrayMenuTemplate({ locale: "zh", status: { ...ready, state: "failed" }, autostart: { supported: false, enabled: false, checked: false } }, actions([]));
    expect(items[1]!.enabled).toBe(true);
  });

  it("开发态自启动项可见但禁用", () => {
    const items = buildTrayMenuTemplate({ locale: "zh", status: ready, autostart: { supported: true, enabled: false, checked: false } }, actions([]));
    expect(items[3]).toMatchObject({ label: "开机自启动", enabled: false, checked: false });
  });

  it("提示文字反映状态", () => {
    expect(trayTooltip("zh", ready)).toBe("gogosling — 守护进程运行中");
    expect(describeDaemonStatus("en", { ...ready, state: "restarting", attempt: 2 })).toBe("daemon restarting (2/5)");
  });

  it("只有 macOS 与 Windows 支持自启动开关", () => {
    expect(autostartSupported("darwin")).toBe(true);
    expect(autostartSupported("win32")).toBe(true);
    expect(autostartSupported("linux")).toBe(false);
  });
});
