import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent } from "../../api/types";
import { TaskContextBar } from "./TaskContextBar";
import { TaskInput, type TaskInputProps } from "./TaskInput";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
const agent: Agent = { id: "agent-a", name: "Local assistant", model: { provider_id: "provider-a", model: "test-model" }, tools: [], extensions: [], autonomy: "ask_all", workspace_dir: "/workspace/project", enabled: true, created_at: "2026-10-13T00:00:00Z", updated_at: "2026-10-13T00:00:00Z" };
let root: Root;
let host: HTMLDivElement;
let props: TaskInputProps;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  props = { value: "draft", onChange: vi.fn(), onSubmit: vi.fn(), materials: [], onRemoveMaterial: vi.fn(), onFiles: vi.fn(), disabled: false, filesDisabled: false, submitDisabled: false, submitLabel: "send", label: "message", placeholder: "", inputKey: "session-a",
    contextActions: <TaskContextBar agent={agent} messages={[]} disabled={false} onAddMaterial={vi.fn()}/> };
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<MemoryRouter><TaskInput {...props}/><button data-outside>outside</button><dialog open><button data-dialog>dialog</button></dialog></MemoryRouter>)); }
function plus() { return host.querySelector<HTMLButtonElement>(".composer-add-menu > button")!; }
function button(text: string) { return [...host.querySelectorAll<HTMLButtonElement>(".composer-add-panels button")].find(item => item.textContent === text)!; }
function contextTrigger(id: string) { return host.querySelector<HTMLElement>(`[data-context-id="${id}"] > summary`)!; }
async function click(element: HTMLElement) { await act(async () => element.click()); }
async function key(element: HTMLElement, key: string) { await act(async () => element.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }))); }
function contextOpen() { return [...host.querySelectorAll<HTMLDetailsElement>(".task-context > details[open]")].map(item => item.dataset.contextId); }

