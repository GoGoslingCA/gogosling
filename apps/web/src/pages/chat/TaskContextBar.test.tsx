import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import type { Agent, Usage } from "../../api/types";
import { TaskContextBar } from "./TaskContextBar";
import { TaskPopoverContext } from "./taskPopovers";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
const agent: Agent = { id: "agent", name: "助手", model: { provider_id: "provider", model: "model-x" }, tools: [], extensions: [], autonomy: "ask_risky", workspace_dir: "/project/workspace", enabled: true, created_at: "2026-10-12T00:00:00Z", updated_at: "2026-10-12T00:00:00Z" };
function render(usage: Usage | null, selectedAgent = agent, open: string | null = null) { const host = document.createElement("div"); host.innerHTML = renderToStaticMarkup(<MemoryRouter><TaskPopoverContext.Provider value={{ open, setOpen: () => undefined }}><TaskContextBar agent={selectedAgent} messages={[]} usage={usage} onAddMaterial={() => undefined} disabled={false} /></TaskPopoverContext.Provider></MemoryRouter>); return host; }

describe("紧凑任务条件", () => {
  it("缺失用量显示未知，真实输入输出比例用于图条而不是任务完成率", () => {
    const partial = render({ input_tokens: 120 }, agent, "more");
    expect(partial.querySelectorAll(".usage-metrics dd")[1]?.textContent).toBe("common.unknown");
    const full = render({ input_tokens: 120, output_tokens: 60 }, agent, "more");
    const bars = full.querySelectorAll<HTMLElement>(".usage-bar");
    expect(bars[0]?.style.width).toBe("100%"); expect(bars[1]?.style.width).toBe("50%");
    expect(full.querySelector('[role="progressbar"]')).toBeNull();
  });

  it("主栏保留短标签，完整路径、权限及费用详情仅在所选弹层打开时挂载", () => {
    const host = render(null);
    expect([...host.querySelectorAll<HTMLDetailsElement>("details")].every((item) => !item.open)).toBe(true);
    expect(host.querySelector(".context-directory")?.textContent).toBe("workspace");
    expect(host.querySelector(".context-popover")).toBeNull();
    expect(host.textContent).not.toContain(agent.workspace_dir);
    expect(render(null, agent, "workspace").textContent).toContain(agent.workspace_dir);
    expect(render(null, agent, "permission").textContent).toContain("agents.autonomyHint_ask_risky");
    const more = render(null, agent, "more");
    expect(more.textContent).toContain("usage.costHint");
    expect(more.textContent).toContain("usage.unreported");
  });

  it("默认工作区的ULID不作为按钮文案，完整真实路径仍在详情", () => {
    const workspace = "/agents/01ARZ3NDEKTSV4RRFFQ69G5FAV";
    const host = render(null, { ...agent, workspace_dir: workspace }, "workspace");
    expect(host.querySelector(".context-directory")?.textContent).toBe("phase2.workspace");
    expect(host.querySelector(".context-popover code")?.textContent).toBe(workspace);
  });
});

