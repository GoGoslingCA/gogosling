"""标准输入输出传输：用两个守护线程把阻塞的管道 I/O 接到事件循环上。

不用 ``loop.connect_read_pipe``：它在 Windows 上对控制台句柄不可用，且三个平台行为不一致；
线程方案在 Linux / macOS / Windows 上表现相同。

读线程直接对 stdin 的文件描述符调用 ``os.read``，而不是 ``sys.stdin.buffer``（``BufferedReader``）：
守护进程发出 ``worker.shutdown`` 后并不关闭管道，读线程会一直阻塞在读取里；若它阻塞在
``BufferedReader.read1`` 上，就一直持有该对象的内部锁，解释器收尾时去获取这把锁会以
"could not acquire lock for <stdin> at interpreter shutdown" 中止进程（退出码 134）。``os.read``
不持有任何 Python 层的锁。:meth:`StdioTransport.aclose` 置停止标志，读线程此后不再投递数据；
文件描述符不在这里关闭——另一个线程正阻塞在它上面时关闭既不能可靠地唤醒读取，还可能让编号被
复用后读到别的文件——由进程退出统一回收（见 ``__main__`` 的退出路径）。

stdout 只写协议消息；日志一律走 stderr，否则会破坏分帧。
"""

from __future__ import annotations

import asyncio
import io
import logging
import os
import queue
import threading
from typing import BinaryIO

_READ_CHUNK_BYTES = 64 * 1024

_log = logging.getLogger(__name__)


class StdioTransport:
    """基于 stdin / stdout 的 :class:`gogosling_worker.rpc.Transport` 实现。"""

    def __init__(self, stdin: BinaryIO, stdout: BinaryIO) -> None:
        self._stdin = stdin
        self._stdin_fd = _fileno(stdin)
        self._stdout = stdout
        self._loop: asyncio.AbstractEventLoop | None = None
        self._incoming: asyncio.Queue[bytes] | None = None
        self._outgoing: queue.Queue[tuple[bytes, asyncio.Future[None]] | None] = queue.Queue()
        self._reader: threading.Thread | None = None
        self._writer: threading.Thread | None = None
        self._stop = threading.Event()
        self._closed = False

    @property
    def stopped(self) -> bool:
        """是否已经调用过 :meth:`aclose`（读线程不再投递数据）。"""
        return self._stop.is_set()

    async def read(self, max_bytes: int) -> bytes:
        """返回下一块已读到的数据；EOF 或关闭后每次调用都返回空字节串。"""
        incoming = self._ensure_started()
        chunk = await incoming.get()
        if not chunk:
            incoming.put_nowait(b"")
        return chunk

    async def write(self, data: bytes) -> None:
        """把一行写到 stdout 并 flush；写线程失败时在这里抛出异常。"""
        self._ensure_started()
        if self._closed:
            raise OSError("stdio transport closed")
        assert self._loop is not None
        future: asyncio.Future[None] = self._loop.create_future()
        self._outgoing.put((data, future))
        await future

    async def aclose(self) -> None:
        """置停止标志并停止写线程。

        调用方（``rpc.Connection.serve``）在此之前已经等待全部写入完成，所以写线程此时空闲。
        读线程可能仍阻塞在 ``os.read`` 里：它不持有任何锁，进程以 ``os._exit`` 结束时随之消失。
        """
        if self._closed:
            return
        self._closed = True
        self._stop.set()
        self._outgoing.put(None)
        if self._incoming is not None:
            # 唤醒仍在等待输入的读者（例如关闭发生在 EOF 之前）。
            self._incoming.put_nowait(b"")

    # ---- 内部 ----

    def _ensure_started(self) -> asyncio.Queue[bytes]:
        if self._incoming is None:
            self._loop = asyncio.get_running_loop()
            self._incoming = asyncio.Queue()
            self._reader = threading.Thread(
                target=self._read_main, name="stdio-reader", daemon=True
            )
            self._writer = threading.Thread(
                target=self._write_main, name="stdio-writer", daemon=True
            )
            self._reader.start()
            self._writer.start()
        return self._incoming

    def _post(self, callback: object, *args: object) -> None:
        """在事件循环线程里执行回调；循环已关闭时静默忽略。"""
        assert self._loop is not None
        try:
            self._loop.call_soon_threadsafe(callback, *args)  # type: ignore[arg-type]
        except RuntimeError:
            pass

    def _read_chunk(self) -> bytes:
        if self._stdin_fd is not None:
            return os.read(self._stdin_fd, _READ_CHUNK_BYTES)
        # 没有文件描述符的流（只在测试里出现）：退回到对象自己的读取方法。
        read1 = getattr(self._stdin, "read1", None)
        data = read1(_READ_CHUNK_BYTES) if callable(read1) else self._stdin.read(_READ_CHUNK_BYTES)
        return data if isinstance(data, bytes) else bytes(data)

    def _read_main(self) -> None:
        incoming = self._incoming
        assert incoming is not None
        try:
            while not self._stop.is_set():
                chunk = self._read_chunk()
                if not chunk or self._stop.is_set():
                    break
                self._post(incoming.put_nowait, chunk)
                del chunk
        except Exception:  # noqa: BLE001 - 读线程的异常等同于 EOF
            _log.debug("stdin 读取结束于异常", exc_info=True)
        finally:
            self._post(incoming.put_nowait, b"")

    def _write_main(self) -> None:
        while True:
            item = self._outgoing.get()
            if item is None:
                return
            data, future = item
            try:
                self._stdout.write(data)
                self._stdout.flush()
            except Exception as exc:  # noqa: BLE001 - 把失败交回事件循环，由连接决定关闭
                self._post(_resolve, future, exc)
            else:
                self._post(_resolve, future, None)
            finally:
                # 回执回调持有 future 即可；阻塞等候下一项时释放已写出的完整正文。
                del item, data, future


def _fileno(stream: BinaryIO) -> int | None:
    """返回流的文件描述符；没有（例如内存流）时返回 None。"""
    try:
        return stream.fileno()
    except (AttributeError, OSError, ValueError, io.UnsupportedOperation):
        return None


def _resolve(future: asyncio.Future[None], exc: BaseException | None) -> None:
    if future.done():
        return
    if exc is None:
        future.set_result(None)
    else:
        future.set_exception(exc)
