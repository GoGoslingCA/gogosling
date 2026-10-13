// Synthetic reducer and summary benchmark. No network, credentials, or user files.
// node --expose-gc tests/benchmarks/tool-history.mjs --baseline /tmp/liveRuns-before.ts
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";
import { stripTypeScriptTypes } from "node:module";

const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--baseline")) throw new Error("Expected --baseline <liveRuns.ts>");
const asModule = source => `data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source, { mode: "transform" })).toString("base64")}`;
const types = asModule(await readFile(new URL("../../apps/web/src/api/types.ts", import.meta.url), "utf8"));
const current = fileURLToPath(new URL("../../apps/web/src/state/liveRuns.ts", import.meta.url));
const files = [...(args.length ? [["before", args[1]]] : []), ["after", current]];
const reducers = await Promise.all(files.map(async ([label, file]) => {
  const source = await readFile(file, "utf8");
  return { label, sourceSha256: createHash("sha256").update(source).digest("hex"), ...await import(asModule(source.replaceAll('"../api/types"', JSON.stringify(types)))) };
}));
const percentile = (values, p) => [...values].sort((a, b) => a - b)[Math.ceil(values.length * p) - 1];
const output = { runtime: process.version, platform: process.platform, arch: process.arch, sampleCount: 10, warmupCount: 2,
  sources: reducers.map(({ label, sourceSha256 }) => ({ label, sourceSha256 })),
  notes: ["Synthetic production reducer measurements, not browser rendering, network latency, or whole-app memory.",
    "Immutable array copies remain O(n); the index removes repeated ID scans and update callbacks without dropping entries.",
    "All final snapshots must match the baseline SHA256, including the approval and terminal state.",
    "P95 uses nearest rank; with 10 samples it is the observed maximum."], scenarios: [] };
for (const callCount of [100, 1000, 5000]) {
  let seq = 0;
  const at = "2026-10-13T00:00:00Z";
  const events = [];
  const event = (type, extra = {}) => ({ seq: ++seq, type, at, payload: { type, run_id: "run", session_id: "session", agent_id: "synthetic", at, ...extra } });
  for (let index = 0; index < callCount; index++) {
    events.push(event("tool.call.started", { tool_call: { id: `tool-${index}`, name: "read_file", arguments: { path: `fixture-${index}.md` } } }));
    if (index === Math.floor(callCount / 2)) {
      events.push(event("approval.requested"));
      events.push(event("approval.resolved"));
    }
    events.push(event("tool.call.completed", { tool_result: { call_id: `tool-${index}`, content: `Synthetic result ${index}` } }));
  }
  events.push(event("run.completed"));
  const results = []; let expectedHash;
  for (const { label, initialLiveRuns, liveRunsReducer } of reducers) {
    const samples = [];
    for (let sample = -2; sample < 10; sample++) {
      let state = initialLiveRuns; global.gc?.();
      const cpuStart = process.cpuUsage(), start = performance.now();
      for (const item of events) state = liveRunsReducer(state, { type: "event", event: item });
      const elapsedMs = performance.now() - start, cpu = process.cpuUsage(cpuStart);
      const hash = createHash("sha256").update(JSON.stringify(state)).digest("hex");
      expectedHash ??= hash;
      if (hash !== expectedHash || state.runs.run.toolCalls.length !== callCount || state.runs.run.status !== "completed") throw new Error("Tool history or completion differs.");
      if (sample >= 0) samples.push({ elapsedMs, cpuMs: (cpu.user + cpu.system) / 1000 });
      state = liveRunsReducer(state, { type: "reset" });
    }
    results.push({ label, elapsedMedianMs: percentile(samples.map(sample => sample.elapsedMs), .5), elapsedP95Ms: percentile(samples.map(sample => sample.elapsedMs), .95), samples });
  }
  output.scenarios.push({ callCount, eventCount: events.length, equalOutputSha256: expectedHash, results });
}
console.log(JSON.stringify(output, null, 2));
