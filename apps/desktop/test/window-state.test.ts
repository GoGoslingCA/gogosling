import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createWindowStateSaver, readWindowState, restoreWindowState, writeWindowState } from "../src/main/window-state.js";

const primary = { x: 0, y: 24, width: 1920, height: 1056 };

describe("restoreWindowState", () => {
  it("新窗口默认在主显示器居中并使用适合并排阅读的尺寸", () => {
    expect(restoreWindowState(undefined, [primary])).toEqual({
      x: 240, y: 102, width: 1440, height: 900, maximized: false,
    });
  });

  it("保留仍然可见的副屏位置、尺寸与最大化偏好", () => {
    const state = { x: -1600, y: 40, width: 1200, height: 800, maximized: true };
    expect(restoreWindowState(state, [primary, { x: -1920, y: 0, width: 1920, height: 1080 }])).toEqual(state);
  });

  it("拔掉副屏后将窗口完整移回主屏工作区", () => {
    expect(restoreWindowState({ x: -1800, y: 20, width: 1200, height: 800, maximized: true }, [primary])).toEqual({
      x: 360, y: 152, width: 1200, height: 800, maximized: true,
    });
  });

  it("屏幕分辨率缩小时限制尺寸并保留标题栏可见", () => {
    expect(restoreWindowState({ x: 600, y: -30, width: 1500, height: 1000 }, [{ x: 0, y: 24, width: 1024, height: 700 }])).toEqual({
      x: 0, y: 24, width: 1024, height: 700, maximized: false,
    });
  });

  it("允许 720 像素窄窗口且极小工作区仍完整可见", () => {
    expect(restoreWindowState({ x: 50, y: 50, width: 720, height: 600 }, [primary]).width).toBe(720);
    expect(restoreWindowState(undefined, [{ x: 0, y: 0, width: 640, height: 480 }])).toEqual({
      x: 0, y: 0, width: 640, height: 480, maximized: false,
    });
  });

  it.each([null, [], { x: 0, y: 0, width: 0, height: 800 }, { x: "0", y: 0, width: 1200, height: 800 }, { x: Number.NaN, y: 0, width: 1200, height: 800 }])(
    "忽略无效偏好而安全恢复默认窗口：%j", (value) => {
      expect(restoreWindowState(value, [primary])).toEqual(restoreWindowState(undefined, [primary]));
    },
  );
});

describe("readWindowState / writeWindowState", () => {
  const directories: string[] = [];
  afterEach(() => {
    for (const directory of directories.splice(0)) fs.rmSync(directory, { recursive: true, force: true });
  });
  const temporaryPath = () => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), "gogosling-window-state-"));
    directories.push(directory);
    return path.join(directory, "ui", "window-state.json");
  };

  it("只保存窗口字段并以私有权限原子替换偏好文件", () => {
    const file = temporaryPath();
    const state = { x: 60, y: 60, width: 1000, height: 700, maximized: true, unrelated: "不会保存" };
    writeWindowState(file, state);
    expect(readWindowState(file, [primary])).toEqual({ x: 60, y: 60, width: 1000, height: 700, maximized: true });
    expect(fs.readFileSync(file, "utf8")).not.toContain("unrelated");
    expect(fs.existsSync(`${file}.tmp`)).toBe(false);
    if (process.platform !== "win32") {
      expect(fs.statSync(file).mode & 0o777).toBe(0o600);
      expect(fs.statSync(path.dirname(file)).mode & 0o777).toBe(0o700);
      fs.chmodSync(file, 0o644);
    }
    writeWindowState(file, { ...state, x: 120 });
    expect(readWindowState(file, [primary]).x).toBe(120);
    if (process.platform !== "win32") expect(fs.statSync(file).mode & 0o777).toBe(0o600);
  });

  it("缺失、损坏与过大文件均不会阻止启动", () => {
    const file = temporaryPath();
    const fallback = restoreWindowState(undefined, [primary]);
    expect(readWindowState(file, [primary])).toEqual(fallback);
    fs.mkdirSync(path.dirname(file));
    fs.writeFileSync(file, "{bad json");
    expect(readWindowState(file, [primary])).toEqual(fallback);
    fs.writeFileSync(file, JSON.stringify({ x: 0, y: 0, width: 900, height: 700, extra: "x".repeat(4096) }));
    expect(readWindowState(file, [primary])).toEqual(fallback);
  });
});

describe("createWindowStateSaver", () => {
  afterEach(() => vi.useRealTimers());

  it("连续调整窗口只定期保存最新状态，退出时立即写入并清理定时器", () => {
    vi.useFakeTimers();
    const save = vi.fn();
    const saver = createWindowStateSaver(save);
    saver.schedule();
    vi.advanceTimersByTime(100);
    saver.schedule();
    vi.advanceTimersByTime(150);
    expect(save).toHaveBeenCalledTimes(1);
    saver.schedule();
    saver.flush();
    expect(save).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(500);
    expect(save).toHaveBeenCalledTimes(2);
    saver.schedule();
    saver.cancel();
    vi.advanceTimersByTime(500);
    expect(save).toHaveBeenCalledTimes(2);
  });
});
