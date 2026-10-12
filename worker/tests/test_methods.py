from __future__ import annotations

import asyncio

from gogosling_worker import __version__
from gogosling_worker.methods import WorkerService
from gogosling_worker.protocol import ProviderCredentials, RunRequest, parse_run_request
from gogosling_worker.providers.base import Provider, ProviderError
from gogosling_worker.rpc import (
    DUPLICATE_RUN,
    INCOMPATIBLE_VERSION,
    INTERNAL_ERROR,
    INVALID_PARAMS,
    PROVIDER_FAILED,
    Dispatcher,
)
from gogosling_worker.runtime import DefaultEngine
from gogosling_worker.runtime.engine import Engine, Host
from tests.harness import WAIT, Daemon, FakeProvider, fixed_clock, run_start_params


class ScriptedEngine(Engine):
    """按需阻塞/抛错的引擎，用于测试服务层的兜底行为。"""

    def __init__(self, behaviour: str = "complete") -> None:
        self.behaviour = behaviour
        self.started = asyncio.Event()
        self.release = asyncio.Event()

    async def run(self, request: RunRequest, host: Host, provider: Provider) -> None:
        host.emit("run.started")
        self.started.set()
        if self.behaviour == "block":
            try:
                await self.release.wait()
            except asyncio.CancelledError:
                host.emit("run.cancelled")
                raise
            host.emit("run.completed")
        elif self.behaviour == "raise":
            raise RuntimeError("engine exploded")
        elif self.behaviour == "no_terminal":
            return
        elif self.behaviour == "provider_error":
            raise ProviderError("模型服务返回 HTTP 401：***")
        else:
            host.emit("run.completed")


def _service(
    daemon: Daemon, engine: Engine | None = None, provider: Provider | None = None
) -> WorkerService:
    def factory(creds: ProviderCredentials, locale: str) -> Provider:
        assert "sk-secret" not in repr(creds)
        return provider or FakeProvider([[]])

    service = WorkerService(
        daemon.conn,
        engine=engine or DefaultEngine(),
        provider_factory=factory,
        clock=fixed_clock,
        shutdown_grace=1.0,
    )
    service.register(daemon.dispatcher)
    return service


async def test_hello_negotiates_version_and_locale() -> None:
    daemon = Daemon(Dispatcher())
    service = _service(daemon)
    async with daemon:
        response = await daemon.call(
            "worker.hello", {"protocol_version": "1", "daemon_version": "0.1.0", "locale": "en-US"}
        )
    result = response["result"]
    assert result["protocol_version"] == "1"
    assert result["worker_version"] == __version__
    assert "run" in result["capabilities"] and "providers.anthropic" in result["capabilities"]
    assert service.locale == "en"


async def test_hello_rejects_incompatible_major_version() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon)
    async with daemon:
        response = await daemon.call("worker.hello", {"protocol_version": "2"})
        missing = await daemon.call("worker.hello", {})
    assert response["error"]["code"] == INCOMPATIBLE_VERSION
    assert response["error"]["data"] == {"supported": ["1"]}
    assert missing["error"]["code"] == INCOMPATIBLE_VERSION


async def test_ping_and_info() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon)
    async with daemon:
        assert (await daemon.call("worker.ping"))["result"] == "pong"
        info = (await daemon.call("worker.info"))["result"]
    assert info["version"] == __version__ and isinstance(info["pid"], int) and info["python"]


async def test_run_start_accepts_and_streams_events_with_identity_fields() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon, engine=ScriptedEngine("complete"))
    async with daemon:
        response = await daemon.call("run.start", run_start_params())
        assert response["result"] == {"accepted": True}
        events = await daemon.recv_events()
    assert [e["type"] for e in events] == ["run.started", "run.completed"]
    for event in events:
        assert event["run_id"] == "run_1"
        assert event["session_id"] == "sess_1"
        assert event["agent_id"] == "agent_1"
        assert event["at"] == "2026-10-10T12:00:00.000000Z"


async def test_run_start_duplicate_run_id_is_rejected_while_in_flight() -> None:
    engine = ScriptedEngine("block")
    daemon = Daemon(Dispatcher())
    _service(daemon, engine=engine)
    async with daemon:
        first = await daemon.call("run.start", run_start_params())
        assert first["result"] == {"accepted": True}
        dup = await daemon.call("run.start", run_start_params())
        assert dup["error"]["code"] == DUPLICATE_RUN
        engine.release.set()
        events = await daemon.recv_events()
        assert events[-1]["type"] == "run.completed"
        # 结束后同一 run_id 可以再次提交。
        again = await daemon.call("run.start", run_start_params())
        assert again["result"] == {"accepted": True}


async def test_run_start_invalid_params() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon)
    async with daemon:
        no_run_id = await daemon.call("run.start", run_start_params(run_id=""))
        bad_kind = await daemon.call(
            "run.start", run_start_params(provider={"kind": "nope", "model": "m"})
        )
        bad_message = await daemon.call(
            "run.start", run_start_params(messages=[{"role": "alien", "parts": []}])
        )
        not_object = await daemon.call("run.start", [])
    for response in (no_run_id, bad_kind, bad_message, not_object):
        assert response["error"]["code"] == INVALID_PARAMS


async def test_run_start_provider_construction_error_maps_to_provider_failed() -> None:
    daemon = Daemon(Dispatcher())

    def factory(creds: ProviderCredentials, locale: str) -> Provider:
        raise ProviderError("供应商 openai_compatible 缺少 base_url")

    service = WorkerService(daemon.conn, engine=DefaultEngine(), provider_factory=factory)
    service.register(daemon.dispatcher)
    async with daemon:
        response = await daemon.call("run.start", run_start_params())
    assert response["error"]["code"] == PROVIDER_FAILED
    assert response["error"]["data"] == {"provider_error": "供应商 openai_compatible 缺少 base_url"}


