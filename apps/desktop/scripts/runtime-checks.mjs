import test from "node:test";
import assert from "node:assert/strict";
import * as fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { verifyRuntime } from "./verify-runtime.mjs";
import { diagnosticsBuildArgs } from "./diagnostics-build.mjs";
import { collectDistributionLicenses, verifyDistributionLicenses } from "./distribution-licenses.mjs";
import "./release-checks.mjs";
import "./electron-runtime-checks.mjs";
import { assertContainedLinks, copyRelocatable, isolatedEnv, nativeTarget, pythonRelative, within } from "./runtime-lib.mjs";

async function distributionFixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-distribution-test-"));
  const write = async (relative, value) => {
    const file = path.join(root, relative); await fs.mkdir(path.dirname(file), { recursive: true });
    await fs.writeFile(file, typeof value === "string" ? value : JSON.stringify(value));
  };
  await write("package-lock.json", { packages: { "node_modules/reader": { version: "1.0.0" }, "node_modules/electron": { version: "2.0.0" } } });
  await write("node_modules/reader/package.json", { name: "reader", version: "1.0.0", license: "MIT" });
  await write("node_modules/reader/LICENSE", "Synthetic upstream license.\n");
  await write("node_modules/electron/package.json", { name: "electron", version: "2.0.0", license: "MIT" });
  await write("node_modules/electron/dist/LICENSE", "Synthetic Electron license.\n");
  await write("node_modules/electron/dist/LICENSES.chromium.html", "<p>Synthetic Chromium notices.</p>\n");
  const index = { schema: 1, packages: [{ name: "reader", version: "1.0.0", lockPath: "node_modules/reader", notices: ["LICENSE"] }] };
  await write("apps/web/dist/licenses/bundled-dependencies.json", index);
  return { root, write, index, output: path.join(root, "output") };
}

