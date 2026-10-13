import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { isolatedHTML } from "./viewers/HTMLViewer";
import { ContentPreview } from "./ContentPreview";
import SheetViewer from "./viewers/SheetViewer";
import SlidesViewer from "./viewers/SlidesViewer";
import DocumentViewer from "./viewers/DocumentViewer";
import type { PreviewDocument } from "./types";
const mocks=vi.hoisted(()=>({read:vi.fn(),content:vi.fn()}));
vi.mock("../state/ConnectionContext",()=>({useApi:()=>({preview:mocks})}));
vi.mock("react-i18next",()=>({useTranslation:()=>({t:(key:string)=>key})}));
let root:Root;let host:HTMLDivElement;let client:QueryClient;
const doc:PreviewDocument={name:"test.txt",kind:"text",format:"text",size:5,mime:"text/plain",version:"1",text:"safe"};
beforeEach(()=>{(globalThis as {IS_REACT_ACT_ENVIRONMENT?:boolean}).IS_REACT_ACT_ENVIRONMENT=true;host=document.createElement("div");document.body.append(host);root=createRoot(host);client=new QueryClient({defaultOptions:{queries:{retry:false}}});mocks.read.mockReset();mocks.content.mockReset();mocks.read.mockResolvedValue(doc);});
afterEach(async()=>{await act(async()=>root.unmount());client.clear();host.remove();});
async function mount(path="file.txt"){await act(async()=>root.render(<QueryClientProvider client={client}><ContentPreview agentId="a" path={path} onClose={()=>undefined}/></QueryClientProvider>));}
async function ready(check:()=>void){await vi.waitFor(async()=>{await act(async()=>{await new Promise(r=>setTimeout(r,0));});check();});}
function button(label:string){return [...host.querySelectorAll<HTMLButtonElement>("button")].find(x=>x.textContent===label)!;}
describe("格式隔离和预览生命周期",()=>{
 it("HTML保留静态样式并阻断脚本、跳转、嵌套文档与远程资源",()=>{
  const html=isolatedHTML('<style>h1{color:red;background:url(https://invalid.test)}</style><h1 onclick="alert(1)">内容</h1><script>alert(1)</script><meta http-equiv="refresh" content="0;url=https://bad.test"><iframe srcdoc="bad"></iframe><a href="javascript:alert(1)">链接</a><img src="https://bad.test/x" srcset="bad 1x"><form action="https://bad.test"></form>');
  expect(html).toContain("h1{color:red");expect(html).toContain("内容");expect(html).toContain("default-src 'none'");expect(html).not.toMatch(/<script|<iframe|onclick=|srcset=|http-equiv="refresh"|<form|href=/i);
 });
 it("文件切换会中止旧请求，迟到结果不能覆盖新文件",async()=>{
  let finish!:(v:PreviewDocument)=>void;mocks.read.mockImplementationOnce(()=>new Promise(resolve=>{finish=resolve;}));await mount("first.txt");await ready(()=>expect(mocks.read).toHaveBeenCalledOnce());const signal=mocks.read.mock.calls[0]![2] as AbortSignal;
  await mount("second.txt");expect(signal.aborted).toBe(true);await act(async()=>finish({...doc,text:"STALE"}));await ready(()=>expect(host.textContent).toContain("safe"));expect(host.textContent).not.toContain("STALE");
 });
 it("关闭二进制预览回收Blob URL且不保留查询缓存",async()=>{
  const create=vi.spyOn(URL,"createObjectURL").mockReturnValue("blob:test-preview");const revoke=vi.spyOn(URL,"revokeObjectURL").mockImplementation(()=>undefined);
  mocks.read.mockResolvedValue({...doc,kind:"image",format:"image",name:"image.png"});mocks.content.mockResolvedValue(new Blob(["test"],{type:"image/png"}));await mount("image.png");await ready(()=>expect(host.querySelector("img")?.getAttribute("src")).toBe("blob:test-preview"));await act(async()=>root.render(null));await ready(()=>expect(revoke).toHaveBeenCalledWith("blob:test-preview"));create.mockRestore();revoke.mockRestore();
 });
 it("反复切换文档只保留当前读取结果，关闭后释放全部预览缓存",async()=>{
  mocks.read.mockImplementation(async(_agent:string,path:string)=>({...doc,name:path,text:"Synthetic local content ".repeat(4000)}));
  for(let index=0;index<12;index++){
   await mount(`note-${index}.md`);
   await ready(()=>expect(host.querySelector("strong")?.textContent).toBe(`note-${index}.md`));
   await ready(()=>expect(client.getQueryCache().getAll()).toHaveLength(1));
  }
  expect(mocks.read).toHaveBeenCalledTimes(12);
  await act(async()=>root.render(null));
  await ready(()=>expect(client.getQueryCache().getAll()).toHaveLength(0));
  expect(host.querySelector(".content-preview")).toBeNull();
 });
 it("表格分页限制DOM规模并可切换工作表",async()=>{
  await act(async()=>root.render(<SheetViewer document={{...doc,kind:"sheet",sheets:[{name:"A",rows:Array.from({length:230},(_,i)=>[String(i)])},{name:"B",rows:[["另一个表"]]}]}} agentId="a" path="a"/>));
  expect(host.querySelectorAll("tbody tr")).toHaveLength(100);await act(async()=>button("preview.next").click());expect(host.querySelector("tbody td")?.textContent).toBe("100");
  const select=host.querySelector("select")!;await act(async()=>{select.value="1";select.dispatchEvent(new Event("change",{bubbles:true}));});expect(host.textContent).toContain("另一个表");expect(host.querySelectorAll("tbody tr")).toHaveLength(1);
 });
 it("幻灯片只挂载当前页",async()=>{await act(async()=>root.render(<SlidesViewer document={{...doc,kind:"slides",slides:[{name:"1",blocks:[{kind:"paragraph",text:"第一页"}]},{name:"2",blocks:[{kind:"paragraph",text:"第二页"}]}]}} agentId="a" path="a"/>));expect(host.textContent).toContain("第一页");expect(host.textContent).not.toContain("第二页");await act(async()=>button("preview.next").click());expect(host.textContent).not.toContain("第一页");expect(host.textContent).toContain("第二页");});
 it("多个Word小表格分页只挂载当前预算内的单元格",async()=>{
  const blocks=Array.from({length:70},(_,table)=>({kind:"table",rows:Array.from({length:12},(_,row)=>Array.from({length:8},(_,column)=>`table-${table} row-${row} cell-${column}`))}));
  await act(async()=>root.render(<DocumentViewer document={{...doc,kind:"document",blocks}} agentId="a" path="many-tables.docx"/>));
  expect(host.querySelectorAll("td")).toHaveLength(960);expect(host.textContent).not.toContain("table-10 ");
  await act(async()=>button("preview.next").click());expect(host.querySelectorAll("td")).toHaveLength(960);expect(host.textContent).not.toContain("table-0 ");expect(host.textContent).toContain("table-10 ");
 });
});

