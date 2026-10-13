#!/usr/bin/env python3
"""Serve a real local app and worker with a repeatable, synthetic long-task model.

This is a manual integration/performance fixture, not a model quality test. The
operator sends the task and approves its final write through the normal UI. All
provider traffic stays on loopback. No real provider credentials are used. Only
non-content metadata and process RSS are exposed by the measurement endpoints.
"""

from __future__ import annotations

import argparse
import collections
import hashlib
import http.client
import json
import mimetypes
import os
import select
import shutil
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

FILES = 600
BATCH = 20
MAX_BODY = 16 * 1024 * 1024
MODEL = "local-long-task-fixture"
RESULT = (
    "Read all 600 synthetic input files. The approval-controlled report is complete.\n"
)
REPOSITORY = Path(__file__).resolve().parents[2]


class Fixture:
    def __init__(self, arguments: argparse.Namespace) -> None:
        self.args = arguments
        self.temporary = tempfile.TemporaryDirectory(
            prefix="gogosling-long-task-home-", dir=arguments.temp_parent
        )
        self.home = Path(self.temporary.name)
        os.chmod(self.home, 0o700)
        self.stop = threading.Event()
        self.condition = threading.Condition()
        self.hold = arguments.hold
        self.delay = arguments.delay
        self.upload_delay = 0.0
        self.upload_waits = 0
        self.upload_cancellations = 0
        self.hold_after = arguments.hold_after_reads
        self.permits = 0
        self.pending = None
        self.started = time.monotonic()
        self.requests: collections.deque[dict] = collections.deque(maxlen=128)
        self.samples: collections.deque[dict] = collections.deque(maxlen=3600)
        self.marks: collections.deque[dict] = collections.deque(maxlen=256)
        self.sessions: list[dict] = []
        self.run_ids: list[str] = []
        self.agent: dict = {}
        self.process: subprocess.Popen | None = None
        self.log = None
        self.endpoint = ""
        self.token = ""
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        fixture = self

        class QuietServer(ThreadingHTTPServer):
            daemon_threads = True

            def handle_error(self, request, client_address) -> None:
                # Request bodies, headers and exceptions are intentionally not logged.
                pass

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args) -> None:
                pass

            def allowed(self) -> bool:
                origin = self.headers.get("Origin")
                return (
                    self.headers.get("Host") == fixture.address
                    and (not origin or origin == fixture.origin)
                    and self.headers.get("Sec-Fetch-Site") != "cross-site"
                )

            def body(self) -> bytes:
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 <= size <= MAX_BODY:
                    raise ValueError("Request size is out of bounds")
                return self.rfile.read(size)

            def reply(self, status: int, body: bytes, content_type: str) -> None:
                self.send_response(status)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(body)))
                self.send_header("Cache-Control", "no-store")
                self.send_header("X-Content-Type-Options", "nosniff")
                self.send_header("X-Frame-Options", "DENY")
                self.end_headers()
                self.wfile.write(body)

            def json(self, value: dict, status: int = 200) -> None:
                self.reply(status, json.dumps(value).encode(), "application/json")

            def do_GET(self) -> None:
                self.route()

            def do_POST(self) -> None:
                self.route()

            def do_PATCH(self) -> None:
                self.route()

            def do_DELETE(self) -> None:
                self.route()

            def do_PUT(self) -> None:
                self.route()

            def route(self) -> None:
                if not self.allowed():
                    self.json(
                        {"error": "Only same-origin loopback requests are accepted"},
                        403,
                    )
                    return
                try:
                    if self.path.startswith("/api/"):
                        self.proxy()
                    elif self.path == "/v1/chat/completions" and self.command == "POST":
                        self.model(json.loads(self.body()))
                    elif self.path == "/__fixture/state" and self.command == "GET":
                        self.json(fixture.snapshot())
                    elif self.path == "/__fixture/samples" and self.command == "GET":
                        with fixture.condition:
                            self.json(
                                {
                                    "samples": list(fixture.samples),
                                    "marks": list(fixture.marks),
                                }
                            )
                    elif self.path == "/__fixture/control" and self.command == "POST":
                        self.control(json.loads(self.body()))
                    elif self.command == "GET":
                        self.static()
                    else:
                        self.json({"error": "Unknown fixture endpoint"}, 404)
                except (BrokenPipeError, ConnectionResetError):
                    pass
                except (
                    OSError,
                    ValueError,
                    TypeError,
                    KeyError,
                    http.client.HTTPException,
                ):
                    # No raw request, header, provider response or token in diagnostics.
                    try:
                        self.json({"error": "Local fixture request failed"}, 500)
                    except (BrokenPipeError, ConnectionResetError):
                        pass

            def control(self, data: dict) -> None:
                with fixture.condition:
                    if "hold" in data:
                        fixture.hold = bool(data["hold"])
                    if "hold_after_reads" in data:
                        value = data["hold_after_reads"]
                        fixture.hold_after = (
                            None if value is None else max(0, min(FILES, int(value)))
                        )
                    if "delay_s" in data:
                        fixture.delay = max(0.0, min(10.0, float(data["delay_s"])))
                    if "upload_delay_s" in data:
                        fixture.upload_delay = max(
                            0.0, min(10.0, float(data["upload_delay_s"]))
                        )
                    if data.get("step_once"):
                        fixture.permits += 1
                    if "mark" in data:
                        fixture.marks.append(
                            {
                                "elapsed_s": round(
                                    time.monotonic() - fixture.started, 3
                                ),
                                "label": str(data["mark"])[:80],
                            }
                        )
                    fixture.condition.notify_all()
                self.json({"ok": True})

            def static(self) -> None:
                path = urllib.parse.unquote(urllib.parse.urlsplit(self.path).path)
                root = fixture.args.web_root.resolve()
                target = (root / path.lstrip("/")).resolve()
                if path == "/":
                    target = root / "index.html"
                if not target.is_relative_to(root) or not target.is_file():
                    self.json({"error": "Static file not found"}, 404)
                    return
                body = target.read_bytes()
                if target.name == "index.html":
                    bridge = b'<script>window.gogosling={getConnection:async()=>({endpoint:location.origin,token:"",mode:"proxy"})};</script>'
                    body = body.replace(b"<head>", b"<head>" + bridge, 1)
                content_type = (
                    mimetypes.guess_type(target.name)[0] or "application/octet-stream"
                )
                self.reply(200, body, content_type)

            def proxy(self) -> None:
                if not fixture.endpoint:
                    self.json({"error": "Daemon is starting"}, 503)
                    return
                address = urllib.parse.urlsplit(fixture.endpoint)
                connection = http.client.HTTPConnection(
                    address.hostname, address.port, timeout=60
                )
                headers = {"Authorization": "Bearer " + fixture.token}
                for name in (
                    "Content-Type",
                    "Accept-Language",
                    "Accept",
                    "Range",
                    "If-Match",
                ):
                    if self.headers.get(name):
                        headers[name] = self.headers[name]
                try:
                    body = (
                        self.body()
                        if self.command in ("POST", "PUT", "PATCH", "DELETE")
                        else None
                    )
                    parts = urllib.parse.urlsplit(self.path).path.split("/")
                    if (
                        self.command == "POST"
                        and len(parts) == 6
                        and parts[:4] == ["", "api", "v1", "sessions"]
                        and parts[4]
                        and parts[5] == "attachments"
                        and not self.wait_for_upload()
                    ):
                        self.close_connection = True
                        return
                    connection.request(
                        self.command, self.path, body=body, headers=headers
                    )
                    response = connection.getresponse()
                    content_type = response.getheader(
                        "Content-Type", "application/octet-stream"
                    )
                    if "text/event-stream" not in content_type:
                        payload = response.read()
                        if (
                            self.command == "POST"
                            and self.path.endswith("/messages")
                            and response.status == 202
                        ):
                            accepted = json.loads(payload)
                            with fixture.condition:
                                fixture.run_ids.append(accepted["id"])
                        self.reply(response.status, payload, content_type)
                        return
                    self.send_response(response.status)
                    self.send_header("Content-Type", content_type)
                    self.send_header("Cache-Control", "no-store")
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.close_connection = True
                    while not fixture.stop.is_set():
                        chunk = response.read1(65536)
                        if not chunk:
                            break
                        self.wfile.write(chunk)
                        self.wfile.flush()
                finally:
                    connection.close()

            def wait_for_upload(self) -> bool:
                # The already-read bounded body stays only on this request's stack.
                # A disconnected caller must not turn the test delay into a late write.
                with fixture.condition:
                    delay = fixture.upload_delay
                    if delay <= 0:
                        return True
                    fixture.upload_waits += 1
                deadline = time.monotonic() + delay
                while not fixture.stop.is_set():
                    try:
                        readable, _, _ = select.select([self.connection], [], [], 0)
                        if readable and not self.connection.recv(1, socket.MSG_PEEK):
                            break
                    except OSError:
                        break
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        return True
                    fixture.stop.wait(min(0.05, remaining))
                with fixture.condition:
                    fixture.upload_cancellations += 1
                return False

            def model(self, data: dict) -> None:
                messages = data.get("messages", [])
                completed = {
                    message.get("tool_call_id")
                    for message in messages
                    if isinstance(message, dict) and message.get("role") == "tool"
                }
                count = sum(
                    f"fixture_read_{index:04d}" in completed for index in range(FILES)
                )
                written = "fixture_write_report" in completed
                phase = "complete" if written else "write" if count == FILES else "read"
                with fixture.condition:
                    fixture.pending = {"completed_reads": count, "phase": phase}
                    fixture.requests.append(
                        {
                            "elapsed_s": round(time.monotonic() - fixture.started, 3),
                            "completed_reads": count,
                            "phase": phase,
                            "input_messages": len(messages),
                        }
                    )
                # Headers and comments keep a deliberately held stream alive; a client
                # cancellation breaks its next flush instead of retaining a blocked read.
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-store")
                self.send_header("Connection", "close")
                self.end_headers()
                self.close_connection = True
                while not fixture.stop.is_set():
                    with fixture.condition:
                        held = fixture.hold or (
                            fixture.hold_after is not None
                            and count >= fixture.hold_after
                        )
                        if not held or fixture.permits:
                            if fixture.permits:
                                fixture.permits -= 1
                            delay = fixture.delay
                            break
                        fixture.condition.wait(timeout=1)
                    self.wfile.write(b": waiting for local fixture control\n\n")
                    self.wfile.flush()
                else:
                    return
                if fixture.stop.wait(delay):
                    return

                def send(delta: dict, finish=None) -> None:
                    value = {
                        "id": "long-task-fixture",
                        "object": "chat.completion.chunk",
                        "model": MODEL,
                        "choices": [
                            {"index": 0, "delta": delta, "finish_reason": finish}
                        ],
                    }
                    self.wfile.write(b"data: " + json.dumps(value).encode() + b"\n\n")
                    self.wfile.flush()

                if phase == "read":
                    send(
                        {
                            "role": "assistant",
                            "content": f"Reading the next {BATCH} local notes ({count + 1}-{min(count + BATCH, FILES)}).\n\n",
                        }
                    )
                    for offset, index in enumerate(
                        range(count, min(count + BATCH, FILES))
                    ):
                        arguments = json.dumps({"path": f"inputs/note-{index:04d}.txt"})
                        send(
                            {
                                "tool_calls": [
                                    {
                                        "index": offset,
                                        "id": f"fixture_read_{index:04d}",
                                        "type": "function",
                                        "function": {
                                            "name": "read_file",
                                            "arguments": arguments,
                                        },
                                    }
                                ]
                            }
                        )
                    send({}, "tool_calls")
                elif phase == "write":
                    send(
                        {
                            "role": "assistant",
                            "content": "All input reads finished. The final report now requires your approval.\n\n",
                        }
                    )
                    send(
                        {
                            "tool_calls": [
                                {
                                    "index": 0,
                                    "id": "fixture_write_report",
                                    "type": "function",
                                    "function": {
                                        "name": "write_file",
                                        "arguments": json.dumps(
                                            {"path": "report.txt", "content": RESULT}
                                        ),
                                    },
                                }
                            ]
                        }
                    )
                    send({}, "tool_calls")
                else:
                    send({"role": "assistant", "content": RESULT})
                    send({}, "stop")
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()
                with fixture.condition:
                    fixture.pending = None

        self.server = QuietServer(("127.0.0.1", 0), Handler)
        self.address = "127.0.0.1:" + str(self.server.server_port)
        self.origin = "http://" + self.address

    def request(self, path: str, data=None, method=None):
        payload = json.dumps(data).encode() if data is not None else None
        request = urllib.request.Request(
            self.endpoint + path,
            data=payload,
            method=method,
            headers={
                "Authorization": "Bearer " + self.token,
                "Content-Type": "application/json",
                "Accept-Language": "en",
            },
        )
        with self.opener.open(request, timeout=30) as response:
            raw = response.read()
            return json.loads(raw) if raw else None

    def start(self) -> dict:
        # Add a stricter fixture-only write rule without removing existing protections.
        rules = json.loads(
            (REPOSITORY / "internal/permission/defaults/rules.json").read_text()
        )
        rules["rules"].append(
            {
                "id": "fixture-write-approval",
                "tool": "write_file",
                "effect": "ask",
                "origin": "user",
            }
        )
        security = self.home / "security"
        security.mkdir(mode=0o700)
        rule_path = security / "rules.json"
        rule_path.write_text(json.dumps(rules))
        os.chmod(rule_path, 0o600)
        uv = shutil.which("uv")
        if not uv:
            raise RuntimeError("uv is required to run the real Python worker")
        worker_command = " ".join(
            json.dumps(part)
            for part in [
                uv,
                "run",
                "--project",
                str(self.args.worker_root),
                "gogosling-worker",
            ]
        )
        environment = {
            **os.environ,
            "GOGOSLING_HOME": str(self.home),
            "GOGOSLING_SECRET_BACKEND": "file",
            "GOGOSLING_DEV": "1",
            "GOGOSLING_RESOURCES": str(self.args.worker_root.parent),
            "GOGOSLING_WORKER_CMD": worker_command,
            "GOGOSLING_ALLOWED_ORIGINS": self.origin,
        }
        log_path = self.home / "process.log"
        self.log = log_path.open("wb")
        os.chmod(log_path, 0o600)
        self.process = subprocess.Popen(
            [str(self.args.daemon), "-home", str(self.home), "-listen", "127.0.0.1:0"],
            cwd=self.args.worker_root.parent,
            env=environment,
            stdout=self.log,
            stderr=self.log,
            stdin=subprocess.DEVNULL,
            start_new_session=True,
        )
        threading.Thread(
            target=self.server.serve_forever, name="long-task-http", daemon=True
        ).start()
        for _ in range(300):
            if self.process.poll() is not None:
                raise RuntimeError("The isolated daemon exited during startup")
            try:
                handle = json.loads((self.home / "daemon.json").read_text())
                self.endpoint, self.token = handle["endpoint"], handle["token"]
                if urllib.parse.urlsplit(self.endpoint).hostname != "127.0.0.1":
                    raise RuntimeError("The daemon did not bind loopback")
                break
            except (FileNotFoundError, json.JSONDecodeError):
                time.sleep(0.1)
        else:
            raise RuntimeError("The daemon did not become ready")
        for _ in range(600):
            status = self.request("/api/v1/version")
            if status.get("worker", {}).get("status") == "ready":
                break
            if self.stop.wait(0.1):
                raise RuntimeError("Fixture was stopped during startup")
        else:
            raise RuntimeError("The real Python worker did not become ready")
        provider = self.request(
            "/api/v1/providers",
            {
                "name": "Local repeatable workload",
                "kind": "ollama",
                "base_url": self.origin + "/v1",
                "default_model": MODEL,
                "enabled": True,
            },
        )
        self.agent = self.request(
            "/api/v1/agents",
            {
                "name": "Long task verification",
                "model": {"provider_id": provider["id"], "model": MODEL},
                "autonomy": "ask_risky",
                "tools": ["read_file", "write_file"],
            },
        )
        workspace = self.home / "agents" / self.agent["id"]
        inputs = workspace / "inputs"
        inputs.mkdir(mode=0o700, parents=True, exist_ok=True)
        for index in range(FILES):
            header = f"Synthetic local note {index:04d}.\n"
            content = (
                header
                + "This fixture contains only generated public test content.\n" * 12
            )[:511] + "\n"
            path = inputs / f"note-{index:04d}.txt"
            path.write_text(content)
            os.chmod(path, 0o600)
        self.sessions = [
            self.request(
                "/api/v1/sessions",
                {"agent_id": self.agent["id"], "title": title, "origin": "desktop"},
            )
            for title in (
                "Long task: 600 local files",
                "Draft recovery A",
                "Draft recovery B",
            )
        ]
        threading.Thread(target=self.sample, name="long-task-rss", daemon=True).start()
        return {
            "label": self.args.label,
            "url": self.origin + "/#/chat/" + self.sessions[0]["id"],
            "origin": self.origin,
            "sessions": [
                {"id": session["id"], "title": session["title"]}
                for session in self.sessions
            ],
            "agent_id": self.agent["id"],
            "daemon_pid": self.process.pid,
            "fixture_pid": os.getpid(),
            "files": FILES,
            "file_bytes": 512,
            "batch_size": BATCH,
            "expected_model_steps": 32,
            "state_url": self.origin + "/__fixture/state",
            "control_url": self.origin + "/__fixture/control",
            "hold": self.hold,
            "hold_after_reads": self.hold_after,
            "delay_s": self.delay,
            "upload_delay_s": self.upload_delay,
        }

    def sample(self) -> None:
        while not self.stop.is_set():
            try:
                output = subprocess.run(
                    ["ps", "-axo", "pid=,ppid=,rss="],
                    capture_output=True,
                    text=True,
                    check=True,
                    timeout=2,
                ).stdout
                processes = {}
                for line in output.splitlines():
                    columns = line.split()
                    if len(columns) == 3:
                        pid, parent, rss = map(int, columns)
                        processes[pid] = (parent, rss)
                owned = {self.process.pid}
                while True:
                    children = {
                        pid for pid, (parent, _) in processes.items() if parent in owned
                    }
                    if children <= owned:
                        break
                    owned |= children
                rows = [
                    {"pid": pid, "rss_kib": processes[pid][1]}
                    for pid in sorted(owned)
                    if pid in processes
                ]
                with self.condition:
                    self.samples.append(
                        {
                            "elapsed_s": round(time.monotonic() - self.started, 3),
                            "processes": rows,
                            "rss_sum_mib": round(
                                sum(row["rss_kib"] for row in rows) / 1024, 3
                            ),
                        }
                    )
            except (OSError, ValueError, subprocess.SubprocessError):
                pass
            self.stop.wait(1)

    def snapshot(self) -> dict:
        with self.condition:
            result = {
                "label": self.args.label,
                "hold": self.hold,
                "hold_after_reads": self.hold_after,
                "delay_s": self.delay,
                "upload_delay_s": self.upload_delay,
                "upload_waits": self.upload_waits,
                "upload_cancellations": self.upload_cancellations,
                "pending": self.pending,
                "requests": list(self.requests),
                "latest_rss": self.samples[-1] if self.samples else None,
            }
        if self.sessions:
            with self.condition:
                run_ids = list(self.run_ids)
            runs = [self.request("/api/v1/runs/" + run_id) for run_id in run_ids]
            result["runs"] = [
                {
                    key: run.get(key)
                    for key in ("id", "status", "error", "started_at", "ended_at")
                }
                for run in runs
            ]
            approvals = self.request("/api/v1/approvals")
            result["pending_approval_ids"] = [
                item["id"] for item in approvals.get("items", [])
            ]
            report = self.home / "agents" / self.agent["id"] / "report.txt"
            result["report_exists"] = report.exists()
            result["report_matches"] = (
                report.is_file()
                and hashlib.sha256(report.read_bytes()).digest()
                == hashlib.sha256(RESULT.encode()).digest()
            )
        return result

    def close(self) -> None:
        self.stop.set()
        with self.condition:
            self.condition.notify_all()
        if self.process is not None and self.process.poll() is None:
            self.process.send_signal(signal.SIGINT)
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGTERM)
                self.process.wait(timeout=5)
        if self.endpoint:
            self.server.shutdown()
        self.server.server_close()
        if self.log is not None:
            self.log.close()
        if self.args.output:
            with self.condition:
                report = {
                    "label": self.args.label,
                    "method": "Real daemon and Python worker; local scripted model; operator-controlled approvals. RSS sums exclude the browser and may count shared pages more than once.",
                    "requests": list(self.requests),
                    "rss_samples": list(self.samples),
                    "marks": list(self.marks),
                }
            self.args.output.write_text(json.dumps(report, indent=2) + "\n")
        self.token = ""
        self.temporary.cleanup()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--daemon", type=Path, required=True)
    parser.add_argument("--web-root", type=Path, required=True)
    parser.add_argument("--worker-root", type=Path, required=True)
    parser.add_argument("--temp-parent", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--label", default="local")
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--hold", action="store_true")
    parser.add_argument("--hold-after-reads", type=int)
    arguments = parser.parse_args()
    for field in ("daemon", "web_root", "worker_root"):
        setattr(arguments, field, getattr(arguments, field).resolve())
    if (
        not arguments.daemon.is_file()
        or not (arguments.web_root / "index.html").is_file()
        or not (arguments.worker_root / "pyproject.toml").is_file()
    ):
        parser.error("The daemon, built web entry, and real worker project must exist")
    fixture = Fixture(arguments)
    for event in (signal.SIGINT, signal.SIGTERM):
        signal.signal(event, lambda *_: fixture.stop.set())
    try:
        print(json.dumps(fixture.start()), flush=True)
        fixture.stop.wait()
    finally:
        fixture.close()


if __name__ == "__main__":
    main()
