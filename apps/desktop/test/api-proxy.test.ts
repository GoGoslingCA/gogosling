import { describe, expect, it } from "vitest";
import {
  apiProxyPath,
  apiProxyTarget,
  isSameOriginRequest,
  proxyApiRequest,
  proxyRequestHeaders,
  proxyResponseHeaders,
  rendererConnection,
  type ApiProxyDeps,
  type DaemonConnection,
} from "../src/main/api-proxy.js";

const ENDPOINT = "http://127.0.0.1:46750";
const CONN: DaemonConnection = { endpoint: ENDPOINT, token: "tok-secret" };

interface Captured {
  url: string;
  init: RequestInit;
}

function makeDeps(respond: (url: string, init: RequestInit) => Response | Promise<Response>, conn: DaemonConnection | null = CONN) {
  const calls: Captured[] = [];
  const errors: unknown[] = [];
  const deps: ApiProxyDeps = {
    connection: () => conn,
    fetch: async (url, init) => {
      calls.push({ url, init });
      return respond(url, init);
    },
    onError: ({ err }) => errors.push(err),
  };
  return { deps, calls, errors };
}

describe("apiProxyPath / apiProxyTarget：路径映射", () => {
  it("app://gogosling/api/... 映射到守护进程的同一路径，查询串保留", () => {
    expect(apiProxyPath("app://gogosling/api/v1/events?since=12&session_id=s1")).toBe("/api/v1/events?since=12&session_id=s1");
    expect(apiProxyTarget("app://gogosling/api/v1/sessions/s%2F1/messages", ENDPOINT)).toBe(`${ENDPOINT}/api/v1/sessions/s%2F1/messages`);
    expect(apiProxyTarget("app://gogosling/api/v1/version", `${ENDPOINT}/`)).toBe(`${ENDPOINT}/api/v1/version`);
  });

  it.each([
    ["页面根路径", "app://gogosling/"],
    ["静态资源", "app://gogosling/assets/index.js"],
    ["/api 本身（没有尾斜杠）", "app://gogosling/api"],
    ["/apix 前缀", "app://gogosling/apix/v1/version"],
    ["/healthz 不代理", "app://gogosling/healthz"],
    [".. 被 URL 解析器归一化后不再是 /api/", "app://gogosling/api/../index.html"],
    ["编码的 .. 同样被归一化", "app://gogosling/api/%2e%2e/secret"],
    ["错误的主机", "app://other/api/v1/version"],
    ["错误的协议", "http://gogosling/api/v1/version"],
    ["不是 URL", "not a url"],
  ])("不代理：%s", (_name, url) => {
    expect(apiProxyPath(url)).toBeNull();
    expect(apiProxyTarget(url, ENDPOINT)).toBeNull();
  });
});

describe("请求头与响应头", () => {
  it("注入主进程的 Bearer 令牌；页面自带的 Authorization、Cookie、Origin 不转发", () => {
    const incoming = new Headers({
      Accept: "text/event-stream",
      "Accept-Language": "en",
      "Content-Type": "application/json",
      "Last-Event-ID": "42",
      Authorization: "Bearer forged",
      Cookie: "a=b",
      Origin: "app://gogosling",
      Referer: "app://gogosling/index.html",
    });
    const out = proxyRequestHeaders(incoming, "tok-secret");
    expect(out.get("authorization")).toBe("Bearer tok-secret");
    expect(out.get("accept")).toBe("text/event-stream");
    expect(out.get("accept-language")).toBe("en");
    expect(out.get("content-type")).toBe("application/json");
    expect(out.get("last-event-id")).toBe("42");
    expect(out.get("cookie")).toBeNull();
    expect(out.get("origin")).toBeNull();
    expect(out.get("referer")).toBeNull();
  });

  it("响应头只交回白名单并统一 nosniff", () => {
    const out = proxyResponseHeaders(
      new Headers({ "Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache", "Set-Cookie": "x=y", "X-Accel-Buffering": "no", Server: "go" }),
    );
    expect(out.get("content-type")).toBe("text/event-stream; charset=utf-8");
    expect(out.get("cache-control")).toBe("no-cache");
    expect(out.get("x-accel-buffering")).toBe("no");
    expect(out.get("x-content-type-options")).toBe("nosniff");
    expect(out.get("set-cookie")).toBeNull();
    expect(out.get("server")).toBeNull();
  });

  it("同源判定：没有 Origin 或 Origin 为 app://gogosling", () => {
    expect(isSameOriginRequest(new Headers())).toBe(true);
    expect(isSameOriginRequest(new Headers({ Origin: "app://gogosling" }))).toBe(true);
    expect(isSameOriginRequest(new Headers({ Origin: "http://127.0.0.1:5173" }))).toBe(false);
    expect(isSameOriginRequest(new Headers({ Origin: "null" }))).toBe(false);
  });
});

