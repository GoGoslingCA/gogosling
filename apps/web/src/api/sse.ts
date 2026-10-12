// EventSource 的替代实现：基于 fetch + ReadableStream，以便携带 Authorization 头。
//
// 设计要点：
//   - SseParser 是纯函数式的增量解析器（按 WHATWG 规范处理 \r\n / \r / \n、多行 data、注释行、id、retry）；
//   - EventStream 负责连接生命周期：指数退避重连、断线后按最后收到的 Seq 续传（since 查询参数 + Last-Event-ID 头）、
//     空闲看门狗（服务端每 15 秒发注释保活，超过 idleTimeoutMs 没有任何字节就视为连接已死）；
//   - 收到 `gap` 事件说明服务端缓冲已丢弃续传位置之后的事件，上层必须全量刷新；gap 的 id 是服务端的
//     当前位置，之后从它续传（即使它比本地的 Seq 小——守护进程重启后 Seq 从头计数）；
//   - 每次连接开头服务端发 `event: hello`（data 为 {"started_at","last_seq"}，不带 id；见 internal/server/events.go）。started_at 与上次连接
//     不同说明守护进程重启过、全局 Seq 已从头计数：丢弃本次连接的后续内容，把续传位置重置为 0 并立即重连，
//     同时通知上层全量刷新（onInstanceChange）。hello 本身不更新 Seq、不交给 onMessage；
//   - 不保留任何模块级状态，可以同时存在多个实例（例如测试或多窗口）。
//
// 服务端的全局 Seq 是续传依据，客户端按序号去重。

import type { ApiEvent } from "./types";

/** 服务端在每次连接开头发送的握手事件。 */
export const HELLO_EVENT = "hello";

/** 握手里我们关心的内容。 */
export interface HelloInfo {
  /** 守护进程实例的启动时间；变化即说明守护进程重启过。 */
  startedAt: string;
}

/**
 * 解析 hello 事件。data 可以是扁平的 {"started_at": ...}，也可以是事件信封 {"type":"hello","payload":{"started_at": ...}}；
 * 不是 hello、data 不是 JSON 或缺少 started_at 时返回 null（忽略）。
 */
export function parseHello(msg: SseMessage): HelloInfo | null {
  if (msg.event !== HELLO_EVENT) return null;
  let raw: unknown;
  try {
    raw = JSON.parse(msg.data);
  } catch {
    return null;
  }
  if (!raw || typeof raw !== "object") return null;
  const obj = raw as Record<string, unknown>;
  const payload = obj.payload && typeof obj.payload === "object" ? (obj.payload as Record<string, unknown>) : {};
  const startedAt = typeof obj.started_at === "string" ? obj.started_at : typeof payload.started_at === "string" ? payload.started_at : "";
  return startedAt ? { startedAt } : null;
}

/** 一条解析后的 SSE 消息。 */
export interface SseMessage {
  /** 事件类型；没有 event 行时为 "message"。 */
  event: string;
  /** 多行 data 以 \n 连接后的结果。 */
  data: string;
  /** 最近一次 id 行的值（跨消息保持）。 */
  lastEventId: string;
  /** retry 行给出的重连间隔（毫秒），仅当本条消息包含合法 retry 时存在。 */
  retry?: number;
}

/**
 * 增量 SSE 解析器。feed() 可以传入任意切分位置的文本片段；每当遇到空行就派发一条消息。
 */
export class SseParser {
  private buffer = "";
  private dataLines: string[] = [];
  private eventType = "";
  private lastEventId = "";
  private retry: number | undefined;
  private sawBom = false;
  /** 上一块以 \r 结尾：下一块开头的 \n 属于同一个 \r\n，要丢掉。 */
  private pendingCR = false;

  constructor(private readonly onMessage: (msg: SseMessage) => void) {}

