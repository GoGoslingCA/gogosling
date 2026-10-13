// 用隔离的 npm 包布局模拟首次安装和损坏缓存；不下载、不执行 Electron 或访问真实网络。
import test from "node:test";
import assert from "node:assert/strict";
import * as fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { electronInstallFailure, electronInstallerEnvironment, ensureElectronRuntime } from "./electron-runtime.mjs";
import { nativeTarget } from "./runtime-lib.mjs";

async function fixture() {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-electron-install-test-")));
  const target = nativeTarget(), version = "44.7.0", directory = path.join(root, "node_modules/electron");
  const executable = target.platform === "darwin" ? "Electron.app/Contents/MacOS/Electron" : target.platform === "win32" ? "electron.exe" : "electron";
  const write = async (relative, content) => {
    const file = path.join(root, relative); await fs.mkdir(path.dirname(file), { recursive: true });
    await fs.writeFile(file, typeof content === "string" ? content : JSON.stringify(content));
  };
  await write("package.json", { name: "synthetic-build", private: true });
  await write("package-lock.json", { packages: { "node_modules/electron": { version } } });
  await write("node_modules/electron/package.json", { name: "electron", version, main: "index.js" });
  await write("node_modules/electron/index.js", 'throw new Error("The runtime entry must not be required before installation");');
  await write("node_modules/electron/install.js", "// Synthetic installer fixture; never executed.\n");
  await write("node_modules/electron/checksums.json", { [`electron-v${version}-${target.platform}-${target.arch}.zip`]: "a".repeat(64) });
  const install = async () => {
    await write("node_modules/electron/path.txt", executable);
    await write("node_modules/electron/dist/version", version);
    await write(`node_modules/electron/dist/${executable}`, "Synthetic executable bytes");
    await write("node_modules/electron/dist/LICENSE", "Synthetic upstream license\n");
    await write("node_modules/electron/dist/LICENSES.chromium.html", "Synthetic Chromium notices\n");
  };
  return { root, target, version, directory, executable, write, install };
}

test("fresh npm layout explicitly installs the locked runtime without requiring electron entry", async () => {
  const f = await fixture(); let calls = 0;
  try {
    const result = await ensureElectronRuntime(f.root, { target: f.target, install: async request => {
      calls++; assert.equal(request.installer, path.join(f.directory, "install.js"));
      assert.equal(request.environment.ELECTRON_INSTALL_ARCH, f.target.arch);
      assert.equal(request.environment.electron_use_remote_checksums, undefined);
      await f.install();
    } });
    assert.equal(result.installed, true); assert.equal(result.version, f.version); assert.equal(calls, 1);
    const cached = await ensureElectronRuntime(f.root, { target: f.target, install: async () => { throw new Error("Complete cache should be reused"); } });
    assert.equal(cached.installed, false);
  } finally { await fs.rm(f.root, { recursive: true, force: true }); }
});

test("stale versions and missing notices force reinstallation despite an existing binary", async () => {
  for (const defect of ["version", "license"]) {
    const f = await fixture(); let calls = 0;
    try {
      await f.install();
      if (defect === "version") await f.write("node_modules/electron/dist/version", "1.0.0");
      else await fs.rm(path.join(f.directory, "dist/LICENSES.chromium.html"));
      await ensureElectronRuntime(f.root, { target: f.target, install: async () => {
        calls++; await assert.rejects(() => fs.stat(path.join(f.directory, "path.txt")), { code: "ENOENT" });
        await f.install();
      } });
      assert.equal(calls, 1);
      assert.equal((await fs.readdir(f.directory)).some(file => file.startsWith(".gogosling-path-")), false);
    } finally { await fs.rm(f.root, { recursive: true, force: true }); }
  }
});