describe("proxyApiRequest", () => {
  it("非 /api 路径返回 null，交给静态文件处理，不访问守护进程", async () => {
    const { deps, calls } = makeDeps(() => new Response("x"));
    expect(await proxyApiRequest(new Request("app://gogosling/index.html"), deps)).toBeNull();
    expect(await proxyApiRequest(new Request("app://gogosling/assets/app.js"), deps)).toBeNull();
    expect(calls).toHaveLength(0);
  });

  it("GET 转发到守护进程，注入令牌，状态码与 JSON 体原样返回", async () => {
    const { deps, calls } = makeDeps(() => new Response('{"items":[]}', { status: 200, headers: { "content-type": "application/json" } }));
    const res = await proxyApiRequest(new Request("app://gogosling/api/v1/agents?limit=5", { headers: { Accept: "application/json", Authorization: "Bearer forged" } }), deps);
    expect(res?.status).toBe(200);
    expect(await res?.text()).toBe('{"items":[]}');
    expect(res?.headers.get("content-type")).toBe("application/json");
    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toBe(`${ENDPOINT}/api/v1/agents?limit=5`);
    expect(calls[0]!.init.method).toBe("GET");
    expect(calls[0]!.init.redirect).toBe("error");
    expect(calls[0]!.init.body).toBeUndefined();
    expect(new Headers(calls[0]!.init.headers).get("authorization")).toBe("Bearer tok-secret");
  });

  it("POST 体与 Content-Type 被转发；Origin 为 app://gogosling 时放行", async () => {
    const { deps, calls } = makeDeps(() => new Response('{"id":"r1"}', { status: 202, headers: { "content-type": "application/json" } }));
    const res = await proxyApiRequest(
      new Request("app://gogosling/api/v1/sessions/s1/messages", {
        method: "POST",
        headers: { "Content-Type": "application/json", Origin: "app://gogosling" },
        body: '{"text":"hi"}',
      }),
      deps,
    );
    expect(res?.status).toBe(202);
    expect(calls[0]!.init.method).toBe("POST");
    expect(new TextDecoder().decode(calls[0]!.init.body as ArrayBuffer)).toBe('{"text":"hi"}');
    expect(new Headers(calls[0]!.init.headers).get("content-type")).toBe("application/json");
  });

  it("其他来源（例如开发服务器页面）的请求被拒绝，不借用主进程令牌", async () => {
    const { deps, calls } = makeDeps(() => new Response("{}"));
    const res = await proxyApiRequest(
      new Request("app://gogosling/api/v1/agents", { method: "DELETE", headers: { Origin: "http://127.0.0.1:5173" } }),
      deps,
    );
    expect(res?.status).toBe(403);
    expect(await res?.json()).toEqual({ error: { code: "forbidden", message: "origin not allowed" } });
    expect(calls).toHaveLength(0);
  });

  it("守护进程未就绪 → 503 unavailable（契约错误信封）", async () => {
    const { deps, calls } = makeDeps(() => new Response("{}"), null);
    const res = await proxyApiRequest(new Request("app://gogosling/api/v1/version"), deps);
    expect(res?.status).toBe(503);
    expect(await res?.json()).toMatchObject({ error: { code: "unavailable" } });
    expect(calls).toHaveLength(0);
  });

  it("转发失败（连接被拒）→ 502 unavailable，并记录错误", async () => {
    const { deps, errors } = makeDeps(() => {
      throw new TypeError("fetch failed");
    });
    const res = await proxyApiRequest(new Request("app://gogosling/api/v1/version"), deps);
    expect(res?.status).toBe(502);
    expect(await res?.json()).toMatchObject({ error: { code: "unavailable" } });
    expect(errors).toHaveLength(1);
  });

  it("守护进程的错误状态（401 等）原样交回，web 的 ApiError 能据此处理", async () => {
    const { deps } = makeDeps(
      () => new Response('{"error":{"code":"unauthorized","message":"bad token"}}', { status: 401, headers: { "content-type": "application/json", "www-authenticate": "Bearer" } }),
    );
    const res = await proxyApiRequest(new Request("app://gogosling/api/v1/version"), deps);
    expect(res?.status).toBe(401);
    expect(res?.headers.get("www-authenticate")).toBe("Bearer");
    expect(await res?.json()).toMatchObject({ error: { code: "unauthorized" } });
  });

  it("204 不带响应体", async () => {
    const { deps } = makeDeps(() => new Response(null, { status: 204 }));
    const res = await proxyApiRequest(new Request("app://gogosling/api/v1/agents/a1", { method: "DELETE" }), deps);
    expect(res?.status).toBe(204);
    expect(res?.body).toBeNull();
  });

  it("流式响应（SSE）逐块交回，不等上游结束", async () => {
    const encoder = new TextEncoder();
    let push!: (chunk: string) => void;
    let close!: () => void;
    const upstreamBody = new ReadableStream<Uint8Array>({
      start(controller) {
        push = (chunk) => controller.enqueue(encoder.encode(chunk));
        close = () => controller.close();
      },
    });
    const { deps, calls } = makeDeps(() => new Response(upstreamBody, { status: 200, headers: { "content-type": "text/event-stream" } }));
    const res = await proxyApiRequest(new Request("app://gogosling/api/v1/events?since=0", { headers: { Accept: "text/event-stream", "Last-Event-ID": "7" } }), deps);
    expect(res?.headers.get("content-type")).toBe("text/event-stream");
    expect(new Headers(calls[0]!.init.headers).get("last-event-id")).toBe("7");

    const reader = res!.body!.getReader();
    const decoder = new TextDecoder();
    push(": connected\n\n");
    expect(decoder.decode((await reader.read()).value)).toBe(": connected\n\n");
    push('id: 1\nevent: text.delta\ndata: {"seq":1}\n\n');
    expect(decoder.decode((await reader.read()).value)).toBe('id: 1\nevent: text.delta\ndata: {"seq":1}\n\n');
    close();
    expect((await reader.read()).done).toBe(true);
  });
});

describe("rendererConnection", () => {
  it("打包后：同源代理地址，不给令牌", () => {
    expect(rendererConnection(CONN, { dev: false })).toEqual({ endpoint: "app://gogosling", token: "", mode: "proxy" });
  });

  it("开发模式（GOGOSLING_WEB_URL）：保持直连", () => {
    expect(rendererConnection(CONN, { dev: true })).toEqual({ endpoint: ENDPOINT, token: "tok-secret", mode: "direct" });
  });

  it("守护进程未就绪：null", () => {
    expect(rendererConnection(null, { dev: false })).toBeNull();
  });
});
