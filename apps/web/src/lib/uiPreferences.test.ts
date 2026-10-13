import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyAppearance, clampArtifactWidth, readUiPreferences, writeUiPreferences, type UiPreferences } from "./uiPreferences";

beforeEach(() => localStorage.clear());
afterEach(() => { localStorage.clear(); delete document.documentElement.dataset.appearance; vi.restoreAllMocks(); });

describe("设备界面偏好", () => {
  it("面板宽度始终落在设计范围，非法数值使用默认宽度", () => {
    expect(clampArtifactWidth(200)).toBe(420);
    expect(clampArtifactWidth(900)).toBe(640);
    expect(clampArtifactWidth(520.7)).toBe(521);
    expect(clampArtifactWidth(Number.NaN)).toBe(500);
    expect(clampArtifactWidth(Number.POSITIVE_INFINITY)).toBe(500);
  });

  it("分别更新外观和宽度后可恢复，存储中只包含允许的 UI 字段", () => {
    writeUiPreferences({ appearance: "dark" });
    writeUiPreferences({ artifactWidth: 600, messageText: "private draft", attachments: ["private.md"], token: "never-store" } as Partial<UiPreferences>);
    expect(readUiPreferences()).toEqual({ appearance: "dark", artifactWidth: 600, taskListVisible: true, projectListVisible: true, expandedProjectId: null, toolsExpanded: true, pinnedSessionIds: [], lastSessionId: null, lastArtifactId: null });
    expect(localStorage.length).toBe(1);
    const saved = JSON.parse(localStorage.getItem(localStorage.key(0)!)!);
    expect(saved).toEqual({ appearance: "dark", artifactWidth: 600, taskListVisible: true, projectListVisible: true, expandedProjectId: null, toolsExpanded: true, pinnedSessionIds: [], lastSessionId: null, lastArtifactId: null });
  });

  it("存储损坏或浏览器拒绝存储时回退，当前外观仍可应用", () => {
    writeUiPreferences({ appearance: "dark" });
    localStorage.setItem(localStorage.key(0)!, "invalid json");
    expect(readUiPreferences()).toEqual({ appearance: "system", artifactWidth: 500, taskListVisible: true, projectListVisible: true, expandedProjectId: null, toolsExpanded: true, pinnedSessionIds: [], lastSessionId: null, lastArtifactId: null });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("blocked", "SecurityError"); });
    expect(() => writeUiPreferences({ appearance: "light" })).not.toThrow();
    applyAppearance("light");
    expect(document.documentElement.dataset.appearance).toBe("light");
  });

  it("手工写入的未知外观和非数字面板宽度不成为有效偏好", () => {
    writeUiPreferences({ appearance: "dark" });
    localStorage.setItem(localStorage.key(0)!, JSON.stringify({ appearance: "unknown", artifactWidth: "640" }));
    expect(readUiPreferences()).toEqual({ appearance: "system", artifactWidth: 500, taskListVisible: true, projectListVisible: true, expandedProjectId: null, toolsExpanded: true, pinnedSessionIds: [], lastSessionId: null, lastArtifactId: null });
  });

  it("旧版偏好默认显示项目，折叠与展开项目只保存展示状态和 ID", () => {
    localStorage.setItem("gogosling.ui.v1", JSON.stringify({ taskListVisible: false, artifactWidth: 560 }));
    expect(readUiPreferences()).toMatchObject({ taskListVisible: false, projectListVisible: true, expandedProjectId: null });
    const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
    writeUiPreferences({ projectListVisible: false, expandedProjectId: id, projectName: "private project", projectFiles: ["private.md"] } as Partial<UiPreferences>);
    expect(readUiPreferences()).toMatchObject({ taskListVisible: false, projectListVisible: false, expandedProjectId: id, artifactWidth: 560 });
    expect(localStorage.getItem("gogosling.ui.v1")).not.toContain("private");
    writeUiPreferences({ projectListVisible: true, expandedProjectId: null });
    expect(readUiPreferences()).toMatchObject({ projectListVisible: true, expandedProjectId: null });
  });

  it("展开项目拒绝路径、连接和错误类型，非法折叠值按默认展开", () => {
    for (const value of ["../private", "https://example.test/project", "short-id", 42, { id: "01ARZ3NDEKTSV4RRFFQ69G5FAV" }]) {
      localStorage.setItem("gogosling.ui.v1", JSON.stringify({ expandedProjectId: value, projectListVisible: "false" }));
      expect(readUiPreferences()).toMatchObject({ expandedProjectId: null, projectListVisible: true });
    }
    writeUiPreferences({ expandedProjectId: "../private" });
    expect(JSON.parse(localStorage.getItem("gogosling.ui.v1")!).expandedProjectId).toBeNull();
  });
});

it("恢复位置仅接受资源ID，拒绝路径和连接信息", () => {
  const id = "01M4Q8EYTXQP72C31WW87MSSGA";
  writeUiPreferences({ lastSessionId: id, lastArtifactId: id });
  expect(readUiPreferences().lastSessionId).toBe(id);
  writeUiPreferences({ lastSessionId: "../private", lastArtifactId: "https://example.test/session" });
  expect(readUiPreferences().lastSessionId).toBeNull();
  expect(readUiPreferences().lastArtifactId).toBeNull();
  expect(localStorage.getItem(localStorage.key(0)!)).not.toContain("private");
});
