"""测试辅助：内存传输、守护进程视角的对端、假供应商与假宿主。"""

from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator, Iterable, Sequence
from datetime import UTC, datetime
from typing import Any

from gogosling_worker.protocol import (
    AgentSpec,
    Message,
    ProviderCredentials,
    RunOptions,
    RunRequest,
    ToolCall,
    ToolResult,
    ToolSpec,
    Usage,
)
from gogosling_worker.providers.base import ChatOptions, Chunk, ProviderError
from gogosling_worker.rpc import Connection, Dispatcher
from gogosling_worker.runtime.engine import RunCancelled

WAIT = 5.0


class MemoryTransport:
    """守护进程写入 ``feed``，工作进程的输出进入 ``outgoing``。"""

    def __init__(self) -> None:
        self.reader = asyncio.StreamReader()
        self.outgoing: asyncio.Queue[bytes] = asyncio.Queue()
        self.closed = False

    async def read(self, max_bytes: int) -> bytes:
        return await self.reader.read(max_bytes)

    async def write(self, data: bytes) -> None:
        await self.outgoing.put(data)

    async def aclose(self) -> None:
        self.closed = True

    def feed(self, data: bytes) -> None:
        self.reader.feed_data(data)

    def feed_eof(self) -> None:
        self.reader.feed_eof()


class Daemon:
    """以守护进程的视角驱动一条连接：发请求、收响应与通知、应答反向请求。"""

    def __init__(self, dispatcher: Dispatcher | None = None, **conn_kwargs: Any) -> None:
        self.transport = MemoryTransport()
        self.dispatcher = dispatcher or Dispatcher()
        conn_kwargs.setdefault("drain_grace", 0.5)
        self.conn = Connection(self.transport, self.dispatcher, **conn_kwargs)
        self._serve: asyncio.Task[None] | None = None
        self._next_id = 100
        # recv_response 途中跳过的消息，按顺序留给 recv / recv_events。
        self._skipped: list[dict[str, Any]] = []

    async def __aenter__(self) -> Daemon:
        self._serve = asyncio.create_task(self.conn.serve())
        return self

    async def __aexit__(self, *exc: object) -> None:
        self.transport.feed_eof()
        assert self._serve is not None
        await asyncio.wait_for(self._serve, WAIT)

    @property
    def serve_task(self) -> asyncio.Task[None]:
        assert self._serve is not None
        return self._serve

    def send_line(self, line: str | bytes) -> None:
        data = line.encode("utf-8") if isinstance(line, str) else line
        self.transport.feed(data + b"\n")

    def send(self, payload: dict[str, Any]) -> None:
        self.send_line(json.dumps(payload, ensure_ascii=False))

    def send_request(self, method: str, params: Any = None) -> int:
        self._next_id += 1
        payload: dict[str, Any] = {"jsonrpc": "2.0", "id": self._next_id, "method": method}
        if params is not None:
            payload["params"] = params
        self.send(payload)
        return self._next_id

    def send_notification(self, method: str, params: Any = None) -> None:
        payload: dict[str, Any] = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            payload["params"] = params
        self.send(payload)

    def reply(self, request_id: Any, result: Any) -> None:
        self.send({"jsonrpc": "2.0", "id": request_id, "result": result})

    def reply_error(self, request_id: Any, code: int, message: str, data: Any = None) -> None:
        error: dict[str, Any] = {"code": code, "message": message}
        if data is not None:
            error["data"] = data
        self.send({"jsonrpc": "2.0", "id": request_id, "error": error})

    async def recv(self, timeout: float = WAIT) -> dict[str, Any]:
        if self._skipped:
            return self._skipped.pop(0)
        return await self._recv_raw(timeout)

    async def _recv_raw(self, timeout: float) -> dict[str, Any]:
        raw = await asyncio.wait_for(self.transport.outgoing.get(), timeout)
        assert raw.endswith(b"\n") and raw.count(b"\n") == 1, raw
        decoded = json.loads(raw)
        assert isinstance(decoded, dict)
        return decoded

    async def recv_response(self, request_id: int, timeout: float = WAIT) -> dict[str, Any]:
        """等待指定 id 的响应；途中收到的通知与反向请求保留，供后续 recv / recv_events 读取。"""
        while True:
            msg = await self._recv_raw(timeout)
            if msg.get("id") == request_id and "method" not in msg:
                return msg
            self._skipped.append(msg)

    async def call(self, method: str, params: Any = None) -> dict[str, Any]:
        request_id = self.send_request(method, params)
        return await self.recv_response(request_id)

    async def recv_events(
        self, until_terminal: bool = True, timeout: float = WAIT
    ) -> list[dict[str, Any]]:
        """收集 run.event 通知直到终态；反向请求与响应原样夹在返回列表之外。"""
        events: list[dict[str, Any]] = []
        while True:
            msg = await self.recv(timeout)
            if msg.get("method") != "run.event":
                continue
            event = msg["params"]["event"]
            events.append(event)
            if until_terminal and event["type"] in ("run.completed", "run.failed", "run.cancelled"):
                return events

    def no_output(self) -> bool:
        return not self._skipped and self.transport.outgoing.empty()


