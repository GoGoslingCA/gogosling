import { createHash } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DesktopDiagnostics, type DiagnosticDependencies } from "../src/main/diagnostics.js";
import type { DiagnosticPreview, DiagnosticResult } from "../src/shared/diagnostics.js";

const EPOCH = Date.UTC(2026, 9, 13, 0, 0, 0);
const TICKET = "https://api.gogosling.ca/v1/desktop-diagnostics/tickets";
const INGEST = "https://api.gogosling.ca/v1/error-logs";
const instances: DesktopDiagnostics[] = [];
interface Call { url: string; init: RequestInit }
interface Batch {
  events: [{ event_id: string; tags: Record<string, unknown>; context: { category: string; recent_errors: { code: string }[] } }];
}

function json(value: unknown, status = 201, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(value), { status, headers: { "content-type": "application/json", ...headers } });
}

function body(call: Call): Record<string, unknown> {
  return JSON.parse(call.init.body as string) as Record<string, unknown>;
}

function ticket(call: Call, now = EPOCH): Response {
  return json({ token: "x".repeat(48), expires_at: new Date(now + 60_000).toISOString(), body_sha256: body(call).body_sha256 });
}

function receipt(call: Call, duplicate = false): Response {
  const sent = body(call) as unknown as Batch;
  return json({ success: true, accepted: duplicate ? 0 : 1, duplicates: duplicate ? 1 : 0, event_ids: [sent.events[0].event_id] });
}

function setup(overrides: Partial<DiagnosticDependencies> = {}, respond?: (call: Call) => Response | Promise<Response>) {
  const calls: Call[] = [];
  const snapshot = vi.fn(() => ({ daemonState: "ready", daemonOwned: true, restartAttempts: 0 }));
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const call = { url, init };
    calls.push(call);
    return respond ? respond(call) : url === TICKET ? ticket(call) : receipt(call);
  });
  const diagnostics = new DesktopDiagnostics({
    enabled: true, version: "0.1.0", platform: "darwin", arch: "arm64", snapshot,
    fetch, now: () => EPOCH, ...overrides,
  });
  instances.push(diagnostics);
  return { diagnostics, calls, fetch, snapshot };
}

function value<T>(result: DiagnosticResult<T>): T {
  expect(result.ok).toBe(true);
  if (!result.ok) throw new Error("测试准备失败");
  return result.value;
}

function preview(diagnostics: DesktopDiagnostics): DiagnosticPreview {
  return value(diagnostics.preview("application"));
}

afterEach(() => { instances.splice(0).forEach((instance) => instance.dispose()); vi.useRealTimers(); });