it("长Markdown仍按页阅读且不会一次布局完整长段落",async()=>{
 mocks.read.mockResolvedValue({...doc,kind:"markdown",format:"markdown",text:"a".repeat(60000)+"LAST_PAGE"});await mount("large.md");await ready(()=>expect(host.textContent).toContain("preview.next"));expect(host.textContent).not.toContain("LAST_PAGE");expect(host.querySelector(".message-markdown")!.textContent?.length).toBeLessThanOrEqual(20000);for(let page=0;page<3;page++)await act(async()=>button("preview.next").click());expect(host.textContent).toContain("LAST_PAGE");expect(host.querySelector("pre")).toBeNull();
});

it("离开音视频时显式停止播放并卸载来源",async()=>{
 const pause=vi.spyOn(HTMLMediaElement.prototype,"pause").mockImplementation(()=>undefined);const load=vi.spyOn(HTMLMediaElement.prototype,"load").mockImplementation(()=>undefined);
 const create=vi.spyOn(URL,"createObjectURL").mockReturnValue("blob:audio-test");const revoke=vi.spyOn(URL,"revokeObjectURL").mockImplementation(()=>undefined);
 mocks.read.mockResolvedValue({...doc,kind:"audio",name:"a.wav"});mocks.content.mockResolvedValue(new Blob(["audio"],{type:"audio/wav"}));await mount("a.wav");await ready(()=>expect(host.querySelector("audio")).not.toBeNull());const media=host.querySelector("audio")!;
 await act(async()=>root.render(null));expect(pause).toHaveBeenCalledOnce();expect(load).toHaveBeenCalledOnce();expect(media.hasAttribute("src")).toBe(false);expect(revoke).toHaveBeenCalledWith("blob:audio-test");pause.mockRestore();load.mockRestore();create.mockRestore();revoke.mockRestore();
});
