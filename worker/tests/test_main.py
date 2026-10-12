"""端到端：以子进程方式启动工作进程，经真实的 stdin / stdout 走一遍握手与退出。"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
from typing import IO, Any

import pytest

from gogosling_worker import __version__

TIMEOUT = 30


def _spawn() -> subprocess.Popen[bytes]:
    env = {
        "PATH": os.environ.get("PATH", ""),
        "SystemRoot": os.environ.get("SystemRoot", ""),
        "GOGOSLING_PROTOCOL_VERSION": "1",
        "GOGOSLING_LOCALE": "zh",
        "PYTHONUNBUFFERED": "1",
        "PYTHONIOENCODING": "utf-8",
        "PYTHONPATH": os.pathsep.join(sys.path),
    }
    return subprocess.Popen(
        [sys.executable, "-m", "gogosling_worker"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
    )


def _send(stdin: IO[bytes], payload: dict[str, Any]) -> None:
    stdin.write(json.dumps(payload).encode("utf-8") + b"\n")
    stdin.flush()


def _read(stdout: IO[bytes]) -> dict[str, Any]:
    line = stdout.readline()
    assert line, "工作进程在给出响应之前关闭了 stdout"
    decoded = json.loads(line)
    assert isinstance(decoded, dict)
    return decoded


def _with_watchdog(proc: subprocess.Popen[bytes]) -> threading.Timer:
    timer = threading.Timer(TIMEOUT, proc.kill)
    timer.start()
    return timer


def test_worker_process_handshake_ping_and_shutdown() -> None:
    proc = _spawn()
    watchdog = _with_watchdog(proc)
    try:
        assert proc.stdin and proc.stdout and proc.stderr
        _send(
            proc.stdin,
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "worker.hello",
                "params": {"protocol_version": "1", "locale": "zh"},
            },
        )
        hello = _read(proc.stdout)
        assert hello["result"]["protocol_version"] == "1"
        assert hello["result"]["worker_version"] == __version__

        _send(proc.stdin, {"jsonrpc": "2.0", "id": 2, "method": "worker.ping"})
        assert _read(proc.stdout)["result"] == "pong"

        _send(proc.stdin, {"jsonrpc": "2.0", "id": 3, "method": "worker.info"})
        assert _read(proc.stdout)["result"]["pid"] == proc.pid

        _send(proc.stdin, {"jsonrpc": "2.0", "method": "worker.shutdown"})
        _, stderr = proc.communicate(timeout=TIMEOUT)
    finally:
        watchdog.cancel()
        if proc.poll() is None:
            proc.kill()
    assert proc.returncode == 0, stderr.decode("utf-8", "replace")


def test_worker_process_answers_everything_piped_before_eof() -> None:
    proc = _spawn()
    watchdog = _with_watchdog(proc)
    try:
        lines = [
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "worker.hello",
                "params": {"protocol_version": "1"},
            },
            {"jsonrpc": "2.0", "id": 2, "method": "worker.ping"},
            {"jsonrpc": "2.0", "method": "worker.shutdown"},
        ]
        payload = "".join(json.dumps(line) + "\n" for line in lines).encode("utf-8")
        stdout, stderr = proc.communicate(input=payload, timeout=TIMEOUT)
    finally:
        watchdog.cancel()
        if proc.poll() is None:
            proc.kill()
    assert proc.returncode == 0, stderr.decode("utf-8", "replace")
    responses = [json.loads(line) for line in stdout.splitlines()]
    assert [r["id"] for r in responses] == [1, 2]
    assert responses[1]["result"] == "pong"


def test_worker_process_exits_on_eof() -> None:
    proc = _spawn()
    watchdog = _with_watchdog(proc)
    try:
        assert proc.stdin and proc.stdout
        _send(proc.stdin, {"jsonrpc": "2.0", "id": 1, "method": "worker.ping"})
        assert _read(proc.stdout)["result"] == "pong"
        # communicate() 会关闭 stdin（EOF），工作进程应随之退出。
        _, stderr = proc.communicate(timeout=TIMEOUT)
    finally:
        watchdog.cancel()
        if proc.poll() is None:
            proc.kill()
    assert proc.returncode == 0, stderr.decode("utf-8", "replace")


def _hello_then_shutdown_keeping_stdin_open() -> tuple[int, bytes]:
    """像守护进程那样：发出 worker.shutdown 之后不关闭 stdin，等待进程自行退出。"""
    proc = _spawn()
    watchdog = _with_watchdog(proc)
    try:
        assert proc.stdin and proc.stdout and proc.stderr
        _send(
            proc.stdin,
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "worker.hello",
                "params": {"protocol_version": "1"},
            },
        )
        assert _read(proc.stdout)["id"] == 1
        _send(proc.stdin, {"jsonrpc": "2.0", "method": "worker.shutdown"})
        # 协议 §6：5 秒内退出；这里给一点余量，stdin 始终保持打开。
        returncode = proc.wait(timeout=8)
        stderr = proc.stderr.read()
    finally:
        watchdog.cancel()
        if proc.poll() is None:
            proc.kill()
        if proc.stdin:
            proc.stdin.close()
    return returncode, stderr


def test_worker_process_exits_zero_after_shutdown_with_stdin_open() -> None:
    # 读线程阻塞在 stdin 上时，解释器收尾曾以退出码 134 中止（could not acquire lock for <stdin>）。
    # 多跑几次：这个问题在修复前是确定性的，修复后不应偶发。
    for _ in range(3):
        returncode, stderr = _hello_then_shutdown_keeping_stdin_open()
        text = stderr.decode("utf-8", "replace")
        assert returncode == 0, text
        assert "Fatal Python error" not in text


def test_worker_process_exits_zero_on_eof_with_run_in_flight() -> None:
    """stdin EOF（守护进程已经不在）时取消在途运行并以 0 退出，不因读线程或在途请求而崩溃。"""
    proc = _spawn()
    watchdog = _with_watchdog(proc)
    params = {
        "run_id": "run_eof",
        "session_id": "s",
        "agent": {"id": "a"},
        # 不可路由的本地地址：供应商请求会挂起或失败，取消必须能打断它。
        "provider": {
            "kind": "openai_compatible",
            "base_url": "http://127.0.0.1:9/v1",
            "model": "m",
        },
        "messages": [{"role": "user", "parts": [{"type": "text", "text": "hi"}]}],
    }
    try:
        lines = [
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "worker.hello",
                "params": {"protocol_version": "1"},
            },
            {"jsonrpc": "2.0", "id": 2, "method": "run.start", "params": params},
        ]
        payload = "".join(json.dumps(line) + "\n" for line in lines).encode("utf-8")
        stdout, stderr = proc.communicate(input=payload, timeout=TIMEOUT)
    finally:
        watchdog.cancel()
        if proc.poll() is None:
            proc.kill()
    assert proc.returncode == 0, stderr.decode("utf-8", "replace")
    assert "Fatal Python error" not in stderr.decode("utf-8", "replace")
    messages = [json.loads(line) for line in stdout.splitlines()]
    accepted = [m for m in messages if m.get("id") == 2]
    assert accepted and accepted[0]["result"] == {"accepted": True}


@pytest.mark.skipif(
    sys.platform == "win32", reason="Windows 下 stderr 编码受控制台影响，只在 POSIX 校验日志通道"
)
def test_worker_process_keeps_stdout_protocol_only() -> None:
    proc = _spawn()
    watchdog = _with_watchdog(proc)
    try:
        assert proc.stdin and proc.stdout
        _send(proc.stdin, {"jsonrpc": "2.0", "id": 1, "method": "nope"})
        response = _read(proc.stdout)
        assert response["error"]["code"] == -32601
        stdout, _ = proc.communicate(timeout=TIMEOUT)
    finally:
        watchdog.cancel()
        if proc.poll() is None:
            proc.kill()
    assert stdout == b""
