import test from "node:test";
import assert from "node:assert/strict";
import * as fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { verifyRuntime } from "./verify-runtime.mjs";
import { assertContainedLinks, copyRelocatable, isolatedEnv, nativeTarget, pythonRelative, within } from "./runtime-lib.mjs";

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
