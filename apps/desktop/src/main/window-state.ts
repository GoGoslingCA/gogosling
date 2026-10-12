// 桌面窗口偏好：仅保存界面尺寸与位置，不读取业务状态或凭据；显示器改变后保证窗口仍可见。

import * as fs from "node:fs";
import * as path from "node:path";

/** 普通窗口与显示器工作区使用的逻辑像素边界。 */
export interface WindowBounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** 只包含可持久化的窗口外观偏好。 */
export interface WindowState extends WindowBounds {
  maximized: boolean;
}

/** 桌面窄窗口也能切换到独立成果页。 */
export const WINDOW_MIN_WIDTH = 720;
/** 保留输入区与任务内容所需的最小高度。 */
export const WINDOW_MIN_HEIGHT = 560;

function validBounds(value: unknown): value is WindowBounds {
  if (!value || typeof value !== "object") return false;
  const bounds = value as Record<string, unknown>;
  return [bounds.x, bounds.y, bounds.width, bounds.height].every((v) => typeof v === "number" && Number.isSafeInteger(v))
    && typeof bounds.width === "number" && bounds.width > 0
    && typeof bounds.height === "number" && bounds.height > 0;
}

function overlap(a: WindowBounds, b: WindowBounds): number {
  return Math.max(0, Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x))
    * Math.max(0, Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y));
}

/** 选择与窗口重叠最多的显示器；完全离屏时回到传入的首个（主）显示器。 */
export function restoreWindowState(value: unknown, workAreas: readonly WindowBounds[]): WindowState {
  const areas = workAreas.filter(validBounds);
  const primary = areas[0] ?? { x: 0, y: 0, width: 1440, height: 900 };
  const saved = validBounds(value) ? value : null;
  const area = saved
    ? areas.reduce((best, candidate) => overlap(saved, candidate) > overlap(saved, best) ? candidate : best, primary)
    : primary;
  const width = Math.min(area.width, Math.max(WINDOW_MIN_WIDTH, saved?.width ?? 1440));
  const height = Math.min(area.height, Math.max(WINDOW_MIN_HEIGHT, saved?.height ?? 900));
  const isOnScreen = saved !== null && overlap(saved, area) > 0;
  return {
    x: isOnScreen ? Math.min(Math.max(saved.x, area.x), area.x + area.width - width) : area.x + Math.round((area.width - width) / 2),
    y: isOnScreen ? Math.min(Math.max(saved.y, area.y), area.y + area.height - height) : area.y + Math.round((area.height - height) / 2),
    width,
    height,
    maximized: saved !== null && (saved as Partial<WindowState>).maximized === true,
  };
}

/** 缺失、损坏或过大的偏好文件只恢复默认窗口，不影响应用启动。 */
export function readWindowState(filePath: string, workAreas: readonly WindowBounds[]): WindowState {
  let value: unknown;
  try {
    if (fs.statSync(filePath).size <= 4096) value = JSON.parse(fs.readFileSync(filePath, "utf8")) as unknown;
  } catch {
    // 偏好不是业务数据；读失败时安全回退。
  }
  return restoreWindowState(value, workAreas);
}

/** 原子替换界面偏好，文件 0600、所在目录 0700；白名单序列化不保留额外字段。 */
export function writeWindowState(filePath: string, state: WindowState): void {
  if (!validBounds(state)) return;
  const directory = path.dirname(filePath);
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  fs.chmodSync(directory, 0o700);
  const temporary = `${filePath}.tmp`;
  const { x, y, width, height } = state;
  try {
    fs.writeFileSync(temporary, JSON.stringify({ x, y, width, height, maximized: state.maximized === true }), { mode: 0o600 });
    fs.chmodSync(temporary, 0o600);
    fs.renameSync(temporary, filePath);
  } finally {
    fs.rmSync(temporary, { force: true });
  }
}

/** 合并连续移动/缩放事件，并在关闭窗口或退出时同步保存最后状态。 */
export function createWindowStateSaver(save: () => void, delay = 250): { schedule(): void; flush(): void; cancel(): void } {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const cancel = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  const flush = () => {
    cancel();
    save();
  };
  return {
    schedule: () => {
      // 节流而非每次事件重置计时，持续拖动也定期保存。
      if (timer === undefined) timer = setTimeout(flush, delay);
    },
    flush,
    cancel,
  };
}
