// 发布暂存的纯函数与文件边界；不在运行时下载或安装依赖。
import * as fs from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const desktopRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
export const repositoryRoot = path.resolve(desktopRoot, "../..");
export const resourcesRoot = path.join(desktopRoot, "resources");
export const stagedRoot = path.join(resourcesRoot, "runtime");
export const pythonVersion = "3.12.14";

export function nativeTarget(platform = process.platform, arch = process.arch) {
  if (!["darwin", "linux", "win32"].includes(platform) || !["x64", "arm64"].includes(arch) || (platform === "win32" && arch !== "x64")) {
    throw new Error(`Unsupported build host: ${platform}/${arch}`);
  }
  return { platform, arch, goos: { darwin: "darwin", linux: "linux", win32: "windows" }[platform], goarch: arch === "x64" ? "amd64" : "arm64" };
}

export function within(root, candidate) {
  const relative = path.relative(path.resolve(root), path.resolve(candidate));
  return relative === "" || (!relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative));
}

// 只允许操作仓库中的固定产物目录，拒绝符号链接替换清理目标。
export async function assertOutputDirectory() {
  if (await fs.realpath(resourcesRoot) !== resourcesRoot) throw new Error("Resources directory must not be a symlink");
  for (const name of ["runtime", "runtime.previous"]) {
    const info = await fs.lstat(path.join(resourcesRoot, name)).catch(e => { if (e.code !== "ENOENT") throw e; return null; });
    if (info?.isSymbolicLink() || (info && !info.isDirectory())) throw new Error(`Unsafe staging destination: ${name}`);
  }
}

export function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: repositoryRoot, encoding: "utf8", maxBuffer: 8 << 20, ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${path.basename(command)} failed (${result.status}): ${(result.stderr || "").slice(-4000)}`);
  return (result.stdout || "").trim();
}

export function pythonRelative(platform = process.platform) {
  return platform === "win32" ? path.join("worker", "Scripts", "python.exe") : path.join("worker", "bin", "python");
}

// 保留相对链接；内部绝对链接改成相对链接，外部链接立即失败。
export async function copyRelocatable(source, destination) {
  const original = await fs.realpath(source);
  await fs.cp(original, destination, { recursive: true, verbatimSymlinks: true, filter: name => !name.split(path.sep).includes("__pycache__") });
  async function visit(dir) {
    for (const entry of await fs.readdir(dir, { withFileTypes: true })) {
      const file = path.join(dir, entry.name);
      if (entry.isSymbolicLink()) {
        const originalFile = path.join(original, path.relative(destination, file));
        const resolved = await fs.realpath(originalFile);
        if (!within(original, resolved)) throw new Error("Runtime contains an external symlink");
        const target = path.join(destination, path.relative(original, resolved));
        await fs.unlink(file);
        await fs.symlink(path.relative(path.dirname(file), target), file);
      } else if (entry.isDirectory()) await visit(file);
    }
  }
  await visit(destination);
}

export async function assertContainedLinks(root) {
  root = await fs.realpath(root);
  async function visit(dir) {
    for (const entry of await fs.readdir(dir, { withFileTypes: true })) {
      const file = path.join(dir, entry.name);
      if (entry.isSymbolicLink()) {
        if (!within(root, await fs.realpath(file))) throw new Error("Runtime symlink escapes bundle");
      } else if (entry.isDirectory()) await visit(file);
    }
  }
  await visit(root);
}

export function isolatedEnv(home, platform = process.platform) {
  const env = { PATH: "", HOME: home, USERPROFILE: home, TMPDIR: home, TEMP: home, TMP: home, PYTHONNOUSERSITE: "1", PYTHONDONTWRITEBYTECODE: "1", PYTHONUNBUFFERED: "1", PYTHONIOENCODING: "utf-8", GOGOSLING_LOCALE: "en" };
  if (platform === "win32") for (const key of ["SystemRoot", "SystemDrive", "ComSpec", "PATHEXT"]) if (process.env[key]) env[key] = process.env[key];
  return env;
}
