import { describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, buildQuery, codeFromStatus, normalizeBaseUrl, parseErrorBody } from "./client";

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", ...headers },
  });
}

describe("ApiClient 请求头与 URL", () => {
  it("携带 Bearer 令牌、Accept-Language 与 JSON 体", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(201, { id: "1" }));
    const client = new ApiClient({ baseUrl: "http://127.0.0.1:46750/", token: "tok", locale: () => "en", fetchImpl });
    const out = await client.post<{ id: string }>("/api/v1/agents", { name: "a" }, { query: { x: 1, skip: undefined } });
    expect(out).toEqual({ id: "1" });
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("http://127.0.0.1:46750/api/v1/agents?x=1");
    const headers = init.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer tok");
    expect(headers["Accept-Language"]).toBe("en");
    expect(headers["Content-Type"]).toBe("application/json");
    expect(init.body).toBe('{"name":"a"}');
    expect(init.method).toBe("POST");
  });

  it("locale 是函数：语言切换后下一次请求立即生效", async () => {
    let lang = "zh";
    const fetchImpl = vi.fn(async () => jsonResponse(200, {}));
    const client = new ApiClient({ baseUrl: "", token: "t", locale: () => lang, fetchImpl });
    await client.get("/a");
    lang = "en";
    await client.get("/a");
    const langs = fetchImpl.mock.calls.map((c) => ((c as unknown as [string, RequestInit])[1].headers as Record<string, string>)["Accept-Language"]);
    expect(langs).toEqual(["zh", "en"]);
  });

  it("FormData 不设置 Content-Type（由浏览器加 boundary）", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(201, { name: "f" }));
    const client = new ApiClient({ baseUrl: "", token: "t", fetchImpl });
    const form = new FormData();
    form.append("file", new Blob(["x"]), "x.txt");
    await client.post("/upload", form);
    const init = (fetchImpl.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
    expect(init.body).toBe(form);
  });

  it("204 与空响应体返回 undefined", async () => {
    const fetchImpl = vi.fn(async () => new Response(null, { status: 204 }));
    const client = new ApiClient({ baseUrl: "", token: "t", fetchImpl });
    await expect(client.delete("/x")).resolves.toBeUndefined();
  });

  it("normalizeBaseUrl 补协议、去末尾斜杠，空串保持同源", () => {
    expect(normalizeBaseUrl(" 127.0.0.1:46750/ ")).toBe("http://127.0.0.1:46750");
    expect(normalizeBaseUrl("https://x.test///")).toBe("https://x.test");
    expect(normalizeBaseUrl("")).toBe("");
  });

  it("桌面壳代理模式：app://gogosling 原样保留，令牌为空时不发 Authorization 头", async () => {
    expect(normalizeBaseUrl("app://gogosling/")).toBe("app://gogosling");
    const fetchImpl = vi.fn(async () => jsonResponse(200, { items: [] }));
    const client = new ApiClient({ baseUrl: "app://gogosling", token: "", locale: () => "zh", fetchImpl });
    expect(client.url("/api/v1/events", { since: 0 })).toBe("app://gogosling/api/v1/events?since=0");
    expect(client.authHeaders()).toEqual({ "Accept-Language": "zh" });
    await client.get("/api/v1/agents");
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("app://gogosling/api/v1/agents");
    const headers = init.headers as Record<string, string>;
    expect(headers.Authorization).toBeUndefined();
    expect("Authorization" in headers).toBe(false);
  });

  it("buildQuery 跳过 undefined/null", () => {
    expect(buildQuery({ a: 1, b: undefined, c: null, d: "x y" })).toBe("?a=1&d=x+y");
    expect(buildQuery({})).toBe("");
  });
});

describe("ApiClient 错误映射", () => {
  it("服务端错误信封 → ApiError{code,message,details,status}", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(409, { error: { code: "conflict", message: "会话有未结束的运行", details: { run_id: "r1" } } }));
    const client = new ApiClient({ baseUrl: "", token: "t", fetchImpl });
    const err = await client.post("/api/v1/sessions/s/messages", { text: "x" }).catch((e: unknown) => e);
    expect(ApiError.is(err, "conflict")).toBe(true);
    const api = err as ApiError;
    expect(api.message).toBe("会话有未结束的运行");
    expect(api.details).toEqual({ run_id: "r1" });
    expect(api.status).toBe(409);
  });

  it("未知错误码按 HTTP 状态推断", () => {
    expect(parseErrorBody(404, { error: { code: "SOMETHING_NEW", message: "m" } })).toMatchObject({ code: "not_found", message: "m" });
  });

  it("非 JSON 响应体（例如代理的 502 HTML）按状态码映射", async () => {
    const fetchImpl = vi.fn(async () => new Response("<html>Bad Gateway</html>", { status: 502, statusText: "Bad Gateway", headers: { "content-type": "text/html" } }));
    const client = new ApiClient({ baseUrl: "", token: "t", fetchImpl });
    const err = await client.get("/x").catch((e: unknown) => e);
    expect(ApiError.is(err, "unavailable")).toBe(true);
    expect((err as ApiError).status).toBe(502);
  });

  it("codeFromStatus 覆盖常见状态码", () => {
    expect(codeFromStatus(400)).toBe("invalid");
    expect(codeFromStatus(401)).toBe("unauthorized");
    expect(codeFromStatus(403)).toBe("forbidden");
    expect(codeFromStatus(404)).toBe("not_found");
    expect(codeFromStatus(409)).toBe("conflict");
    expect(codeFromStatus(503)).toBe("unavailable");
    expect(codeFromStatus(504)).toBe("timeout");
    expect(codeFromStatus(500)).toBe("internal");
  });

  it("fetch 抛出 TypeError → network；AbortError → aborted", async () => {
    const client = new ApiClient({
      baseUrl: "",
      token: "t",
      fetchImpl: async () => {
        throw new TypeError("Failed to fetch");
      },
    });
    const err = await client.get("/x").catch((e: unknown) => e);
    expect(ApiError.is(err, "network")).toBe(true);
    expect((err as ApiError).status).toBe(0);

    const aborted = new ApiClient({
      baseUrl: "",
      token: "t",
      fetchImpl: async () => {
        throw new DOMException("The operation was aborted.", "AbortError");
      },
    });
    const err2 = await aborted.get("/x").catch((e: unknown) => e);
    expect(ApiError.is(err2, "aborted")).toBe(true);
  });

  it("401 触发 onUnauthorized 回调，但仍然抛出错误", async () => {
    const onUnauthorized = vi.fn();
    const client = new ApiClient({
      baseUrl: "",
      token: "t",
      onUnauthorized,
      fetchImpl: async () => jsonResponse(401, { error: { code: "unauthorized", message: "bad token" } }),
    });
    await expect(client.get("/x")).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    expect(onUnauthorized.mock.calls[0]?.[0]).toBeInstanceOf(ApiError);
  });

  it("2xx 但响应体不是 JSON → internal", async () => {
    const client = new ApiClient({ baseUrl: "", token: "t", fetchImpl: async () => new Response("not json", { status: 200 }) });
    const err = await client.get("/x").catch((e: unknown) => e);
    expect(ApiError.is(err, "internal")).toBe(true);
  });
});
