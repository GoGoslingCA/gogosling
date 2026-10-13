// 诊断 IPC 复用主窗口顶层框架检查；源码构建不注册预览/发送/取消通道。
import { IPC } from "../shared/ipc.js";
import type { DesktopDiagnostics } from "./diagnostics.js";

/** 注册本地能力查询，以及仅发布构建可用的诊断操作。 */
export function installDiagnosticsIpc<Event>(options: {
  handle: (channel: string, listener: (event: Event, ...args: unknown[]) => unknown) => void;
  assertTrusted: (event: Event) => void;
  diagnostics: Pick<DesktopDiagnostics, "status" | "preview" | "prepareTask" | "cancelPreparation" | "send" | "cancel">;
}): void {
  const { handle, assertTrusted, diagnostics } = options;
  handle(IPC.diagnosticsStatus, event => {
    assertTrusted(event);
    return diagnostics.status();
  });
  if (!diagnostics.status().available) return;
  handle(IPC.diagnosticsPreview, (event, category) => {
    assertTrusted(event);
    return diagnostics.preview(category);
  });
  handle(IPC.diagnosticsTaskPreview, (event, request) => {
    assertTrusted(event);
    return diagnostics.prepareTask(request);
  });
  handle(IPC.diagnosticsCancelPreparation, (event, requestId) => {
    assertTrusted(event);
    diagnostics.cancelPreparation(requestId);
  });
  handle(IPC.diagnosticsSend, (event, id) => {
    assertTrusted(event);
    return diagnostics.send(id);
  });
  handle(IPC.diagnosticsCancel, (event, id, discard) => {
    assertTrusted(event);
    if (typeof discard !== "boolean") return;
    diagnostics.cancel(id, discard);
  });
}
