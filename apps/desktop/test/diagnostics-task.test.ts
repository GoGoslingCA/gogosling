import { afterEach, describe, expect, it, vi } from "vitest";
import { DesktopDiagnostics, type DiagnosticDependencies } from "../src/main/diagnostics.js";
import { DiagnosticTaskReadError, readDiagnosticTask } from "../src/main/diagnostics-task.js";
import type { DiagnosticTaskRequest } from "../src/shared/diagnostics.js";

const NOW = Date.UTC(2026, 9, 13);
const request: DiagnosticTaskRequest = {
  kind: "task", requestId: "00000000-0000-4000-8000-000000000001",
  runId: "01ARZ3NDEKTSV4RRFFQ69G5FAV", sessionId: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
};
const ended = "2026-10-12T23:59:00.123456789Z";
const run = { id: request.runId, session_id: request.sessionId, status: "failed", ended_at: ended };
const instances: DesktopDiagnostics[] = [];
function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "content-type": "application/json" } });
}
function setup(patch: Partial<DiagnosticDependencies> = {}) {
  const loadTask = vi.fn(async () => run);
  const fetch = vi.fn(async () => json({}, 422));
  const snapshot = vi.fn(() => ({ daemonState: "ready" }));
  const diagnostics = new DesktopDiagnostics({ enabled: true, version: "0.0.1103-beta", platform: "darwin", arch: "arm64", now: () => NOW, snapshot, loadTask, fetch, ...patch });
  instances.push(diagnostics);
  return { diagnostics, loadTask, fetch, snapshot };
}
afterEach(() => { instances.splice(0).forEach(instance => instance.dispose()); vi.useRealTimers(); });