  /** 喂入一段文本（已解码的 UTF-8）。 */
  feed(chunk: string): void {
    if (!chunk) return;
    if (this.pendingCR) {
      this.pendingCR = false;
      if (chunk.charCodeAt(0) === 10) chunk = chunk.slice(1);
      if (!chunk) return;
    }
    this.buffer += chunk;
    if (!this.sawBom) {
      // 规范要求忽略流开头的 BOM。
      if (this.buffer.charCodeAt(0) === 0xfeff) this.buffer = this.buffer.slice(1);
      if (this.buffer.length > 0) this.sawBom = true;
    }
    let start = 0;
    for (;;) {
      const nl = this.buffer.indexOf("\n", start);
      const cr = this.buffer.indexOf("\r", start);
      let end = -1;
      let skip = 1;
      if (cr !== -1 && (nl === -1 || cr < nl)) {
        end = cr;
        if (cr === this.buffer.length - 1) {
          // \r 是缓冲最后一个字符：先按行结束处理，不等待下一块（避免延迟），
          // 下一块若以 \n 开头则视为同一个 \r\n。
          this.pendingCR = true;
        } else if (this.buffer.charCodeAt(cr + 1) === 10) {
          skip = 2;
        }
      } else if (nl !== -1) {
        end = nl;
      } else {
        break;
      }
      this.processLine(this.buffer.slice(start, end));
      start = end + skip;
    }
    this.buffer = this.buffer.slice(start);
  }

  /** 流结束：丢弃未以空行结束的半条消息（规范行为）。 */
  end(): void {
    this.buffer = "";
    this.pendingCR = false;
    this.resetEvent();
  }

  private processLine(line: string): void {
    if (line === "") {
      this.dispatch();
      return;
    }
    if (line.startsWith(":")) return; // 注释行（保活）
    const colon = line.indexOf(":");
    let field: string;
    let value: string;
    if (colon === -1) {
      field = line;
      value = "";
    } else {
      field = line.slice(0, colon);
      value = line.slice(colon + 1);
      if (value.startsWith(" ")) value = value.slice(1);
    }
    switch (field) {
      case "event":
        this.eventType = value;
        break;
      case "data":
        this.dataLines.push(value);
        break;
      case "id":
        // 含 NUL 的 id 必须忽略（规范）。
        if (!value.includes("\u0000")) this.lastEventId = value;
        break;
      case "retry":
        if (/^\d+$/.test(value)) this.retry = Number(value);
        break;
      default:
        break; // 未知字段忽略
    }
  }

  private dispatch(): void {
    if (this.dataLines.length === 0) {
      // 没有 data 的事件不派发，但 id / retry 已经生效。
      this.resetEvent();
      return;
    }
    const msg: SseMessage = {
      event: this.eventType || "message",
      data: this.dataLines.join("\n"),
      lastEventId: this.lastEventId,
    };
    if (this.retry !== undefined) msg.retry = this.retry;
    this.resetEvent();
    this.onMessage(msg);
  }

  private resetEvent(): void {
    this.dataLines = [];
    this.eventType = "";
    this.retry = undefined;
  }
}

export type EventStreamStatus = "idle" | "connecting" | "open" | "reconnecting" | "closed" | "unauthorized";

export interface BackoffOptions {
  initialMs: number;
  maxMs: number;
  factor: number;
}

const DEFAULT_BACKOFF: BackoffOptions = { initialMs: 1000, maxMs: 30_000, factor: 2 };

/** 计算第 attempt 次（从 0 起）重连的等待时间：指数退避 + 抖动，服务端 retry 优先作为下限。 */
export function backoffDelay(attempt: number, opts: BackoffOptions, serverRetryMs: number | undefined, random: () => number): number {
  const exp = Math.min(opts.maxMs, opts.initialMs * Math.pow(opts.factor, attempt));
  const base = serverRetryMs !== undefined ? Math.max(serverRetryMs, Math.min(exp, opts.maxMs)) : exp;
  // 0.5x ~ 1x 的抖动，避免多个客户端同时重连。
  return Math.round(base * (0.5 + random() * 0.5));
}

/** 最小化的响应形状，便于测试用假对象替代 Response。 */
export interface StreamResponseLike {
  ok: boolean;
  status: number;
  body: ReadableStream<Uint8Array> | null;
}

export type StreamFetch = (url: string, init: { headers: Record<string, string>; signal: AbortSignal }) => Promise<StreamResponseLike>;

