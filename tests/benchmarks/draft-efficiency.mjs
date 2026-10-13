// Synthetic in-memory draft benchmark. No network, credentials, or user content.
// node --expose-gc tests/benchmarks/draft-efficiency.mjs [--baseline /tmp/draft-sources]
// The baseline directory must contain draftController.ts and DraftProvider.tsx.
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { performance } from "node:perf_hooks";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { stripTypeScriptTypes } from "node:module";

const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--baseline")) throw new Error("Expected --baseline <directory>");
const root = fileURLToPath(new URL("../../apps/web/src/state/", import.meta.url));
const sources = [...(args.length ? [["before", args[1]]] : []), ["after", root]];
const asModule = (source) => `data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source, { mode: "transform" })).toString("base64")}`;
const client = asModule(await readFile(new URL("../../apps/web/src/api/client.ts", import.meta.url), "utf8"));
const versions = await Promise.all(sources.map(async ([label, directory]) => {
  const controller = await readFile(join(directory, "draftController.ts"), "utf8");
  const provider = await readFile(join(directory, "DraftProvider.tsx"), "utf8");
  const controllerModule = asModule(controller.replace('from "../api/client"', `from "${client}"`));
  // Run the real registry class, excluding the React provider component and hooks.
  const registrySource = provider.slice(provider.indexOf("export class DraftRegistry"), provider.indexOf("export const DraftContext"));
  const registryModule = asModule(`import { DraftController, draftScopeKey } from "${controllerModule}";\n${registrySource}`);
  return { label, ...(await import(controllerModule)), ...(await import(registryModule)), sourceSha256: createHash("sha256").update(controller).update(provider).digest("hex") };
}));
const scope = { kind: "composer", agent_id: "synthetic-agent", session_id: "synthetic-session" };
const percentile = (values, p) => [...values].sort((a, b) => a - b)[Math.ceil(values.length * p) - 1];
const scenarios = [
  { name: "unchanged_large_draft", textCharacters: 524_000, operations: 100, create: ({ DraftController }) => {
    const text = "x".repeat(524_000), controller = new DraftController(scope, { text });
    controller.configure(null, false, false);
    return { run: () => { for (let index = 0; index < 100; index++) controller.setContent({ text }); }, check: () => { if (controller.getSnapshot().content.text !== text) throw new Error("Draft changed"); }, close: () => controller.dispose() };
  } },
  { name: "edit_large_draft", textCharacters: 524_000, operations: 100, create: ({ DraftController }) => {
    const texts = Array.from({ length: 100 }, (_, index) => "x".repeat(524_000) + index);
    const controller = new DraftController(scope, { text: texts[0] }); controller.configure(null, false, false);
    return { run: () => { for (const text of texts) controller.setContent({ text }); }, check: () => { if (controller.getSnapshot().content.text !== texts.at(-1) || !controller.getSnapshot().dirty) throw new Error("Latest edit lost"); }, close: () => controller.dispose() };
  } },
  { name: "many_retained_draft_scopes", draftScopes: 1000, operations: 1000, create: ({ DraftRegistry }) => {
    const registry = new DraftRegistry(); registry.configure(null, false, false);
    let active;
    for (let index = 0; index < 1000; index++) { active = registry.get({ ...scope, session_id: `session-${index}` }, { text: "" }); registry.activate(active); }
    let notifications = 0; registry.subscribe(() => notifications++);
    return { run: () => { for (let index = 0; index < 1000; index++) active.setContent({ text: `edit-${index}` }); }, check: () => { if (active.getSnapshot().content.text !== "edit-999" || !registry.getSnapshot()) throw new Error("Dirty draft lost"); return { aggregateNotifications: notifications }; }, close: () => registry.dispose() };
  } },
];
const output = {
  runtime: process.version, platform: process.platform, arch: process.arch, sampleCount: 10, warmupCount: 2,
  sources: versions.map(({ label, sourceSha256 }) => ({ label, sourceSha256 })),
  notes: [
    "Synthetic controller and registry measurements only; not whole-app frame, memory, or energy measurements.",
    "The text scenarios use 524000 ASCII characters, below the default 524288-byte draft limit.",
    "The registry benchmark extracts the production class unchanged, excluding the React provider component.",
    "No network or autosave timers are used; explicit save and conflict behavior are covered separately by regression tests.",
    "P95 uses nearest rank; with 10 samples it is the observed maximum. GC runs before timing when available.",
  ], scenarios: [],
};
for (const { create, ...scenario } of scenarios) {
  const results = [];
  for (const version of versions) {
    const samples = []; let observations;
    for (let sample = -2; sample < 10; sample++) {
      const fixture = create(version); global.gc?.();
      const cpuStart = process.cpuUsage(), start = performance.now(); fixture.run();
      const elapsedMs = performance.now() - start, cpu = process.cpuUsage(cpuStart);
      observations = fixture.check(); fixture.close();
      if (sample >= 0) samples.push({ elapsedMs, cpuMs: (cpu.user + cpu.system) / 1000 });
    }
    results.push({ label: version.label, elapsedMedianMs: percentile(samples.map((sample) => sample.elapsedMs), .5), elapsedP95Ms: percentile(samples.map((sample) => sample.elapsedMs), .95), cpuMedianMs: percentile(samples.map((sample) => sample.cpuMs), .5), ...observations, samples });
  }
  output.scenarios.push({ ...scenario, results });
}
console.log(JSON.stringify(output, null, 2));
