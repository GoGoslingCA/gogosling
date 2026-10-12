import { describe, expect, it, vi } from "vitest";
import { QuitFlow } from "../src/main/quit-flow.js";

function setup() {
  let completeStop!: () => void;
  const stopped = new Promise<void>((resolve) => { completeStop = resolve; });
  const stopDaemon = vi.fn(() => stopped);
  const quit = vi.fn();
  const onStopError = vi.fn();
  const flow = new QuitFlow({ stopDaemon, quit, onStopError });
  return { flow, stopDaemon, quit, onStopError, completeStop, stopped };
}

describe("桌面退出流程", () => {
  it("只在全部窗口完成卸载后停止服务，停止完成才最终退出", async () => {
    const { flow, stopDaemon, quit, completeStop, stopped } = setup();
    flow.beforeQuit();
    expect(flow.isQuitting).toBe(true);
    expect(stopDaemon).not.toHaveBeenCalled();
    const allowUnload = { preventDefault: vi.fn() };
    flow.preventedUnload(allowUnload, true);
    expect(allowUnload.preventDefault).toHaveBeenCalledOnce();
    expect(stopDaemon).not.toHaveBeenCalled();

    const willQuit = { preventDefault: vi.fn() };
    flow.willQuit(willQuit);
    expect(willQuit.preventDefault).toHaveBeenCalledOnce();
    expect(stopDaemon).toHaveBeenCalledOnce();
    expect(quit).not.toHaveBeenCalled();
    completeStop();
    await stopped;
    expect(quit).toHaveBeenCalledOnce();
  });

  it("取消未保存修改确认时保持服务并恢复正常状态，之后仍可重试退出", async () => {
    const { flow, stopDaemon, quit, completeStop, stopped } = setup();
    flow.beforeQuit();
    const unload = { preventDefault: vi.fn() };
    flow.preventedUnload(unload, false);
    expect(unload.preventDefault).not.toHaveBeenCalled();
    expect(flow.isQuitting).toBe(false);
    expect(stopDaemon).not.toHaveBeenCalled();
    expect(quit).not.toHaveBeenCalled();

    flow.beforeQuit();
    flow.preventedUnload(unload, true);
    flow.willQuit({ preventDefault: vi.fn() });
    completeStop();
    await stopped;
    expect(stopDaemon).toHaveBeenCalledOnce();
    expect(quit).toHaveBeenCalledOnce();
  });

  it("停止期间重复退出不会重复停止服务，最终退出不会形成循环", async () => {
    const { flow, stopDaemon, quit, completeStop, stopped } = setup();
    flow.beforeQuit();
    flow.willQuit({ preventDefault: vi.fn() });
    flow.beforeQuit();
    flow.willQuit({ preventDefault: vi.fn() });
    expect(stopDaemon).toHaveBeenCalledOnce();
    completeStop();
    await stopped;
    expect(quit).toHaveBeenCalledOnce();

    flow.beforeQuit();
    const finalQuit = { preventDefault: vi.fn() };
    flow.willQuit(finalQuit);
    expect(finalQuit.preventDefault).not.toHaveBeenCalled();
    expect(stopDaemon).toHaveBeenCalledOnce();
  });

  it("普通页面重载取消或确认均不会启动退出流程", () => {
    const { flow, stopDaemon, quit } = setup();
    flow.preventedUnload({ preventDefault: vi.fn() }, false);
    flow.preventedUnload({ preventDefault: vi.fn() }, true);
    expect(flow.isQuitting).toBe(false);
    expect(stopDaemon).not.toHaveBeenCalled();
    expect(quit).not.toHaveBeenCalled();
  });

  it("停止服务失败会记录错误并完成退出，不会再次触发停止", async () => {
    const failure = new Error("stop failed");
    const stopDaemon = vi.fn(async () => { throw failure; });
    const quit = vi.fn();
    const onStopError = vi.fn();
    const flow = new QuitFlow({ stopDaemon, quit, onStopError });
    flow.beforeQuit();
    flow.willQuit({ preventDefault: vi.fn() });
    await Promise.resolve();
    expect(onStopError).toHaveBeenCalledWith(failure);
    expect(quit).toHaveBeenCalledOnce();
    flow.willQuit({ preventDefault: vi.fn() });
    expect(stopDaemon).toHaveBeenCalledOnce();
  });
});