describe("手动诊断的可见性与隐私边界", () => {
  it("源码版禁用且不采集本地状态或发起网络请求", async () => {
    const { diagnostics, snapshot, fetch } = setup({ enabled: false });
    diagnostics.record("daemon_start_failed");
    expect(diagnostics.status()).toEqual({ available: false });
    expect(diagnostics.preview("application")).toEqual({ ok: false, code: "disabled" });
    expect(await diagnostics.send("anything")).toEqual({ ok: false, code: "disabled" });
    expect(diagnostics.cancel("anything", true)).toEqual({ ok: false, code: "disabled" });
    expect(snapshot).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("预览只保留枚举与数值，不复制日志、路径、内容或任意字段", () => {
    const privateText = "private-content-marker";
    const snapshot = vi.fn(() => ({
      daemonState: "failed", daemonOwned: true, restartAttempts: 2,
      errorCode: "spawn_failed", exitCode: 127,
      message: privateText, path: privateText, key: privateText, url: privateText,
      sessions: [privateText], stack: privateText, unexpected: privateText,
    }));
    const { diagnostics, fetch } = setup({ snapshot });
    diagnostics.record(privateText);
    diagnostics.record({ code: "spawn_failed", message: privateText });
    diagnostics.record("spawn_failed");
    diagnostics.record("http_429");
    diagnostics.record("http_429/private-marker");
    const result = preview(diagnostics);
    const batch = JSON.parse(result.body) as Batch;
    expect(batch.events[0].tags).toEqual({ platform: "darwin", arch: "arm64", daemon_state: "failed", daemon_owned: true, restart_attempts: 2, error_code: "spawn_failed", exit_code: 127 });
    expect(batch.events[0].context.recent_errors.map((entry) => entry.code)).toEqual(["spawn_failed", "http_429"]);
    expect(result.body).not.toContain(privateText);
    expect(fetch).not.toHaveBeenCalled();
    expect(result.retentionDays).toBe(90);
    expect(result.destination).toBe(INGEST);
    expect(result.byteLength).toBe(Buffer.byteLength(result.body));
    expect(result.byteLength).toBeLessThanOrEqual(16 * 1024);
    expect(result.sha256).toBe(createHash("sha256").update(result.body).digest("hex"));
  });

  it("不信任状态字段、版本与平台中的注入，不执行状态访问器", () => {
    const getter = vi.fn(() => "ready");
    const untrusted = Object.create({ daemonOwned: true }) as object;
    Object.defineProperties(untrusted, {
      daemonState: { get: getter }, restartAttempts: { value: Infinity },
      errorCode: { value: "private-marker" }, exitCode: { value: 4294967296 },
    });
    const { diagnostics } = setup({ snapshot: () => untrusted, version: "0.1.0+private-marker", platform: "private-marker", arch: "private-marker" });
    const result = preview(diagnostics);
    expect((JSON.parse(result.body) as Batch).events[0].tags).toEqual({ platform: "unknown", arch: "unknown" });
    expect(result.body).not.toContain("private-marker");
    expect(result.body).toContain('"release":"unknown"');
    expect(getter).not.toHaveBeenCalled();
    expect(diagnostics.preview("private-marker")).toEqual({ ok: false, code: "invalid_request" });
    expect(diagnostics.preview({ category: "task" })).toEqual({ ok: false, code: "invalid_request" });
  });

  it("生命周期记录只保留最近二十条，未知记录不会挤掉有效记录", () => {
    const { diagnostics } = setup();
    diagnostics.record("daemon_start_failed");
    for (let count = 0; count < 20; count++) diagnostics.record("load_failed");
    for (let count = 0; count < 30; count++) diagnostics.record("unknown");
    const records = (JSON.parse(preview(diagnostics).body) as Batch).events[0].context.recent_errors;
    expect(records).toHaveLength(20);
    expect(records.every((entry) => entry.code === "load_failed")).toBe(true);
  });

  it("采集失败只返回错误码，不泄露异常内容，后续预览仍能使用", () => {
    const snapshot = vi.fn<() => unknown>().mockImplementationOnce(() => { throw new Error("private-marker"); }).mockReturnValue({});
    const { diagnostics } = setup({ snapshot });
    expect(diagnostics.preview("application")).toEqual({ ok: false, code: "unavailable" });
    expect(preview(diagnostics).body).not.toContain("private-marker");
  });
});

describe("确认内容与短效上传票据", () => {
  it("只上传不可变预览；票据绑定准确字节与哈希，凭据只到固定接收地址", async () => {
    const { diagnostics, calls, snapshot } = setup();
    const report = preview(diagnostics);
    snapshot.mockReturnValue({ daemonState: "failed", daemonOwned: false, restartAttempts: 1 });
    diagnostics.record("spawn_failed");
    const uploaded = value(await diagnostics.send(report.id));
    expect(calls).toHaveLength(2);
    expect(calls.map((call) => call.url)).toEqual([TICKET, INGEST]);
    expect(body(calls[0]!)).toEqual({ schema_version: 1, body_sha256: report.sha256, body_bytes: report.byteLength, event_id: uploaded.eventId });
    expect(calls[1]!.init.body).toBe(report.body);
    expect(snapshot).toHaveBeenCalledTimes(1);
    expect(Object.isFrozen(report)).toBe(true);
    expect(report.body).not.toContain("x".repeat(48));
    expect(new Headers(calls[0]!.init.headers).has("authorization")).toBe(false);
    expect(new Headers(calls[1]!.init.headers).get("authorization")).toBe(`Bearer ${"x".repeat(48)}`);
    for (const call of calls) {
      expect(call.init.redirect).toBe("error");
      expect(call.init.credentials).toBe("omit");
      expect(call.init.cache).toBe("no-store");
      expect(call.init.referrerPolicy).toBe("no-referrer");
      expect(new Headers(call.init.headers).has("cookie")).toBe(false);
    }
  });

  it("票据接口必须返回约定的 201，不接受返回的地址覆盖固定接收端", async () => {
    let correct = false;
    const { diagnostics, calls } = setup({}, (call) => call.url === TICKET
      ? json({ token: "x".repeat(48), expires_at: new Date(EPOCH + 60_000).toISOString(), body_sha256: body(call).body_sha256,
        url: "https://example.invalid/collect", destination: "https://example.invalid/collect" }, correct ? 201 : 200)
      : receipt(call));
    const report = preview(diagnostics);
    expect(await diagnostics.send(report.id)).toEqual({ ok: false, code: "invalid_response" });
    correct = true;
    expect((await diagnostics.send(report.id)).ok).toBe(true);
    expect(calls.map((call) => call.url)).toEqual([TICKET, TICKET, INGEST]);
  });

  it("确认上传成功后立即释放预览与定时器，不允许重复发送已完成预览", async () => {
    vi.useFakeTimers();
    const { diagnostics, calls } = setup();
    const report = preview(diagnostics);
    expect(vi.getTimerCount()).toBe(1);
    expect((await diagnostics.send(report.id)).ok).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
    expect(await diagnostics.send(report.id)).toEqual({ ok: false, code: "stale_preview" });
    expect(diagnostics.cancel(report.id, true)).toEqual({ ok: false, code: "stale_preview" });
    expect(calls).toHaveLength(2);
    expect(diagnostics.preview("task").ok).toBe(true);
  });

  it("人工重试保持原事件与字节，每次获取新票据并识别服务端去重", async () => {
    let ingests = 0;
    const { diagnostics, calls } = setup({}, (call) => {
      if (call.url === TICKET) return ticket(call);
      ingests++;
      return ingests === 1 ? Promise.reject(new Error("connection-lost")) : receipt(call, true);
    });
    const report = preview(diagnostics);
    expect(await diagnostics.send(report.id)).toEqual({ ok: false, code: "unavailable" });
    const result = value(await diagnostics.send(report.id));
    expect(result.duplicate).toBe(true);
    expect(calls).toHaveLength(4);
    expect(calls[1]!.init.body).toBe(calls[3]!.init.body);
    expect(body(calls[0]!)).toEqual(body(calls[2]!));
  });

  it("预览过期或替换后拒绝旧标识，不能从调用参数替换内容", async () => {
    let now = EPOCH;
    const { diagnostics, fetch } = setup({ now: () => now });
    const old = preview(diagnostics);
    const current = preview(diagnostics);
    expect(await diagnostics.send(old.id)).toEqual({ ok: false, code: "stale_preview" });
    expect(await diagnostics.send({ id: current.id, body: "injected" })).toEqual({ ok: false, code: "invalid_request" });
    now += 10 * 60_000;
    expect(await diagnostics.send(current.id)).toEqual({ ok: false, code: "stale_preview" });
    expect(fetch).not.toHaveBeenCalled();
  });

  it.each([
    ["不匹配的哈希", { body_sha256: "0".repeat(64) }],
    ["过期票据", { expires_at: new Date(EPOCH).toISOString() }],
    ["寿命超过两分钟", { expires_at: new Date(EPOCH + 120_001).toISOString() }],
    ["没有时区", { expires_at: "2026-10-13T00:01:00" }],
    ["过短的票据", { token: "x" }],
    ["包含换行的票据", { token: `${"x".repeat(40)}\nforged` }],
    ["过长的票据", { token: "x".repeat(2049) }],
  ])("拒绝%s且不发送诊断内容", async (_label, patch) => {
    const { diagnostics, calls } = setup({}, (call) => json({
      token: "x".repeat(48), expires_at: new Date(EPOCH + 60_000).toISOString(), body_sha256: body(call).body_sha256, ...patch,
    }));
    expect(await diagnostics.send(preview(diagnostics).id)).toEqual({ ok: false, code: "invalid_response" });
    expect(calls).toHaveLength(1);
  });

  it.each([
    { success: false, accepted: 1, duplicates: 0 },
    { success: true, accepted: 1, duplicates: 1 },
    { success: true, accepted: 0.5, duplicates: 0.5 },
    { success: true, accepted: 0, duplicates: 0 },
    { success: true, accepted: "1", duplicates: 0 },
  ])("拒绝无效确认统计 %j", async (patch) => {
    const { diagnostics } = setup({}, (call) => call.url === TICKET ? ticket(call) : json({ ...patch, event_ids: [(body(call) as unknown as Batch).events[0].event_id] }));
    expect(await diagnostics.send(preview(diagnostics).id)).toEqual({ ok: false, code: "invalid_response" });
  });

  it("拒绝错误事件标识与额外标识，不把任意服务器文本展示给用户", async () => {
    const { diagnostics } = setup({}, (call) => call.url === TICKET ? ticket(call) : json({ success: true, accepted: 1, duplicates: 0, event_ids: ["unexpected-event"], message: "private-server-marker" }));
    expect(await diagnostics.send(preview(diagnostics).id)).toEqual({ ok: false, code: "invalid_response" });
  });
});

describe("边界响应与失败恢复", () => {
  it.each([TICKET, INGEST])("%s 阶段拒绝重定向且不请求返回的地址", async (phase) => {
    const { diagnostics, calls } = setup({}, (call) => call.url === phase ? new Response(null, { status: 307, headers: { location: "https://example.invalid/collect" } }) : ticket(call));
    expect(await diagnostics.send(preview(diagnostics).id)).toEqual({ ok: false, code: "invalid_response" });
    expect(calls.every((call) => call.url === TICKET || call.url === INGEST)).toBe(true);
    expect(calls).toHaveLength(phase === TICKET ? 1 : 2);
  });

  it.each([401, 403])("鉴权错误 %i 不自动重试且不返回服务器错误内容", async (status) => {
    const { diagnostics, calls } = setup({}, () => json({ message: "private-server-marker" }, status));
    expect(await diagnostics.send(preview(diagnostics).id)).toEqual({ ok: false, code: "unauthorized" });
    expect(calls).toHaveLength(1);
  });

  it.each([429, 503])("响应 %i 遵守等待时间，主进程阻止提前重试", async (status) => {
    let now = EPOCH;
    let errors = 1;
    const { diagnostics, calls } = setup({ now: () => now }, (call) => {
      if (errors-- > 0) return json({ message: "private-server-marker" }, status, { "retry-after": "5" });
      return call.url === TICKET ? ticket(call, now) : receipt(call);
    });
    const report = preview(diagnostics);
    const failure = { ok: false, code: status === 429 ? "rate_limited" : "unavailable", retryAfterSeconds: 5 };
    expect(await diagnostics.send(report.id)).toEqual(failure);
    expect(await diagnostics.send(report.id)).toEqual(failure);
    expect(calls).toHaveLength(1);
    now += 5000;
    expect((await diagnostics.send(report.id)).ok).toBe(true);
    expect(calls).toHaveLength(3);
  });

  it.each([
    ["0", 1], ["999999999", 3600], [new Date(EPOCH + 5000).toUTCString(), 5], ["private-marker", undefined],
  ])("等待时间 %s 只转换为有界数字", async (header, expected) => {
    const { diagnostics } = setup({}, () => json({}, 429, { "retry-after": String(header) }));
    const result = await diagnostics.send(preview(diagnostics).id);
    expect(result).toEqual(expected === undefined ? { ok: false, code: "rate_limited" } : { ok: false, code: "rate_limited", retryAfterSeconds: expected });
  });

  it.each([TICKET, INGEST])("%s 阶段限制响应流到八 KiB 并取消阅读器", async (phase) => {
    const cancel = vi.fn();
    const { diagnostics } = setup({}, (call) => call.url === phase ? new Response(new ReadableStream({
      start(controller) { controller.enqueue(new Uint8Array(8193)); }, cancel,
    }), { status: 201, headers: { "content-type": "application/json" } }) : ticket(call));
    expect(await diagnostics.send(preview(diagnostics).id)).toEqual({ ok: false, code: "invalid_response" });
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it("提前拒绝超大 Content-Length、不正确类型与破损 JSON", async () => {
    const variants = [
      () => new Response("{}", { status: 201, headers: { "content-type": "application/json", "content-length": "8193" } }),
      () => new Response("{}", { status: 201, headers: { "content-type": "text/html" } }),
      () => new Response("{", { status: 201, headers: { "content-type": "application/json" } }),
    ];
    for (const response of variants) {
      const { diagnostics } = setup({}, response);
      expect(await diagnostics.send(preview(diagnostics).id)).toEqual({ ok: false, code: "invalid_response" });
    }
  });
});

describe("中断、截止时间与资源回收", () => {
  it.each([TICKET, INGEST])("取消 %s 阶段会中断请求并立即允许人工重试", async (phase) => {
    let stalled = true;
    const { diagnostics, calls } = setup({}, (call) => {
      if (call.url === phase && stalled) return new Promise<Response>(() => undefined);
      return call.url === TICKET ? ticket(call) : receipt(call);
    });
    const report = preview(diagnostics);
    const pending = diagnostics.send(report.id);
    await vi.waitFor(() => expect(calls.at(-1)?.url).toBe(phase));
    expect(await diagnostics.send(report.id)).toEqual({ ok: false, code: "busy" });
    expect(diagnostics.preview("task")).toEqual({ ok: false, code: "busy" });
    expect(diagnostics.cancel("not-current", true)).toEqual({ ok: false, code: "stale_preview" });
    expect(calls.at(-1)!.init.signal!.aborted).toBe(false);
    expect(diagnostics.cancel(report.id, false)).toEqual({ ok: true, value: null });
    expect(calls.at(-1)!.init.signal!.aborted).toBe(true);
    stalled = false;
    const retry = diagnostics.send(report.id);
    expect(await pending).toEqual({ ok: false, code: "cancelled" });
    expect((await retry).ok).toBe(true);
  });

  it.each([TICKET, INGEST])("取消 %s 的响应体阅读会关闭流且不泄漏并发槽", async (phase) => {
    const cancel = vi.fn();
    const { diagnostics, calls } = setup({}, (call) => call.url === phase ? new Response(new ReadableStream({ cancel }), { status: 201, headers: { "content-type": "application/json" } }) : ticket(call));
    const report = preview(diagnostics);
    const pending = diagnostics.send(report.id);
    await vi.waitFor(() => expect(calls.at(-1)?.url).toBe(phase));
    diagnostics.cancel(report.id, true);
    expect(await pending).toEqual({ ok: false, code: "cancelled" });
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(await diagnostics.send(report.id)).toEqual({ ok: false, code: "stale_preview" });
    expect(diagnostics.preview("task").ok).toBe(true);
  });

  it.each([TICKET, INGEST])("%s 挂起时十五秒截止，后续预览仍可正常打开", async (phase) => {
    vi.useFakeTimers();
    const { diagnostics, calls } = setup({}, (call) => call.url === phase ? new Promise<Response>(() => undefined) : ticket(call));
    const pending = diagnostics.send(preview(diagnostics).id);
    await vi.advanceTimersByTimeAsync(14_999);
    expect(calls.at(-1)?.url).toBe(phase);
    expect(calls.at(-1)?.init.signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await pending).toEqual({ ok: false, code: "timeout" });
    expect(calls.at(-1)?.init.signal?.aborted).toBe(true);
    expect(diagnostics.preview("task").ok).toBe(true);
  });

  it.each([TICKET, INGEST])("%s 响应体挂起仍执行十五秒截止并关闭阅读器", async (phase) => {
    vi.useFakeTimers();
    const cancel = vi.fn();
    const { diagnostics, calls } = setup({}, (call) => call.url === phase
      ? new Response(new ReadableStream({ cancel }), { status: 201, headers: { "content-type": "application/json" } })
      : ticket(call));
    const pending = diagnostics.send(preview(diagnostics).id);
    await vi.advanceTimersByTimeAsync(15_000);
    expect(await pending).toEqual({ ok: false, code: "timeout" });
    expect(calls.at(-1)?.init.signal?.aborted).toBe(true);
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(diagnostics.preview("task").ok).toBe(true);
  });

  it("两个阶段共用十五秒而不是分别计时", async () => {
    vi.useFakeTimers();
    const { diagnostics, calls } = setup({}, (call) => call.url === TICKET
      ? new Promise<Response>((resolve) => setTimeout(() => resolve(ticket(call)), 10_000))
      : new Promise<Response>(() => undefined));
    const pending = diagnostics.send(preview(diagnostics).id);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(calls).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(5000);
    expect(await pending).toEqual({ ok: false, code: "timeout" });
    expect(calls[1]!.init.signal!.aborted).toBe(true);
  });

  it("取消后迟到的响应被取消，不再发送实际报告", async () => {
    let resolveLate!: (response: Response) => void;
    const cancel = vi.fn();
    const { diagnostics, calls } = setup({}, () => new Promise<Response>((resolve) => { resolveLate = resolve; }));
    const report = preview(diagnostics);
    const pending = diagnostics.send(report.id);
    diagnostics.cancel(report.id, true);
    expect(await pending).toEqual({ ok: false, code: "cancelled" });
    resolveLate(new Response(new ReadableStream({ cancel }), { status: 201 }));
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledTimes(1));
    expect(calls).toHaveLength(1);
  });

  it("窗口离开时 clear 中止并清除预览，销毁后不再收集或上传", async () => {
    const { diagnostics, snapshot, calls } = setup({}, () => new Promise<Response>(() => undefined));
    const report = preview(diagnostics);
    const pending = diagnostics.send(report.id);
    diagnostics.clear();
    expect(await pending).toEqual({ ok: false, code: "cancelled" });
    expect(await diagnostics.send(report.id)).toEqual({ ok: false, code: "stale_preview" });
    expect(diagnostics.status()).toEqual({ available: true });
    const next = preview(diagnostics);
    const nextPending = diagnostics.send(next.id);
    diagnostics.dispose();
    expect(await nextPending).toEqual({ ok: false, code: "cancelled" });
    expect(diagnostics.preview("task")).toEqual({ ok: false, code: "disabled" });
    expect(await diagnostics.send(next.id)).toEqual({ ok: false, code: "disabled" });
    expect(snapshot).toHaveBeenCalledTimes(2);
    expect(calls).toHaveLength(2);
    expect(calls.every((call) => call.init.signal?.aborted)).toBe(true);
  });

  it.each([TICKET, INGEST])("真实本地 TCP：取消 %s 及时关闭连接，后续手动重试可用", async (phase) => {
    let stalled = true;
    let notifyStarted!: () => void;
    let notifyClosed!: () => void;
    const started = new Promise<void>((resolve) => { notifyStarted = resolve; });
    const closed = new Promise<void>((resolve) => { notifyClosed = resolve; });
    const serve = async (request: IncomingMessage, response: ServerResponse): Promise<void> => {
      const chunks: Buffer[] = [];
      for await (const chunk of request) chunks.push(Buffer.from(chunk as Uint8Array));
      const payload = JSON.parse(Buffer.concat(chunks).toString("utf8")) as Record<string, unknown>;
      const isTicket = request.url === new URL(TICKET).pathname;
      if (stalled && (isTicket ? TICKET : INGEST) === phase) {
        response.once("close", notifyClosed);
        if (!isTicket) {
          response.writeHead(201, { "content-type": "application/json" });
          response.write('{"success":');
        }
        notifyStarted();
        return;
      }
      response.writeHead(201, { "content-type": "application/json" });
      response.end(JSON.stringify(isTicket
        ? { token: "x".repeat(48), expires_at: new Date(EPOCH + 60_000).toISOString(), body_sha256: payload.body_sha256 }
        : { success: true, accepted: 1, duplicates: 0, event_ids: [(payload as unknown as Batch).events[0].event_id] }));
    };
    const server = createServer((request, response) => {
      void serve(request, response).catch(() => response.destroy());
    });
    const bounded = async <T,>(promise: Promise<T>): Promise<T> => {
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        return await Promise.race([promise, new Promise<T>((_resolve, reject) => {
          timer = setTimeout(() => reject(new Error("本地请求未在两秒内释放资源")), 1800);
        })]);
      } finally { if (timer) clearTimeout(timer); }
    };
    await bounded(new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolve);
    }));
    const port = (server.address() as AddressInfo).port;
    const endpoints: string[] = [];
    const { diagnostics } = setup({ fetch: (url, init) => {
      endpoints.push(url);
      // 仅测试适配器改写到临时回环服务器，生产服务没有可配置接收地址。
      return globalThis.fetch(`http://127.0.0.1:${port}${new URL(url).pathname}`, init);
    } });
    try {
      const report = preview(diagnostics);
      const pending = diagnostics.send(report.id);
      await bounded(started);
      const cancelStarted = performance.now();
      diagnostics.cancel(report.id, false);
      expect(await bounded(pending)).toEqual({ ok: false, code: "cancelled" });
      await bounded(closed);
      expect(performance.now() - cancelStarted).toBeLessThan(2000);
      stalled = false;
      expect((await bounded(diagnostics.send(report.id))).ok).toBe(true);
      expect(endpoints.every((url) => url === TICKET || url === INGEST)).toBe(true);
      expect(endpoints).toHaveLength(phase === TICKET ? 3 : 4);
    } finally {
      diagnostics.dispose();
      await bounded(new Promise<void>((resolve) => {
        server.close(() => resolve());
        server.closeAllConnections();
      }));
    }
  });

  it("十分钟后自动释放预览且不触发网络", async () => {
    vi.useFakeTimers();
    const { diagnostics, fetch } = setup();
    const report = preview(diagnostics);
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(await diagnostics.send(report.id)).toEqual({ ok: false, code: "stale_preview" });
    expect(fetch).not.toHaveBeenCalled();
  });
});
