// 守护进程尚未就绪时的原生诊断确认；不依赖网页或本地 HTTP 连接。
import type { MessageBoxOptions, MessageBoxReturnValue } from "electron";
import type { DesktopDiagnostics } from "./diagnostics.js";
import { t, type ShellLocale } from "./i18n.js";

/** macOS 必须绑定窗口，否则原生弹窗同步阻塞且不能由 AbortSignal 关闭。 */
export function parentedDiagnosticDialog<Parent extends { isDestroyed(): boolean }>(
  parent: Parent,
  show: (parent: Parent, options: MessageBoxOptions) => Promise<MessageBoxReturnValue>,
): (options: MessageBoxOptions) => Promise<MessageBoxReturnValue> {
  return options => {
    if (parent.isDestroyed()) return Promise.reject(new Error("diagnostic window closed"));
    return show(parent, options);
  };
}

/** 预览、明确确认、上传中取消；任何原生弹窗异常也会撤销当前请求。 */
export async function reviewNativeDiagnostics(
  diagnostics: DesktopDiagnostics,
  locale: ShellLocale,
  show: (options: MessageBoxOptions) => Promise<MessageBoxReturnValue>,
): Promise<void> {
  const prepared = diagnostics.preview("connection");
  if (!prepared.ok) return;
  const preview = prepared.value;
  const title = t(locale, "diagnostics.title");
  try {
    const consent = await show({
      type: "info", title, message: title,
      detail: `${t(locale, "diagnostics.privacy", { destination: preview.destination })}\n\n${JSON.stringify(JSON.parse(preview.body), null, 2)}`,
      buttons: [t(locale, "dialog.dismiss"), t(locale, "diagnostics.send")],
      defaultId: 0, cancelId: 0, noLink: true,
    });
    if (consent.response !== 1) return;
    for (;;) {
      const progress = new AbortController();
      let finished = false;
      const closed = show({
        type: "info", title, message: t(locale, "diagnostics.sending"), detail: t(locale, "diagnostics.cancelHint"),
        buttons: [t(locale, "diagnostics.cancel")], defaultId: 0, cancelId: 0, signal: progress.signal,
      }).then(() => { if (!finished) diagnostics.cancel(preview.id, false); }, () => { if (!finished) diagnostics.cancel(preview.id, false); });
      const result = await diagnostics.send(preview.id);
      finished = true;
      progress.abort();
      await closed;
      if (!result.ok && result.code === "cancelled") return;
      if (result.ok) {
        await show({ type: "info", title, message: t(locale, "diagnostics.sent"), buttons: [t(locale, "dialog.ok")] });
        return;
      }
      const retryable = !["disabled", "stale_preview", "invalid_request"].includes(result.code);
      const retry = await show({
        type: "warning", title, message: t(locale, "diagnostics.failed"),
        detail: result.retryAfterSeconds ? t(locale, "diagnostics.retryAfter", { seconds: result.retryAfterSeconds }) : t(locale, "diagnostics.retryHint"),
        buttons: [t(locale, "dialog.dismiss"), ...(retryable ? [t(locale, "dialog.retry")] : [])],
        defaultId: 0, cancelId: 0, noLink: true,
      });
      if (!retryable || retry.response !== 1) return;
    }
  } catch {
    // 窗口关闭或原生弹窗被系统撤销：只清理，不在退出流程中产生未处理拒绝。
  } finally {
    diagnostics.cancel(preview.id, true);
  }
}
