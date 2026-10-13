// Synthetic live-state benchmark; no network, model, credentials, or user files.
// node --expose-gc tests/benchmarks/live-run-store.mjs [--baseline /tmp/liveRunStore.ts]
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";
import * as nodeModule from "node:module";

if (typeof nodeModule.stripTypeScriptTypes !== "function") throw new Error("This benchmark requires stripTypeScriptTypes support; use current Node 22 LTS.");
const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--baseline")) throw new Error("Expected --baseline <source.ts>");
const current = fileURLToPath(new URL("../../apps/web/src/state/liveRunStore.ts", import.meta.url));
const files = [...(args.length ? [["before", args[1]]] : []), ["after", current]];
const moduleUrl = source => `data:text/javascript;base64,${Buffer.from(nodeModule.stripTypeScriptTypes(source, { mode: "transform" })).toString("base64")}`;
const typesSource = await readFile(new URL("../../apps/web/src/api/types.ts", import.meta.url), "utf8");
const reducerSource = await readFile(new URL("../../apps/web/src/state/liveRuns.ts", import.meta.url), "utf8");
const types = moduleUrl(typesSource);
const reducer = moduleUrl(reducerSource.replaceAll('"../api/types"', JSON.stringify(types)));
const stores = await Promise.all(files.map(async ([label, path]) => {
  const source = await readFile(path, "utf8");
  const { LiveRunStore } = await import(moduleUrl(source.replaceAll('"../api/types"', JSON.stringify(types)).replaceAll('"./liveRuns"', JSON.stringify(reducer))));
  return { label, sourceSha256: createHash("sha256").update(source).digest("hex"), LiveRunStore };
}));
const percentile = (values, p) => [...values].sort((a, b) => a - b)[Math.ceil(values.length * p) - 1];
const output = {
  runtime: process.version, platform: process.platform, arch: process.arch, sampleCount: 10, warmupCount: 2,
  sources: stores.map(({ label, sourceSha256 }) => ({ label, sourceSha256 })),
  reducerSha256: createHash("sha256").update(reducerSource).digest("hex"),
  notes: [
    "Synthetic store-only execution; not network, browser painting, whole-app memory, or energy measurement.",
    "One foreground subscriber; background fixtures represent retained unsaved terminal runs, not concurrent inference workers.",
    "Timing covers 200 flushes and their delta ingestion, not fixture seeding or snapshot hashing.",
    "P95 uses nearest rank; with 10 samples it is the observed maximum.",
  ],
  scenarios: [],
};
for (const [backgroundSessions, activeSessions] of [[0, 1], [100, 4], [1000, 4]]) {
  let seq = 0;
  const event = (type, session, extra = {}) => ({ seq: ++seq, type, at: "2026-10-13T00:00:00Z", payload: { type, session_id: session, run_id: session, agent_id: "synthetic", at: "2026-10-13T00:00:00Z", ...extra } });
  const seeds = [];
  for (let index = 0; index < backgroundSessions; index++) {
    seeds.push(event("text.delta", `background-${index}`, { delta: "Retained unsaved draft." }));
    seeds.push(event("run.failed", `background-${index}`, { error: "Synthetic failure." }));
  }
  for (let index = 0; index < activeSessions; index++) seeds.push(event("run.started", `active-${index}`));
  const frames = Array.from({ length: 200 }, () => Array.from({ length: 8 * activeSessions }, (_, index) => event("text.delta", `active-${index % activeSessions}`, { delta: "Text 中文🙂" })));
  let expectedHash;
  const rows = [];
  for (const { label, LiveRunStore } of stores) {
    const samples = [];
    for (let sample = -2; sample < 10; sample++) {
      const store = new LiveRunStore();
      let notifications = 0;
      const unsubscribe = store.subscribe("active-0", () => notifications++);
      for (const seed of seeds) store.receive(seed);
      notifications = 0;
      global.gc?.();
      const cpuStart = process.cpuUsage();
      const started = performance.now();
      for (const frame of frames) {
        for (const delta of frame) store.receive(delta);
        store.flush();
      }
      const elapsedMs = performance.now() - started;
      const cpu = process.cpuUsage(cpuStart);
      const digest = createHash("sha256");
      for (let index = 0; index < backgroundSessions; index++) digest.update(JSON.stringify(store.snapshot(`background-${index}`)));
      for (let index = 0; index < activeSessions; index++) digest.update(JSON.stringify(store.snapshot(`active-${index}`)));
      const hash = digest.digest("hex");
      expectedHash ??= hash;
      if (hash !== expectedHash || notifications !== 200) throw new Error("Store output or notifications differ.");
      if (sample >= 0) samples.push({ elapsedMs, cpuMs: (cpu.user + cpu.system) / 1000, notifications });
      unsubscribe(); store.dispatch({ type: "reset" });
      if (store.snapshot("active-0").runs.length) throw new Error("Reset retained live state.");
    }
    rows.push({ label, elapsedMedianMs: percentile(samples.map(sample => sample.elapsedMs), 0.5), elapsedP95Ms: percentile(samples.map(sample => sample.elapsedMs), 0.95), cpuMedianMs: percentile(samples.map(sample => sample.cpuMs), 0.5), samples });
  }
  output.scenarios.push({ backgroundSessions, activeSessions, flushes: frames.length, deltaEvents: frames.flat().length, equalOutputSha256: expectedHash, results: rows });
}
console.log(JSON.stringify(output, null, 2));
