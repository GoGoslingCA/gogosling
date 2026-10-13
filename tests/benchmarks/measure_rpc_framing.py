"""Measure RPC framing with synthetic bytes; no model, network, or user data.

Run with Python 3.12 and --source pointing to a trusted local rpc.py revision.
Timing and tracemalloc peaks are collected separately; inputs exist before tracing.
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import math
import platform
import statistics
import sys
import time
import tracemalloc
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--label", default="candidate")
    parser.add_argument("--samples", type=int, default=9)
    args = parser.parse_args()
    if args.samples < 1:
        parser.error("samples must be positive")
    spec = importlib.util.spec_from_file_location("rpc_framing_benchmark", args.source)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    cases = [
        ("small-lines-64k-chunks", b'{"id":1}\n' * 100_000, 65536, 16 * 1024 * 1024),
        (
            "8m-line-64k-chunks",
            b"x" * (8 * 1024 * 1024) + b"\n",
            65536,
            16 * 1024 * 1024,
        ),
        ("1m-line-128b-chunks", b"x" * (1024 * 1024) + b"\n", 128, 16 * 1024 * 1024),
        ("discard-8m-line", b"x" * (8 * 1024 * 1024) + b"\n{}\n", 65536, 1024),
    ]
    results = []
    for name, data, chunk_size, limit in cases:
        chunks = [data[i : i + chunk_size] for i in range(0, len(data), chunk_size)]

        def run(
            chunks: list[bytes] = chunks, limit: int = limit
        ) -> tuple[int, int, int]:
            splitter = module.LineSplitter(max_line=limit)
            count = total = discarded = 0
            for chunk in chunks:
                for line in splitter.feed(chunk):
                    if isinstance(line, bytes):
                        count += 1
                        total += len(line)
                    else:
                        discarded += line.size
            assert splitter.flush() is None
            return count, total, discarded

        expected = run()
        samples = []
        for _ in range(args.samples):
            start = time.perf_counter_ns()
            actual = run()
            samples.append((time.perf_counter_ns() - start) / 1_000_000)
            assert actual == expected
        tracemalloc.start()
        assert run() == expected
        _, peak = tracemalloc.get_traced_memory()
        tracemalloc.stop()
        results.append(
            {
                "case": name,
                "bytes": len(data),
                "chunk_bytes": chunk_size,
                "samples_ms": samples,
                "median_ms": statistics.median(samples),
                "p95_ms": sorted(samples)[math.ceil(0.95 * len(samples)) - 1],
                "parser_peak_bytes": peak,
                "output_counts": expected,
            }
        )
    print(
        json.dumps(
            {
                "label": args.label,
                "python": platform.python_version(),
                "machine": platform.machine(),
                "samples_per_case": args.samples,
                "source_sha256": hashlib.sha256(args.source.read_bytes()).hexdigest(),
                "cases": results,
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()
