from __future__ import annotations

import asyncio

from gogosling_worker.protocol import ToolCall, ToolResult
from gogosling_worker.runtime.context import RunContext
from tests.harness import Daemon, make_request


async def test_host_result_truncation_flag_is_forwarded_over_rpc() -> None:
    async with Daemon() as daemon:
        host = RunContext(daemon.conn, make_request(), locale="zh")
        call = ToolCall("call_1", "read_file", {"path": "large.txt"})
        task = asyncio.create_task(host.call_tool(call))
        request = await daemon.recv()
        assert request["method"] == "host.tool.call"
        daemon.reply(
            request["id"],
            {"result": {"call_id": "call_1", "content": "partial", "truncated": True}},
        )
        result = await task
    assert result == ToolResult("call_1", "partial", truncated=True)
