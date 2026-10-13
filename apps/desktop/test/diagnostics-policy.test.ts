import { describe, it, expect } from "vitest";
import { releaseDiagnosticsEnabled } from "../src/main/diagnostics-policy.js";
import { installDiagnosticsIpc } from "../src/main/diagnostics-ipc.js";
import { DesktopDiagnostics } from "../src/main/diagnostics.js";
import { IPC } from "../src/shared/ipc.js";

describe("release diagnostics boundary", () => {
  const metadata = { gogoslingDiagnostics: { version: 1, enabled: true } };
  it("源码与开发地址不会读取或启用发布标记", () => {
    const readMetadata = () => { throw new Error("must not read"); };
    expect(releaseDiagnosticsEnabled({ isPackaged: false, hasDevOverride: false, readMetadata })).toBe(false);
    expect(releaseDiagnosticsEnabled({ isPackaged: true, hasDevOverride: true, readMetadata })).toBe(false);
    expect(releaseDiagnosticsEnabled({ isPackaged: false, hasDevOverride: false, readMetadata: () => metadata })).toBe(false);
  });
  it("只有明确版本标记的原生包启用，标记缺失损坏与字符串布尔值全部关闭", () => {
    for (const value of [null, {}, { gogoslingDiagnostics: true }, { gogoslingDiagnostics: { version: 1, enabled: "true" } }]) {
      expect(releaseDiagnosticsEnabled({ isPackaged: true, hasDevOverride: false, readMetadata: () => value })).toBe(false);
    }
    expect(releaseDiagnosticsEnabled({ isPackaged: true, hasDevOverride: false, readMetadata: () => metadata })).toBe(true);
    expect(releaseDiagnosticsEnabled({ isPackaged: true, hasDevOverride: false, readMetadata: () => ({ gogoslingDiagnostics: { version: "1", enabled: true } }) })).toBe(true);
    expect(releaseDiagnosticsEnabled({ isPackaged: true, hasDevOverride: false, readMetadata: () => { throw new Error("unreadable"); } })).toBe(false);
  });
  function register(enabled: boolean) {
    const handlers = new Map<string, (event: { trusted: boolean }, ...args: unknown[]) => unknown>();
    const diagnostics = new DesktopDiagnostics({ enabled, version: "0.1.0", platform: "darwin", arch: "arm64", snapshot: () => ({}) });
    installDiagnosticsIpc<{ trusted: boolean }>({ diagnostics, handle: (name, listener) => handlers.set(name, listener), assertTrusted: event => {
      if (!event.trusted) throw new Error("untrusted sender");
    } });
    return { handlers, diagnostics };
  }
  it("源码不注册预览、发送与取消通道", () => {
    const { handlers } = register(false);
    expect([...handlers.keys()]).toEqual([IPC.diagnosticsStatus]);
    expect(handlers.get(IPC.diagnosticsStatus)!({ trusted: true })).toEqual({ available: false });
  });
  it("所有通道在读取任何诊断前拒绝不可信页面", () => {
    const { handlers, diagnostics } = register(true);
    expect(handlers.size).toBe(4);
    for (const handle of handlers.values()) expect(() => handle({ trusted: false }, "application", true)).toThrow("untrusted sender");
    expect(handlers.get(IPC.diagnosticsPreview)!({ trusted: true }, "not-a-category")).toEqual({ ok: false, code: "invalid_request" });
    diagnostics.dispose();
  });
});