// 真实挂载验证互斥、焦点和表单行为；静态快照不能发现多个 details 同时打开的问题。
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach } from "vitest";
let mountedRoot:Root;let container:HTMLDivElement;
beforeEach(()=>{(globalThis as {IS_REACT_ACT_ENVIRONMENT?:boolean}).IS_REACT_ACT_ENVIRONMENT=true;container=document.createElement("div");document.body.append(container);mountedRoot=createRoot(container);});
afterEach(async()=>{await act(async()=>mountedRoot.unmount());container.remove();});
async function mountInteractive(selectedAgent=agent,onAddMaterial=vi.fn(),onSubmit=vi.fn()){
 await act(async()=>mountedRoot.render(<MemoryRouter><form onSubmit={e=>{e.preventDefault();onSubmit();}}><TaskContextBar agent={selectedAgent} usage={null} messages={[{id:"m",session_id:"s",seq:1,role:"user",created_at:agent.created_at,parts:[{type:"attachment",attachment:{name:"source.md",path:"inbound/source.md",size:4}}]}]} onAddMaterial={onAddMaterial} disabled={false}/></form><button data-outside>outside</button><dialog open><button data-dialog>dialog</button></dialog></MemoryRouter>));
}
function summary(id:string){return container.querySelector<HTMLElement>(`[data-context-id="${id}"] > summary`)!;}
async function toggle(id:string){await act(async()=>summary(id).click());}
function openMenus(){return [...container.querySelectorAll<HTMLDetailsElement>(".task-context > details[open]")].map(d=>d.dataset.contextId);}
describe("任务条件弹层生命周期",()=>{
 it("关闭时不构造大量材料按钮，展开读取后关闭即卸载且不丢原始引用",async()=>{
  let namesRead=0;
  const attachments=Array.from({length:200},(_,index)=>({path:`inbound/${index}.md`,size:4,get name(){namesRead++;return `source-${index}.md`;}}));
  const messages=[{id:"many",session_id:"s",seq:1,role:"user" as const,created_at:agent.created_at,parts:attachments.map(attachment=>({type:"attachment" as const,attachment}))}];
  const add=vi.fn();
  const show=async()=>act(async()=>mountedRoot.render(<MemoryRouter><TaskContextBar agent={agent} usage={{input_tokens:120}} messages={messages} onAddMaterial={add} disabled={false}/></MemoryRouter>));
  await show();expect(namesRead).toBe(0);expect(container.querySelectorAll(".context-popover")).toHaveLength(0);
  await toggle("more");expect(container.querySelectorAll(".context-material")).toHaveLength(200);expect(namesRead).toBe(200);
  await toggle("more");await show();expect(container.querySelectorAll(".context-material")).toHaveLength(0);expect(namesRead).toBe(200);
  await toggle("more");await act(async()=>container.querySelectorAll<HTMLButtonElement>(".context-material")[199]!.click());
  expect(add).toHaveBeenCalledExactlyOnceWith(attachments[199]);expect(container.querySelectorAll(".context-popover")).toHaveLength(0);
 });
 it("工作目录切换全部审批时只保留权限弹层，再点同一入口会收起",async()=>{await mountInteractive();await toggle("workspace");expect(openMenus()).toEqual(["workspace"]);await toggle("permission");expect(openMenus()).toEqual(["permission"]);expect(summary("workspace").getAttribute("aria-expanded")).toBe("false");await toggle("permission");expect(openMenus()).toEqual([]);});
 it("外部点击和移出焦点会关闭，且不阻止其他对话框",async()=>{await mountInteractive();await toggle("workspace");await act(async()=>container.querySelector("[data-outside]")!.dispatchEvent(new Event("pointerdown",{bubbles:true})));expect(openMenus()).toEqual([]);await toggle("permission");await act(async()=>container.querySelector<HTMLElement>("[data-dialog]")!.focus());expect(openMenus()).toEqual([]);expect(container.querySelector("dialog")?.open).toBe(true);});
 it("Esc关闭并把焦点还给触发按钮，嵌套说明不会关闭主弹层",async()=>{await mountInteractive();await toggle("more");const inner=container.querySelector<HTMLElement>(".context-more-menu .compact-explainer summary")!;await act(async()=>{inner.dispatchEvent(new Event("pointerdown",{bubbles:true}));inner.click();});expect(openMenus()).toEqual(["more"]);await act(async()=>inner.dispatchEvent(new KeyboardEvent("keydown",{key:"Escape",bubbles:true,cancelable:true})));expect(openMenus()).toEqual([]);expect(document.activeElement).toBe(summary("more"));});
 it("引用材料后收起且不会提交外层消息表单",async()=>{const add=vi.fn(),send=vi.fn();await mountInteractive(agent,add,send);await toggle("more");await act(async()=>container.querySelector<HTMLButtonElement>(".context-material")!.click());expect(add).toHaveBeenCalledOnce();expect(send).not.toHaveBeenCalled();expect(openMenus()).toEqual([]);});
 it("进入权限设置或切换智能体后不遗留旧弹层",async()=>{await mountInteractive();await toggle("permission");await act(async()=>container.querySelector<HTMLElement>(".context-permission-menu a")!.click());expect(openMenus()).toEqual([]);await toggle("workspace");await mountInteractive({...agent,id:"other"});expect(openMenus()).toEqual([]);});
});
