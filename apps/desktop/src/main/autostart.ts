// 开机自启动的平台差异（纯逻辑；main.ts 只负责把结果交给 app.setLoginItemSettings / getLoginItemSettings）。
//
// Electron 的登录项 API 在两个平台上语义不同：
//   - Windows：写注册表 Run 项，值是 "exe" + args。读取时 getLoginItemSettings 要传**同样的** args
//     才能比对上，否则 openAtLogin 永远是 false——托盘勾选框一直未勾选，用户再点一次又写入一次，
//     自启动就关不掉了（XP-10）。
//   - macOS：args 不生效（类型标注 @platform win32），登录启动时 argv 里没有 --hidden；
//     openAsHidden 在 macOS 13 起被系统忽略，Electron 也已从类型里移除，所以改用
//     getLoginItemSettings().wasOpenedAtLogin 判断"本次是登录项拉起的"，据此隐藏启动（XP-14）。

import { HIDDEN_ARG, shouldStartHidden } from "./shell-security.js";

/** setLoginItemSettings 的参数（Electron Settings 的子集）。 */
export interface LoginItemWrite {
  openAtLogin: boolean;
  args?: string[];
}

/** getLoginItemSettings 的参数（Electron LoginItemSettingsOptions 的子集）。 */
export interface LoginItemQuery {
  args?: string[];
}

/** getLoginItemSettings 返回值里我们关心的字段。 */
export interface LoginItemState {
  openAtLogin: boolean;
  /** macOS：本次是否由登录项拉起。 */
  wasOpenedAtLogin?: boolean;
  /** Windows：可执行文件是否会在登录时以任意参数启动（不比对 args）。 */
  executableWillLaunchAtLogin?: boolean;
}

/** 登录项携带的启动参数：只有 Windows 支持。 */
export function loginItemArgs(platform: NodeJS.Platform): string[] | undefined {
  return platform === "win32" ? [HIDDEN_ARG] : undefined;
}

/** 写入登录项时的参数。 */
export function loginItemWrite(platform: NodeJS.Platform, enabled: boolean): LoginItemWrite {
  const args = loginItemArgs(platform);
  return args ? { openAtLogin: enabled, args } : { openAtLogin: enabled };
}

/** 读取登录项时的参数：必须与写入时的 args 相同（Windows 按完整命令行比对）。 */
export function loginItemQuery(platform: NodeJS.Platform): LoginItemQuery | undefined {
  const args = loginItemArgs(platform);
  return args ? { args } : undefined;
}

/**
 * 托盘勾选框的状态。Windows 上再兜底看 executableWillLaunchAtLogin：旧版本以不同参数写入的
 * Run 项也算"已开启"，用户取消勾选时 setLoginItemSettings({openAtLogin:false}) 会按名称删除它。
 */
export function autostartChecked(platform: NodeJS.Platform, state: LoginItemState): boolean {
  if (state.openAtLogin) return true;
  return platform === "win32" && state.executableWillLaunchAtLogin === true;
}

/** 本次启动是否只驻留托盘：命令行带 --hidden（Windows 登录项），或 macOS 由登录项拉起。 */
export function startHiddenAtLaunch(argv: readonly string[], platform: NodeJS.Platform, loginItem: Pick<LoginItemState, "wasOpenedAtLogin"> | null): boolean {
  if (shouldStartHidden(argv)) return true;
  return platform === "darwin" && loginItem?.wasOpenedAtLogin === true;
}
