#!/usr/bin/env python3
"""Measure a real local daemon's preview API with isolated synthetic files.

This measures authenticated HTTP reads and parser-slot release, not UI paint,
energy use, or end-to-end task performance. It never calls a model service.
Python's standard library is sufficient; RSS sampling requires ps on macOS/Linux.
"""

from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import math
import os
import platform
import statistics
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from preview_fixtures import write_fixtures

REPOSITORY = Path(__file__).resolve().parents[2]


def summary(values: list[float]) -> dict[str, float]:
    ordered = sorted(values)
    return {
        "p50_ms": round(statistics.median(values), 3),
        "p95_ms": round(ordered[math.ceil(len(values) * 0.95) - 1], 3),
        "max_ms": round(max(values), 3),
    }


def measure(daemon: Path, samples: int, cancellations: int, idle: float, label: str) -> dict:
    with tempfile.TemporaryDirectory(prefix="gogosling-preview-benchmark-") as temporary:
        home = Path(temporary)
        os.chmod(home, 0o700)
        environment = {
            **os.environ,
            "GOGOSLING_HOME": str(home),
            "GOGOSLING_SECRET_BACKEND": "file",
            "GOGOSLING_DEV": "1",
            "GOGOSLING_RESOURCES": str(REPOSITORY),
            "GOGOSLING_ALLOWED_ORIGINS": "",
        }
        with (home / "process.log").open("wb") as log:
            os.chmod(home / "process.log", 0o600)
            process = subprocess.Popen(
                [str(daemon), "-home", str(home), "-listen", "127.0.0.1:0"],
                cwd=REPOSITORY,
                env=environment,
                stdout=log,
                stderr=log,
                stdin=subprocess.DEVNULL,
            )
            stopped = threading.Event()
            watcher: threading.Thread | None = None
            try:
                handle = None
                for _ in range(150):
                    if process.poll() is not None:
                        raise RuntimeError("The benchmark daemon exited before becoming ready")
                    try:
                        handle = json.loads((home / "daemon.json").read_text(encoding="utf-8"))
                        break
                    except (FileNotFoundError, json.JSONDecodeError):
                        time.sleep(0.1)
                if handle is None:
                    raise RuntimeError("The benchmark daemon did not become ready in 15 seconds")
                endpoint = handle["endpoint"]
                headers = {
                    "Authorization": "Bearer " + handle["token"],
                    "Content-Type": "application/json",
                }
                address = urllib.parse.urlsplit(endpoint)
                if address.scheme != "http" or address.hostname != "127.0.0.1":
                    raise RuntimeError(
                        "The benchmark daemon did not bind the requested loopback address"
                    )
                # Local requests must not be forwarded to an environment-configured proxy.
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

                def request(path: str, data: dict | None = None) -> bytes:
                    body = json.dumps(data).encode() if data is not None else None
                    request = urllib.request.Request(endpoint + path, data=body, headers=headers)
                    with opener.open(request, timeout=20) as response:
                        return response.read()

                def rss() -> float | None:
                    if platform.system() not in ("Darwin", "Linux"):
                        return None
                    try:
                        result = subprocess.run(
                            ["ps", "-o", "rss=", "-p", str(process.pid)],
                            capture_output=True,
                            text=True,
                            check=True,
                            timeout=1,
                        )
                        return int(result.stdout.strip()) / 1024
                    except (OSError, ValueError, subprocess.SubprocessError):
                        return None

                provider = json.loads(
                    request(
                        "/api/v1/providers",
                        {
                            "name": "Local preview measurement",
                            "kind": "ollama",
                            "base_url": "http://127.0.0.1:1",
                            "default_model": "fixture",
                            "enabled": True,
                        },
                    )
                )
                agent = json.loads(
                    request(
                        "/api/v1/agents",
                        {
                            "name": "Preview measurements",
                            "tools": [],
                            "model": {"provider_id": provider["id"], "model": "fixture"},
                        },
                    )
                )
                fixtures = write_fixtures(home / "agents" / agent["id"])

                def route(name: str) -> str:
                    return f"/api/v1/agents/{agent['id']}/file-preview?path=" + urllib.parse.quote(
                        name
                    )

                baseline_rss = rss()
                peak_rss = [baseline_rss]

                def monitor() -> None:
                    while not stopped.is_set():
                        current = rss()
                        if current is not None:
                            peak_rss[0] = max(peak_rss[0] or 0, current)
                        stopped.wait(0.02)

                watcher = threading.Thread(target=monitor, name="preview-rss", daemon=True)
                watcher.start()
                files = []
                for fixture in fixtures:
                    elapsed = []
                    response_bytes = 0
                    for _ in range(samples):
                        started = time.perf_counter()
                        body = request(route(str(fixture["name"])))
                        elapsed.append((time.perf_counter() - started) * 1000)
                        response_bytes = len(body)
                    files.append(
                        {
                            **fixture,
                            "response_bytes": response_bytes,
                            **summary(elapsed),
                            "duration_ms": [round(value, 3) for value in elapsed],
                            "rss_after_mib": rss(),
                        }
                    )
                cancelled = []
                for _ in range(cancellations):
                    connection = http.client.HTTPConnection(
                        address.hostname, address.port, timeout=20
                    )
                    connection.request("GET", route("document-large.docx"), headers=headers)
                    time.sleep(0.003)
                    started = time.perf_counter()
                    connection.close()
                    attempts = 0
                    while True:
                        attempts += 1
                        try:
                            request(route("notes-24k.md"))
                            break
                        except urllib.error.HTTPError as error:
                            if error.code != 503 or time.perf_counter() - started > 5:
                                raise RuntimeError(
                                    "The next preview did not recover after cancellation"
                                ) from None
                            time.sleep(0.001)
                    cancelled.append(
                        {
                            "release_ms": round((time.perf_counter() - started) * 1000, 3),
                            "probe_attempts": attempts,
                        }
                    )
                after_rss = rss()
                time.sleep(idle)
                idle_rss = rss()
                stopped.set()
                watcher.join(timeout=2)
                return {
                    "schema_version": 1,
                    "label": label,
                    "daemon_sha256": hashlib.sha256(daemon.read_bytes()).hexdigest(),
                    "platform": platform.system(),
                    "architecture": platform.machine(),
                    "samples_per_file": samples,
                    "percentile_method": "nearest-rank",
                    "scope": (
                        "Local authenticated preview API; synthetic files; no UI or model requests"
                    ),
                    "rss_sample_interval_ms": 20,
                    "idle_seconds": idle,
                    "rss_note": (
                        "Process RSS includes reusable runtime capacity; "
                        "no forced GC; null means unavailable"
                    ),
                    "rss_idle_mib": baseline_rss,
                    "rss_peak_sampled_mib": peak_rss[0],
                    "rss_after_work_mib": after_rss,
                    "rss_after_idle_mib": idle_rss,
                    "files": files,
                    "cancel_large_docx": {
                        **summary([value["release_ms"] for value in cancelled]),
                        "samples": cancelled,
                        "scope": (
                            "Close client socket 3 ms after request, then probe the parser slot "
                            "with a Markdown read"
                        ),
                    },
                }
            finally:
                stopped.set()
                if watcher is not None:
                    watcher.join(timeout=2)
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--daemon", type=Path, required=True, help="Previously built gogoslingd binary"
    )
    parser.add_argument(
        "--output",
        type=Path,
        required=True,
        help="Destination JSON; contains no token or local path",
    )
    parser.add_argument(
        "--label",
        default="local",
        help="Build label recorded verbatim; do not include private data",
    )
    parser.add_argument("--samples", type=int, default=25)
    parser.add_argument("--cancel-samples", type=int, default=20)
    parser.add_argument("--idle-seconds", type=float, default=3)
    args = parser.parse_args()
    if args.samples < 1 or args.cancel_samples < 1 or not 0 <= args.idle_seconds <= 300:
        parser.error("Sample counts must be positive; idle time must be between 0 and 300 seconds")
    daemon = args.daemon.expanduser().resolve()
    if not daemon.is_file():
        parser.error("The daemon binary does not exist")
    report = measure(daemon, args.samples, args.cancel_samples, args.idle_seconds, args.label)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(
        json.dumps(
            {"label": args.label, "files": len(report["files"]), "samples_per_file": args.samples}
        )
    )


if __name__ == "__main__":
    main()
