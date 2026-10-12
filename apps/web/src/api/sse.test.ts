import { describe, expect, it, vi } from "vitest";
import { backoffDelay, decodeApiEvent, EventStream, parseHello, SseParser, type SseMessage, type StreamResponseLike } from "./sse";

function collect(): { messages: SseMessage[]; parser: SseParser } {
  const messages: SseMessage[] = [];
  const parser = new SseParser((m) => messages.push(m));
  return { messages, parser };
}

describe("SseParser", () => {
  it("解析 id / event / data 并在空行派发", () => {
    const { messages, parser } = collect();
    parser.feed('id: 12\nevent: text.delta\ndata: {"a":1}\n\n');
    expect(messages).toEqual([{ event: "text.delta", data: '{"a":1}', lastEventId: "12" }]);
  });

  it("多行 data 用换行拼接，没有 event 时类型为 message", () => {
    const { messages, parser } = collect();
    parser.feed("data: line1\ndata: line2\n\n");
    expect(messages[0]).toMatchObject({ event: "message", data: "line1\nline2" });
  });

  it("接受 \\r\\n 与 \\r 作为行结束符，并容忍跨块切分", () => {
    const { messages, parser } = collect();
    parser.feed("event: a\r");
    parser.feed("\ndata: x\r\n\r\n");
    parser.feed("data: y\r\r");
    expect(messages.map((m) => [m.event, m.data])).toEqual([
      ["a", "x"],
      ["message", "y"],
    ]);
  });

  it("块末尾的 \\r 立即结束一行，下一块开头的 \\n 不会产生多余空行", () => {
    const { messages, parser } = collect();
    parser.feed("data: a\r");
    parser.feed("\ndata: b\r");
    parser.feed("\n\r\n");
    expect(messages).toHaveLength(1);
    expect(messages[0]?.data).toBe("a\nb");
  });

  it("注释行（保活）被忽略，id 跨消息保持，含 NUL 的 id 被忽略", () => {
    const { messages, parser } = collect();
    parser.feed(": keep-alive\n\nid: 5\ndata: a\n\nid: bad\u0000\ndata: b\n\n");
    expect(messages).toHaveLength(2);
    expect(messages[0]?.lastEventId).toBe("5");
    expect(messages[1]?.lastEventId).toBe("5");
  });

  it("data 行冒号后的单个空格被去掉，其余保留；无冒号的字段值为空", () => {
    const { messages, parser } = collect();
    parser.feed("data:  two spaces\n\ndata\n\n");
    expect(messages[0]?.data).toBe(" two spaces");
    expect(messages[1]?.data).toBe("");
  });

  it("retry 只接受整数并随消息返回", () => {
    const { messages, parser } = collect();
    parser.feed("retry: 2500\ndata: a\n\nretry: abc\ndata: b\n\n");
    expect(messages[0]?.retry).toBe(2500);
    expect(messages[1]?.retry).toBeUndefined();
  });

  it("没有 data 的块不派发，开头的 BOM 被忽略", () => {
    const { messages, parser } = collect();
    parser.feed("﻿event: x\n\ndata: ok\n\n");
    expect(messages).toHaveLength(1);
    expect(messages[0]?.data).toBe("ok");
  });

  it("任意位置切块都能得到相同结果", () => {
    const full = 'id: 1\nevent: e\ndata: {"k":"中文"}\n\n: ping\n\nid: 2\ndata: a\ndata: b\n\n';
    const reference = collect();
    reference.parser.feed(full);
    for (let cut = 1; cut < full.length; cut++) {
      const { messages, parser } = collect();
      parser.feed(full.slice(0, cut));
      parser.feed(full.slice(cut));
      expect(messages, `cut at ${cut}`).toEqual(reference.messages);
    }
  });
});

describe("decodeApiEvent", () => {
  it("把 data 解析为事件信封，seq 优先取 body", () => {
    const ev = decodeApiEvent({ event: "text.delta", data: '{"seq":9,"type":"text.delta","at":"2026-10-10T00:00:00Z","payload":{"run_id":"r"}}', lastEventId: "9" });
    expect(ev).toMatchObject({ type: "text.delta", seq: 9 });
  });

  it("gap 事件不需要 data", () => {
    const ev = decodeApiEvent({ event: "gap", data: "", lastEventId: "100" });
    expect(ev).toMatchObject({ type: "gap", seq: 100 });
  });

  it("非法 JSON 返回 null", () => {
    expect(decodeApiEvent({ event: "x", data: "{", lastEventId: "" })).toBeNull();
  });
});

