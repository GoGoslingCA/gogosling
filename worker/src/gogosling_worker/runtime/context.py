"""把 :class:`gogosling_worker.rpc.Connection` 适配成引擎看到的 :class:`Host`。

负责三件事：给事件补齐 ``run_id`` / ``session_id`` / ``agent_id`` / ``at``；保证终态只发一次；
把 ``host.tool.call`` 的错误码翻译成引擎能处理的结果（``-32003`` → 取消，``-32004`` → 错误结果）。
"""

from __future__ import annotations

import logging
from typing import Any

from gogosling_worker.i18n import tr
from gogosling_worker.protocol import (
    EVENT_RUN_FAILED,
    EVENT_RUN_STARTED,
    TERMINAL_EVENTS,
    Clock,
    Message,
    RunRequest,
    ToolCall,
    ToolResult,
    Usage,
    now_utc,
    run_event,
)
from gogosling_worker.rpc import (
    PERMISSION_DENIED,
    RUN_CANCELLED,
    Connection,
    JsonRpcError,
    LineTooLong,
)
from gogosling_worker.runtime.engine import RunCancelled

NOTIFY_RUN_EVENT = "run.event"
METHOD_HOST_TOOL_CALL = "host.tool.call"

_log = logging.getLogger(__name__)


class RunContext:
    """一次运行的宿主句柄。"""

    def __init__(
        self,
        conn: Connection,
        request: RunRequest,
        *,
        locale: str,
        clock: Clock = now_utc,
        logger: logging.Logger | None = None,
    ) -> None:
        self._conn = conn
        self._run_id = request.run_id
        self._session_id = request.session_id
        self._agent_id = request.agent.id
        self._locale = locale
        self._clock = clock
        self._log = logger or _log
        self._started_sent = False
        self._terminal_sent = False

    @property
    def locale(self) -> str:
        return self._locale

    @property
    def run_id(self) -> str:
        return self._run_id

    @property
    def started_sent(self) -> bool:
        """是否已经发出 run.started。"""
        return self._started_sent

    @property
    def terminal_sent(self) -> bool:
        """是否已经发出终态事件。"""
        return self._terminal_sent

    def emit(
        self,
        event_type: str,
        *,
        delta: str | None = None,
        tool_call: ToolCall | None = None,
        tool_result: ToolResult | None = None,
        usage: Usage | None = None,
        message: Message | None = None,
        error: str | None = None,
    ) -> None:
        if self._terminal_sent:
            self._log.debug(
                "终态之后的事件被忽略", extra={"run_id": self._run_id, "event": event_type}
            )
            return
        event = run_event(
            event_type,
            run_id=self._run_id,
            session_id=self._session_id,
            agent_id=self._agent_id,
            at=self._clock(),
            delta=delta,
            tool_call=tool_call,
            tool_result=tool_result,
            usage=usage,
            message=message,
            error=error,
        )
        try:
            self._conn.notify(NOTIFY_RUN_EVENT, {"run_id": self._run_id, "event": event})
        except LineTooLong as exc:
            if event_type not in TERMINAL_EVENTS:
                self._log.warning(
                    "事件超过单行上限被丢弃", extra={"run_id": self._run_id, "size": exc.size}
                )
                return
            # 终态必须送达：改发一个不含大负载的 run.failed。
            fallback = run_event(
                EVENT_RUN_FAILED,
                run_id=self._run_id,
                session_id=self._session_id,
                agent_id=self._agent_id,
                at=self._clock(),
                error=tr("run.event_too_large", self._locale),
            )
            self._conn.notify(NOTIFY_RUN_EVENT, {"run_id": self._run_id, "event": fallback})
        if event_type == EVENT_RUN_STARTED:
            self._started_sent = True
        if event_type in TERMINAL_EVENTS:
            self._terminal_sent = True

    async def call_tool(self, call: ToolCall) -> ToolResult:
        params = {"run_id": self._run_id, "call": call.to_json()}
        try:
            raw = await self._conn.request(METHOD_HOST_TOOL_CALL, params)
        except LineTooLong:
            return ToolResult(call.id, tr("tool.arguments_too_large", self._locale), is_error=True)
        except JsonRpcError as exc:
            if exc.code == RUN_CANCELLED:
                raise RunCancelled(exc.message) from exc
            if exc.code == PERMISSION_DENIED:
                reason = _reason_from(exc.data) or exc.message
                return ToolResult(call.id, reason, is_error=True)
            return ToolResult(
                call.id, tr("tool.host_error", self._locale, reason=exc.message), is_error=True
            )
        return self._parse_result(raw, call.id)

    def _parse_result(self, raw: Any, call_id: str) -> ToolResult:
        result = raw.get("result") if isinstance(raw, dict) else None
        if not isinstance(result, dict):
            return ToolResult(call_id, tr("tool.malformed_result", self._locale), is_error=True)
        content = result.get("content")
        returned_id = result.get("call_id")
        return ToolResult(
            call_id=returned_id if isinstance(returned_id, str) and returned_id else call_id,
            content=content if isinstance(content, str) else "",
            is_error=bool(result.get("is_error", False)),
            truncated=result.get("truncated") is True,
        )


def _reason_from(data: Any) -> str | None:
    if isinstance(data, dict):
        reason = data.get("reason")
        if isinstance(reason, str) and reason:
            return reason
    return None
