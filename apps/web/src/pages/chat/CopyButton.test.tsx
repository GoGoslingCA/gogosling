import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/Toast";
import { CopyButton } from "./CopyButton";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

let root: Root;
let container: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  container = document.createElement("div");
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
async function render() {
  await act(async () => root.render(<ToastProvider><CopyButton text={'<literal>\ncode()'} code /></ToastProvider>));
}

describe("正文和代码复制", () => {
  it("复制原文到剪贴板，并短暂显示成功反馈", async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    await render();
    await act(async () => container.querySelector("button")!.click());
    expect(writeText).toHaveBeenCalledWith("<literal>\ncode()");
    expect(container.querySelector("button")!.textContent).toBe("chat.copied");
    await act(async () => vi.advanceTimersByTime(2000));
    expect(container.querySelector("button")!.textContent).toBe("chat.copyCode");
  });
  it("剪贴板拒绝访问时展示失败反馈，不伪报成功", async () => {
    vi.stubGlobal("navigator", { clipboard: { writeText: vi.fn(async () => { throw new Error("denied"); }) } });
    await render();
    await act(async () => container.querySelector("button")!.click());
    expect(container.querySelector(".toast.error")?.textContent).toBe("chat.copyFailed");
    expect(container.querySelector("button")!.textContent).toBe("chat.copyCode");
  });
  it("浏览器没有剪贴板 API 时仍有可操作的失败反馈", async () => {
    vi.stubGlobal("navigator", {});
    await render();
    await act(async () => container.querySelector("button")!.click());
    expect(container.querySelector(".toast.error")?.textContent).toBe("chat.copyFailed");
  });
});
