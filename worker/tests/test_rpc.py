from __future__ import annotations

import asyncio
import json
from typing import Any

import pytest

from gogosling_worker.rpc import (
    INTERNAL_ERROR,
    INVALID_REQUEST,
    METHOD_NOT_FOUND,
    PARSE_ERROR,
    Connection,
    ConnectionClosed,
    Dispatcher,
    JsonRpcError,
    LineSplitter,
    LineTooLong,
    OverlongLine,
)
from tests.harness import WAIT, Daemon


def _ping_dispatcher() -> Dispatcher:
    dispatcher = Dispatcher()
    dispatcher.register("worker.ping", lambda _params: "pong")
    return dispatcher


# ---- 分帧 ----


def test_splitter_returns_complete_lines_and_strips_cr() -> None:
    splitter = LineSplitter(max_line=64)
    assert splitter.feed(b"a\r\nb\n") == [b"a", b"b"]
    assert splitter.feed(b"par") == []
    assert splitter.feed(b"tial\n") == [b"partial"]
    assert splitter.flush() is None


def test_splitter_discards_overlong_line_as_a_whole() -> None:
    splitter = LineSplitter(max_line=8)
    out = splitter.feed(b"0123456789")
    assert out == []
    out = splitter.feed(b"abcdef\nnext\n")
    assert out == [OverlongLine(16), b"next"]


def test_splitter_flush_reports_overlong_tail() -> None:
    splitter = LineSplitter(max_line=4)
    assert splitter.feed(b"toolong") == []
    assert splitter.flush() == OverlongLine(7)
    assert splitter.flush() is None


# ---- 正向请求 ----


async def test_ping_returns_pong() -> None:
    async with Daemon(_ping_dispatcher()) as daemon:
        response = await daemon.call("worker.ping")
    assert response == {"jsonrpc": "2.0", "id": 101, "result": "pong"}


async def test_unknown_method_returns_error() -> None:
    async with Daemon(_ping_dispatcher()) as daemon:
        response = await daemon.call("nope")
    assert response["id"] == 101
    assert response["error"]["code"] == METHOD_NOT_FOUND


async def test_invalid_json_returns_parse_error() -> None:
    async with Daemon(_ping_dispatcher()) as daemon:
        daemon.send_line("{not json")
        response = await daemon.recv()
    assert response["id"] is None
    assert response["error"]["code"] == PARSE_ERROR


async def test_non_object_message_is_invalid_request() -> None:
    async with Daemon(_ping_dispatcher()) as daemon:
        daemon.send_line("[1,2,3]")
        response = await daemon.recv()
    assert response["error"]["code"] == INVALID_REQUEST


async def test_notification_has_no_response_and_blank_lines_are_skipped() -> None:
    async with Daemon(_ping_dispatcher()) as daemon:
        daemon.send_line("")
        daemon.send_notification("worker.ping")
        daemon.send_line("   ")
        request_id = daemon.send_request("worker.ping")
        response = await daemon.recv_response(request_id)
        assert response["result"] == "pong"
        assert daemon.no_output()


async def test_handler_exception_becomes_internal_error_without_killing_connection() -> None:
    dispatcher = _ping_dispatcher()

    def boom(_params: object) -> None:
        raise RuntimeError("boom")

    dispatcher.register("boom", boom)
    async with Daemon(dispatcher) as daemon:
        response = await daemon.call("boom")
        assert response["error"]["code"] == INTERNAL_ERROR
        assert "boom" not in response["error"]["message"]
        # 连接仍然可用。
        assert (await daemon.call("worker.ping"))["result"] == "pong"


async def test_jsonrpc_error_from_handler_keeps_code_and_data() -> None:
    dispatcher = Dispatcher()

    async def denied(_params: object) -> None:
        raise JsonRpcError(-32001, "unsupported", {"supported": ["1"]})

    dispatcher.register("hello", denied)
    async with Daemon(dispatcher) as daemon:
        response = await daemon.call("hello")
    assert response["error"] == {
        "code": -32001,
        "message": "unsupported",
        "data": {"supported": ["1"]},
    }