async def test_run_cancel_unknown_returns_false_and_in_flight_returns_true() -> None:
    engine = ScriptedEngine("block")
    daemon = Daemon(Dispatcher())
    provider = FakeProvider([[]])
    _service(daemon, engine=engine, provider=provider)
    async with daemon:
        unknown = await daemon.call("run.cancel", {"run_id": "nope"})
        assert unknown["result"] == {"cancelled": False}
        await daemon.call("run.start", run_start_params())
        await asyncio.wait_for(engine.started.wait(), WAIT)
        cancelled = await daemon.call("run.cancel", {"run_id": "run_1"})
        assert cancelled["result"] == {"cancelled": True}
        events = await daemon.recv_events()
        assert [e["type"] for e in events] == ["run.started", "run.cancelled"]
        again = await daemon.call("run.cancel", {"run_id": "run_1"})
        assert again["result"] == {"cancelled": False}
    assert provider.closed


async def test_cancel_before_run_task_starts_still_yields_terminal_event() -> None:
    engine = ScriptedEngine("block")
    daemon = Daemon(Dispatcher())
    _service(daemon, engine=engine)
    async with daemon:
        # 两条请求在同一块输入里到达：run.cancel 会在运行 task 迈出第一步之前取消它。
        start_id = daemon.send_request("run.start", run_start_params())
        cancel_id = daemon.send_request("run.cancel", {"run_id": "run_1"})
        assert (await daemon.recv_response(start_id))["result"] == {"accepted": True}
        assert (await daemon.recv_response(cancel_id))["result"] == {"cancelled": True}
        events = await daemon.recv_events()
    assert [e["type"] for e in events] == ["run.started", "run.cancelled"]
    assert not engine.started.is_set()


async def test_shutdown_right_after_run_start_yields_terminal_event() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon, engine=ScriptedEngine("block"))
    serve = asyncio.create_task(daemon.conn.serve())
    start_id = daemon.send_request("run.start", run_start_params())
    daemon.send_notification("worker.shutdown")
    await asyncio.wait_for(serve, WAIT)
    assert (await daemon.recv_response(start_id))["result"] == {"accepted": True}
    events = await daemon.recv_events()
    assert [e["type"] for e in events] == ["run.started", "run.cancelled"]


async def test_engine_exception_becomes_run_failed() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon, engine=ScriptedEngine("raise"))
    async with daemon:
        await daemon.call("run.start", run_start_params())
        events = await daemon.recv_events()
    assert events[-1]["type"] == "run.failed"
    assert "RuntimeError" in events[-1]["error"]
    assert "exploded" not in events[-1]["error"]


async def test_engine_without_terminal_event_gets_run_failed_safety_net() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon, engine=ScriptedEngine("no_terminal"))
    async with daemon:
        await daemon.call("run.start", run_start_params())
        events = await daemon.recv_events()
    assert [e["type"] for e in events] == ["run.started", "run.failed"]


async def test_provider_error_from_engine_is_reported_as_run_failed() -> None:
    daemon = Daemon(Dispatcher())
    _service(daemon, engine=ScriptedEngine("provider_error"))
    async with daemon:
        await daemon.call("run.start", run_start_params())
        events = await daemon.recv_events()
    assert events[-1] == {
        "type": "run.failed",
        "run_id": "run_1",
        "session_id": "sess_1",
        "agent_id": "agent_1",
        "at": "2026-10-10T12:00:00.000000Z",
        "error": "模型服务返回 HTTP 401：***",
    }


async def test_shutdown_cancels_in_flight_runs_then_closes() -> None:
    engine = ScriptedEngine("block")
    daemon = Daemon(Dispatcher())
    service = _service(daemon, engine=engine)
    serve = asyncio.create_task(daemon.conn.serve())
    await daemon.call("run.start", run_start_params())
    await asyncio.wait_for(engine.started.wait(), WAIT)
    daemon.send_notification("worker.shutdown")
    await asyncio.wait_for(serve, WAIT)
    events = await daemon.recv_events()
    assert [e["type"] for e in events] == ["run.started", "run.cancelled"]
    assert service.active_runs == 0
    assert daemon.conn.closed and daemon.conn.close_reason == "closed"


async def test_run_start_after_shutdown_is_rejected() -> None:
    daemon = Daemon(Dispatcher())
    service = _service(daemon)
    serve = asyncio.create_task(daemon.conn.serve())
    daemon.send_notification("worker.shutdown")
    await asyncio.wait_for(serve, WAIT)
    await service.aclose()
    daemon.transport.feed(b"")
    # 连接已关闭，直接调用处理器验证拒绝逻辑。
    try:
        await service.run_start(run_start_params())
    except Exception as exc:  # noqa: BLE001
        assert getattr(exc, "code", None) == INTERNAL_ERROR
    else:
        raise AssertionError("shutdown 之后 run.start 应被拒绝")


async def test_eof_cancels_runs_on_aclose() -> None:
    engine = ScriptedEngine("block")
    daemon = Daemon(Dispatcher())
    service = _service(daemon, engine=engine)
    serve = asyncio.create_task(daemon.conn.serve())
    await daemon.call("run.start", run_start_params())
    await asyncio.wait_for(engine.started.wait(), WAIT)
    daemon.transport.feed_eof()
    await asyncio.wait_for(serve, WAIT)
    assert service.active_runs == 1
    await service.aclose()
    assert service.active_runs == 0


async def test_credentials_never_appear_in_repr() -> None:
    request = parse_run_request(run_start_params())
    assert "sk-secret-key-123456" not in repr(request)
    assert request.provider.api_key == "sk-secret-key-123456"
