import { describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError } from "../client";
import { workbenchApi } from "./workbench";

describe("工作台 API 的协议与读取边界", () => {
  it("草稿更新和删除都发送预期版本，owner 不由客户端补入", async () => {
    const fetchImpl = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ id: "draft/1", version: 3 }))).mockResolvedValueOnce(new Response(null, { status: 204 }));
    const api = workbenchApi(new ApiClient({ baseUrl: "http://127.0.0.1", token: "", fetchImpl }));
    await api.updateDraft("draft/1", { kind: "composer", agent_id: "a1", session_id: "s1", text: "新文字", expected_version: 2 });
    await api.deleteDraft("draft/1", 3);
    expect(fetchImpl.mock.calls[0]?.[0]).toBe("http://127.0.0.1/api/v1/drafts/draft%2F1");
    expect(fetchImpl.mock.calls[0]?.[1].method).toBe("PUT");
    expect(JSON.parse(fetchImpl.mock.calls[0]?.[1].body as string)).toEqual({ kind: "composer", agent_id: "a1", session_id: "s1", text: "新文字", expected_version: 2 });
    expect(fetchImpl.mock.calls[1]?.[0]).toBe("http://127.0.0.1/api/v1/drafts/draft%2F1?expected_version=3");
  });

  it("成果正文和下载保留原始字节，不把 HTML 或非 JSON 正文当 JSON 解析", async () => {
    const text = "<h1>仅作文本</h1>\n正文";
    const fetchImpl = vi.fn().mockResolvedValueOnce(new Response(text)).mockResolvedValueOnce(new Response(text));
    const api = workbenchApi(new ApiClient({ baseUrl: "http://127.0.0.1", token: "", fetchImpl }));
    expect(await api.readRevision("a/1", "r/1")).toBe(text);
    const blob = await api.downloadRevision("a/1", "r/1");
    expect(await blob.text()).toBe(text);
    expect(fetchImpl.mock.calls[0]?.[0]).toBe("http://127.0.0.1/api/v1/artifacts/a%2F1/revisions/r%2F1/content");
    expect(fetchImpl.mock.calls[1]?.[1].cache).toBe("no-store");
  });

  it("原始内容接口失败仍沿用 ApiError，不把错误信封返回成成果正文", async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ error: { code: "forbidden", message: "" } }), { status: 403 }));
    const api = workbenchApi(new ApiClient({ baseUrl: "http://127.0.0.1", token: "", fetchImpl }));
    await expect(api.readRevision("a1", "r1")).rejects.toBeInstanceOf(ApiError);
  });

  it("项目关联带双向 CAS，首次成果保存保留幂等键和来源消息", async () => {
    const fetchImpl = vi.fn(async () => new Response("{}"));
    const api = workbenchApi(new ApiClient({ baseUrl: "http://127.0.0.1", token: "", fetchImpl }));
    await api.setSessionProject("s1", { project_id: "p1", expected_project_id: null });
    await api.createArtifact("s1", { title: "草稿", kind: "document", filename: "draft.md", media_type: "text/markdown", source_message_id: "m1", content: "正文" }, "request-1");
    const calls = fetchImpl.mock.calls as unknown as Array<[string, RequestInit]>;
    expect(JSON.parse(calls[0]![1].body as string)).toEqual({ project_id: "p1", expected_project_id: null });
    expect(calls[1]![1].headers).toMatchObject({ "Idempotency-Key": "request-1" });
    expect(JSON.parse(calls[1]![1].body as string).source_message_id).toBe("m1");
  });
});