async def test_requests_are_served_concurrently() -> None:
    dispatcher = _ping_dispatcher()
    gate = asyncio.Event()

    async def slow(_params: object) -> str:
        await gate.wait()
        return "slow"

    dispatcher.register("slow", slow)
    async with Daemon(dispatcher) as daemon:
        slow_id = daemon.send_request("slow")
        fast = await daemon.call("worker.ping")
        assert fast["result"] == "pong"
        gate.set()
        response = await daemon.recv_response(slow_id)
        assert response["result"] == "slow"


async def test_oversized_incoming_line_is_rejected_without_breaking_framing() -> None:
    async with Daemon(_ping_dispatcher(), max_line_bytes=160) as daemon:
        daemon.send_line("x" * 400)
        err = await daemon.recv()
        assert err["error"]["code"] == PARSE_ERROR
        assert (await daemon.call("worker.ping"))["result"] == "pong"


async def test_oversized_result_is_replaced_by_internal_error() -> None:
    dispatcher = Dispatcher()
    dispatcher.register("big", lambda _params: "y" * 200)
    async with Daemon(dispatcher, max_line_bytes=128) as daemon:
        response = await daemon.call("big")
    assert response["error"]["code"] == INTERNAL_ERROR
    assert "too large" in response["error"]["message"]


async def test_unserializable_result_is_internal_error() -> None:
    dispatcher = Dispatcher()
    dispatcher.register("obj", lambda _params: object())
    async with Daemon(dispatcher) as daemon:
        response = await daemon.call("obj")
    assert response["error"]["code"] == INTERNAL_ERROR


# ---- 通知与反向请求 ----


async def test_notify_writes_single_line_with_unicode() -> None:
    dispatcher = Dispatcher()
    async with Daemon(dispatcher) as daemon:
        daemon.conn.notify("run.event", {"delta": "你好\n世界"})
        raw = await asyncio.wait_for(daemon.transport.outgoing.get(), WAIT)
    assert raw.count(b"\n") == 1
    assert json.loads(raw) == {
        "jsonrpc": "2.0",
        "method": "run.event",
        "params": {"delta": "你好\n世界"},
    }
    assert "你好".encode() in raw  # ensure_ascii=False


async def test_reverse_request_round_trip() -> None:
    dispatcher = Dispatcher()
    daemon = Daemon(dispatcher)

    async def call_tool(_params: object) -> Any:
        return await daemon.conn.request("host.tool.call", {"call": {"id": "c1"}})

    dispatcher.register("run", call_tool)
    async with daemon:
        run_id = daemon.send_request("run")
        reverse = await daemon.recv()
        assert reverse["method"] == "host.tool.call"
        assert isinstance(reverse["id"], int)
        daemon.reply(reverse["id"], {"result": {"call_id": "c1", "content": "ok"}})
        response = await daemon.recv_response(run_id)
    assert response["result"] == {"result": {"call_id": "c1", "content": "ok"}}


async def test_reverse_request_error_raises_jsonrpc_error() -> None:
    dispatcher = Dispatcher()
    daemon = Daemon(dispatcher)
    seen: list[JsonRpcError] = []

    async def call_tool(_params: object) -> str:
        try:
            await daemon.conn.request("host.tool.call", {})
        except JsonRpcError as exc:
            seen.append(exc)
            return "handled"
        return "unexpected"

    dispatcher.register("run", call_tool)
    async with daemon:
        run_id = daemon.send_request("run")
        reverse = await daemon.recv()
        daemon.reply_error(reverse["id"], -32004, "denied", {"reason": "no"})
        response = await daemon.recv_response(run_id)
    assert response["result"] == "handled"
    assert seen[0].code == -32004 and seen[0].data == {"reason": "no"}


