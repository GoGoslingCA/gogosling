import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, Draft, DraftInput, DraftUpdateInput } from "../../api/types";
import { StartPage } from "./StartPage";
import { DraftContext, DraftRegistry } from "../../state/DraftProvider";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

let root: Root;
let container: HTMLDivElement;
let props: Parameters<typeof StartPage>[0];
const agent: Agent = { id: "agent-a", name: "写作助手", model: { provider_id: "provider-a", model: "test-model" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "/workspace/agent-a", enabled: true, created_at: "2026-10-12T01:00:00Z", updated_at: "2026-10-12T01:00:00Z" };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (value: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  props = { agents: [agent], sessions: [], loading: false, error: null, busy: false, timeZone: "UTC", onStart: vi.fn(async () => undefined), onSelect: vi.fn(), onHistory: vi.fn() };
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});
async function render() { await act(async () => root.render(<MemoryRouter><StartPage {...props} /></MemoryRouter>)); }
function textarea() { return container.querySelector("textarea")!; }
async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea(), value);
    textarea().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
function submit() { container.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); }
async function chooseFiles(files: File[]) {
  await act(async () => {
    const input = container.querySelector('input[type="file"]')!;
    Object.defineProperty(input, "files", { configurable: true, value: files });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
function dropFiles(files: File[]) {
  const event = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: { types: ["Files"], files, dropEffect: "none" } });
  container.querySelector("form")!.dispatchEvent(event);
  return event;
}

describe("开始页任务创建", () => {
  it("传递完整目标给新任务，不将目标截成标题长度；创建失败保留可重试输入", async () => {
    const request = deferred<void>();
    props.onStart = vi.fn(() => request.promise);
    await render();
    const goal = `  ${"需求详情".repeat(40)}  `;
    await type(goal);
    await act(async () => submit());
    expect(props.onStart).toHaveBeenCalledWith(agent.id, goal.trim(), []);
    expect(textarea().value).toBe(goal);
    await act(async () => request.reject(new Error("offline")));
    expect(textarea().value).toBe(goal);
  });

  it("只允许启用的智能体创建任务，没有可用智能体时给出配置入口", async () => {
    props.agents = [{ ...agent, enabled: false }];
    await render();
    expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true);
    expect(container.querySelector<HTMLAnchorElement>('a[href="/agents"]')).not.toBeNull();
    await act(async () => submit());
    expect(props.onStart).not.toHaveBeenCalled();
  });

  it("场景入口只填入可编辑想法，不自动创建或执行任务", async () => {
    await render();
    const write = Array.from(container.querySelectorAll("button")).find((button) => button.textContent?.includes("work.entry_write"))!;
    await act(async () => write.click());
    expect(textarea().value).toBe("work.prompt_write");
    expect(document.activeElement).toBe(textarea());
    expect(props.onStart).not.toHaveBeenCalled();
  });

  it("输入法合成和 Shift+Enter 不创建任务，普通 Enter 提交", async () => {
    await render();
    await type("整理需求");
    await act(async () => {
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true }));
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 229, bubbles: true }));
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }));
    });
    expect(props.onStart).not.toHaveBeenCalled();
    await act(async () => textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(props.onStart).toHaveBeenCalledWith(agent.id, "整理需求", []);
  });

  it("同一个事件循环连续提交只创建一个任务", async () => {
    const request = deferred<void>();
    props.onStart = vi.fn(() => request.promise);
    await render();
    await type("只创建一次");
    await act(async () => { submit(); submit(); });
    expect(props.onStart).toHaveBeenCalledTimes(1);
    await act(async () => request.resolve());
  });

  it("首页选择和拖入本地文件后可移除，提交时把原始 File 对象交给宿主", async () => {
    const chosen = new File(["需求正文"], "requirements.md");
    const dropped = new File(["示例"], "example.txt");
    await render();
    await chooseFiles([chosen]);
    expect(container.querySelectorAll(".attachment-chip")).toHaveLength(1);
    await act(async () => dropFiles([dropped]));
    expect(container.querySelectorAll(".attachment-chip")).toHaveLength(2);
    await act(async () => container.querySelector<HTMLButtonElement>(".attachment-chip button")!.click());
    expect(container.querySelectorAll(".attachment-chip")).toHaveLength(1);
    expect(container.querySelector(".attachment-chip")?.textContent).toContain("example.txt");
    expect(props.onStart).not.toHaveBeenCalled();
    await act(async () => submit());
    expect(props.onStart).toHaveBeenCalledWith(agent.id, "", [dropped]);
    expect(container.querySelector(".attachment-chip")).toBeNull();
  });

  it("任一阶段失败后保留正文和文件，重试锁定原智能体但允许修正资料", async () => {
    const request = deferred<void>();
    props.onStart = vi.fn(() => request.promise);
    const selected = new File(["需求正文"], "requirements.md");
    await render();
    await type("  生成计划  ");
    await chooseFiles([selected]);
    await act(async () => submit());
    props = { ...props, startStage: "uploading", busy: true, startLocked: true };
    await render();
    expect(textarea().disabled).toBe(true);
    expect(container.querySelector<HTMLButtonElement>(".attachment-chip button")!.disabled).toBe(true);
    expect(container.querySelector('[role="status"]')?.textContent).toBe("phase2.stage_uploading");
    const unwanted = new File(["不可加入"], "blocked.txt");
    await act(async () => dropFiles([unwanted]));
    expect(container.querySelectorAll(".attachment-chip")).toHaveLength(1);
    await act(async () => request.reject(new Error("upload failed")));
    props = { ...props, busy: false, startStage: null };
    await render();
    expect(textarea().value).toBe("  生成计划  ");
    expect(container.querySelector(".attachment-chip")?.textContent).toContain("requirements.md");
    expect(container.querySelector<HTMLSelectElement>("#start-agent")!.disabled).toBe(true);
    expect(textarea().disabled).toBe(false);
    const updated = new File(["补充"], "more.md");
    await chooseFiles([updated]);
    await type("修正后的要求");
    props.onStart = vi.fn(async () => undefined);
    await render();
    await act(async () => submit());
    expect(props.onStart).toHaveBeenCalledWith(agent.id, "修正后的要求", [selected, updated]);
    expect(textarea().value).toBe("");
    expect(container.querySelector(".attachment-chip")).toBeNull();
  });

  it("空白 Enter 不创建任务，仅独立的新建空任务按钮允许空白创建", async () => {
    await render();
    expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true);
    await act(async () => textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(props.onStart).not.toHaveBeenCalled();
    const empty = Array.from(container.querySelectorAll("button")).find((button) => button.textContent === "phase2.emptyTask")!;
    await act(async () => empty.click());
    expect(props.onStart).toHaveBeenCalledWith(agent.id, "", []);
  });

  it("选择资料即报告未发送草稿，移除最后一份资料后解除离开保护", async () => {
    props.onDraftDirtyChange = vi.fn();
    await render();
    expect(props.onDraftDirtyChange).toHaveBeenLastCalledWith(false);
    await chooseFiles([new File(["需求"], "requirements.md")]);
    expect(props.onDraftDirtyChange).toHaveBeenLastCalledWith(true);
    const beforeUnload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(beforeUnload);
    expect(beforeUnload.defaultPrevented).toBe(true);
    await act(async () => container.querySelector<HTMLButtonElement>(".attachment-chip button")!.click());
    expect(props.onDraftDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it("开始条件未满足时只阻止开始，仍可修改正文和移除文件，不误报正在开始", async () => {
    props.startBlocked = true;
    await render();
    const empty = Array.from(container.querySelectorAll<HTMLButtonElement>("button")).find((button) => button.textContent === "phase2.emptyTask")!;
    expect(empty.disabled).toBe(true);
    expect(textarea().disabled).toBe(false);
    await act(async () => empty.click());
    await type("可以继续修改要求");
    await chooseFiles([new File(["需求"], "requirements.md")]);
    expect(textarea().value).toBe("可以继续修改要求");
    const remove = container.querySelector<HTMLButtonElement>(".attachment-chip button")!;
    expect(remove.disabled).toBe(false);
    await act(async () => remove.click());
    expect(container.querySelector(".attachment-chip")).toBeNull();
    expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true);
    expect(container.textContent).not.toContain("phase2.starting");
    await act(async () => {
      submit();
      textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(props.onStart).not.toHaveBeenCalled();
  });

  it("更换助手前先保存当前未发送内容，本地文件不带到下一名助手", async () => {
    const editor = { ...agent, id: "agent-b", name: "校对助手", workspace_dir: "/workspace/agent-b" };
    props.agents = [agent, editor];
    const drafts = new Map<string, Draft>();
    const api = {
      listDrafts: vi.fn(async (query: { agent_id?: string } = {}) => [...drafts.values()].filter((item) => item.agent_id === query.agent_id)),
      getDraft: vi.fn(async (id: string) => drafts.get(id)!),
      createDraft: vi.fn(async (input: DraftInput) => {
        const saved: Draft = { id: `draft-${input.agent_id}`, owner_id: "local", kind: "composer", agent_id: input.agent_id, text: input.text, material_ids: input.material_ids ?? [], version: 1, created_at: agent.created_at, updated_at: agent.updated_at };
        drafts.set(saved.id, saved);
        return saved;
      }),
      updateDraft: vi.fn(async (id: string, input: DraftUpdateInput) => {
        const saved = { ...drafts.get(id)!, ...input, version: input.expected_version + 1 };
        drafts.set(id, saved);
        return saved;
      }),
      deleteDraft: vi.fn(async () => undefined),
    };
    const registry = new DraftRegistry();
    registry.configure(api, true, false);
    try {
      await act(async () => root.render(<MemoryRouter><DraftContext.Provider value={registry}><StartPage {...props} /></DraftContext.Provider></MemoryRouter>));
      await type("甲还没发出的要求");
      await chooseFiles([new File(["本地"], "local.md")]);
      const select = container.querySelector<HTMLSelectElement>("#start-agent")!;
      await act(async () => {
        select.value = editor.id;
        select.dispatchEvent(new Event("change", { bubbles: true }));
      });
      await vi.waitFor(() => expect(api.createDraft).toHaveBeenCalledWith(expect.objectContaining({ agent_id: agent.id, text: "甲还没发出的要求" })));
      expect(textarea().value).not.toBe("甲还没发出的要求");
      expect(container.querySelector(".attachment-chip")).toBeNull();
      expect(select.value).toBe(editor.id);
    } finally { registry.dispose(); }
  });

  it("持久草稿恢复项目资料勾选，后续选择写回草稿并随开始请求发送，不重复显示原始 ID", async () => {
    let remote: Draft = { id: "draft-a", owner_id: "local", kind: "composer", agent_id: agent.id, project_id: "project-a", text: "", material_ids: ["material-a"], version: 1, created_at: agent.created_at, updated_at: agent.updated_at };
    const api = {
      listDrafts: vi.fn(async () => [remote]),
      getDraft: vi.fn(async () => remote),
      createDraft: vi.fn(async (input: DraftInput) => ({ ...remote, ...input })),
      updateDraft: vi.fn(async (_id: string, input: DraftUpdateInput) => { remote = { ...remote, ...input, version: input.expected_version + 1 }; return remote; }),
      deleteDraft: vi.fn(async () => undefined),
    };
    const registry = new DraftRegistry();
    registry.configure(api, true, false);
    const selectionChanged = vi.fn();
    function ProjectStart() {
      const [selected, setSelected] = useState<string[]>([]);
      return <><label><input type="checkbox" aria-label="需求资料" checked={selected.includes("material-a")} onChange={() => setSelected((current) => current.includes("material-a") ? current.filter((id) => id !== "material-a") : [...current, "material-a"])} />需求资料</label>
        <label><input type="checkbox" aria-label="补充资料" checked={selected.includes("material-b")} onChange={() => setSelected((current) => current.includes("material-b") ? current.filter((id) => id !== "material-b") : [...current, "material-b"])} />补充资料</label>
        <StartPage {...props} projectId="project-a" projectMaterials={[{ id: "material-a", name: "requirements.md" }, { id: "material-b", name: "notes.md" }]} projectMaterialIds={selected} onProjectMaterialIdsChange={(ids) => { selectionChanged(ids); setSelected(ids); }} />
      </>;
    }
    try {
      await act(async () => root.render(<MemoryRouter><DraftContext.Provider value={registry}><ProjectStart /></DraftContext.Provider></MemoryRouter>));
      expect(selectionChanged).toHaveBeenCalledWith(["material-a"]);
      expect(container.querySelector<HTMLInputElement>('input[aria-label="需求资料"]')!.checked).toBe(true);
      expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(false);
      expect(container.textContent).not.toContain("material-a");
      expect(container.querySelector(".start-composer .attachment-chip")).toBeNull();
      await act(async () => container.querySelector<HTMLButtonElement>(".composer-add-menu > button")!.click());
      await act(async () => container.querySelector<HTMLButtonElement>('[data-local-menu="root"] > button')!.click());
      const addNotes = [...container.querySelectorAll<HTMLButtonElement>('[data-local-menu="files"] > button')].find(button => button.textContent === "notes.md")!;
      await act(async () => addNotes.click());
      expect(container.querySelector<HTMLInputElement>('input[aria-label="补充资料"]')!.checked).toBe(true);
      expect(container.querySelector(".composer-add-panels")).toBeNull();
      expect(props.onStart).not.toHaveBeenCalled();
      await act(async () => submit());
      expect(api.updateDraft).toHaveBeenCalledWith("draft-a", expect.objectContaining({ material_ids: ["material-a", "material-b"], expected_version: 1 }));
      expect(props.onStart).toHaveBeenCalledWith(agent.id, "", [], expect.objectContaining({ material_ids: ["material-a", "material-b"] }));
    } finally { registry.dispose(); }
  });
});