class FakeProvider:
    """按脚本吐出分片；每次 stream_chat 消费一段脚本。"""

    def __init__(self, scripts: Iterable[Sequence[Chunk | Exception]]) -> None:
        self._scripts = [list(s) for s in scripts]
        self.calls: list[tuple[list[Message], list[ToolSpec], ChatOptions]] = []
        self.closed = False
        self.started = asyncio.Event()
        self.block: asyncio.Event | None = None
        self.interrupted = False

    async def stream_chat(
        self,
        messages: Sequence[Message],
        tools: Sequence[ToolSpec],
        options: ChatOptions,
    ) -> AsyncIterator[Chunk]:
        self.calls.append((list(messages), list(tools), options))
        self.started.set()
        if not self._scripts:
            raise ProviderError("script exhausted")
        script = self._scripts.pop(0)
        try:
            for item in script:
                if isinstance(item, Exception):
                    raise item
                yield item
            if self.block is not None:
                await self.block.wait()
        except asyncio.CancelledError:
            self.interrupted = True
            raise

    async def aclose(self) -> None:
        self.closed = True


class FakeHost:
    """记录事件、按脚本应答工具调用。"""

    def __init__(
        self,
        tool_results: dict[str, ToolResult | Exception] | None = None,
        *,
        locale: str = "zh",
    ) -> None:
        self.events: list[dict[str, Any]] = []
        self.tool_calls: list[ToolCall] = []
        self._results = tool_results or {}
        self._locale = locale
        self.tool_started = asyncio.Event()
        self.tool_block: asyncio.Event | None = None
        self.tool_cancelled = False

    @property
    def locale(self) -> str:
        return self._locale

    def emit(self, event_type: str, **fields: Any) -> None:
        self.events.append(
            {"type": event_type, **{k: v for k, v in fields.items() if v is not None}}
        )

    async def call_tool(self, call: ToolCall) -> ToolResult:
        self.tool_calls.append(call)
        self.tool_started.set()
        if self.tool_block is not None:
            try:
                await self.tool_block.wait()
            except asyncio.CancelledError:
                self.tool_cancelled = True
                raise
        scripted = self._results.get(call.name)
        if isinstance(scripted, Exception):
            raise scripted
        if scripted is None:
            return ToolResult(call.id, f"ok:{call.name}")
        return ToolResult(call.id, scripted.content, scripted.is_error, scripted.truncated)

    def types(self) -> list[str]:
        return [e["type"] for e in self.events]


def cancel_tool_host() -> FakeHost:
    return FakeHost({"read_file": RunCancelled("cancelled")})


def make_request(
    *,
    run_id: str = "run_1",
    system_prompt: str = "你是助理",
    messages: Sequence[Message] = (),
    tools: Sequence[ToolSpec] = (),
    max_steps: int = 32,
    kind: str = "openai_compatible",
) -> RunRequest:
    return RunRequest(
        run_id=run_id,
        session_id="sess_1",
        agent=AgentSpec(
            id="agent_1", name="助理", system_prompt=system_prompt, autonomy="ask_risky"
        ),
        provider=ProviderCredentials(
            kind=kind, model="m", base_url="https://api.test/v1", api_key="sk-secret-key-123456"
        ),
        messages=tuple(messages),
        tools=tuple(tools),
        options=RunOptions(max_steps=max_steps),
    )


def run_start_params(**overrides: Any) -> dict[str, Any]:
    params: dict[str, Any] = {
        "run_id": "run_1",
        "session_id": "sess_1",
        "agent": {
            "id": "agent_1",
            "name": "助理",
            "system_prompt": "你是助理",
            "autonomy": "ask_risky",
        },
        "provider": {
            "kind": "openai_compatible",
            "base_url": "https://api.test/v1",
            "api_key": "sk-secret-key-123456",
            "model": "gpt-test",
        },
        "messages": [{"role": "user", "parts": [{"type": "text", "text": "你好"}]}],
        "tools": [
            {
                "name": "read_file",
                "description": "读文件",
                "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}},
                "source": "builtin",
                "risk": "read",
            }
        ],
        "options": {"max_steps": 4},
    }
    params.update(overrides)
    return params


def fixed_clock() -> datetime:
    return datetime(2026, 10, 10, 12, 0, 0, tzinfo=UTC)


USAGE_10_5 = Usage(input_tokens=10, output_tokens=5)
