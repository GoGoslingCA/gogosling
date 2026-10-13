// Synthetic parser benchmark; no network, model, credentials, or user files.
// node --expose-gc tests/benchmarks/stream-parser.mjs [--baseline /tmp/baseline-sse.ts]
import { readFile } from "node:fs/promises";
import { performance } from "node:perf_hooks";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import * as nodeModule from "node:module";

if (typeof nodeModule.stripTypeScriptTypes !== "function") throw new Error("This benchmark requires a Node.js version with stripTypeScriptTypes support; use the current Node 22 LTS.");

const current = fileURLToPath(new URL("../../apps/web/src/api/sse.ts", import.meta.url));
const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--baseline")) throw new Error("Expected --baseline <source.ts>");
const files = [...(args.length ? [["before", args[1]]] : []), ["after", current]];
const parsers = await Promise.all(files.map(async ([label, path]) => {
  const source = await readFile(path, "utf8");
  const outputText = nodeModule.stripTypeScriptTypes(source, { mode: "transform" });
  const { SseParser } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
  return { label, sourceSha256: createHash("sha256").update(source).digest("hex"), SseParser };
}));
const chunksOf = (value, size) => Array.from({ length: Math.ceil(value.length / size) }, (_, index) => value.slice(index * size, (index + 1) * size));
const records = Array.from({ length: 10_000 }, (_, index) => `id: ${index + 1}\nevent: text.delta\ndata: {"delta":"${"x".repeat(80)}"}\n\n`).join("");
const longLine = `data: ${"中文🙂".repeat(65_536)}\n\n`;
const scenarios = [
  { name: "lf_replay_burst", chunks: [records], events: 10_000 },
  { name: "cr_replay_burst", chunks: [records.replaceAll("\n", "\r")], events: 10_000 },
  { name: "replay_64k_chunks", chunks: chunksOf(records, 65_536), events: 10_000 },
  { name: "small_event_chunks", chunks: records.split(/(?<=\n\n)/), events: 10_000 },
  { name: "long_line_128_character_chunks", chunks: chunksOf(longLine, 128), events: 1 },
];
const percentile = (values, p) => [...values].sort((a, b) => a - b)[Math.ceil(values.length * p) - 1];
const output = {
  runtime: process.version, platform: process.platform, arch: process.arch, sampleCount: 10, warmupCount: 2,
  sources: parsers.map(({ label, sourceSha256 }) => ({ label, sourceSha256 })),
  notes: [
    "Synthetic parser-only execution; not a whole-application, network, rendering, or energy benchmark.",
    "P95 uses nearest rank; with 10 samples it is the observed maximum.",
    "Retained heap is the heapUsed change after parser.end() and forced GC, not peak or whole-app memory; small negative values may reflect runtime GC noise.",
  ],
  scenarios: [],
};
for (const scenario of scenarios) {
  const rows = [];
  let expectedHash;
  for (const { label, SseParser } of parsers) {
    const digest = createHash("sha256");
    const check = new SseParser(message => digest.update(JSON.stringify(message)));
    for (const chunk of scenario.chunks) check.feed(chunk);
    check.end();
    const hash = digest.digest("hex");
    expectedHash ??= hash;
    if (hash !== expectedHash) throw new Error(`Parser outputs differ: ${scenario.name}`);
    const elapsed = [], cpu = [], retainedHeap = [], samples = [];
    for (let sample = -2; sample < 10; sample++) {
      global.gc?.();
      const heapBefore = process.memoryUsage().heapUsed;
      let count = 0;
      const parser = new SseParser(() => count++);
      const startedCpu = process.cpuUsage();
      const started = performance.now();
      for (const chunk of scenario.chunks) parser.feed(chunk);
      parser.end();
      const wallMs = performance.now() - started;
      const cpuUsed = process.cpuUsage(startedCpu);
      if (count !== scenario.events) throw new Error(`Event count differs: ${scenario.name}`);
      global.gc?.();
      if (sample >= 0) {
        const cpuMs = (cpuUsed.user + cpuUsed.system) / 1000;
        const retainedHeapBytes = process.memoryUsage().heapUsed - heapBefore;
        elapsed.push(wallMs); cpu.push(cpuMs); retainedHeap.push(retainedHeapBytes);
        samples.push({ elapsedMs: wallMs, cpuMs, ...(global.gc ? { retainedHeapBytesAfterEndAndGC: retainedHeapBytes } : {}) });
      }
    }
    rows.push({ label, elapsedMedianMs: percentile(elapsed, 0.5), elapsedP95Ms: percentile(elapsed, 0.95), cpuMedianMs: percentile(cpu, 0.5), ...(global.gc ? { retainedHeapMedianBytesAfterEndAndGC: percentile(retainedHeap, 0.5) } : {}), samples });
  }
  output.scenarios.push({ name: scenario.name, characters: scenario.chunks.reduce((sum, value) => sum + value.length, 0), chunks: scenario.chunks.length, events: scenario.events, equalOutputSha256: expectedHash, results: rows });
}
console.log(JSON.stringify(output, null, 2));
