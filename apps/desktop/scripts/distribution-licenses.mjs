// 仅在构建时收集实际安装且匹配锁文件的分发许可；保留上游原文，不复制开发配置。
import * as fs from "node:fs/promises";
import path from "node:path";
import { within } from "./runtime-lib.mjs";

async function readJSON(file) { return JSON.parse(await fs.readFile(file, "utf8")); }

async function resolvePackage(root, from, name) {
  if (!/^(?:@[a-z0-9._-]+\/)?[a-z0-9._-]+$/i.test(name)) throw new Error("Invalid dependency package name");
  let directory = from;
  while (within(root, directory)) {
    const candidate = path.join(directory, "node_modules", name);
    try {
      const real = await fs.realpath(candidate);
      if (!within(root, real)) throw new Error(`Dependency escapes the repository: ${name}`);
      if ((await fs.stat(path.join(real, "package.json"))).isFile()) return real;
    } catch (error) { if (error.code !== "ENOENT") throw error; }
    if (directory === root) break;
    directory = path.dirname(directory);
  }
  throw new Error(`Installed dependency is missing: ${name}`);
}

function lockedPackage(lock, root, directory, pkg) {
  const key = path.relative(root, directory).split(path.sep).join("/");
  if (lock.packages?.[key]?.version !== pkg.version) throw new Error(`Installed dependency does not match package-lock.json: ${pkg.name}`);
  if (!/^(?:@[a-z0-9._-]+\/)?[a-z0-9._-]+$/i.test(pkg.name ?? "") || !/^[a-z0-9.+-]+$/i.test(pkg.version ?? "")) throw new Error("Unsafe dependency identity");
}

// 返回可随安装包公开的相对路径索引；不记录构建机路径或账户信息。
export async function collectDistributionLicenses(repositoryRoot, licenseRoot) {
  const root = await fs.realpath(repositoryRoot);
  const lock = await readJSON(path.join(root, "package-lock.json"));
  const webIndex = await readJSON(path.join(root, "apps", "web", "dist", "licenses", "bundled-dependencies.json"));
  if (webIndex.schema !== 1 || !Array.isArray(webIndex.packages) || !webIndex.packages.length) throw new Error("Web distribution index is missing; rebuild the web app before packaging");
  const packages = [];
  for (const entry of webIndex.packages) {
    if (typeof entry.lockPath !== "string" || !/^node_modules\//.test(entry.lockPath) || /\\|\0/.test(entry.lockPath) || entry.lockPath.split("/").some(part => part === "." || part === "..")) throw new Error("Unsafe Web dependency lock path");
    const directory = await fs.realpath(path.resolve(root, entry.lockPath));
    if (!within(root, directory)) throw new Error("Web dependency escapes the repository");
    const pkg = await readJSON(path.join(directory, "package.json"));
    lockedPackage(lock, root, directory, pkg);
    if (pkg.name !== entry.name || pkg.version !== entry.version) throw new Error("Web distribution index is stale; rebuild the web app");
    const notices = entry.notices;
    if (!Array.isArray(notices) || !notices.length) throw new Error(`No distribution license found: ${pkg.name}`);
    const output = path.join(licenseRoot, "javascript", `${pkg.name}@${pkg.version}`);
    for (const file of notices) {
      if (typeof file !== "string" || path.isAbsolute(file) || /\\|\0/.test(file) || file.split("/").some(part => part === "." || part === ".." || !part)) throw new Error("Unsafe Web license path");
      if (!/^(?:licen[sc]es?|notice|copying)(?:$|[._-])/i.test(file.split("/")[0]) && !/^(?:licen[sc]es?|notice|copying)(?:$|[._-])/i.test(path.basename(file))) throw new Error("Web distribution entry is not a notice");
      if (!within(directory, await fs.realpath(path.join(directory, file)))) throw new Error("Web license escapes its package");
      const destination = path.join(output, file);
      await fs.mkdir(path.dirname(destination), { recursive: true });
      await fs.copyFile(path.join(directory, file), destination);
    }
    packages.push({ name: pkg.name, version: pkg.version, license: pkg.license ?? "SEE LICENSE FILES", files: notices.map(file => path.relative(licenseRoot, path.join(output, file)).split(path.sep).join("/")) });
  }
  const electron = await resolvePackage(root, path.join(root, "apps", "desktop"), "electron");
  const electronPackage = await readJSON(path.join(electron, "package.json"));
  lockedPackage(lock, root, electron, electronPackage);
  if (electronPackage.name !== "electron") throw new Error("Unexpected Electron package identity");
  const electronFiles = ["LICENSE", "LICENSES.chromium.html"];
  await fs.mkdir(path.join(licenseRoot, "electron"), { recursive: true });
  for (const file of electronFiles) {
    const source = await fs.realpath(path.join(electron, "dist", file));
    if (!within(electron, source) || !(await fs.stat(source)).isFile()) throw new Error("Unsafe Electron notice file");
    await fs.copyFile(source, path.join(licenseRoot, "electron", file));
  }
  packages.push({ name: "electron", version: electronPackage.version, license: electronPackage.license, files: electronFiles.map(file => `electron/${file}`) });
  const index = { schema: 1, packages: packages.sort((a, b) => `${a.name}@${a.version}`.localeCompare(`${b.name}@${b.version}`)) };
  await fs.writeFile(path.join(licenseRoot, "distribution-index.json"), JSON.stringify(index, null, 2) + "\n");
  return index;
}

// 在直接调用打包器时仍检查许可索引，避免复用缺少声明的旧暂存目录。
export async function verifyDistributionLicenses(licenseRoot) {
  licenseRoot = await fs.realpath(licenseRoot);
  const index = await readJSON(path.join(licenseRoot, "distribution-index.json"));
  if (index.schema !== 1 || !Array.isArray(index.packages) || !index.packages.length) throw new Error("Distribution license index is missing or invalid");
  const electron = index.packages.find(pkg => pkg.name === "electron");
  if (!electron?.files?.includes("electron/LICENSE") || !electron?.files?.includes("electron/LICENSES.chromium.html")) throw new Error("Electron distribution notices are missing");
  for (const pkg of index.packages) {
    if (!Array.isArray(pkg.files) || !pkg.files.length) throw new Error("Distribution license entry is empty");
    for (const file of pkg.files) {
      if (typeof file !== "string" || path.isAbsolute(file) || /\\|\0/.test(file) || file.split("/").some(part => part === "." || part === ".." || !part)) throw new Error("Unsafe distribution license path");
      const target = path.resolve(licenseRoot, file);
      if (!within(licenseRoot, target) || !within(licenseRoot, await fs.realpath(target)) || !(await fs.stat(target)).isFile()) throw new Error("Distribution license file is missing or outside the bundle");
    }
  }
  return index;
}
