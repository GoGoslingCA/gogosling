import { act, StrictMode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import { TaskInput } from "./TaskInput";

const { enhance, api }=vi.hoisted(()=>{const enhance=vi.fn();return {enhance,api:{agents:{enhancePrompt:enhance}}};});
vi.mock("../../state/ConnectionContext",()=>({useApi:()=>api}));
vi.mock("react-i18next",()=>({useTranslation:()=>({t:(key:string)=>key})}));
let root:Root;let host:HTMLDivElement;let send=vi.fn<() => void>();let change=vi.fn<(text: string) => void>();
const previous={show:Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype,"showModal"),close:Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype,"close")};
function Harness({scope="a",agentId="agent",enabled=true,initial="请解释这段代码"}:{scope?:string;agentId?:string;enabled?:boolean;initial?:string}){
 const [text,setText]=useState(initial);
 return <TaskInput value={text} onChange={next=>{change(next);setText(next);}} onSubmit={send} inputKey={scope} enhanceAgentId={enabled?agentId:undefined} materials={[{id:"file",name:"source.ts"}]} onRemoveMaterial={()=>undefined} onFiles={()=>undefined} disabled={false} filesDisabled={false} submitDisabled={!text} submitLabel="send" label="draft" placeholder="draft"/>;
}
beforeEach(()=>{
 (globalThis as {IS_REACT_ACT_ENVIRONMENT?:boolean}).IS_REACT_ACT_ENVIRONMENT=true;send=vi.fn<() => void>();change=vi.fn<(text: string) => void>();enhance.mockReset();enhance.mockResolvedValue({text:"请解释这段代码的功能、执行流程与边界情况。"});
 host=document.createElement("div");document.body.append(host);root=createRoot(host);
 Object.defineProperty(HTMLDialogElement.prototype,"showModal",{configurable:true,value(this:HTMLDialogElement){this.open=true;}});
 Object.defineProperty(HTMLDialogElement.prototype,"close",{configurable:true,value(this:HTMLDialogElement){this.open=false;}});
});
afterEach(async()=>{await act(async()=>root.unmount());host.remove();for(const [key,name]of [["show","showModal"],["close","close"]]as const){const descriptor=previous[key];if(descriptor)Object.defineProperty(HTMLDialogElement.prototype,name,descriptor);else delete HTMLDialogElement.prototype[name];}});
async function ready(check:()=>void){await vi.waitFor(async()=>{await act(async()=>{await new Promise(r=>setTimeout(r,0));});check();},{timeout:3000,interval:10});}
async function mount(props:Parameters<typeof Harness>[0]={}){await act(async()=>root.render(<StrictMode><Harness {...props}/></StrictMode>));}
function button(text:string){const item=[...host.querySelectorAll<HTMLButtonElement>("button")].find(node=>node.textContent===text);if(!item)throw new Error("未找到按钮 "+text);return item;}
async function click(text:string){await act(async()=>button(text).click());}
async function edit(label:string,text:string){const node=host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${label}"]`)!;await act(async()=>{Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,"value")!.set!.call(node,text);node.dispatchEvent(new Event("input",{bubbles:true}));});}

describe("提示词优化的发送前流程",()=>{
 it("一键直接回填且不弹窗；StrictMode只调用一次，还原不发送或删除附件",async()=>{
  await mount();expect(enhance).not.toHaveBeenCalled();await click("enhance.action");
  await ready(()=>expect(change).toHaveBeenCalledOnce());expect(enhance).toHaveBeenCalledOnce();expect(host.querySelector("dialog")).toBeNull();expect(send).not.toHaveBeenCalled();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("draft");expect(host.textContent).toContain("source.ts");
  expect(host.querySelector<HTMLTextAreaElement>('textarea[aria-label="draft"]')?.value).toContain("执行流程");
  await click("enhance.restore");expect(change).toHaveBeenLastCalledWith("请解释这段代码");
 });
 it("仅调整时打开单输入弹窗，携带原文和修改意见，应用后还原到最初原文",async()=>{
  await mount();await click("enhance.action");await ready(()=>expect(change).toHaveBeenCalledOnce());await click("enhance.adjust");
  expect(host.querySelectorAll("dialog textarea")).toHaveLength(1);await edit("enhance.feedback","限制100字，只讲功能");
  enhance.mockResolvedValueOnce({text:"请在100字以内解释这段代码的主要功能。"});await click("enhance.applyFeedback");
  await ready(()=>expect(host.querySelector("dialog")).toBeNull());expect(enhance.mock.calls[1]?.[3]).toEqual({draft:"请解释这段代码的功能、执行流程与边界情况。",feedback:"限制100字，只讲功能"});expect(enhance.mock.calls[1]?.[1]).toBe("请解释这段代码");
  expect(change).toHaveBeenLastCalledWith("请在100字以内解释这段代码的主要功能。");await click("enhance.restore");expect(change).toHaveBeenLastCalledWith("请解释这段代码");expect(send).not.toHaveBeenCalled();
 });
 it("取消中止请求且迟到结果不能回填",async()=>{
  let finish!:(value:{text:string})=>void;enhance.mockImplementation(()=>new Promise(resolve=>{finish=resolve;}));
  await mount();await click("enhance.action");await ready(()=>expect(enhance).toHaveBeenCalledOnce());const signal=enhance.mock.calls[0]![2] as AbortSignal;
  await click("common.cancel");expect(signal.aborted).toBe(true);await act(async()=>finish({text:"迟到结果"}));expect(change).not.toHaveBeenCalled();expect(host.querySelector("dialog")).toBeNull();
 });
 it("切换会话或手改后改回也不能应用旧结果",async()=>{
  let finish!:(value:{text:string})=>void;enhance.mockImplementation(()=>new Promise(resolve=>{finish=resolve;}));
  await mount();await click("enhance.action");await ready(()=>expect(enhance).toHaveBeenCalledOnce());const signal=enhance.mock.calls[0]![2] as AbortSignal;
  await edit("draft","新的文字");await edit("draft","请解释这段代码");expect(signal.aborted).toBe(true);await act(async()=>finish({text:"过期优化"}));expect(change).toHaveBeenCalledTimes(2);
  await click("enhance.action");await ready(()=>expect(enhance).toHaveBeenCalledTimes(2));const second=enhance.mock.calls[1]![2] as AbortSignal;await mount({scope:"b"});expect(second.aborted).toBe(true);await act(async()=>finish({text:"旧会话结果"}));expect(change).toHaveBeenCalledTimes(2);
 });
 it("发送立即取消优化，迟到响应不重写已发送的草稿",async()=>{
  let finish!:(value:{text:string})=>void;enhance.mockImplementation(()=>new Promise(resolve=>{finish=resolve;}));await mount();await click("enhance.action");await ready(()=>expect(enhance).toHaveBeenCalledOnce());const signal=enhance.mock.calls[0]![2] as AbortSignal;
  await click("send");expect(send).toHaveBeenCalledOnce();expect(signal.aborted).toBe(true);await act(async()=>finish({text:"过期内容"}));expect(change).not.toHaveBeenCalled();
 });
 it("错误保留原文，可直接重试；空输入禁用且旧内核没有入口",async()=>{
  await mount({enabled:false});expect(host.textContent).not.toContain("enhance.action");await mount();await edit("draft","");expect(button("enhance.action").disabled).toBe(true);
  await edit("draft","整理计划");enhance.mockRejectedValueOnce(new ApiError("conflict","任务较多",{status:409}));await click("enhance.action");await ready(()=>expect(host.textContent).toContain("任务较多"));expect(host.querySelector<HTMLTextAreaElement>('textarea[aria-label="draft"]')?.value).toBe("整理计划");
  await click("enhance.action");await ready(()=>expect(host.textContent).toContain("enhance.restore"));expect(send).not.toHaveBeenCalled();
 });
 it("优化后继续手改，隐藏还原入口以免覆盖新要求",async()=>{
  await mount();await click("enhance.action");await ready(()=>expect(change).toHaveBeenCalledOnce());await edit("draft","我继续修改的要求");expect(host.textContent).not.toContain("enhance.restore");
 });
});
