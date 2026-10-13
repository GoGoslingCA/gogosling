// Synthetic retained-heap measurement of the production draft registry, without React or HTTP.
// node --expose-gc tests/benchmarks/draft-reclaim.mjs [--baseline /tmp/draft-sources]
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { performance } from "node:perf_hooks";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { stripTypeScriptTypes } from "node:module";

const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--baseline")) throw new Error("Expected --baseline <directory>");
if (!global.gc) throw new Error("Run Node with --expose-gc for comparable retained heap samples");
const root = fileURLToPath(new URL("../../apps/web/src/state/", import.meta.url));
const asModule = source => `data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source, { mode: "transform" })).toString("base64")}`;
const client = asModule(await readFile(new URL("../../apps/web/src/api/client.ts", import.meta.url), "utf8"));
const versions = await Promise.all([...(args.length ? [["before", args[1]]] : []), ["after", root]].map(async ([label, directory]) => {
  const controller = await readFile(join(directory, "draftController.ts"), "utf8"), provider = await readFile(join(directory, "DraftProvider.tsx"), "utf8");
  const controllerModule = asModule(controller.replace('from "../api/client"', `from "${client}"`));
  const registrySource = provider.slice(provider.includes("const releasedSnapshot:") ? provider.indexOf("const releasedSnapshot:") : provider.indexOf("export class DraftRegistry"), provider.indexOf("export const DraftContext"));
  return { label, ...await import(asModule(`import { DraftController, draftScopeKey } from "${controllerModule}";\n${registrySource}`)), sourceSha256: createHash("sha256").update(controller).update(provider).digest("hex") };
}));
const draftCount = 200, textBytes = 131072, sampleCount = 5;
const drain = async () => { for (let index = 0; index < 8; index++) await Promise.resolve(); };
const scope = index => ({ kind: "composer", agent_id: "synthetic-agent", session_id: `session-${index}` });
const text = index => Buffer.alloc(textBytes, 65 + index % 26).toString("ascii");
const api = {
  listDrafts: async query => [{ ...query, id: `draft-${query.session_id}`, text: text(Number(query.session_id.split("-").at(-1))), owner_id: "local", version: 1, created_at: "2026-10-13T00:00:00Z", updated_at: "2026-10-13T00:00:00Z" }],
  getDraft: async () => { throw new Error("Unexpected read"); }, createDraft: async () => { throw new Error("Unexpected save"); }, updateDraft: async () => { throw new Error("Unexpected save"); }, deleteDraft: async () => { throw new Error("Unexpected delete"); },
};
const output = { runtime: process.version, platform: process.platform, arch: process.arch, draftCount, textBytes, sampleCount, sources: versions.map(({ label, sourceSha256 }) => ({ label, sourceSha256 })), notes: [
  "Synthetic production registry/controller measurements. This is not browser or whole-app RSS, frame rate, or energy usage.",
  "Each generated persisted draft contains 131072 ASCII bytes, below the default draft size limit. No network, database, credentials, or user content.",
  "GC is forced before/after each sample to compare reachable heap, not expected operating-system RSS release timing.",
  "The fake server generates bodies on demand without retaining them, so its storage cannot mask client retention.",
  "Protected dirty, active, and legacy-memory drafts are separately asserted; they are not counted as reclaimable.",
], results: [] };
for (const version of versions) {
  const samples = [];
  for (let sample = -1; sample < sampleCount; sample++) {
    global.gc(); const baseline = process.memoryUsage().heapUsed, started = performance.now();
    const registry = new version.DraftRegistry(); registry.configure(api, true, false);
    for (let index = 0; index < draftCount; index++) {
      const controller = registry.get(scope(index), { text: "" });
      const release = registry.subscribeDraft ? registry.subscribeDraft(controller, () => {}) : controller.subscribe(() => {});
      registry.activate(controller); await controller.hydrate();
      if (controller.getSnapshot().content.text !== text(index)) throw new Error("Hydration changed body");
      release(); await drain();
    }
    const elapsedMs = performance.now() - started;
    global.gc(); const retainedHeapDeltaBytes = process.memoryUsage().heapUsed - baseline;
    // Runtime fields are inspected only by this standalone benchmark; no debug API is shipped.
    const retainedControllers = registry.controllers.size;
    const reopened = registry.get(scope(0), { text: "" }), release = registry.subscribeDraft ? registry.subscribeDraft(reopened, () => {}) : reopened.subscribe(() => {});
    registry.activate(reopened); await reopened.hydrate();
    if (reopened.getSnapshot().content.text !== text(0)) throw new Error("Reopening lost persisted content");
    if (registry.read && !registry.read(reopened).hydrated) throw new Error("Active controller was reclaimed");
    reopened.setContent({ text: "Unsaved synthetic edit" }); release(); await drain();
    if (registry.get(scope(0), { text: "" }) !== reopened || !reopened.getSnapshot().dirty) throw new Error("Dirty draft was reclaimed");
    registry.dispose();
    const legacy = new version.DraftRegistry(); legacy.configure(null, false, false);
    const memory = legacy.get(scope(0), { text: "Legacy memory content" }); legacy.activate(memory); await drain();
    if (legacy.get(scope(0), { text: "" }) !== memory || memory.getSnapshot().content.text !== "Legacy memory content") throw new Error("Legacy memory draft was reclaimed");
    legacy.dispose();
    if (sample >= 0) samples.push({ elapsedMs, retainedHeapDeltaBytes, retainedControllers });
  }
  output.results.push({ label: version.label, samples });
}
console.log(JSON.stringify(output, null, 2));
