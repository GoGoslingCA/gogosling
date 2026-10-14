"""已交付的协议正文不应被空闲读写循环继续持有；不依赖强制垃圾回收。"""

from __future__ import annotations

import asyncio
import hashlib
import io
import tracemalloc

from gogosling_worker.rpc import Dispatcher
from gogosling_worker.transport import StdioTransport
from tests.harness import WAIT, Daemon

PAYLOAD_BYTES = 2 * 1024 * 1024
IDLE_BUDGET_BYTES = 256 * 1024


async def _settle() -> None:
    # 让已完成处理器及传输回执释放；不发送下一条消息掩盖上一条消息的滞留。
    for _ in range(10):
        await asyncio.sleep(0)


async def test_idle_rpc_reader_releases_delivered_large_request() -> None:
    dispatcher = Dispatcher()
    dispatcher.register("length", lambda params: len(params["text"]))
    async with Daemon(dispatcher) as daemon:
        await _settle()
        tracemalloc.start()
        try:
            response = await daemon.call("length", {"text": "x" * PAYLOAD_BYTES})
            assert response["result"] == PAYLOAD_BYTES
            await _settle()
            retained, _ = tracemalloc.get_traced_memory()
            assert retained < IDLE_BUDGET_BYTES, f"空闲读循环仍保留大请求：{retained} 字节"
        finally:
            tracemalloc.stop()


async def test_idle_rpc_writer_releases_delivered_large_notification() -> None:
    async with Daemon() as daemon:
        await _settle()
        tracemalloc.start()
        try:
            daemon.conn.notify("run.event", {"text": "y" * PAYLOAD_BYTES})

            # helper 的解析对象在返回长度后即可释放，测试自身不保留正文。
            async def consume() -> int:
                return len((await daemon.recv())["params"]["text"])

            assert await consume() == PAYLOAD_BYTES
            await _settle()
            retained, _ = tracemalloc.get_traced_memory()
            assert retained < IDLE_BUDGET_BYTES, f"空闲写循环仍保留大通知：{retained} 字节"
        finally:
            tracemalloc.stop()


class _DigestWriter(io.BytesIO):
    """验证完整写出但不保存输出，以免把测试缓冲区算成传输的内存。"""

    size = 0
    digest = ""

    def write(self, data: bytes | bytearray | memoryview) -> int:
        self.size += len(data)
        self.digest = hashlib.sha256(data).hexdigest()
        return len(data)


async def test_idle_stdio_writer_releases_delivered_large_line() -> None:
    writer = _DigestWriter()
    transport = StdioTransport(io.BytesIO(), writer)
    assert await transport.read(1) == b""
    expected = hashlib.sha256(b"z" * PAYLOAD_BYTES).hexdigest()
    tracemalloc.start()
    try:
        await asyncio.wait_for(transport.write(b"z" * PAYLOAD_BYTES), WAIT)
        assert writer.size == PAYLOAD_BYTES
        assert writer.digest == expected
        # 跨线程回执可能比线程进入下一次 get() 早一个调度周期。
        deadline = asyncio.get_running_loop().time() + 1
        while True:
            retained, _ = tracemalloc.get_traced_memory()
            if retained < IDLE_BUDGET_BYTES or asyncio.get_running_loop().time() >= deadline:
                break
            await asyncio.sleep(0.01)
        assert retained < IDLE_BUDGET_BYTES, f"空闲 stdio 线程仍保留大行：{retained} 字节"
    finally:
        tracemalloc.stop()
        await transport.aclose()