async def test_reverse_request_fails_when_connection_closes() -> None:
    dispatcher = Dispatcher()
    daemon = Daemon(dispatcher)
    outcome: list[str] = []

    async def wait_forever(_params: object) -> None:
        try:
            await daemon.conn.request("host.tool.call", {})
        except ConnectionClosed:
            outcome.append("closed")
            raise
        except asyncio.CancelledError:
            outcome.append("cancelled")
            raise

    dispatcher.register("run", wait_forever)
    async with daemon:
        daemon.send_request("run")
        await daemon.recv()  # 反向请求已发出
    assert outcome == ["closed"] or outcome == ["cancelled"]
    assert daemon.conn.closed and daemon.conn.close_reason == "eof"


async def test_cancelled_waiter_ignores_late_response() -> None:
    dispatcher = Dispatcher()
    daemon = Daemon(dispatcher)
    task_holder: list[asyncio.Task[Any]] = []

    async def start(_params: object) -> str:
        task = asyncio.create_task(daemon.conn.request("host.tool.call", {}))
        task_holder.append(task)
        return "started"

    dispatcher.register("start", start)
    dispatcher.register("worker.ping", lambda _params: "pong")
    async with daemon:
        await daemon.call("start")
        reverse = await daemon.recv()
        task_holder[0].cancel()
        with pytest.raises(asyncio.CancelledError):
            await task_holder[0]
        daemon.reply(reverse["id"], {"late": True})
        # 迟到的响应被忽略，连接照常工作。
        assert (await daemon.call("worker.ping"))["result"] == "pong"


async def test_requests_received_before_eof_are_still_answered() -> None:
    dispatcher = _ping_dispatcher()

    async def slow(_params: object) -> str:
        await asyncio.sleep(0.05)
        return "late-but-answered"

    dispatcher.register("slow", slow)
    daemon = Daemon(dispatcher)
    serve = asyncio.create_task(daemon.conn.serve())
    daemon.send_request("slow")
    daemon.send_request("worker.ping")
    daemon.transport.feed_eof()
    await asyncio.wait_for(serve, WAIT)
    results = {(await daemon.recv())["result"], (await daemon.recv())["result"]}
    assert results == {"late-but-answered", "pong"}
    assert daemon.conn.closed and daemon.conn.close_reason == "eof"


async def test_unknown_response_id_is_ignored() -> None:
    async with Daemon(_ping_dispatcher()) as daemon:
        daemon.send({"jsonrpc": "2.0", "id": 999, "result": "stray"})
        assert (await daemon.call("worker.ping"))["result"] == "pong"


async def test_notify_too_long_raises_and_closed_connection_drops() -> None:
    dispatcher = Dispatcher()
    daemon = Daemon(dispatcher, max_line_bytes=64)
    async with daemon:
        with pytest.raises(LineTooLong):
            daemon.conn.notify("big", {"x": "z" * 100})
    # 关闭后通知静默丢弃，request 立即失败。
    daemon.conn.notify("after", {})
    with pytest.raises(ConnectionClosed):
        await daemon.conn.request("after", {})


async def test_close_flushes_queued_output_and_cancels_handlers() -> None:
    dispatcher = Dispatcher()
    daemon = Daemon(dispatcher)
    cancelled = asyncio.Event()

    async def hang(_params: object) -> None:
        try:
            await asyncio.Event().wait()
        except asyncio.CancelledError:
            cancelled.set()
            raise

    dispatcher.register("hang", hang)
    serve = asyncio.create_task(daemon.conn.serve())
    daemon.send_request("hang")
    await asyncio.sleep(0.01)
    daemon.conn.notify("last", {"n": 1})
    daemon.conn.close()
    await asyncio.wait_for(serve, WAIT)
    assert cancelled.is_set()
    last = await daemon.recv()
    assert last["method"] == "last"
    assert daemon.transport.closed


def test_connection_exposes_dispatcher_registry() -> None:
    dispatcher = Dispatcher()
    dispatcher.register("a", lambda _p: None)
    with pytest.raises(ValueError):
        dispatcher.register("a", lambda _p: None)
    assert dispatcher.methods() == ["a"]
    assert isinstance(Connection, type)
