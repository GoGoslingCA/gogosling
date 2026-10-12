"""stdio 传输：读线程直接读文件描述符，关闭后不再投递数据。"""

from __future__ import annotations

import asyncio
import os

from gogosling_worker.transport import StdioTransport

WAIT = 5.0


async def test_reads_from_fd_until_eof_and_writes_lines() -> None:
    in_r, in_w = os.pipe()
    out_r, out_w = os.pipe()
    with os.fdopen(in_r, "rb") as stdin, os.fdopen(out_w, "wb") as stdout:
        transport = StdioTransport(stdin, stdout)
        os.write(in_w, b'{"a":1}\n')
        assert await asyncio.wait_for(transport.read(1024), WAIT) == b'{"a":1}\n'
        await asyncio.wait_for(transport.write(b"pong\n"), WAIT)
        assert os.read(out_r, 1024) == b"pong\n"
        os.close(in_w)
        assert await asyncio.wait_for(transport.read(1024), WAIT) == b""
        # EOF 之后每次读取都返回空。
        assert await asyncio.wait_for(transport.read(1024), WAIT) == b""
        await transport.aclose()
        assert transport.stopped
    os.close(out_r)


async def test_aclose_wakes_reader_while_stdin_stays_open() -> None:
    """守护进程发出 worker.shutdown 后并不关闭管道：关闭传输不能依赖 stdin EOF。"""
    in_r, in_w = os.pipe()
    out_r, out_w = os.pipe()
    with os.fdopen(in_r, "rb") as stdin, os.fdopen(out_w, "wb") as stdout:
        transport = StdioTransport(stdin, stdout)
        pending = asyncio.create_task(transport.read(1024))
        await asyncio.sleep(0.05)
        await transport.aclose()
        assert await asyncio.wait_for(pending, WAIT) == b""
        # 关闭之后到达的数据不再投递。
        os.write(in_w, b"late\n")
        await asyncio.sleep(0.05)
        assert await asyncio.wait_for(transport.read(1024), WAIT) == b""
    os.close(in_w)
    os.close(out_r)