test("unlocked package versions and missing embedded checksums cannot start installation", async () => {
  const f = await fixture(); let calls = 0;
  try {
    const options = { target: f.target, install: async () => { calls++; } };
    await f.write("package-lock.json", { packages: { "node_modules/electron": { version: "1.0.0" } } });
    await assert.rejects(() => ensureElectronRuntime(f.root, options), /package-lock/);
    await f.write("package-lock.json", { packages: { "node_modules/electron": { version: f.version } } });
    await f.write("node_modules/electron/checksums.json", {});
    await assert.rejects(() => ensureElectronRuntime(f.root, options), /archive checksum/);
    assert.equal(calls, 0);
  } finally { await fs.rm(f.root, { recursive: true, force: true }); }
});

test("failed or incomplete installation restores the previous entry marker", async () => {
  const f = await fixture();
  try {
    await f.install(); await fs.rm(path.join(f.directory, "dist/LICENSE"));
    await assert.rejects(() => ensureElectronRuntime(f.root, { install: async () => { throw new Error("Synthetic network failure"); } }), /network failure/);
    assert.equal(await fs.readFile(path.join(f.directory, "path.txt"), "utf8"), f.executable);
    await assert.rejects(() => ensureElectronRuntime(f.root, { install: async () => {} }), /expected version, binary, and license notices/);
    assert.equal(await fs.readFile(path.join(f.directory, "path.txt"), "utf8"), f.executable);
    assert.equal(await fs.readFile(path.join(f.directory, `dist/${f.executable}`), "utf8"), "Synthetic executable bytes");
  } finally { await fs.rm(f.root, { recursive: true, force: true }); }
});

test("installer environment prevents alternate destinations, checksum bypass, and token inheritance", () => {
  const env = electronInstallerEnvironment({ PATH: "synthetic-path", HOME: "synthetic-home", GH_TOKEN: "synthetic-token", OPENAI_API_KEY: "synthetic-key", ELECTRON_OVERRIDE_DIST_PATH: "/outside", ELECTRON_MIRROR: "https://example.invalid", electron_use_remote_checksums: "1", npm_config_electron_use_remote_checksums: "1", NODE_OPTIONS: "--require=/outside.js" }, nativeTarget());
  assert.equal(env.PATH, "synthetic-path"); assert.equal(env.HOME, "synthetic-home");
  for (const name of ["GH_TOKEN", "OPENAI_API_KEY", "ELECTRON_OVERRIDE_DIST_PATH", "ELECTRON_MIRROR", "electron_use_remote_checksums", "npm_config_electron_use_remote_checksums", "NODE_OPTIONS"]) assert.equal(env[name], undefined);
});

test("installer errors retain actionable status without proxy credentials or raw output", () => {
  const result = electronInstallFailure({ status: 1, stderr: "HTTPError: Response code 403 (Forbidden) https://synthetic-user:synthetic-password@example.invalid/?token=synthetic-token" });
  assert.ok(result.includes("exit 1")); assert.ok(result.includes("HTTP 403"));
  assert.equal(result.includes("synthetic"), false); assert.equal(result.includes("https://"), false);
  assert.ok(electronInstallFailure({ error: { code: "ETIMEDOUT" } }).includes("ETIMEDOUT"));
  assert.ok(electronInstallFailure({ status: 1, stderr: "SHA256 checksum mismatch" }).includes("checksum verification failed"));
});

test("fresh installation refuses external or dangling destination links", { skip: process.platform === "win32" }, async () => {
  const f = await fixture(); let calls = 0;
  try {
    const options = { install: async () => { calls++; } };
    await fs.symlink(path.join(f.root, "absent-external-file"), path.join(f.directory, "path.txt"));
    await assert.rejects(() => ensureElectronRuntime(f.root, options), /Unsafe Electron installation destination/);
    await fs.rm(path.join(f.directory, "path.txt"));
    const outside = path.join(f.root, "external-directory"); await fs.mkdir(outside);
    await fs.symlink(outside, path.join(f.directory, "dist"));
    await assert.rejects(() => ensureElectronRuntime(f.root, options), /Unsafe Electron installation destination/);
    assert.equal(calls, 0); assert.deepEqual(await fs.readdir(outside), []);
  } finally { await fs.rm(f.root, { recursive: true, force: true }); }
});
