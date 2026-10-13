// 仅保存非业务 UI 偏好。正文、附件、连接地址和令牌不进入浏览器持久存储。
export type Appearance = "system" | "light" | "dark";
const KEY = "gogosling.ui.v1";
export interface UiPreferences {
  appearance: Appearance; artifactWidth: number;
  taskListVisible: boolean; projectListVisible: boolean; expandedProjectId: string | null;
  toolsExpanded: boolean; pinnedSessionIds: string[]; lastSessionId: string | null; lastArtifactId: string | null;
}
const defaults = (): UiPreferences => ({ appearance: "system", artifactWidth: 500, taskListVisible: true, projectListVisible: true, expandedProjectId: null, toolsExpanded: true, pinnedSessionIds: [], lastSessionId: null, lastArtifactId: null });
function resourceID(value: unknown): string | null { return typeof value === "string" && /^[0-9A-Z]{26}$/.test(value) ? value : null; }
function pins(value: unknown): string[] {
  return Array.isArray(value) ? [...new Set(value.filter((item): item is string => typeof item === "string" && /^[0-9A-Z]{26}$/.test(item)))].slice(0, 20) : [];
}

/** 读取并校验旧版本或手工修改过的界面偏好，存储不可用时仍可使用应用。 */
export function readUiPreferences(): UiPreferences {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(KEY) ?? "{}");
    const data = parsed && typeof parsed === "object" ? parsed as Partial<UiPreferences> : {};
    return {
      lastSessionId: resourceID(data.lastSessionId),
      lastArtifactId: resourceID(data.lastArtifactId),
      taskListVisible: data.taskListVisible !== false,
      projectListVisible: data.projectListVisible !== false,
      expandedProjectId: resourceID(data.expandedProjectId),
      toolsExpanded: data.toolsExpanded !== false,
      pinnedSessionIds: pins(data.pinnedSessionIds),
      appearance: data.appearance === "dark" || data.appearance === "light" ? data.appearance : "system",
      artifactWidth: clampArtifactWidth(data.artifactWidth ?? 500),
    };
  } catch { return defaults(); }
}

/** 成果面板遵守 420–640 px 设计边界。 */
export function clampArtifactWidth(width: number): number {
  return Number.isFinite(width) ? Math.min(640, Math.max(420, Math.round(width))) : 500;
}

/** 白名单写入，避免把意外传入的业务数据持久化。 */
export function writeUiPreferences(update: Partial<UiPreferences>): void {
  const prefs = { ...readUiPreferences(), ...update };
  try { localStorage.setItem(KEY, JSON.stringify({ appearance: prefs.appearance, artifactWidth: clampArtifactWidth(prefs.artifactWidth), taskListVisible: prefs.taskListVisible !== false, projectListVisible: prefs.projectListVisible !== false, expandedProjectId: resourceID(prefs.expandedProjectId), toolsExpanded: prefs.toolsExpanded !== false, pinnedSessionIds: pins(prefs.pinnedSessionIds), lastSessionId: resourceID(prefs.lastSessionId), lastArtifactId: resourceID(prefs.lastArtifactId) })); } catch { /* 偏好存储失败不阻止当前操作。 */ }
}

/** 系统主题由 CSS 媒体查询处理。 */
export function applyAppearance(appearance: Appearance): void {
  document.documentElement.dataset.appearance = appearance;
}