export interface EventStreamOptions {
  /** 不带 since 的事件流 URL，例如 http://127.0.0.1:46750/api/v1/events 或 /api/v1/events。 */
  url: string;
  /** 每次连接时取得的请求头（Authorization、Accept-Language）。 */
  headers: () => Record<string, string>;
  /**
   * 续传起点：最后收到的全局 Seq，服务端回放其后的事件。0 表示回放服务端缓冲里的全部事件
   * （不是"从当前位置开始"）；缓冲已滚动时服务端先发 gap，并以 gap 的 id 作为新的续传位置。
   */
  since?: number;
  /** 只订阅某个会话。 */
  sessionId?: string;
  onMessage: (msg: SseMessage) => void;
  onStatus?: (status: EventStreamStatus, info?: { attempt?: number; delayMs?: number; error?: unknown }) => void;
  /** 守护进程实例变化（hello 的 started_at 与上次不同）：续传位置已重置为 0，上层应全量刷新。 */
  onInstanceChange?: (info: { previous: string; current: string }) => void;
  fetchImpl?: StreamFetch;
  /** 多久没有任何字节就视为连接已死（服务端每 15 秒保活一次）。 */
  idleTimeoutMs?: number;
  backoff?: Partial<BackoffOptions>;
  /** 可注入的等待实现（测试用）；必须响应 signal 中止。 */
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  random?: () => number;
}

function defaultSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const timer = setTimeout(done, ms);
    function done() {
      signal.removeEventListener("abort", done);
      clearTimeout(timer);
      resolve();
    }
    signal.addEventListener("abort", done, { once: true });
  });
}

/**
 * 长连接事件流。start() 后在后台循环连接直到 stop()。
 * 续传位置由 lastSeq 维护：每收到一条带 id 的消息就更新；重连时作为 since 与 Last-Event-ID 发送。
 */
export class EventStream {
  private controller: AbortController | null = null;
  private stopped = true;
  private attempt = 0;
  private serverRetryMs: number | undefined;
  private seq: number;
  private status: EventStreamStatus = "idle";
  /** 最近一次 hello 给出的守护进程实例（started_at）。 */
  private instance: string | null = null;

  constructor(private readonly options: EventStreamOptions) {
    this.seq = options.since ?? 0;
  }

  /** 最后收到的全局 Seq。 */
  get lastSeq(): number {
    return this.seq;
  }

  get currentStatus(): EventStreamStatus {
    return this.status;
  }

  start(): void {
    if (!this.stopped) return;
    this.stopped = false;
    this.controller = new AbortController();
    void this.loop(this.controller.signal);
  }

  stop(): void {
    if (this.stopped) return;
    this.stopped = true;
    this.controller?.abort();
    this.controller = null;
    this.setStatus("closed");
  }

  private setStatus(status: EventStreamStatus, info?: { attempt?: number; delayMs?: number; error?: unknown }): void {
    this.status = status;
    this.options.onStatus?.(status, info);
  }

  /** 当前识别到的守护进程实例（最近一次 hello 的 started_at）。 */
  get daemonInstance(): string | null {
    return this.instance;
  }

