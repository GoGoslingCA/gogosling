"""Compare synthetic provider message assembly; no network or private data.

uv run --project worker python tests/benchmarks/measure_message_assembly.py \
    --baseline /tmp/messages-before.py
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import platform
import statistics
import sys
import time
import tracemalloc
from pathlib import Path

from gogosling_worker.protocol import Message, Part, ToolCall, ToolResult


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    args = parser.parse_args()
    current = (
        Path(__file__).resolve().parents[2]
        / "worker/src/gogosling_worker/providers/messages.py"
    )
    versions = []
    for label, source in [("before", args.baseline), ("after", current)]:
        spec = importlib.util.spec_from_file_location(
            f"message_benchmark_{label}", source
        )
        assert spec and spec.loader
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        versions.append(
            (label, module, hashlib.sha256(source.read_bytes()).hexdigest())
        )
    output = {
        "python": platform.python_version(),
        "machine": platform.machine(),
        "samples": 9,
        "sources": [
            {"label": label, "sha256": digest} for label, _, digest in versions
        ],
        "notes": [
            "Synthetic synchronous request-body preparation, not model latency or whole-app energy.",
            "Time is per call; peaks exclude prebuilt fixtures. Versions alternate between samples.",
            "The fragmented cases are supported stress inputs, not a claim about typical generated messages.",
        ],
        "cases": [],
    }
    for name, groups, count, text_size, iterations in [
        ("single_text_part", 1, 1, 128, 500),
        ("32_large_parts", 1, 32, 16_384, 10),
        ("4096_small_parts", 1, 4096, 128, 1),
        ("tool_boundaries", 16, 4, 256, 50),
    ]:
        parts = []
        for group in range(groups):
            parts.extend(Part.of_text("x" * text_size) for _ in range(count))
            if groups > 1:
                call_id = f"call-{group}"
                parts.extend(
                    [
                        Part.of_tool_call(ToolCall(call_id, "read", {})),
                        Part.of_tool_result(ToolResult(call_id, "synthetic result")),
                    ]
                )
        history = [
            Message("user", (Part.of_text("synthetic request"),)),
            Message("assistant", tuple(parts)),
        ]
        outputs = [
            json.dumps(
                [
                    m.to_openai_messages(history, locale="en"),
                    m.to_anthropic_request(history, locale="en"),
                ],
                sort_keys=True,
            )
            for _, m, _ in versions
        ]
        assert outputs[0] == outputs[1]
        timings: dict[str, list[float]] = {label: [] for label, _, _ in versions}
        for sample in range(-2, 9):
            for label, module, _ in versions[:: 1 if sample % 2 == 0 else -1]:
                start = time.perf_counter_ns()
                for _ in range(iterations):
                    module.to_openai_messages(history, locale="en")
                if sample >= 0:
                    timings[label].append(
                        (time.perf_counter_ns() - start) / 1_000_000 / iterations
                    )
        rows = []
        for label, module, _ in versions:
            tracemalloc.start()
            module.to_openai_messages(history, locale="en")
            _, peak = tracemalloc.get_traced_memory()
            tracemalloc.stop()
            rows.append(
                {
                    "label": label,
                    "median_ms_per_call": statistics.median(timings[label]),
                    "samples_ms_per_call": timings[label],
                    "peak_allocated_bytes": peak,
                }
            )
        output["cases"].append(
            {
                "case": name,
                "text_parts": groups * count,
                "text_characters": groups * count * text_size,
                "iterations_per_sample": iterations,
                "equal_output_sha256": hashlib.sha256(outputs[0].encode()).hexdigest(),
                "results": rows,
            }
        )
    print(json.dumps(output, indent=2))


if __name__ == "__main__":
    main()
