import { act, StrictMode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { usePanelResize } from "./usePanelResize";

let host: HTMLDivElement;
let root: Root;
let frames: Map<number, FrameRequestCallback>;
let captured: Set<number>;
const onResize = vi.fn();
const onResizeEnd = vi.fn();

function Fixture({ enabled = true, save = true }: { enabled?: boolean; save?: boolean }) {
  const [width, setWidth] = useState(500);
  const resize = usePanelResize({ width, enabled, onResize: next => { onResize(next); setWidth(next); }, onResizeEnd: save ? onResizeEnd : undefined });
  return <div role="separator" aria-valuenow={width} {...resize} />;
}
function mount(enabled = true, save = true) {
  act(() => root.render(<StrictMode><Fixture enabled={enabled} save={save} /></StrictMode>));
  const element = host.querySelector<HTMLDivElement>("[role=separator]")!;
  element.setPointerCapture = id => { captured.add(id); };
  element.hasPointerCapture = id => captured.has(id);
  element.releasePointerCapture = id => { captured.delete(id); };
  return element;
}
function pointer(element: HTMLElement, type: string, x: number, id = 1, button = 0) {
  const event = new MouseEvent(type, { bubbles: true, clientX: x, button });
  Object.defineProperty(event, "pointerId", { value: id });
  element.dispatchEvent(event);
}
function paint() {
  act(() => {
    const pending = [...frames.values()]; frames.clear();
    for (const callback of pending) callback(performance.now());
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  frames = new Map(); captured = new Set(); onResize.mockReset(); onResizeEnd.mockReset();
  let nextId = 0;
  vi.stubGlobal("requestAnimationFrame", vi.fn((callback: FrameRequestCallback) => { frames.set(++nextId, callback); return nextId; }));
  vi.stubGlobal("cancelAnimationFrame", vi.fn((id: number) => { frames.delete(id); }));
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });

describe("面板拖动更新", () => {
  it("同帧多次移动只更新最新宽度，松手立即提交最终坐标而不是旧状态", () => {
    const element = mount();
    act(() => {
      pointer(element, "pointerdown", 800);
      for (let x = 799; x >= 730; x--) pointer(element, "pointermove", x);
    });
    expect(onResize).not.toHaveBeenCalled(); expect(frames.size).toBe(1);
    paint(); expect(onResize.mock.calls).toEqual([[570]]); expect(element.getAttribute("aria-valuenow")).toBe("570");
    act(() => { pointer(element, "pointermove", 720); pointer(element, "pointerup", 750); });
    expect(onResize.mock.calls).toEqual([[570], [550]]); expect(onResizeEnd.mock.calls).toEqual([[550]]);
    expect(frames.size).toBe(0); expect(captured.size).toBe(0); paint(); expect(onResize).toHaveBeenCalledTimes(2);
  });

  it("取消与失去捕获丢弃待更新帧，不保存尚未显示的位置", () => {
    const element = mount();
    for (const type of ["pointercancel", "lostpointercapture"]) {
      act(() => { pointer(element, "pointerdown", 800); pointer(element, "pointermove", 700); pointer(element, type, 700); });
      expect(frames.size).toBe(0); expect(captured.size).toBe(0);
      paint(); act(() => pointer(element, "pointerup", 700));
    }
    expect(onResize).not.toHaveBeenCalled(); expect(onResizeEnd).not.toHaveBeenCalled();
  });

  it("隐藏或卸载会取消回调并释放捕获，再次显示可以继续拖动", () => {
    let element = mount();
    act(() => { pointer(element, "pointerdown", 800); pointer(element, "pointermove", 700); });
    mount(false); expect(frames.size).toBe(0); expect(captured.size).toBe(0); paint();
    element = mount();
    act(() => { pointer(element, "pointerdown", 800); pointer(element, "pointermove", 700); });
    act(() => root.render(null)); expect(frames.size).toBe(0); expect(captured.size).toBe(0); paint();
    expect(onResize).not.toHaveBeenCalled(); expect(onResizeEnd).not.toHaveBeenCalled();
  });

  it("键盘立即调宽并夹紧边界，无保存回调也能使用", () => {
    const element = mount(true, false);
    act(() => {
      for (const key of ["ArrowLeft", "ArrowLeft", "ArrowRight", "Home", "ArrowRight", "End", "ArrowLeft"]) element.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
    });
    expect(onResize.mock.calls).toEqual([[520], [540], [520], [420], [640]]);
    expect(onResizeEnd).not.toHaveBeenCalled(); expect(frames.size).toBe(0);
    expect(element.getAttribute("aria-valuenow")).toBe("640");
  });

  it("键盘调整取消拖动帧，并同步保存新宽度", () => {
    const element = mount();
    act(() => {
      pointer(element, "pointerdown", 800); pointer(element, "pointermove", 700);
      element.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    });
    expect(onResize.mock.calls).toEqual([[480]]); expect(onResizeEnd.mock.calls).toEqual([[480]]); expect(frames.size).toBe(0);
    paint(); expect(onResize).toHaveBeenCalledOnce();
  });

  it("忽略右键和其他指针，最后坐标仍会夹紧范围", () => {
    const element = mount();
    act(() => {
      pointer(element, "pointerdown", 800, 1, 2); pointer(element, "pointerup", 600);
      pointer(element, "pointerdown", 800);
      pointer(element, "pointermove", 700, 2); pointer(element, "pointercancel", 700, 2);
      pointer(element, "pointerup", 400);
    });
    expect(onResize.mock.calls).toEqual([[640]]); expect(onResizeEnd.mock.calls).toEqual([[640]]); expect(frames.size).toBe(0);
  });
});
