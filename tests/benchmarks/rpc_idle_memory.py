#!/usr/bin/env python3
"""Measure idle Python protocol allocations after a large delivered message.

Run against a worker checkout with its Python environment. This uses synthetic
messages and local memory streams, no provider, network requests, forced GC,
or process restart. It measures traced Python allocations, not process RSS.
"""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import io
import json
import platform
import statistics
import sys
import time
import tracemalloc
from pathlib import Path
from typing import Any


class MemoryTransport:
    def __init__(self) -> None:
        self.reader = asyncio.StreamReader()
        self.output: asyncio.Queue[bytes] = asyncio.Queue()

    async def read(self, maximum: int) -> bytes:
        return await self.reader.read(maximum)

    async def write(self, data: bytes) -> None:
        self.output.put_nowait(data)

    async def aclose(self) -> None:
        pass


class DigestWriter(io.BytesIO):
    size = 0
    digest = ""

    def write(self, data: bytes | bytearray | memoryview) -> int:
        self.size += len(data)
        self.digest = hashlib.sha256(data).hexdigest()
        return len(data)


async def settle() -> None:
    for _ in range(10):
        await asyncio.sleep(0)


def sample(started: float) -> dict[str, int | float]:
    retained, peak = tracemalloc.get_traced_memory()
    return {
        "retained_bytes": retained,
        "peak_bytes": peak,
        "delivery_ms": round((time.perf_counter() - started) * 1000, 3),
    }


async def rpc_read(size: int) -> dict[str, int | float]:
    from gogosling_worker.rpc import Connection, Dispatcher

    transport = MemoryTransport()
    dispatcher = Dispatcher()
    dispatcher.register("length", lambda params: len(params["text"]))
    conn = Connection(transport, dispatcher)
    serving = asyncio.create_task(conn.serve())
    await settle()
    tracemalloc.start()
    try:
        started = time.perf_counter()
        transport.reader.feed_data(
            json.dumps(
                {"jsonrpc": "2.0", "method": "length", "id": 1, "params": {"text": "x" * size}}
            ).encode()
            + b"\n"
        )
        assert json.loads(await transport.output.get())["result"] == size
        await settle()
        return sample(started)
    finally:
        tracemalloc.stop()
        transport.reader.feed_eof()
        await serving


async def rpc_write(size: int) -> dict[str, int | float]:
    from gogosling_worker.rpc import Connection, Dispatcher

    transport = MemoryTransport()
    conn = Connection(transport, Dispatcher())
    serving = asyncio.create_task(conn.serve())
    await settle()
    tracemalloc.start()
    try:
        started = time.perf_counter()
        conn.notify("run.event", {"text": "y" * size})
        assert len(json.loads(await transport.output.get())["params"]["text"]) == size
        await settle()
        return sample(started)
    finally:
        tracemalloc.stop()
        transport.reader.feed_eof()
        await serving


async def stdio_write(size: int) -> dict[str, int | float]:
    from gogosling_worker.transport import StdioTransport

    writer = DigestWriter()
    transport = StdioTransport(io.BytesIO(), writer)
    assert await transport.read(1) == b""
    expected = hashlib.sha256(b"z" * size).hexdigest()
    tracemalloc.start()
    try:
        started = time.perf_counter()
        await transport.write(b"z" * size)
        assert writer.size == size and writer.digest == expected
        delivered_ms = round((time.perf_counter() - started) * 1000, 3)
        # Allow the writer thread to return to its empty queue after acknowledgement.
        await asyncio.sleep(0.02)
        result = sample(started)
        result["delivery_ms"] = delivered_ms
        return result
    finally:
        tracemalloc.stop()
        await transport.aclose()


async def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worker-root", type=Path, required=True)
    parser.add_argument("--bytes", type=int, default=2 * 1024 * 1024)
    parser.add_argument("--rounds", type=int, default=5)
    args = parser.parse_args()
    root = args.worker_root.resolve()
    sys.path.insert(0, str(root / "src"))
    result: dict[str, Any] = {
        "python": platform.python_version(),
        "payload_bytes": args.bytes,
        "rounds": args.rounds,
        "measurement": "traced Python allocations while idle; no forced garbage collection",
        "source_sha256": {
            name: hashlib.sha256((root / "src/gogosling_worker" / name).read_bytes()).hexdigest()
            for name in ("rpc.py", "transport.py")
        },
        "cases": {},
    }
    for name, benchmark in (
        ("rpc_reader", rpc_read),
        ("rpc_writer", rpc_write),
        ("stdio_writer", stdio_write),
    ):
        samples = [await benchmark(args.bytes) for _ in range(args.rounds)]
        result["cases"][name] = {
            "samples": samples,
            "median": {
                key: statistics.median(row[key] for row in samples)
                for key in ("retained_bytes", "peak_bytes", "delivery_ms")
            },
        }
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    asyncio.run(main())
