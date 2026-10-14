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
const listSource = await readFile(new URL("../../apps/web/src/state/persistentList.ts", import.meta.url), "utf8");
const list = asModule(listSource);
const current = fileURLToPath(new URL("../../apps/web/src/state/liveRuns.ts", import.meta.url));
const files = [...(args.length ? [["before", args[1]]] : []), ["after", current]];
const reducers = await Promise.all(files.map(async ([label, file]) => {
  const source = await readFile(file, "utf8");
  return { label, sourceSha256: createHash("sha256").update(source).digest("hex"), supportSourceSha256: source.includes('"./persistentList"') ? createHash("sha256").update(listSource).digest("hex") : undefined, ...await import(asModule(source.replaceAll('"../api/types"', JSON.stringify(types)).replaceAll('"./persistentList"', JSON.stringify(list)))) };
}));
const percentile = (values, p) => [...values].sort((a, b) => a - b)[Math.ceil(values.length * p) - 1];
const output = { runtime: process.version, platform: process.platform, arch: process.arch, sampleCount: 10, warmupCount: 2,
  sources: reducers.map(({ label, sourceSha256, supportSourceSha256 }) => ({ label, sourceSha256, supportSourceSha256 })),
  notes: ["Synthetic production reducer measurements, not browser rendering, network latency, or whole-app memory.",
    "The current implementation copies paths of bounded 32-entry blocks for lists above 128 items; small lists use native arrays. All entries and old snapshots remain readable.",
    "All final snapshots must match the baseline SHA256, including the approval and terminal state.",
    "P95 uses nearest rank; with 10 samples it is the observed maximum."], scenarios: [] };
for (const callCount of [100, 1000, 5000, 20000]) {
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
const retentionCallCount = 5000, retainedUpdates = 400;
const at = "2026-10-14T00:00:00Z";
let retentionSeq = 0;
const retentionEvent = (type, extra) => ({ seq: ++retentionSeq, type, at, payload: { type, run_id: "run", session_id: "session", agent_id: "synthetic", at, ...extra } });
const seedEvents = [];
for (let index = 0; index < retentionCallCount; index++) {
  seedEvents.push(retentionEvent("tool.call.started", { tool_call: { id: `tool-${index}`, name: "read_file", arguments: {} } }));
  seedEvents.push(retentionEvent("tool.call.completed", { tool_result: { call_id: `tool-${index}`, content: `original-${index}` } }));
}
const changes = Array.from({ length: retainedUpdates }, (_, index) => retentionEvent("tool.call.completed", { tool_result: { call_id: `tool-${index}`, content: `replacement-${index}` } }));
const retentionResults = []; let expectedVersionHash;
for (const { label, initialLiveRuns, liveRunsReducer } of reducers) {
  const samples = [];
  for (let sample = 0; sample < 5; sample++) {
    let state = initialLiveRuns;
    for (const event of seedEvents) state = liveRunsReducer(state, { type: "event", event });
    const versions = [state];
    global.gc?.();
    const heapBefore = process.memoryUsage().heapUsed, start = performance.now();
    for (const event of changes) { state = liveRunsReducer(state, { type: "event", event }); versions.push(state); }
    const elapsedMs = performance.now() - start;
    global.gc?.();
    const retainedHeapDeltaBytes = global.gc ? process.memoryUsage().heapUsed - heapBefore : null;
    const digest = createHash("sha256");
    for (const index of [0, 1, 100, 200, 400]) digest.update(JSON.stringify(versions[index]));
    const hash = digest.digest("hex");
    expectedVersionHash ??= hash;
    if (hash !== expectedVersionHash || versions[0].runs.run.toolCalls.at(0).result.content !== "original-0" || state.runs.run.toolCalls.at(399).result.content !== "replacement-399") throw new Error("Retained snapshot differs.");
    samples.push({ elapsedMs, retainedHeapDeltaBytes });
    versions.length = 0;
  }
  retentionResults.push({ label, elapsedMedianMs: percentile(samples.map(sample => sample.elapsedMs), .5), retainedHeapDeltaMedianBytes: global.gc ? percentile(samples.map(sample => sample.retainedHeapDeltaBytes), .5) : null, samples });
}
output.retainedSnapshots = { callCount: retentionCallCount, retainedUpdates, sampleCount: 5, forcedGC: Boolean(global.gc), equalRepresentativeSnapshotsSha256: expectedVersionHash,
  notes: ["Retains the seed and 400 immutable versions after updating existing tool results. Timing excludes seed creation, hashing, and GC.", "Heap delta is sampled with explicit Node GC before and after updates. It is not browser or OS RSS, and does not predict collection timing."], results: retentionResults };
console.log(JSON.stringify(output, null, 2));
