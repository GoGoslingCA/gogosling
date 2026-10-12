import { describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError } from "../client";
import { previewApi } from "./preview";
describe("文件预览协议",()=>{
 it("路径只作为查询值，二进制不走JSON解析且取消信号透传",async()=>{
  const fetchImpl=vi.fn(async()=>new Response("%PDF-1.4",{headers:{"Content-Type":"application/pdf"}}));const api=previewApi(new ApiClient({baseUrl:"http://127.0.0.1",token:"",fetchImpl}));const controller=new AbortController();
  const blob=await api.content("a/1","inbound/文件 #1.pdf","v1",controller.signal);expect(await blob.text()).toBe("%PDF-1.4");
  const [url,options]=fetchImpl.mock.calls[0] as unknown as [string,RequestInit];expect(new URL(url).pathname).toBe("/api/v1/agents/a%2F1/file-content");expect(new URL(url).searchParams.get("path")).toBe("inbound/文件 #1.pdf");expect(options.signal).toBe(controller.signal);expect(options.cache).toBe("no-store");
 });
 it("损坏文件错误不会被当作文件Blob",async()=>{const fetchImpl=vi.fn(async()=>new Response(JSON.stringify({error:{code:"invalid",message:"bad"}}),{status:400}));const api=previewApi(new ApiClient({baseUrl:"http://127.0.0.1",token:"",fetchImpl}));await expect(api.content("a","bad.docx")).rejects.toBeInstanceOf(ApiError);});
});