describe("backoffDelay", () => {
  const opts = { initialMs: 1000, maxMs: 30_000, factor: 2 };
  it("指数增长并封顶", () => {
    expect(backoffDelay(0, opts, undefined, () => 1)).toBe(1000);
    expect(backoffDelay(3, opts, undefined, () => 1)).toBe(8000);
    expect(backoffDelay(10, opts, undefined, () => 1)).toBe(30_000);
  });
  it("带抖动且不低于一半", () => {
    expect(backoffDelay(0, opts, undefined, () => 0)).toBe(500);
  });
  it("服务端 retry 作为下限", () => {
    expect(backoffDelay(0, opts, 5000, () => 1)).toBe(5000);
  });
});

/** 用字符串数组构造一个可读流响应。 */
function streamResponse(chunks: string[], status = 200): StreamResponseLike {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(encoder.encode(c));
      controller.close();
    },
  });
  return { ok: status >= 200 && status < 300, status, body };
}

/** 永不结束的流，直到 signal 中止。 */
function hangingResponse(signal: AbortSignal, prelude = ""): StreamResponseLike {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      if (prelude) controller.enqueue(encoder.encode(prelude));
      signal.addEventListener("abort", () => {
        try {
          controller.error(new DOMException("aborted", "AbortError"));
        } catch {
          // 已关闭
        }
      });
    },
  });
  return { ok: true, status: 200, body };
}