  private buildUrl(): string {
    const url = new URL(this.options.url, typeof location !== "undefined" ? location.href : "http://localhost/");
    url.searchParams.set("since", String(this.seq));
    if (this.options.sessionId) url.searchParams.set("session_id", this.options.sessionId);
    // 相对 URL 要保持相对（开发代理），所以没有 scheme 时只返回 path+search；
    // 带 scheme 的（http(s)://、桌面壳的 app://）原样保留。
    if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(this.options.url)) return `${url.pathname}${url.search}`;
    return url.toString();
  }

  /**
   * 处理一条 SSE 消息并维护续传位置。返回 "restart" 表示 hello 显示守护进程已换实例，
   * 调用方应丢弃本次连接的后续内容并立即重连。
   */
  private accept(msg: SseMessage): "ok" | "restart" {
    if (msg.event === HELLO_EVENT) {
      const hello = parseHello(msg);
      if (!hello) return "ok";
      const previous = this.instance;
      this.instance = hello.startedAt;
      if (previous !== null && previous !== hello.startedAt) {
        this.seq = 0;
        this.options.onInstanceChange?.({ previous, current: hello.startedAt });
        return "restart";
      }
      return "ok";
    }
    const id = Number(msg.lastEventId);
    const hasId = msg.lastEventId !== "" && Number.isFinite(id) && id >= 0;
    if (msg.event === "gap") {
      // gap 的 id 是服务端的当前位置：无条件采用（守护进程重启后它可能比本地 Seq 小，
      // 只取较大者会让每次重连都再收到 gap）。
      if (hasId) this.seq = id;
    } else if (hasId && id > this.seq) {
      this.seq = id;
    }
    if (msg.retry !== undefined) this.serverRetryMs = msg.retry;
    this.options.onMessage(msg);
    return "ok";
  }

  private async loop(signal: AbortSignal): Promise<void> {
    const fetchImpl: StreamFetch =
      this.options.fetchImpl ?? ((url, init) => globalThis.fetch(url, { ...init, cache: "no-store" }) as Promise<StreamResponseLike>);
    const sleep = this.options.sleep ?? defaultSleep;
    const random = this.options.random ?? Math.random;
    const backoff: BackoffOptions = { ...DEFAULT_BACKOFF, ...(this.options.backoff ?? {}) };
    const idleTimeoutMs = this.options.idleTimeoutMs ?? 45_000;

    while (!signal.aborted) {
      this.setStatus(this.attempt === 0 ? "connecting" : "reconnecting", { attempt: this.attempt });
      // 每次连接用独立的 controller，这样空闲看门狗可以只中止本次连接而不结束整个循环。
      const connection = new AbortController();
      const onOuterAbort = () => connection.abort();
      signal.addEventListener("abort", onOuterAbort, { once: true });
      let idleTimer: ReturnType<typeof setTimeout> | undefined;
      // hello 显示守护进程换了实例：丢弃本次连接剩余的内容，立即从 since=0 重连。
      let restarted = false;
      const touch = () => {
        if (idleTimer !== undefined) clearTimeout(idleTimer);
        idleTimer = setTimeout(() => connection.abort(), idleTimeoutMs);
      };
      try {
        const headers: Record<string, string> = {
          ...this.options.headers(),
          Accept: "text/event-stream",
        };
        if (this.seq > 0) headers["Last-Event-ID"] = String(this.seq);
        const response = await fetchImpl(this.buildUrl(), { headers, signal: connection.signal });
        if (response.status === 401 || response.status === 403) {
          // 令牌失效不重试：由上层重新连接。
          this.setStatus("unauthorized");
          this.stopped = true;
          return;
        }
        if (!response.ok || !response.body) {
          throw new Error(`event stream HTTP ${response.status}`);
        }
        this.setStatus("open");
        this.attempt = 0;
        const parser = new SseParser((msg) => {
          if (restarted) return;
          if (this.accept(msg) === "restart") {
            restarted = true;
            connection.abort();
          }
        });
        const reader = response.body.getReader();
        const decoder = new TextDecoder("utf-8");
        touch();
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          touch();
          if (value && value.byteLength > 0) parser.feed(decoder.decode(value, { stream: true }));
          if (restarted) break;
        }
        parser.feed(decoder.decode());
        parser.end();
        // 服务端正常关闭也视为断线，走重连。
        throw new Error("event stream closed by server");
      } catch (err) {
        if (signal.aborted) return;
        if (restarted) {
          // 不是故障：不退避，立即按新的续传位置重连。
          this.attempt = 0;
          continue;
        }
        const delayMs = backoffDelay(this.attempt, backoff, this.serverRetryMs, random);
        this.attempt += 1;
        this.setStatus("reconnecting", { attempt: this.attempt, delayMs, error: err });
        await sleep(delayMs, signal);
      } finally {
        if (idleTimer !== undefined) clearTimeout(idleTimer);
        signal.removeEventListener("abort", onOuterAbort);
        connection.abort();
      }
    }
  }
}

/** 把 SSE 消息解码为契约中的 Event 信封；data 不是合法 JSON 时返回 null。 */
export function decodeApiEvent(msg: SseMessage): ApiEvent | null {
  if (msg.event === "gap") {
    const seq = Number(msg.lastEventId);
    return { type: "gap", seq: Number.isFinite(seq) ? seq : 0, at: new Date().toISOString() };
  }
  let raw: unknown;
  try {
    raw = JSON.parse(msg.data);
  } catch {
    return null;
  }
  if (!raw || typeof raw !== "object") return null;
  const obj = raw as Record<string, unknown>;
  const type = typeof obj.type === "string" ? obj.type : msg.event;
  const seqFromBody = typeof obj.seq === "number" ? obj.seq : Number(msg.lastEventId);
  return {
    ...(obj as object),
    type,
    seq: Number.isFinite(seqFromBody) ? seqFromBody : 0,
    at: typeof obj.at === "string" ? obj.at : new Date().toISOString(),
  } as ApiEvent;
}
