// 网页 IPC 仅向可信主窗口开放；远程 WebContents 没有桥接且不能调用这些通道。
import { IPC } from "../shared/ipc.js";
import type { WorkspaceBrowser } from "./workspace-browser.js";

/** 注入注册与信任检查，便于测试每个通道的发送者校验。 */
export function installWorkspaceBrowserIpc<Event>(options: {
  handle(channel: string, listener: (event: Event, payload?: unknown) => unknown): void;
  assertTrusted(event: Event): void;
  browser: Pick<WorkspaceBrowser, "show" | "hide" | "close" | "navigate" | "action" | "current">;
}): void {
  const handlers: [string, (payload?: unknown) => unknown][] = [
    [IPC.workspaceBrowserCurrent, () => options.browser.current()],
    [IPC.workspaceBrowserShow, input => options.browser.show(input)],
    [IPC.workspaceBrowserHide, () => options.browser.hide()],
    [IPC.workspaceBrowserClose, input => options.browser.close(input)],
    [IPC.workspaceBrowserNavigate, input => options.browser.navigate(input)],
    [IPC.workspaceBrowserAction, input => options.browser.action(input)],
  ];
  for (const [channel, handler] of handlers) options.handle(channel, (event, payload) => {
    options.assertTrusted(event);
    return handler(payload);
  });
}