describe("本地加号菜单", () => {
  it("加号只打开菜单，选择本地文件才触发多文件选择器，不请求远端或提交消息", async () => {
    const network = vi.spyOn(globalThis, "fetch");
    await render();
    const picker = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    const pickerClick = vi.spyOn(picker, "click");
    await click(plus());
    expect(plus().getAttribute("aria-expanded")).toBe("true");
    expect(pickerClick).not.toHaveBeenCalled();
    expect([...host.querySelectorAll('[data-local-menu="root"] > button')].map(item => item.textContent)).toEqual(["compose.addFiles", "phase2.workspace", "compose.approvalMode"]);
    expect(button("compose.addFiles").hasAttribute("aria-haspopup")).toBe(false);
    await click(button("compose.addFiles"));
    expect(host.querySelector('[data-local-menu="files"]')).toBeNull();
    expect(pickerClick).toHaveBeenCalledOnce();
    expect(picker.multiple).toBe(true);
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    expect(props.onFiles).not.toHaveBeenCalled();
    const files = [new File(["a"], "notes.md"), new File(["b"], "plan.txt")];
    await act(async () => {
      Object.defineProperty(picker, "files", { configurable: true, value: files });
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(props.onFiles).toHaveBeenCalledWith(files);
    expect(props.onSubmit).not.toHaveBeenCalled();
    expect(network).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(host.querySelector("textarea"));
  });

  it("没有任务条件和项目资料时仅保留可用的本地文件入口", async () => {
    props.contextActions = null;
    await render();
    await click(plus());
    expect(host.querySelectorAll('[data-local-menu="root"] > button')).toHaveLength(1);
    const picker = vi.spyOn(host.querySelector<HTMLInputElement>('input[type="file"]')!, "click");
    await click(button("compose.addFiles"));
    expect(picker).toHaveBeenCalledOnce();
    expect(host.querySelector(".composer-add-panels")).toBeNull();
  });

  it("项目资料只使用传入数据，选择后收起且不会提交外层表单", async () => {
    props.projectMaterials = [{ id: "m-a", name: "requirements.md", selected: false }, { id: "m-b", name: "plan.md", selected: true }];
    props.onToggleProjectMaterial = vi.fn();
    await render();
    await click(plus());
    await click(button("compose.addFiles"));
    expect(button("plan.md").getAttribute("aria-checked")).toBe("true");
    await click(button("requirements.md"));
    expect(props.onToggleProjectMaterial).toHaveBeenCalledWith("m-a");
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    expect(props.onSubmit).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(host.querySelector("textarea"));
  });

  it("加号与目录审批模型保持互斥，切换和重复点击不会遗留旧弹层", async () => {
    await render();
    await click(contextTrigger("workspace"));
    await click(plus());
    expect(contextOpen()).toEqual([]);
    await click(button("compose.approvalMode"));
    expect(contextOpen()).toEqual(["permission"]);
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    expect(document.activeElement).toBe(contextTrigger("permission"));
    await click(contextTrigger("model"));
    expect(contextOpen()).toEqual(["model"]);
    await click(plus());
    await click(button("phase2.workspace"));
    expect(contextOpen()).toEqual(["workspace"]);
    await click(contextTrigger("permission"));
    expect(contextOpen()).toEqual(["permission"]);
    await click(contextTrigger("permission"));
    expect(contextOpen()).toEqual([]);
  });

  it("支持方向键首尾导航和子菜单往返，Esc逐层关闭并返回触发按钮", async () => {
    props.projectMaterials = [{ id: "material", name: "notes.md", selected: false }];
    props.onToggleProjectMaterial = vi.fn();
    await render();
    await key(plus(), "ArrowDown");
    expect(document.activeElement).toBe(button("compose.addFiles"));
    await key(button("compose.addFiles"), "End");
    expect(document.activeElement).toBe(button("compose.approvalMode"));
    await key(button("compose.approvalMode"), "ArrowDown");
    expect(document.activeElement).toBe(button("compose.addFiles"));
    await key(button("compose.addFiles"), "ArrowUp");
    expect(document.activeElement).toBe(button("compose.approvalMode"));
    await key(button("compose.approvalMode"), "Home");
    await key(button("compose.addFiles"), "ArrowRight");
    expect(document.activeElement).toBe(button("compose.localFiles"));
    await key(button("compose.localFiles"), "ArrowLeft");
    expect(document.activeElement).toBe(button("compose.addFiles"));
    expect(host.querySelector('[data-local-menu="files"]')).toBeNull();
    await key(button("compose.addFiles"), "ArrowRight");
    await key(button("compose.localFiles"), "Escape");
    expect(document.activeElement).toBe(button("compose.addFiles"));
    await key(button("compose.addFiles"), "Escape");
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    expect(document.activeElement).toBe(plus());
  });

  it("窄屏只通过点击展开子菜单，指针经过其他行不会误关闭或打开工作目录", async () => {
    props.projectMaterials = [{ id: "material", name: "notes.md", selected: false }];
    props.onToggleProjectMaterial = vi.fn();
    vi.stubGlobal("innerWidth", 640);
    await render();
    await click(plus());
    await act(async () => button("compose.addFiles").dispatchEvent(new Event("pointerover", { bubbles: true })));
    expect(host.querySelector('[data-local-menu="files"]')).toBeNull();
    await click(button("compose.addFiles"));
    expect(document.activeElement).toBe(button("compose.localFiles"));
    await act(async () => button("phase2.workspace").dispatchEvent(new Event("pointerover", { bubbles: true })));
    expect(host.querySelector('[data-local-menu="files"]')).not.toBeNull();
    expect(contextOpen()).toEqual([]);
    await key(button("compose.localFiles"), "ArrowLeft");
    expect(document.activeElement).toBe(button("compose.addFiles"));
    await key(button("compose.addFiles"), "ArrowRight");
    expect(document.activeElement).toBe(button("compose.localFiles"));
  });

  it("按按钮可用空间选择展开方向，展开子菜单不重新改变方向", async () => {
    props.projectMaterials = [{ id: "material", name: "notes.md", selected: false }];
    props.onToggleProjectMaterial = vi.fn();
    vi.stubGlobal("innerHeight", 760);
    await render();
    const bounds = vi.spyOn(plus(), "getBoundingClientRect");
    bounds.mockReturnValue({ top: 440, bottom: 472 } as DOMRect);
    await click(plus());
    const panels = host.querySelector<HTMLElement>(".composer-add-panels")!;
    expect(panels.dataset.placement).toBe("above");
    expect(panels.style.getPropertyValue("--composer-menu-space")).toBe("424px");
    await click(button("compose.addFiles"));
    expect(panels.dataset.placement).toBe("above");
    await key(button("compose.localFiles"), "Escape");
    await key(button("compose.addFiles"), "Escape");
    bounds.mockReturnValue({ top: 120, bottom: 152 } as DOMRect);
    await click(plus());
    expect(host.querySelector<HTMLElement>(".composer-add-panels")!.dataset.placement).toBe("below");
    expect(host.querySelector<HTMLElement>(".composer-add-panels")!.style.getPropertyValue("--composer-menu-space")).toBe("592px");
  });

  it("宽屏文件菜单在右侧不足时向左展开，并按根菜单顶部的剩余高度限制长列表", async () => {
    vi.stubGlobal("innerWidth", 1000);
    vi.stubGlobal("innerHeight", 760);
    props.projectMaterials = Array.from({ length: 30 }, (_, index) => ({ id: `m-${index}`, name: `material-${index}.md`, selected: false }));
    props.onToggleProjectMaterial = vi.fn();
    await render();
    await click(plus());
    const menu = host.querySelector<HTMLElement>('[data-local-menu="root"]')!;
    const bounds = vi.spyOn(menu, "getBoundingClientRect");
    bounds.mockReturnValue({ top: 300, bottom: 425, left: 800, right: 984 } as DOMRect);
    await click(button("compose.addFiles"));
    let child = host.querySelector<HTMLElement>('[data-local-menu="files"]')!;
    expect(child.dataset.side).toBe("left");
    expect(child.style.getPropertyValue("--composer-submenu-top")).toBe("0px");
    expect(child.style.getPropertyValue("--composer-submenu-width")).toBe("220px");
    expect(host.querySelectorAll('[data-local-menu="files"] > button')).toHaveLength(31);
    await key(button("compose.localFiles"), "ArrowLeft");
    bounds.mockReturnValue({ top: 600, bottom: 725, left: 38, right: 222 } as DOMRect);
    await click(button("compose.addFiles"));
    child = host.querySelector<HTMLElement>('[data-local-menu="files"]')!;
    expect(child.dataset.side).toBe("right");
    expect(child.style.getPropertyValue("--composer-submenu-height")).toBe("144px");
    expect(child.style.getPropertyValue("--composer-submenu-top")).toBe("0px");
  });

  it("点击外部、移出焦点、窗口失焦和会话切换均收起，不影响其他对话框", async () => {
    await render();
    await click(plus());
    await act(async () => host.querySelector("[data-outside]")!.dispatchEvent(new Event("pointerdown", { bubbles: true })));
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    await click(plus());
    await act(async () => host.querySelector<HTMLElement>("[data-dialog]")!.focus());
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    expect(host.querySelector("dialog")?.open).toBe(true);
    await click(plus());
    await act(async () => window.dispatchEvent(new Event("blur")));
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    await click(plus());
    props.inputKey = "session-b";
    await render();
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    await click(contextTrigger("workspace"));
    props.inputKey = "session-c";
    await render();
    expect(contextOpen()).toEqual([]);
  });

  it("禁用和上传期间不允许打开菜单，状态变化关闭已打开菜单，Tab不困住焦点", async () => {
    await render();
    await click(plus());
    await key(button("compose.addFiles"), "Tab");
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    await click(plus());
    props.filesDisabled = true;
    await render();
    expect(plus().disabled).toBe(true);
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    await click(plus());
    expect(host.querySelector(".composer-add-panels")).toBeNull();
    props.filesDisabled = false;
    props.disabled = true;
    await render();
    expect(plus().disabled).toBe(true);
    expect(props.onFiles).not.toHaveBeenCalled();
    expect(props.onSubmit).not.toHaveBeenCalled();
  });
});

describe("分层输入操作", () => {
  it("分离模型与任务条件后共享弹层开关，Esc和加号仍回到正确入口", async () => {
    props.contextActions = <TaskContextBar part="context" agent={agent} messages={[]} disabled={false} onAddMaterial={vi.fn()}/>;
    props.modelAction = <TaskContextBar part="model" agent={agent} messages={[]} disabled={false} onAddMaterial={vi.fn()}/>;
    props.actions = <select aria-label="assistant"><option>Local assistant</option></select>;
    const createEmpty = vi.fn();
    props.secondaryAction = <button type="button" onClick={createEmpty}>empty task</button>;
    await render();
    const primary = host.querySelector(".task-input-primary")!;
    expect(primary.contains(contextTrigger("model"))).toBe(true);
    expect(primary.contains(host.querySelector(".composer-send"))).toBe(true);
    expect(primary.contains(host.querySelector("select"))).toBe(false);
    expect(host.querySelectorAll('[data-context-id="model"]')).toHaveLength(1);
    expect(host.querySelectorAll('[data-context-id="workspace"]')).toHaveLength(1);
    await click(contextTrigger("workspace"));
    await click(contextTrigger("model"));
    expect(contextOpen()).toEqual(["model"]);
    await key(contextTrigger("model"), "Escape");
    expect(contextOpen()).toEqual([]);
    expect(document.activeElement).toBe(contextTrigger("model"));
    await click(plus());
    await click(button("compose.approvalMode"));
    expect(contextOpen()).toEqual(["permission"]);
    expect(document.activeElement).toBe(contextTrigger("permission"));
    await click(host.querySelector<HTMLButtonElement>(".task-input-secondary button")!);
    expect(createEmpty).toHaveBeenCalledOnce();
    expect(props.onSubmit).not.toHaveBeenCalled();
  });

  it("保留输入法换行和Enter发送，并在辅助操作分行时显示保存错误", async () => {
    props.status = "save failed";
    await render();
    const textarea = host.querySelector("textarea")!;
    await act(async () => textarea.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true })));
    await key(textarea, "Enter");
    expect(props.onSubmit).not.toHaveBeenCalled();
    await act(async () => textarea.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true })));
    await act(async () => textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true })));
    expect(props.onSubmit).not.toHaveBeenCalled();
    await key(textarea, "Enter");
    expect(props.onSubmit).toHaveBeenCalledOnce();
    expect(host.querySelector('[role="status"]')?.className).toBe("composer-hint");
    expect(host.querySelector('[role="status"]')?.textContent).toBe("save failed");
  });
});

