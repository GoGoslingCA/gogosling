// Electron 的 npm 包不一定自动下载二进制；构建前显式准备锁定版本，不依赖本机旧缓存。
import * as fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { assertContainedLinks, nativeTarget, repositoryRoot, within } from "./runtime-lib.mjs";

function executablePath(platform) {
  return platform === "darwin" ? "Electron.app/Contents/MacOS/Electron" : platform === "win32" ? "electron.exe" : "electron";
}

// 仅继承下载所需的系统、代理与证书设置；不传入 API 令牌，也不允许镜像或校验绕过。
export function electronInstallerEnvironment(environment, target) {
  const allowed = new Set(["PATH", "HOME", "USERPROFILE", "TMPDIR", "TMP", "TEMP", "SystemRoot", "SystemDrive", "ComSpec", "PATHEXT", "LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR", "ELECTRON_GET_USE_PROXY", "CI"]);
  const allowedNames = new Set([...allowed].map(key => key.toLowerCase()));
  const result = Object.fromEntries(Object.entries(environment).filter(([key, value]) => allowedNames.has(key.toLowerCase()) && typeof value === "string"));
  return { ...result, ELECTRON_INSTALL_PLATFORM: target.platform, ELECTRON_INSTALL_ARCH: target.arch, ELECTRON_GET_NO_PROGRESS: "1" };
}

// 只保留错误类别和状态；安装器原始堆栈可能包含带认证信息的代理 URL。
export function electronInstallFailure(result) {
  const details = [];
  if (typeof result.status === "number") details.push(`exit ${result.status}`);
  if (/^[A-Z0-9_]{1,32}$/.test(result.error?.code ?? "")) details.push(result.error.code);
  const stderr = typeof result.stderr === "string" ? result.stderr : "";
  const status = /(?:response code|status(?: code)?)\D{0,4}([45]\d\d)/i.exec(stderr);
  if (status) details.push(`HTTP ${status[1]}`);
  const code = /\b(?:ETIMEDOUT|ECONNRESET|ECONNREFUSED|ENOTFOUND|EAI_AGAIN|EACCES|EPERM|ENOSPC)\b/.exec(stderr);
  if (code) details.push(code[0]);
  if (/checksum|sha256/i.test(stderr)) details.push("checksum verification failed");
  return `Electron runtime installation failed${details.length ? ` (${[...new Set(details)].join("; ")})` : ""}`;
}

async function internalFile(directory, relative) {
  const resolved = await fs.realpath(path.join(directory, relative));
  if (!within(directory, resolved) || !(await fs.stat(resolved)).isFile()) throw new Error("Electron runtime file is outside its package or is not a regular file");
  return resolved;
}

async function runtimeReady(directory, version, target) {
  try {
    const recorded = (await fs.readFile(await internalFile(directory, "dist/version"), "utf8")).trim().replace(/^v/, "");
    const entry = (await fs.readFile(await internalFile(directory, "path.txt"), "utf8")).trim();
    if (recorded !== version || entry !== executablePath(target.platform)) return false;
    for (const file of [`dist/${entry}`, "dist/LICENSE", "dist/LICENSES.chromium.html"]) {
      if (!(await fs.stat(await internalFile(directory, file))).size) return false;
    }
    return true;
  } catch (error) { if (error.code === "ENOENT") return false; throw error; }
}

async function installRuntime({ installer, directory, environment }) {
  const result = spawnSync(process.execPath, [installer], { cwd: directory, env: environment, encoding: "utf8", maxBuffer: 2 << 20, timeout: 300000 });
  if (result.error || result.status !== 0) throw new Error(electronInstallFailure(result));
}

export async function ensureElectronRuntime(root = repositoryRoot, options = {}) {
  root = await fs.realpath(root);
  const target = options.target ?? nativeTarget();
  nativeTarget(target.platform, target.arch);
  // 不能 require("electron")：缺少 dist 时该入口会在下载前直接报错。
  const require = createRequire(path.join(root, "package.json"));
  const metadata = await fs.realpath(require.resolve("electron/package.json"));
  const directory = path.dirname(metadata);
  if (!within(root, directory)) throw new Error("Installed Electron package is outside the repository");
  const pkg = JSON.parse(await fs.readFile(metadata, "utf8"));
  const lock = JSON.parse(await fs.readFile(path.join(root, "package-lock.json"), "utf8"));
  const lockPath = path.relative(root, directory).split(path.sep).join("/");
  if (pkg.name !== "electron" || !/^\d+\.\d+\.\d+(?:-[a-z0-9.-]+)?$/i.test(pkg.version ?? "") || lock.packages?.[lockPath]?.version !== pkg.version) throw new Error("Electron package version does not match package-lock.json");
  const checksums = JSON.parse(await fs.readFile(await internalFile(directory, "checksums.json"), "utf8"));
  const archive = `electron-v${pkg.version}-${target.platform}-${target.arch}.zip`;
  if (!/^[a-f0-9]{64}$/i.test(checksums[archive] ?? "")) throw new Error("Locked Electron package does not provide the required archive checksum");
  // 安装器会写入这些固定位置；缺失文件不能让外部符号链接绕过边界检查。
  for (const name of ["dist", "path.txt"]) {
    const destination = path.join(directory, name);
    const stat = await fs.lstat(destination).catch(error => { if (error.code === "ENOENT") return null; throw error; });
    if (stat && (stat.isSymbolicLink() || (name === "dist" ? !stat.isDirectory() : !stat.isFile()))) throw new Error("Unsafe Electron installation destination");
    if (name === "dist" && stat) await assertContainedLinks(destination);
  }
  if (await runtimeReady(directory, pkg.version, target)) return { version: pkg.version, platform: target.platform, arch: target.arch, installed: false };
  const installer = await internalFile(directory, "install.js");
  const marker = path.join(directory, "path.txt"), backup = path.join(directory, `.gogosling-path-${process.pid}-${Date.now()}.txt`);
  let previous = false;
  try {
    // 官方安装器只检查版本/入口，不检查许可；暂移入口标记，使不完整缓存也能重新下载。
    try { await internalFile(directory, "path.txt"); await fs.rename(marker, backup); previous = true; } catch (error) { if (error.code !== "ENOENT") throw error; }
    await (options.install ?? installRuntime)({ installer, directory, environment: electronInstallerEnvironment(options.environment ?? process.env, target) });
    if (!await runtimeReady(directory, pkg.version, target)) throw new Error("Electron installer completed without the expected version, binary, and license notices");
    if (previous) await fs.rm(backup);
    return { version: pkg.version, platform: target.platform, arch: target.arch, installed: true };
  } catch (error) {
    if (previous) await fs.rename(backup, marker);
    throw error;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  ensureElectronRuntime().then(result => console.log(JSON.stringify(result))).catch(error => { console.error(error.message); process.exitCode = 1; });
}
