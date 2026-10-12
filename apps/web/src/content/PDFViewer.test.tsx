import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import PDFViewer from "./viewers/PDFViewer";
const mocks=vi.hoisted(()=>({content:vi.fn(),getDocument:vi.fn()}));
vi.mock("pdfjs-dist",()=>({GlobalWorkerOptions:{workerSrc:""},getDocument:mocks.getDocument}));
vi.mock("pdfjs-dist/build/pdf.worker.min.mjs?url",()=>({default:"/worker.mjs"}));
vi.mock("../state/ConnectionContext",()=>({useApi:()=>({preview:{content:mocks.content}})}));
vi.mock("react-i18next",()=>({useTranslation:()=>({t:(key:string)=>key})}));
let root:Root;let host:HTMLDivElement;let client:QueryClient;
beforeEach(()=>{(globalThis as {IS_REACT_ACT_ENVIRONMENT?:boolean}).IS_REACT_ACT_ENVIRONMENT=true;host=document.createElement("div");document.body.append(host);root=createRoot(host);client=new QueryClient();mocks.content.mockReset();mocks.getDocument.mockReset();vi.stubGlobal("ResizeObserver",class {observe(){} disconnect(){}});});
afterEach(async()=>{await act(async()=>root.unmount());client.clear();host.remove();vi.unstubAllGlobals();});
async function ready(check:()=>void){await vi.waitFor(async()=>{await act(async()=>{await new Promise(r=>setTimeout(r,0));});check();});}
it("关闭PDF终止未结束的解析任务，禁止XFA并设置像素预算",async()=>{
 mocks.content.mockResolvedValue(new Blob(["%PDF-1.4"]));const destroy=vi.fn(async()=>undefined);mocks.getDocument.mockReturnValue({promise:new Promise(()=>undefined),destroy});
 await act(async()=>root.render(<QueryClientProvider client={client}><PDFViewer agentId="a" path="a.pdf" document={{name:"a.pdf",kind:"pdf",format:"pdf",mime:"application/pdf",size:8,version:"1"}}/></QueryClientProvider>));
 await ready(()=>expect(mocks.getDocument).toHaveBeenCalledOnce());expect(mocks.getDocument.mock.calls[0]![0]).toMatchObject({enableXfa:false,maxImageSize:16000000,canvasMaxAreaInBytes:32000000,stopAtErrors:true});
 await act(async()=>root.render(null));expect(destroy).toHaveBeenCalledOnce();
});
