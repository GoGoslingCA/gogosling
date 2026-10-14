// Synthetic production-controller allocation benchmark. No network or user content.
// node --expose-gc tests/benchmarks/draft-allocation.mjs [--baseline /tmp/draft-sources]
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { performance } from "node:perf_hooks";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { stripTypeScriptTypes } from "node:module";

const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--baseline")) throw new Error("Expected --baseline <directory>");
const root = fileURLToPath(new URL("../../apps/web/src/state/", import.meta.url));
const asModule = source => `data:text/javascript;base64,${Buffer.from(stripTypeScriptTypes(source, { mode: "transform" })).toString("base64")}`;
const client = asModule(await readFile(new URL("../../apps/web/src/api/client.ts", import.meta.url), "utf8"));
const versions = await Promise.all([...(args.length ? [["before", args[1]]] : []), ["after", root]].map(async ([label, directory]) => {
  const source = await readFile(join(directory, "draftController.ts"), "utf8");
  return { label, ...await import(asModule(source.replace('from "../api/client"', `from "${client}"`))), sourceSha256: createHash("sha256").update(source).digest("hex") };
}));
const originalStringify = JSON.stringify, originalTimeout = global.setTimeout, originalClear = global.clearTimeout;
const scope = { kind: "composer", agent_id: "synthetic-agent", session_id: "synthetic-session" };
const textBytes = 131072, drafts = 24, samples = 5;
const output = { runtime: process.version, platform: process.platform, arch: process.arch, textBytes, drafts, samples, sources: versions.map(({ label, sourceSha256 }) => ({ label, sourceSha256 })), notes: [
  "Synthetic production-controller autosaves, excluding HTTP, React, textarea layout, and whole-app RSS.",
  "The production debounce callback runs through a deterministic fake timer; all saves finish with equivalent content and version.",
  "JSON fingerprint calls and generated characters are counted directly. These allocations exclude API request/response encoding, which is still necessary.",
  "GC runs before each sample only; no forced GC occurs during the measured workload. Sampled heap peaks include natural GC timing and runtime noise.",
  "The fake API round-trips its response through JSON to represent separately decoded response strings. This overhead is shared by both versions.",
], results: [] };
for (const version of versions) {
  const results = [];
  for (let sample = -1; sample < samples; sample++) {
    const controllers = [], tasks = new Map(); let nextTimer = 0, fingerprints = 0, fingerprintCharacters = 0;
    let writes = 0;
    const api = { listDrafts: async () => [], getDraft: async () => { throw new Error("Unexpected read"); }, updateDraft: async () => { throw new Error("Unexpected update"); }, deleteDraft: async () => { throw new Error("Unexpected delete"); }, createDraft: async input => {
      writes++;
      return JSON.parse(originalStringify({ ...input, id: `draft-${writes}`, owner_id: "local", version: 1, created_at: "2026-10-14T00:00:00Z", updated_at: "2026-10-14T00:00:00Z" }));
    } };
    global.setTimeout = callback => { const handle = ++nextTimer; tasks.set(handle, callback); return handle; };
    global.clearTimeout = handle => { tasks.delete(handle); };
    JSON.stringify = (value, ...rest) => { const encoded = originalStringify(value, ...rest); if (value && typeof value.text === "string" && Object.keys(value).join(",") === "text,filename,attachments,material_ids") { fingerprints++; fingerprintCharacters += encoded.length; } return encoded; };
    try {
      global.gc?.(); const heapBefore = process.memoryUsage().heapUsed;
      let sampledHeapPeak = heapBefore;
      const cpuStart = process.cpuUsage(), start = performance.now();
      for (let index = 0; index < drafts; index++) {
        const controller = new version.DraftController({ ...scope, session_id: `session-${index}` }, { text: "" });
        controllers.push(controller); controller.configure(api, true, false); await controller.hydrate();
        controller.setContent({ text: Buffer.alloc(textBytes, 65 + index % 26).toString("ascii") });
        const entry = tasks.entries().next().value;
        if (!entry) throw new Error("Autosave was not scheduled");
        tasks.delete(entry[0]); entry[1]();
        for (let turn = 0; turn < 12; turn++) await Promise.resolve();
        if (controller.getSnapshot().dirty || controller.getSnapshot().status !== "saved" || controller.getSnapshot().content.text.length !== textBytes) throw new Error("Autosave changed its behavior");
        sampledHeapPeak = Math.max(sampledHeapPeak, process.memoryUsage().heapUsed);
      }
      const elapsedMs = performance.now() - start, cpu = process.cpuUsage(cpuStart);
      if (writes !== drafts || tasks.size) throw new Error("Autosave count changed");
      const retainedFingerprintCharacters = controllers.reduce((sum, controller) => sum + (controller.currentFingerprint?.length ?? 0), 0);
      if (sample >= 0) results.push({ elapsedMs, cpuMs: (cpu.user + cpu.system) / 1000, fingerprints, fingerprintCharacters, retainedFingerprintCharacters, sampledHeapPeakDeltaBytes: sampledHeapPeak - heapBefore });
    } finally {
      for (const controller of controllers) controller.dispose();
      global.setTimeout = originalTimeout; global.clearTimeout = originalClear; JSON.stringify = originalStringify;
    }
  }
  output.results.push({ label: version.label, samples: results });
}
console.log(originalStringify(output, null, 2));
