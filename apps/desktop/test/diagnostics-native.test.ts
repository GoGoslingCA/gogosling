import { it, expect, vi } from "vitest";
import { DesktopDiagnostics } from "../src/main/diagnostics.js";
import { parentedDiagnosticDialog, reviewNativeDiagnostics } from "../src/main/diagnostics-native.js";
import type { MessageBoxOptions, MessageBoxReturnValue } from "electron";

function service(fetch: (url: string, init: RequestInit) => Promise<Response>) {
  return new DesktopDiagnostics({ enabled: true, version: "0.1.0", platform: "darwin", arch: "arm64", snapshot: () => ({ daemonState: "failed" }), fetch });
}

it("原生预览取消不会申请凭据或上传，关闭后释放预览", async () => {
  const fetch = vi.fn(); const diagnostics = service(fetch);
  const show = vi.fn(async () => ({ response: 0, checkboxChecked: false }));
  await reviewNativeDiagnostics(diagnostics, "en", show);
  expect(show).toHaveBeenCalledOnce(); expect(fetch).not.toHaveBeenCalled();
  expect((show.mock.calls[0] as unknown as [MessageBoxOptions])[0].defaultId).toBe(0);
  expect(diagnostics.preview("application").ok).toBe(true);
  diagnostics.dispose();
});

it("启动失败也能明确确认后上传，完成关闭进度弹窗", async () => {
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    const response = url.endsWith("tickets") ? {
      token: "synthetic".repeat(8), expires_at: new Date(Date.now() + 60_000).toISOString(), body_sha256: body.body_sha256,
    } : { success: true, accepted: 1, duplicates: 0, event_ids: [(body.events as { event_id: string }[])[0]!.event_id] };
    return new Response(JSON.stringify(response), { status: 201, headers: { "Content-Type": "application/json" } });
  });
  const diagnostics = service(fetch);
  const show = vi.fn((options: MessageBoxOptions): Promise<MessageBoxReturnValue> => {
    if (options.signal) return new Promise(resolve => options.signal!.addEventListener("abort", () => resolve({ response: 0, checkboxChecked: false }), { once: true }));
    return Promise.resolve({ response: options.buttons?.length === 2 ? 1 : 0, checkboxChecked: false });
  });
  await reviewNativeDiagnostics(diagnostics, "en", show);
  expect(fetch).toHaveBeenCalledTimes(2); expect(show).toHaveBeenCalledTimes(3);
  expect(show.mock.calls[2]![0].message).toBe("Diagnostics sent.");
  diagnostics.dispose();
});

it("原生发送中取消会中断请求，不弹出成功提示", async () => {
  let signal: AbortSignal | undefined;
  const fetch = vi.fn((_url: string, init: RequestInit) => { signal = init.signal as AbortSignal; return new Promise<Response>(() => {}); });
  const diagnostics = service(fetch);
  const show = vi.fn(async (options: MessageBoxOptions) => ({ response: options.signal ? 0 : 1, checkboxChecked: false }));
  await reviewNativeDiagnostics(diagnostics, "en", show);
  expect(signal?.aborted).toBe(true); expect(show).toHaveBeenCalledTimes(2);
  expect(diagnostics.preview("application").ok).toBe(true);
  diagnostics.dispose();
});

it("macOS 进度弹窗始终绑定活窗口，关闭窗口后不会回退到同步弹窗", async () => {
  let destroyed = false;
  const parent = { isDestroyed: () => destroyed };
  const show = vi.fn(async () => ({ response: 0, checkboxChecked: false }));
  const display = parentedDiagnosticDialog(parent, show);
  const options = { message: "synthetic", signal: new AbortController().signal };
  await display(options);
  expect(show).toHaveBeenCalledWith(parent, options);
  destroyed = true;
  await expect(display(options)).rejects.toThrow("diagnostic window closed");
  expect(show).toHaveBeenCalledOnce();
});

it("原生上传回执丢失后明确重试沿用同一报告和事件编号", async () => {
  const bodies: string[] = []; let ingests = 0;
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    if (url.endsWith("tickets")) return new Response(JSON.stringify({ token: "synthetic".repeat(8), expires_at: new Date(Date.now() + 60_000).toISOString(), body_sha256: body.body_sha256 }), { status: 201, headers: { "Content-Type": "application/json" } });
    bodies.push(String(init.body)); ingests++;
    if (ingests === 1) throw new Error("synthetic lost acknowledgement");
    return new Response(JSON.stringify({ success: true, accepted: 0, duplicates: 1, event_ids: [(body.events as { event_id: string }[])[0]!.event_id] }), { status: 201, headers: { "Content-Type": "application/json" } });
  });
  const diagnostics = service(fetch);
  const show = vi.fn((options: MessageBoxOptions): Promise<MessageBoxReturnValue> => {
    if (options.signal) return new Promise(resolve => options.signal!.addEventListener("abort", () => resolve({ response: 0, checkboxChecked: false }), { once: true }));
    return Promise.resolve({ response: options.buttons?.length === 2 ? 1 : 0, checkboxChecked: false });
  });
  await reviewNativeDiagnostics(diagnostics, "en", show);
  expect(ingests).toBe(2); expect(bodies[0]).toBe(bodies[1]);
  expect(show.mock.calls.at(-1)![0].message).toBe("Diagnostics sent.");
  diagnostics.dispose();
});