function flush(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

describe("EventStream", () => {
  it("断线后带着最后的 Seq 重连（since 查询参数 + Last-Event-ID 头）", async () => {
    const calls: Array<{ url: string; headers: Record<string, string> }> = [];
    const received: string[] = [];
    let connections = 0;
    const stream = new EventStream({
      url: "http://127.0.0.1:1/api/v1/events",
      headers: () => ({ Authorization: "Bearer t" }),
      since: 3,
      onMessage: (m) => received.push(m.data),
      fetchImpl: async (url, init) => {
        calls.push({ url, headers: init.headers });
        connections += 1;
        if (connections === 1) return streamResponse(['id: 7\nevent: x\ndata: a\n\n', "id: 8\ndata: b\n\n"]);
        return hangingResponse(init.signal, ": hi\n\n");
      },
      sleep: async () => undefined,
      random: () => 1,
    });
    stream.start();
    for (let i = 0; i < 20 && connections < 2; i++) await flush();
    stream.stop();

    expect(received).toEqual(["a", "b"]);
    expect(calls[0]?.url).toContain("since=3");
    expect(calls[0]?.headers["Last-Event-ID"]).toBe("3");
    expect(calls[0]?.headers.Authorization).toBe("Bearer t");
    expect(calls[0]?.headers.Accept).toBe("text/event-stream");
    expect(calls[1]?.url).toContain("since=8");
    expect(calls[1]?.headers["Last-Event-ID"]).toBe("8");
    expect(stream.lastSeq).toBe(8);
  });

  it("相对 URL 保持相对（开发代理），并附带 session_id", async () => {
    const urls: string[] = [];
    const stream = new EventStream({
      url: "/api/v1/events",
      sessionId: "s1",
      headers: () => ({}),
      onMessage: () => undefined,
      fetchImpl: async (url, init) => {
        urls.push(url);
        return hangingResponse(init.signal);
      },
    });
    stream.start();
    await flush();
    stream.stop();
    expect(urls[0]).toBe("/api/v1/events?since=0&session_id=s1");
  });

  it("401 时进入 unauthorized 状态且不再重连", async () => {
    const statuses: string[] = [];
    let connections = 0;
    const stream = new EventStream({
      url: "/api/v1/events",
      headers: () => ({}),
      onMessage: () => undefined,
      onStatus: (s) => statuses.push(s),
      fetchImpl: async () => {
        connections += 1;
        return streamResponse([], 401);
      },
      sleep: async () => undefined,
    });
    stream.start();
    for (let i = 0; i < 10; i++) await flush();
    expect(connections).toBe(1);
    expect(statuses).toContain("unauthorized");
    stream.stop();
  });

  it("HTTP 错误按退避重连并上报 reconnecting 状态", async () => {
    const delays: number[] = [];
    let connections = 0;
    const stream = new EventStream({
      url: "/api/v1/events",
      headers: () => ({}),
      onMessage: () => undefined,
      onStatus: (s, info) => {
        if (s === "reconnecting" && info?.delayMs !== undefined) delays.push(info.delayMs);
      },
      fetchImpl: async (_url, init) => {
        connections += 1;
        if (connections <= 2) return streamResponse([], 503);
        return hangingResponse(init.signal);
      },
      sleep: async () => undefined,
      random: () => 1,
      backoff: { initialMs: 100, maxMs: 1000, factor: 2 },
    });
    stream.start();
    for (let i = 0; i < 20 && connections < 3; i++) await flush();
    stream.stop();
    expect(connections).toBe(3);
    expect(delays).toEqual([100, 200]);
  });

  it("空闲超过 idleTimeoutMs 没有任何字节就主动断开重连", async () => {
    vi.useFakeTimers();
    try {
      let connections = 0;
      const stream = new EventStream({
        url: "/api/v1/events",
        headers: () => ({}),
        onMessage: () => undefined,
        fetchImpl: async (_url, init) => {
          connections += 1;
          return hangingResponse(init.signal);
        },
        sleep: async () => undefined,
        idleTimeoutMs: 1000,
      });
      stream.start();
      await vi.advanceTimersByTimeAsync(10);
      expect(connections).toBe(1);
      await vi.advanceTimersByTimeAsync(1100);
      expect(connections).toBe(2);
      stream.stop();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stop 之后不会再发起连接", async () => {
    let connections = 0;
    const stream = new EventStream({
      url: "/api/v1/events",
      headers: () => ({}),
      onMessage: () => undefined,
      fetchImpl: async () => {
        connections += 1;
        return streamResponse(["data: a\n\n"]);
      },
      sleep: async (_ms, signal) =>
        new Promise((r) => {
          signal.addEventListener("abort", () => r(), { once: true });
        }),
    });
    stream.start();
    await flush();
    stream.stop();
    for (let i = 0; i < 5; i++) await flush();
    expect(connections).toBe(1);
    expect(stream.currentStatus).toBe("closed");
  });
});

describe("parseHello", () => {
  it("接受扁平的 started_at 与事件信封里的 payload.started_at", () => {
    // 与 internal/server/events.go 的 hello 帧一致：扁平 {"started_at","last_seq"}，不带 id。
    expect(parseHello({ event: "hello", data: '{"started_at":"2026-10-10T01:00:00Z","last_seq":42}', lastEventId: "" })).toEqual({ startedAt: "2026-10-10T01:00:00Z" });
    expect(parseHello({ event: "hello", data: '{"type":"hello","payload":{"started_at":"T2"}}', lastEventId: "5" })).toEqual({ startedAt: "T2" });
  });

  it("不是 hello、非法 JSON 或缺 started_at 时返回 null", () => {
    expect(parseHello({ event: "text.delta", data: '{"started_at":"x"}', lastEventId: "" })).toBeNull();
    expect(parseHello({ event: "hello", data: "{", lastEventId: "" })).toBeNull();
    expect(parseHello({ event: "hello", data: "{}", lastEventId: "" })).toBeNull();
  });
});

describe("EventStream：守护进程实例与续传位置", () => {
  it("桌面壳代理地址 app://gogosling 原样保留（不被当作相对路径）", async () => {
    const urls: string[] = [];
    const stream = new EventStream({
      url: "app://gogosling/api/v1/events",
      headers: () => ({}),
      onMessage: () => undefined,
      fetchImpl: async (url, init) => {
        urls.push(url);
        return hangingResponse(init.signal);
      },
    });
    stream.start();
    await flush();
    stream.stop();
    expect(urls[0]).toBe("app://gogosling/api/v1/events?since=0");
  });

  it("hello 的 started_at 变化：since 重置为 0、立即重连（不退避）、通知上层，旧连接剩余内容被丢弃", async () => {
    const calls: Array<{ url: string; headers: Record<string, string> }> = [];
    const received: string[] = [];
    const changes: Array<{ previous: string; current: string }> = [];
    const sleeps: number[] = [];
    let connections = 0;
    const stream = new EventStream({
      url: "/api/v1/events",
      headers: () => ({}),
      onMessage: (m) => received.push(`${m.event}:${m.data}`),
      onInstanceChange: (info) => changes.push(info),
      fetchImpl: async (url, init) => {
        calls.push({ url, headers: init.headers });
        connections += 1;
        if (connections === 1) {
          // 实例 A：收到 1..2 后断开。
          return streamResponse(['event: hello\ndata: {"started_at":"A","last_seq":0}\n\n', "id: 1\nevent: x\ndata: a1\n\n", "id: 2\nevent: x\ndata: a2\n\n"]);
        }
        if (connections === 2) {
          // 实例 B（重启后）：since=2 还在 B 的范围内，服务端会从 3 开始静默回放——hello 让客户端识破。
          return streamResponse(['event: hello\ndata: {"started_at":"B","last_seq":5}\n\nid: 3\nevent: x\ndata: b3\n\n']);
        }
        return hangingResponse(init.signal, 'event: hello\ndata: {"started_at":"B"}\n\nid: 1\nevent: x\ndata: b1\n\n');
      },
      sleep: async (ms) => {
        sleeps.push(ms);
      },
      random: () => 1,
    });
    stream.start();
    for (let i = 0; i < 40 && received.length < 3; i++) await flush();
    stream.stop();

    expect(changes).toEqual([{ previous: "A", current: "B" }]);
    // hello 不交给 onMessage；B 的第一条连接里 hello 之后的 b3 被丢弃。
    expect(received).toEqual(["x:a1", "x:a2", "x:b1"]);
    expect(calls[1]?.url).toContain("since=2");
    expect(calls[2]?.url).toContain("since=0");
    expect(calls[2]?.headers["Last-Event-ID"]).toBeUndefined();
    // 只有 A 的连接正常关闭后的那一次退避；实例变化后的重连没有等待。
    expect(sleeps).toHaveLength(1);
    expect(stream.daemonInstance).toBe("B");
    expect(stream.lastSeq).toBe(1);
  });

  it("同一实例的 hello 不影响续传位置", async () => {
    const calls: string[] = [];
    let connections = 0;
    const stream = new EventStream({
      url: "/api/v1/events",
      headers: () => ({}),
      onMessage: () => undefined,
      onInstanceChange: () => {
        throw new Error("不应触发");
      },
      fetchImpl: async (url, init) => {
        calls.push(url);
        connections += 1;
        if (connections === 1) return streamResponse(['id: 9\nevent: hello\ndata: {"started_at":"A"}\n\n', "id: 4\nevent: x\ndata: a\n\n"]);
        return hangingResponse(init.signal, 'event: hello\ndata: {"started_at":"A"}\n\n');
      },
      sleep: async () => undefined,
    });
    stream.start();
    for (let i = 0; i < 20 && connections < 2; i++) await flush();
    await flush();
    stream.stop();
    // hello 自带的 id 不推进续传位置（只有真实事件才推进）。
    expect(calls[1]).toContain("since=4");
  });

  it("gap 的 id 即使小于本地 Seq 也被采用（守护进程重启后 Seq 从头计数），不会反复收到 gap", async () => {
    const calls: string[] = [];
    const events: string[] = [];
    let connections = 0;
    const stream = new EventStream({
      url: "/api/v1/events",
      since: 500,
      headers: () => ({}),
      onMessage: (m) => events.push(m.event),
      fetchImpl: async (url, init) => {
        calls.push(url);
        connections += 1;
        if (connections === 1) return streamResponse(['id: 12\nevent: gap\ndata: {"type":"gap","seq":12}\n\n']);
        return hangingResponse(init.signal);
      },
      sleep: async () => undefined,
    });
    stream.start();
    for (let i = 0; i < 20 && connections < 2; i++) await flush();
    stream.stop();
    expect(events).toEqual(["gap"]);
    expect(calls[0]).toContain("since=500");
    expect(calls[1]).toContain("since=12");
  });
});