test("distribution keeps complete upstream notices and rejects stale or traversing web indexes", async () => {
  const { root, write, index, output } = await distributionFixture();
  try {
    await collectDistributionLicenses(root, output);
    const verified = await verifyDistributionLicenses(output);
    assert.equal(verified.packages.length, 2);
    assert.equal(await fs.readFile(path.join(output, "javascript/reader@1.0.0/LICENSE"), "utf8"), "Synthetic upstream license.\n");
    assert.equal(await fs.readFile(path.join(output, "electron/LICENSES.chromium.html"), "utf8"), "<p>Synthetic Chromium notices.</p>\n");
    assert.equal(JSON.stringify(verified).includes(root), false);
    index.packages[0].version = "9.0.0";
    await write("apps/web/dist/licenses/bundled-dependencies.json", index);
    await assert.rejects(() => collectDistributionLicenses(root, output), /stale/);
    index.packages[0].version = "1.0.0"; index.packages[0].notices = ["../../outside/LICENSE"];
    await write("apps/web/dist/licenses/bundled-dependencies.json", index);
    await assert.rejects(() => collectDistributionLicenses(root, output), /Unsafe Web license/);
    index.packages[0].lockPath = "node_modules/../../outside";
    await write("apps/web/dist/licenses/bundled-dependencies.json", index);
    await assert.rejects(() => collectDistributionLicenses(root, output), /Unsafe Web dependency/);
    await fs.rm(path.join(output, "electron/LICENSES.chromium.html"));
    await assert.rejects(() => verifyDistributionLicenses(output), /ENOENT/);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test("distribution refuses installed versions that differ from the lock file", async () => {
  const { root, write, output } = await distributionFixture();
  try {
    await write("node_modules/reader/package.json", { name: "reader", version: "9.0.0" });
    await assert.rejects(() => collectDistributionLicenses(root, output), /package-lock/);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test("native packages configure valid brand icons instead of the Electron default", async () => {
  const desktop = path.resolve(import.meta.dirname, "..");
  const config = await fs.readFile(path.join(desktop, "electron-builder.yml"), "utf8");
  for (const ext of ["icns", "ico", "png"]) assert.ok(config.includes(`icon: build/icon.${ext}`));
  const icns = await fs.readFile(path.join(desktop, "build/icon.icns"));
  assert.equal(icns.toString("ascii", 0, 4), "icns"); assert.equal(icns.readUInt32BE(4), icns.length);
  const ico = await fs.readFile(path.join(desktop, "build/icon.ico"));
  assert.equal(ico.readUInt16LE(2), 1); assert.ok(ico.readUInt16LE(4) >= 4);
  for (let i = 0; i < ico.readUInt16LE(4); i++) {
    const size = ico.readUInt32LE(6 + i * 16 + 8), offset = ico.readUInt32LE(6 + i * 16 + 12);
    assert.ok(offset + size <= ico.length); assert.equal(ico.toString("ascii", offset + 1, offset + 4), "PNG");
  }
  const png = await fs.readFile(path.join(desktop, "build/icon.png"));
  assert.equal(png.toString("ascii", 1, 4), "PNG"); assert.equal(png.readUInt32BE(16), 512); assert.equal(png.readUInt32BE(20), 512);
});

test("diagnostics requires an explicit package flag and has no credential input", () => {
  assert.ok(diagnosticsBuildArgs([]).includes("--config.extraMetadata.gogoslingDiagnostics.enabled=false"));
  const release = diagnosticsBuildArgs(["--dir", "--diagnostics"]);
  assert.ok(release.includes("--dir"));
  assert.ok(release.includes("--config.extraMetadata.gogoslingDiagnostics.enabled=true"));
  assert.ok(!release.includes("--diagnostics"));
  assert.equal(release.some(arg => /token|bearer|secret/i.test(arg)), false);
});

test("only supported native targets map to release runtimes", () => {
  assert.equal(nativeTarget("darwin", "arm64").goarch, "arm64");
  assert.equal(nativeTarget("win32", "x64").goos, "windows");
  assert.throws(() => nativeTarget("win32", "arm64"), /Unsupported/);
  assert.throws(() => nativeTarget("linux", "ia32"), /Unsupported/);
  assert.equal(pythonRelative("win32"), path.join("worker", "Scripts", "python.exe"));
});

test("runtime containment rejects sibling-prefix and traversal escapes", () => {
  const root = path.resolve("bundle");
  assert.equal(within(root, path.join(root, "lib", "python")), true);
  assert.equal(within(root, root + "-other"), false);
  assert.equal(within(root, path.join(root, "..", "outside")), false);
});

test("smoke process environment does not inherit development credentials or tool paths", () => {
  const previousWorker = process.env.GOGOSLING_WORKER_CMD, previousPythonPath = process.env.PYTHONPATH;
  process.env.GOGOSLING_WORKER_CMD = "external-worker";
  process.env.PYTHONPATH = "external-packages";
  try {
    const env = isolatedEnv("synthetic-home");
    assert.equal(env.PATH, "");
    assert.equal(env.GOGOSLING_WORKER_CMD, undefined);
    assert.equal(env.PYTHONPATH, undefined);
    assert.equal(env.PYTHONNOUSERSITE, "1");
  } finally {
    if (previousWorker === undefined) delete process.env.GOGOSLING_WORKER_CMD; else process.env.GOGOSLING_WORKER_CMD = previousWorker;
    if (previousPythonPath === undefined) delete process.env.PYTHONPATH; else process.env.PYTHONPATH = previousPythonPath;
  }
});

test("copy preserves internal links after relocation and rejects external links", { skip: process.platform === "win32" }, async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-copy-test-"));
  try {
    const original = path.join(root, "original"), copied = path.join(root, "copied"), renamed = path.join(root, "renamed");
    await fs.mkdir(original); await fs.writeFile(path.join(original, "python"), "synthetic");
    await fs.symlink(path.join(original, "python"), path.join(original, "python3"));
    await copyRelocatable(original, copied); await fs.rename(copied, renamed);
    await fs.rm(original, { recursive: true });
    await assertContainedLinks(renamed);
    assert.equal(await fs.readFile(path.join(renamed, "python3"), "utf8"), "synthetic");
    const bad = path.join(root, "bad"); await fs.mkdir(bad);
    await fs.symlink(path.join(renamed, "python"), path.join(bad, "external"));
    await assert.rejects(() => copyRelocatable(bad, path.join(root, "reject")), /external symlink/);
    await assert.rejects(() => assertContainedLinks(bad), /escapes bundle/);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});


test("failed relocated verification cleans its temporary copy and preserves input", { skip: process.platform === "win32" }, async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-failure-test-"));
  const before = new Set((await fs.readdir(os.tmpdir())).filter(name => name.startsWith("gogosling relocated runtime ")));
  try {
    await fs.mkdir(path.join(root, "worker", "bin"), { recursive: true });
    const python = path.join(root, "worker", "bin", "python");
    await fs.writeFile(python, "#!/bin/sh\nexit 7\n", { mode: 0o700 });
    await fs.writeFile(path.join(root, "runtime-manifest.json"), JSON.stringify({ platform: process.platform, arch: process.arch }));
    await assert.rejects(() => verifyRuntime(root), /failed \(7\)/);
    assert.equal((await fs.stat(python)).isFile(), true);
    const after = (await fs.readdir(os.tmpdir())).filter(name => name.startsWith("gogosling relocated runtime "));
    assert.equal(after.some(name => !before.has(name)), false);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});
