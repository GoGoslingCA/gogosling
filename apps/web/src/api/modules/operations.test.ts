import { describe, expect, it, vi } from "vitest";
import { ApiClient } from "../client";
import { channelsApi } from "./channels";
import { auditApi } from "./audit";

function createClient(response: unknown = { items: [] }, status = 200) {
  const fetchImpl = vi.fn<typeof fetch>(async () => new Response(status === 204 ? null : JSON.stringify(response), { status }));
  return { client: new ApiClient({ baseUrl: "http://127.0.0.1:46750", token: "", fetchImpl }), fetchImpl };
}

describe("通道 API 契约", () => {
  it("读取按智能体过滤，携带取消信号；响应无 items 时回退为空列表", async () => {
    const { client, fetchImpl } = createClient({});
    const signal = new AbortController().signal;
    await expect(channelsApi(client).list("agent / 1", signal)).resolves.toEqual([]);
    expect(fetchImpl).toHaveBeenCalledWith("http://127.0.0.1:46750/api/v1/channels?agent_id=agent+%2F+1", expect.objectContaining({ method: "GET", signal }));
  });

  it("创建通过 JSON 体传递只写凭据，令牌不进入 URL", async () => {
    const { client, fetchImpl } = createClient({ id: "channel" }, 201);
    await channelsApi(client).create({ agent_id: "a", name: "bot", kind: "telegram", secret: "memory-only-example", config: { group_mode: "mention" } });
    const [url, options] = fetchImpl.mock.calls[0];
    expect(url).toBe("http://127.0.0.1:46750/api/v1/channels");
    expect(options?.method).toBe("POST");
    expect(JSON.parse(options?.body as string)).toEqual({ agent_id: "a", name: "bot", kind: "telegram", secret: "memory-only-example", config: { group_mode: "mention" } });
  });

  it("资源 ID 编码为路径段，重载与更新使用各自契约方法", async () => {
    const { client, fetchImpl } = createClient({ id: "a/b" });
    const api = channelsApi(client);
    await api.get("a/b");
    await api.update("a/b", { enabled: false });
    await api.reload("a/b");
    expect(fetchImpl.mock.calls.map(([url, options]) => [url, options?.method])).toEqual([
      ["http://127.0.0.1:46750/api/v1/channels/a%2Fb", "GET"],
      ["http://127.0.0.1:46750/api/v1/channels/a%2Fb", "PATCH"],
      ["http://127.0.0.1:46750/api/v1/channels/a%2Fb/reload", "POST"],
    ]);
    expect(JSON.parse(fetchImpl.mock.calls[1][1]?.body as string)).toEqual({ enabled: false });
    expect(fetchImpl.mock.calls[2][1]?.body).toBeUndefined();
  });

  it("删除接受 204 空响应", async () => {
    const { client, fetchImpl } = createClient(undefined, 204);
    await expect(channelsApi(client).remove("c")).resolves.toBeUndefined();
    expect(fetchImpl.mock.calls[0][1]?.method).toBe("DELETE");
  });
});

describe("审计 API 契约", () => {
  it("编码全部过滤项与分页，省略缺席值并携带取消信号", async () => {
    const { client, fetchImpl } = createClient({ items: [{ id: "entry" }] });
    const signal = new AbortController().signal;
    await expect(auditApi(client).list({ agent_id: "a", session_id: "s", run_id: "r", actor: "telegram:42", action: "tool.call", since: "2026-10-12T01:00:00Z", limit: 50, offset: 100 }, signal)).resolves.toEqual([{ id: "entry" }]);
    const url = new URL(fetchImpl.mock.calls[0][0] as string);
    expect(Object.fromEntries(url.searchParams)).toEqual({ agent_id: "a", session_id: "s", run_id: "r", actor: "telegram:42", action: "tool.call", since: "2026-10-12T01:00:00Z", limit: "50", offset: "100" });
    expect(fetchImpl.mock.calls[0][1]).toMatchObject({ signal, method: "GET" });
  });

  it("默认查询不添加空过滤项", async () => {
    const { client, fetchImpl } = createClient({});
    await expect(auditApi(client).list()).resolves.toEqual([]);
    expect(fetchImpl.mock.calls[0][0]).toBe("http://127.0.0.1:46750/api/v1/audit");
  });
});