it("没有项目资料时方向键不打开空子菜单，Esc返回加号，取消选文件后回到输入", async () => {
  await render();
  const picker = host.querySelector<HTMLInputElement>('input[type="file"]')!;
  const choose = vi.spyOn(picker, "click");
  await key(plus(), "ArrowDown");
  const files = button("compose.addFiles");
  expect(document.activeElement).toBe(files); expect(files.hasAttribute("aria-haspopup")).toBe(false);
  await key(files, "ArrowRight"); expect(host.querySelector('[data-local-menu="files"]')).toBeNull(); expect(choose).not.toHaveBeenCalled();
  await key(files, "Escape"); expect(document.activeElement).toBe(plus());
  await click(plus()); await click(button("compose.addFiles")); expect(choose).toHaveBeenCalledOnce();
  await act(async () => picker.dispatchEvent(new Event("cancel")));
  expect(document.activeElement).toBe(host.querySelector("textarea")); expect(props.onSubmit).not.toHaveBeenCalled();
});

it("项目资料清空后恢复直接选文件，关闭旧子菜单并保留可用焦点", async () => {
  props.projectMaterials = [{ id: "material", name: "notes.md", selected: false }]; props.onToggleProjectMaterial = vi.fn();
  await render(); await click(plus()); await click(button("compose.addFiles"));
  expect(document.activeElement).toBe(button("compose.localFiles"));
  props.projectMaterials = []; await render();
  expect(host.querySelector('[data-local-menu="files"]')).toBeNull(); expect(document.activeElement).toBe(button("compose.addFiles"));
  const choose = vi.spyOn(host.querySelector<HTMLInputElement>('input[type="file"]')!, "click");
  await click(button("compose.addFiles")); expect(choose).toHaveBeenCalledOnce(); expect(props.onToggleProjectMaterial).not.toHaveBeenCalled();
});