describe("真实失败任务的诊断边界", () => {
  it("无错误不生成报告，渲染进程不能通过通用类别伪造任务", () => {
    const { diagnostics, fetch, loadTask } = setup();
    expect(diagnostics.preview("application")).toEqual({ ok: false, code: "no_error" });
    expect(diagnostics.preview("connection")).toEqual({ ok: false, code: "no_error" });
    expect(diagnostics.preview("task")).toEqual({ ok: false, code: "invalid_request" });
    diagnostics.record("unknown");
    expect(diagnostics.preview("preview")).toEqual({ ok: false, code: "no_error" });
    diagnostics.record("load_failed");
    expect(diagnostics.preview("application").ok).toBe(true);
    expect(fetch).not.toHaveBeenCalled(); expect(loadTask).not.toHaveBeenCalled();
  });

  it("源码版不采集或查询；能力仅在配置可信任务读取器时开启", async () => {
    const { diagnostics, snapshot, loadTask, fetch } = setup({ enabled: false });
    expect(diagnostics.status()).toEqual({ available: false });
    expect(await diagnostics.prepareTask(request)).toEqual({ ok: false, code: "disabled" });
    expect(snapshot).not.toHaveBeenCalled(); expect(loadTask).not.toHaveBeenCalled(); expect(fetch).not.toHaveBeenCalled();
    expect(setup({ loadTask: undefined }).diagnostics.status()).toEqual({ available: true });
    expect(setup().diagnostics.status()).toEqual({ available: true, taskReports: true });
  });

  it.each([
    null, "task", {}, [], { ...request, kind: "application" }, { ...request, requestId: "arbitrary" },
    { ...request, runId: "../path" }, { ...request, sessionId: request.sessionId.toLowerCase() },
    { ...request, runId: "Z".repeat(26) }, { ...request, error: "private-marker" },
    { ...request, endedAt: ended }, Object.create(request) as unknown,
  ])("拒绝额外字段与非法任务参数 %j", async input => {
    const { diagnostics, loadTask } = setup();
    expect(await diagnostics.prepareTask(input)).toEqual({ ok: false, code: "invalid_request" });
    expect(loadTask).not.toHaveBeenCalled();
  });

  it("不执行输入访问器，也不执行或上传运行记录的错误访问器", async () => {
    const getter = vi.fn(() => request.runId);
    const input = { ...request };
    Object.defineProperty(input, "runId", { get: getter });
    const raw = { ...run, error: "private-error", user_id: "private-user", trace_id: "private-trace" };
    const errorGetter = vi.fn(() => "private-error");
    Object.defineProperty(raw, "error", { get: errorGetter });
    const { diagnostics } = setup({ loadTask: async () => raw });
    expect(await diagnostics.prepareTask(input)).toEqual({ ok: false, code: "invalid_request" });
    expect(getter).not.toHaveBeenCalled();
    const report = await diagnostics.prepareTask(request);
    expect(report.ok).toBe(true);
    if (report.ok) expect(report.value.body).not.toContain("private-");
    expect(errorGetter).not.toHaveBeenCalled();
  });

  it.each(["queued", "running", "awaiting_approval", "completed", "cancelled", "unknown"])("状态 %s 不允许生成失败报告", async status => {
    const { diagnostics, fetch } = setup({ loadTask: async () => ({ ...run, status }) });
    expect(await diagnostics.prepareTask(request)).toEqual({ ok: false, code: "no_error" });
    expect(fetch).not.toHaveBeenCalled();
  });

  it.each([
    { id: request.sessionId }, { session_id: request.runId }, { ended_at: undefined },
    { ended_at: "2026-02-30T01:00:00Z" }, { ended_at: "2026-10-12T23:59:00" },
    { ended_at: "2026-10-12T23:59:00+08:00" }, { ended_at: "private-marker" },
  ])("拒绝任务归属或失败时间不匹配 %j", async patch => {
    const { diagnostics } = setup({ loadTask: async () => ({ ...run, ...patch }) });
    expect(await diagnostics.prepareTask(request)).toEqual({ ok: false, code: "invalid_response" });
  });

  it("任务报告只使用核实状态和时间，schema 2 被旧服务拒绝时不降级或自动重试", async () => {
    const { diagnostics, fetch, snapshot } = setup();
    diagnostics.record("http_503");
    const result = await diagnostics.prepareTask(request);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const payload = JSON.parse(result.value.body) as { events: Record<string, unknown>[] };
    expect(payload.events[0]?.timestamp).toBe(ended);
    expect(payload.events[0]?.tags).toEqual({ platform: "darwin", arch: "arm64" });
    expect(payload.events[0]?.context).toEqual({ category: "task", recent_errors: [], task_failure: {
      run_id: request.runId, session_id: request.sessionId, occurred_at: ended, status: "failed", error_code: "run_failed",
    } });
    expect(result.value.body).not.toContain(request.requestId);
    expect(snapshot).not.toHaveBeenCalled(); expect(fetch).not.toHaveBeenCalled();
    expect(await diagnostics.send(result.value.id)).toEqual({ ok: false, code: "unsupported_report" });
    expect(fetch).toHaveBeenCalledOnce();
    const init = (fetch.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(JSON.parse(String(init.body))).toEqual({ schema_version: 2, body_sha256: result.value.sha256, body_bytes: result.value.byteLength, event_id: payload.events[0]?.event_id });
  });

  it("旧生命周期报告继续使用 schema 1，422 不误报任务契约错误", async () => {
    const { diagnostics, fetch } = setup();
    diagnostics.record("load_failed");
    const result = diagnostics.preview("application");
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(await diagnostics.send(result.value.id)).toEqual({ ok: false, code: "unavailable" });
    expect(JSON.parse(String((fetch.mock.calls[0] as unknown as [string, RequestInit])[1].body)).schema_version).toBe(1);
  });

  it("任务报告回执丢失后人工重试保持 schema 2、字节和报告编号", async () => {
    const uploads: string[] = []; const tickets: Record<string, unknown>[] = [];
    const fetch = vi.fn(async (url: string, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      if (url.endsWith("/tickets")) {
        tickets.push(body);
        return json({ token: "synthetic-ticket".repeat(4), expires_at: new Date(NOW + 60_000).toISOString(), body_sha256: body.body_sha256 }, 201);
      }
      uploads.push(String(init.body));
      if (uploads.length === 1) throw new Error("synthetic lost receipt");
      return json({ success: true, accepted: 0, duplicates: 1, event_ids: [(body.events as { event_id: string }[])[0]!.event_id] }, 201);
    });
    const { diagnostics, loadTask } = setup({ fetch });
    const preview = await diagnostics.prepareTask(request);
    expect(preview.ok).toBe(true);
    if (!preview.ok) return;
    expect(await diagnostics.send(preview.value.id)).toEqual({ ok: false, code: "unavailable" });
    const retry = await diagnostics.send(preview.value.id);
    expect(retry).toMatchObject({ ok: true, value: { duplicate: true } });
    expect(tickets).toHaveLength(2); expect(tickets[0]?.schema_version).toBe(2); expect(tickets[1]).toEqual(tickets[0]);
    expect(uploads).toEqual([preview.value.body, preview.value.body]); expect(loadTask).toHaveBeenCalledOnce();
    expect(await diagnostics.send(preview.value.id)).toEqual({ ok: false, code: "stale_preview" });
  });

  it("发送中不能准备另一项任务；新准备开始即清除旧预览", async () => {
    const { diagnostics } = setup({ fetch: async () => new Promise(() => undefined) });
    const prepared = await diagnostics.prepareTask(request);
    expect(prepared.ok).toBe(true);
    if (!prepared.ok) return;
    const sending = diagnostics.send(prepared.value.id);
    expect(await diagnostics.prepareTask(request)).toEqual({ ok: false, code: "busy" });
    diagnostics.cancel(prepared.value.id, false);
    expect(await sending).toEqual({ ok: false, code: "cancelled" });
    await diagnostics.prepareTask({ ...request, requestId: "00000000-0000-4000-8000-000000000002" });
    expect(await diagnostics.send(prepared.value.id)).toEqual({ ok: false, code: "stale_preview" });
  });

  it("准备阶段互斥，取消立即释放槽且迟到结果不能覆盖新预览", async () => {
    let resolveLate!: (value: unknown) => void;
    let signal!: AbortSignal;
    const loadTask = vi.fn((_request: DiagnosticTaskRequest, received: AbortSignal) => {
      signal = received;
      return new Promise<unknown>(resolve => { resolveLate = resolve; });
    });
    const { diagnostics } = setup({ loadTask });
    const pending = diagnostics.prepareTask(request);
    expect(await diagnostics.prepareTask(request)).toEqual({ ok: false, code: "busy" });
    expect(diagnostics.preview("application")).toEqual({ ok: false, code: "busy" });
    expect(await diagnostics.send("unused")).toEqual({ ok: false, code: "busy" });
    diagnostics.cancelPreparation("different"); expect(signal.aborted).toBe(false);
    diagnostics.cancelPreparation(request.requestId); expect(signal.aborted).toBe(true);
    expect(await pending).toEqual({ ok: false, code: "cancelled" });
    loadTask.mockResolvedValueOnce(run);
    const next = await diagnostics.prepareTask({ ...request, requestId: "00000000-0000-4000-8000-000000000002" });
    expect(next.ok).toBe(true);
    resolveLate({ ...run, ended_at: "2026-10-01T00:00:00Z" });
    await Promise.resolve();
    if (next.ok) expect(diagnostics.cancel(next.value.id, true)).toEqual({ ok: true, value: null });
  });

  it.each(["clear", "dispose"] as const)("%s 中止未完成的本地核实", async method => {
    let signal!: AbortSignal;
    const { diagnostics } = setup({ loadTask: async (_request, received) => { signal = received; return new Promise(() => undefined); } });
    const pending = diagnostics.prepareTask(request);
    diagnostics[method]();
    expect(signal.aborted).toBe(true);
    expect(await pending).toEqual({ ok: false, code: "cancelled" });
  });

  it("三秒后结束本地核实，不等待不遵守中断的读取器", async () => {
    vi.useFakeTimers(); let signal!: AbortSignal;
    const { diagnostics } = setup({ loadTask: async (_request, received) => { signal = received; return new Promise(() => undefined); } });
    const pending = diagnostics.prepareTask(request);
    await vi.advanceTimersByTimeAsync(2999); expect(signal.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await pending).toEqual({ ok: false, code: "timeout" }); expect(signal.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("任务核实的本地 HTTP 读取器", () => {
  const connection = () => ({ endpoint: "http://127.0.0.1:46750", token: "synthetic-local-credential" });
  it("使用固定本地路由和主进程凭据，不接受外部地址", async () => {
    const fetch = vi.fn(async () => json(run));
    expect(await readDiagnosticTask(request, new AbortController().signal, { connection, fetch })).toEqual(run);
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`http://127.0.0.1:46750/api/v1/runs/${request.runId}`);
    expect(init.redirect).toBe("error"); expect(init.credentials).toBe("omit");
    expect(new Headers(init.headers).get("authorization")).toBe("Bearer synthetic-local-credential");
    for (const endpoint of ["https://example.invalid", "http://localhost:46750", "http://127.0.0.1:46750/private", "http://user@127.0.0.1:46750"]) {
      await expect(readDiagnosticTask(request, new AbortController().signal, { connection: () => ({ endpoint, token: "synthetic-local-credential" }), fetch })).rejects.toMatchObject({ code: "unavailable" });
    }
    expect(fetch).toHaveBeenCalledOnce();
  });

  it.each([
    () => new Response(null, { status: 307, headers: { location: "https://example.invalid" } }),
    () => json({}, 401), () => new Response("{}", { headers: { "content-type": "text/plain" } }),
    () => new Response("{}", { headers: { "content-type": "application/json", "content-length": "65537" } }),
    () => new Response("{", { headers: { "content-type": "application/json" } }),
  ])("拒绝重定向、错误状态、非 JSON 或无效正文", async response => {
    await expect(readDiagnosticTask(request, new AbortController().signal, { connection, fetch: async () => response() })).rejects.toBeInstanceOf(DiagnosticTaskReadError);
  });

  it("未知任务转为无可报告错误", async () => {
    await expect(readDiagnosticTask(request, new AbortController().signal, { connection, fetch: async () => json({}, 404) })).rejects.toMatchObject({ code: "no_error" });
  });

  it("读取超过 64 KiB 时关闭流", async () => {
    const cancel = vi.fn();
    await expect(readDiagnosticTask(request, new AbortController().signal, { connection, fetch: async () => new Response(new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(65537)); }, cancel }), { headers: { "content-type": "application/json" } }) })).rejects.toMatchObject({ code: "invalid_response" });
    expect(cancel).toHaveBeenCalledOnce();
  });

  it("准备取消后丢弃迟到响应并关闭正文", async () => {
    let late!: (response: Response) => void; const cancel = vi.fn(); const controller = new AbortController();
    const pending = readDiagnosticTask(request, controller.signal, { connection, fetch: () => new Promise(resolve => { late = resolve; }) });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ code: "unavailable" });
    late(new Response(new ReadableStream({ cancel }), { headers: { "content-type": "application/json" } }));
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledOnce());
  });

  it("正文读取挂起时取消会释放阅读器", async () => {
    const cancel = vi.fn(); const controller = new AbortController();
    const pending = readDiagnosticTask(request, controller.signal, { connection, fetch: async () => new Response(new ReadableStream({ cancel }), { headers: { "content-type": "application/json" } }) });
    await Promise.resolve(); await Promise.resolve();
    controller.abort();
    await expect(pending).rejects.toBeInstanceOf(DiagnosticTaskReadError);
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledOnce());
  });
});
