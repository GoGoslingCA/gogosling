import { describe, expect, it } from "vitest";
import { autostartChecked, loginItemQuery, loginItemWrite, startHiddenAtLaunch } from "../src/main/autostart.js";

describe("登录项读写参数", () => {
  it("Windows：读取时传与写入时完全相同的 args（否则 openAtLogin 永远为 false）", () => {
    const write = loginItemWrite("win32", true);
    const query = loginItemQuery("win32");
    expect(write).toEqual({ openAtLogin: true, args: ["--hidden"] });
    expect(query).toEqual({ args: write.args });
    expect(loginItemWrite("win32", false)).toEqual({ openAtLogin: false, args: ["--hidden"] });
  });

  it("macOS：args 不生效，不传", () => {
    expect(loginItemWrite("darwin", true)).toEqual({ openAtLogin: true });
    expect(loginItemQuery("darwin")).toBeUndefined();
  });
});

describe("autostartChecked", () => {
  it("openAtLogin 为真即勾选", () => {
    expect(autostartChecked("win32", { openAtLogin: true })).toBe(true);
    expect(autostartChecked("darwin", { openAtLogin: true })).toBe(true);
    expect(autostartChecked("darwin", { openAtLogin: false })).toBe(false);
  });

  it("Windows：参数不同的旧 Run 项（executableWillLaunchAtLogin）也显示为已开启，用户才能关掉它", () => {
    expect(autostartChecked("win32", { openAtLogin: false, executableWillLaunchAtLogin: true })).toBe(true);
    expect(autostartChecked("win32", { openAtLogin: false, executableWillLaunchAtLogin: false })).toBe(false);
    expect(autostartChecked("darwin", { openAtLogin: false, executableWillLaunchAtLogin: true })).toBe(false);
  });
});

describe("startHiddenAtLaunch", () => {
  it("命令行带 --hidden（Windows 登录项）时隐藏", () => {
    expect(startHiddenAtLaunch(["app", "--hidden"], "win32", null)).toBe(true);
    expect(startHiddenAtLaunch(["app"], "win32", { wasOpenedAtLogin: true })).toBe(false);
  });

  it("macOS：由登录项拉起（wasOpenedAtLogin）时隐藏，argv 里没有 --hidden 也一样", () => {
    expect(startHiddenAtLaunch(["app"], "darwin", { wasOpenedAtLogin: true })).toBe(true);
    expect(startHiddenAtLaunch(["app"], "darwin", { wasOpenedAtLogin: false })).toBe(false);
    expect(startHiddenAtLaunch(["app"], "darwin", null)).toBe(false);
  });

  it("Linux 只看命令行", () => {
    expect(startHiddenAtLaunch(["app"], "linux", { wasOpenedAtLogin: true })).toBe(false);
  });
});
