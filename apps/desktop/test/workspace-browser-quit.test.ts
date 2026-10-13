import { describe, expect, it, vi } from "vitest";
import { WorkspaceBrowserQuitGate } from "../src/main/workspace-browser-quit.js";

const tick = () => new Promise(resolve => setImmediate(resolve));

describe("网页退出检查", () => {
  it("异步关闭失败后可以重试，退出延迟到原生回调外", async () => {
    const closeBrowser = vi.fn().mockRejectedValueOnce(new Error("closed window")).mockResolvedValue(true);
    const quit = vi.fn();
    const gate = new WorkspaceBrowserQuitGate({ closeBrowser, quit });
    const event = { preventDefault: vi.fn() };
    expect(gate.beforeQuit(event)).toBe(false);
    await tick();
    expect(quit).not.toHaveBeenCalled();
    expect(gate.beforeQuit(event)).toBe(false);
    expect(quit).not.toHaveBeenCalled();
    await tick();
    await tick();
    expect(quit).toHaveBeenCalledTimes(1);
    expect(gate.beforeQuit(event)).toBe(true);
  });
  it("重复退出共用检查，取消不会进入守护进程停止阶段", async () => {
    let finish!: (allowed: boolean) => void;
    const closeBrowser = vi.fn(() => new Promise<boolean>(resolve => { finish = resolve; }));
    const quit = vi.fn();
    const gate = new WorkspaceBrowserQuitGate({ closeBrowser, quit });
    const event = { preventDefault: vi.fn() };
    expect(gate.beforeQuit(event)).toBe(false);
    expect(gate.beforeQuit(event)).toBe(false);
    await tick();
    expect(closeBrowser).toHaveBeenCalledTimes(1);
    finish(false);
    await tick();
    expect(quit).not.toHaveBeenCalled();
    expect(gate.beforeQuit(event)).toBe(false);
    await tick();
    expect(closeBrowser).toHaveBeenCalledTimes(2);
    finish(false);
  });
});
