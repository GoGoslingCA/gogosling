// 仅供受控手动工作流使用：只读构建与写入发布分离，发布前逐项复验，不覆盖既有资产。
import * as fs from "node:fs/promises";
import { createReadStream } from "node:fs";
import path from "node:path";
import os from "node:os";
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { repositoryRoot, within } from "./runtime-lib.mjs";

const repository = "GoGoslingCA/gogosling";
const tagPattern = /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)-beta(?:\.(?:0|[1-9]\d*))?$/;
const manifestName = "runtime-manifest.json";
const checksumName = "SHA256SUMS.txt";
async function readJSON(file) { return JSON.parse(await fs.readFile(file, "utf8")); }

export function validateReleaseContext(env) {
  if (env.GITHUB_ACTIONS !== "true" || env.GITHUB_EVENT_NAME !== "workflow_dispatch" || env.GITHUB_REF !== "refs/heads/main" || env.GITHUB_REPOSITORY !== repository) throw new Error("Desktop releases require a manual main-branch workflow in the project repository");
  if (!tagPattern.test(env.RELEASE_TAG ?? "")) throw new Error("Invalid beta tag");
  if (!/^[a-f0-9]{40}$/.test(env.GITHUB_SHA ?? "")) throw new Error("Invalid source commit");
  return { repository, tag: env.RELEASE_TAG, version: env.RELEASE_TAG.slice(1), sha: env.GITHUB_SHA };
}

export function validateVersions(context, desktop, web, lock) {
  if (desktop.version !== context.version || web.version !== context.version || lock.packages?.["apps/desktop"]?.version !== context.version || lock.packages?.["apps/web"]?.version !== context.version) throw new Error("Tag must match the desktop, web, and lockfile versions");
}

export function assertSuccessfulCI(runs, context) {
  const matching = runs.filter(run => run.head_sha === context.sha && run.head_branch === "main" && run.event === "push" && run.path === ".github/workflows/ci.yml").sort((a, b) => Number(b.id) - Number(a.id) || Number(b.run_attempt) - Number(a.run_attempt));
  if (!matching.length || matching[0].status !== "completed" || matching[0].conclusion !== "success") throw new Error("The latest main CI run for this exact source commit must have succeeded");
}

export function validateRuntimeManifest(manifest, context) {
  if (manifest.schema !== 1 || manifest.source_commit !== context.sha || manifest.source_dirty !== false || manifest.app_version !== context.version || manifest.platform !== "darwin" || manifest.arch !== "arm64") throw new Error("Runtime manifest does not match the clean Apple Silicon release source");
  for (const key of ["relocated", "isolated_path", "mock_inference", "daemon_worker_ready"]) if (manifest.verified?.[key] !== true) throw new Error("Runtime verification is incomplete");
}

export function assetNames(context) { return [`gogosling-${context.version}-mac-arm64.dmg`, manifestName, checksumName]; }

export async function sha256(file) {
  const digest = createHash("sha256");
  for await (const chunk of createReadStream(file)) digest.update(chunk);
  return digest.digest("hex");
}

// 对镜像内应用逐文件比较内容、执行权限和符号链接；不跟随链接离开 app。
export async function compareAppTrees(expected, actual) {
  const first = (await fs.readdir(expected)).sort(), second = (await fs.readdir(actual)).sort();
  if (JSON.stringify(first) !== JSON.stringify(second)) throw new Error("Disk image app file list differs from the verified app");
  for (const name of first) {
    const left = path.join(expected, name), right = path.join(actual, name);
    const [a, b] = await Promise.all([fs.lstat(left), fs.lstat(right)]);
    if ((a.mode & 0o777) !== (b.mode & 0o777)) throw new Error("Disk image app permissions differ from the verified app");
    if (a.isDirectory() && b.isDirectory()) await compareAppTrees(left, right);
    else if (a.isFile() && b.isFile()) {
      if (a.size !== b.size || await sha256(left) !== await sha256(right)) throw new Error("Disk image app content differs from the verified app");
    } else if (a.isSymbolicLink() && b.isSymbolicLink()) {
      if (await fs.readlink(left) !== await fs.readlink(right)) throw new Error("Disk image app symlink differs from the verified app");
    } else throw new Error("Disk image app contains a different or unsupported file type");
  }
}

export async function verifyReleaseAssets(directory, context) {
  const names = assetNames(context), entries = await fs.readdir(directory, { withFileTypes: true });
  if (entries.length !== names.length || entries.some(entry => !entry.isFile() || !names.includes(entry.name))) throw new Error("Release artifact must contain exactly the installer, manifest, and checksums");
  const lines = (await fs.readFile(path.join(directory, checksumName), "utf8")).trimEnd().split("\n");
  const expected = new Map();
  for (const line of lines) {
    const match = /^([a-f0-9]{64})  ([a-zA-Z0-9._-]+)$/.exec(line);
    if (!match || !names.slice(0, 2).includes(match[2]) || expected.has(match[2])) throw new Error("Invalid or duplicate release checksum entry");
    expected.set(match[2], match[1]);
  }
  if (expected.size !== 2) throw new Error("Both installer and manifest checksums are required");
  const verified = [];
  for (const name of names) {
    const file = path.join(directory, name), digest = await sha256(file), stat = await fs.stat(file);
    if (!stat.size || (expected.has(name) && digest !== expected.get(name))) throw new Error(`Release checksum mismatch: ${name}`);
    verified.push({ name, sha256: digest, size: stat.size, file });
  }
  validateRuntimeManifest(await readJSON(path.join(directory, manifestName)), context);
  return verified;
}

export function releaseMarker(context) { return `<!-- gogosling-release schema=1 tag=${context.tag} source=${context.sha} -->`; }

export async function releaseBody(root, context) {
  const notesFile = path.join(root, "docs", "releases", `${context.tag}.md`);
  let body = await fs.readFile(notesFile, "utf8");
  const matches = [...body.matchAll(/(!?\[[^\]\n]*\]\()(<[^>\n]+>|[^)\s]+)(\))/g)];
  for (const match of matches.reverse()) {
    const original = match[2], target = original.startsWith("<") ? original.slice(1, -1) : original;
    if (target.startsWith("https://") || target.startsWith("#")) continue;
    if (/^[a-z][a-z0-9+.-]*:/i.test(target) || target.startsWith("//") || target.includes("\\")) throw new Error("Release notes contain an unsupported link");
    const boundary = target.search(/[?#]/), pathname = boundary < 0 ? target : target.slice(0, boundary), suffix = boundary < 0 ? "" : target.slice(boundary);
    const file = path.resolve(path.dirname(notesFile), decodeURIComponent(pathname));
    if (!within(root, file) || !within(await fs.realpath(root), await fs.realpath(file))) throw new Error("Release note link escapes the repository");
    const relative = path.relative(root, file).split(path.sep).map(encodeURIComponent).join("/");
    const absolute = match[1].startsWith("![")
      ? `https://raw.githubusercontent.com/${context.repository}/${context.tag}/${relative}${suffix}`
      : `https://github.com/${context.repository}/blob/${context.tag}/${relative}${suffix}`;
    const start = match.index + match[1].length;
    body = body.slice(0, start) + absolute + body.slice(start + original.length);
  }
  return `${body.trim()}\n\nSource: [\`${context.sha.slice(0, 7)}\`](https://github.com/${context.repository}/commit/${context.sha}).\n\n${releaseMarker(context)}\n`;
}

export function validateRetryDraft(release, context, body, assets) {
  if (!release.draft) throw new Error("A published release already exists; it will not be changed");
  if (release.tag_name !== context.tag || release.target_commitish !== context.sha || release.prerelease !== true || release.author?.login !== "github-actions[bot]" || release.body?.replaceAll("\r\n", "\n").trim() !== body.trim() || !release.body?.includes(releaseMarker(context))) throw new Error("Existing draft does not belong to this exact automated release");
  const names = new Set(assets.map(asset => asset.name));
  if (!Array.isArray(release.assets) || release.assets.some(asset => !names.has(asset.name)) || new Set(release.assets.map(asset => asset.name)).size !== release.assets.length) throw new Error("Draft contains unexpected assets; all existing files have been preserved");
  for (const asset of release.assets) {
    const local = assets.find(item => item.name === asset.name);
    if (asset.state !== "uploaded" || asset.size !== local.size || (asset.digest && asset.digest !== `sha256:${local.sha256}`)) throw new Error("Draft asset conflicts with the verified build; no files were overwritten");
  }
}

function command(binary, args, options = {}) {
  const result = spawnSync(binary, args, { cwd: repositoryRoot, encoding: "utf8", maxBuffer: 2 << 20, ...options });
  if (result.error || result.status !== 0) throw new Error(`${path.basename(binary)} ${args[0]} failed (exit ${result.status ?? "unavailable"})`);
  return result.stdout.trim();
}

async function api(route, options = {}) {
  if (!process.env.GH_TOKEN) throw new Error("The short-lived workflow token is unavailable");
  const response = await fetch(`https://api.github.com/repos/${repository}/${route}`, { headers: { Authorization: `Bearer ${process.env.GH_TOKEN}`, Accept: options.binary ? "application/octet-stream" : "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28" }, signal: AbortSignal.timeout(options.binary ? 180000 : 30000) });
  if (response.status === 404 && options.optional) return null;
  if (!response.ok) { await response.body?.cancel(); throw new Error(`GitHub release API failed (${response.status})`); }
  return options.binary ? response : response.json();
}

async function assertCI(context) {
  const response = await api(`actions/workflows/ci.yml/runs?head_sha=${context.sha}&branch=main&event=push&per_page=100`);
  if (!Array.isArray(response.workflow_runs)) throw new Error("CI response is missing workflow runs");
  assertSuccessfulCI(response.workflow_runs, context);
}

async function sourceGate(context, arm64) {
  if (arm64 && (process.platform !== "darwin" || process.arch !== "arm64")) throw new Error("Release installer must be built on a native macOS ARM64 runner");
  if (command("git", ["rev-parse", "HEAD"]) !== context.sha || command("git", ["status", "--porcelain", "--untracked-files=no"])) throw new Error("Release checkout must match the clean workflow source commit");
  validateVersions(context, await readJSON(path.join(repositoryRoot, "apps/desktop/package.json")), await readJSON(path.join(repositoryRoot, "apps/web/package.json")), await readJSON(path.join(repositoryRoot, "package-lock.json")));
  await releaseBody(repositoryRoot, context);
  await assertCI(context);
}

async function prepare(context) {
  if (process.platform !== "darwin" || process.arch !== "arm64") throw new Error("Packaging verification requires macOS ARM64");
  const output = path.join(repositoryRoot, "apps/desktop/release/publish"), resources = path.join(output, "mac-arm64/gogosling.app/Contents/Resources");
  const require = createRequire(import.meta.url), { extractFile } = require("@electron/asar");
  const packaged = JSON.parse(extractFile(path.join(resources, "app.asar"), "package.json").toString("utf8"));
  if (packaged.version !== context.version || packaged.gogoslingDiagnostics?.enabled !== true) throw new Error("Packaged app version or diagnostics marker is incorrect");
  validateRuntimeManifest(await readJSON(path.join(resources, manifestName)), context);
  const icon = command("/usr/libexec/PlistBuddy", ["-c", "Print :CFBundleIconFile", path.join(resources, "../Info.plist")]);
  if (path.basename(icon) !== icon || !icon.endsWith(".icns") || await sha256(path.join(resources, icon)) !== await sha256(path.join(repositoryRoot, "apps/desktop/build/icon.icns"))) throw new Error("Packaged app does not contain the verified brand icon");
  const { verifyDistributionLicenses } = await import("./distribution-licenses.mjs");
  await verifyDistributionLicenses(path.join(resources, "licenses"));
  const { verifyRuntime } = await import("./verify-runtime.mjs");
  await verifyRuntime(resources);
  const [installer] = assetNames(context), diskImage = path.join(output, installer);
  command("/usr/bin/hdiutil", ["verify", diskImage]);
  const mountRoot = await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-image-check-")), mount = path.join(mountRoot, "mounted");
  let mounted = false;
  try {
    await fs.mkdir(mount);
    command("/usr/bin/hdiutil", ["attach", "-readonly", "-nobrowse", "-mountpoint", mount, diskImage]);
    mounted = true;
    await compareAppTrees(path.join(resources, "../.."), path.join(mount, "gogosling.app"));
  } finally {
    if (mounted) command("/usr/bin/hdiutil", ["detach", mount]);
    await fs.rm(mountRoot, { recursive: true, force: true });
  }
  const directory = path.join(repositoryRoot, "apps/desktop/release/publish-assets");
  await fs.mkdir(directory, { recursive: true });
  if ((await fs.readdir(directory)).length) throw new Error("Release asset staging directory must be empty");
  await fs.copyFile(diskImage, path.join(directory, installer), fs.constants.COPYFILE_EXCL);
  await fs.copyFile(path.join(resources, manifestName), path.join(directory, manifestName), fs.constants.COPYFILE_EXCL);
  const checksums = await Promise.all([installer, manifestName].map(async name => `${await sha256(path.join(directory, name))}  ${name}`));
  await fs.writeFile(path.join(directory, checksumName), checksums.join("\n") + "\n", { flag: "wx" });
  await verifyReleaseAssets(directory, context);
  console.log("Verified installer, source manifest, license notices, brand icon, and SHA-256 checksums.");
}

async function existingRelease(tag) {
  for (let page = 1; page <= 100; page++) {
    const releases = await api(`releases?per_page=100&page=${page}`);
    if (!Array.isArray(releases)) throw new Error("Invalid release list response");
    const found = releases.filter(release => release.tag_name === tag);
    if (found.length > 1) throw new Error("Multiple releases use this tag");
    if (found.length) return found[0];
    if (releases.length < 100) return null;
  }
  throw new Error("Release lookup exceeded its bounded page limit");
}

async function checkTag(context) {
  const ref = await api(`git/ref/tags/${context.tag}`, { optional: true });
  if (!ref) return;
  let object = ref.object;
  for (let depth = 0; object?.type === "tag" && depth < 5; depth++) object = (await api(`git/tags/${object.sha}`)).object;
  if (object?.type !== "commit" || object.sha !== context.sha) throw new Error("Existing tag targets a different source commit; it will not be changed");
}

async function verifyRemoteAsset(remote, local) {
  if (!Number.isSafeInteger(remote.id) || remote.id < 1) throw new Error("Invalid release asset identifier");
  const response = await api(`releases/assets/${remote.id}`, { binary: true });
  const digest = createHash("sha256"); let received = 0;
  try {
    for await (const chunk of response.body) {
      received += chunk.length;
      if (received > local.size) throw new Error("Remote release asset is larger than the verified build");
      digest.update(chunk);
    }
  } finally { if (!response.body.locked) await response.body.cancel(); }
  if (received !== local.size || digest.digest("hex") !== local.sha256) throw new Error("Remote draft asset checksum does not match; no files were overwritten");
}

async function publish(context) {
  await sourceGate(context, false);
  const assets = await verifyReleaseAssets(path.join(repositoryRoot, "apps/desktop/release/publish-assets"), context);
  const body = await releaseBody(repositoryRoot, context);
  await checkTag(context);
  let release = await existingRelease(context.tag);
  if (release) validateRetryDraft(release, context, body, assets);
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-release-notes-"));
  try {
    const notes = path.join(temporary, "release.md"); await fs.writeFile(notes, body, { mode: 0o600 });
    if (!release) {
      command("gh", ["release", "create", context.tag, "--repo", context.repository, "--target", context.sha, "--title", `gogosling ${context.version}`, "--notes-file", notes, "--prerelease", "--draft", "--latest=false"]);
      release = await existingRelease(context.tag);
      if (!release) throw new Error("Created draft could not be read back");
      validateRetryDraft(release, context, body, assets);
    }
    // 重试只补不存在的资产；已有资产须逐字节核对，冲突或额外文件一律保留并停止。
    for (const local of assets) {
      release = await existingRelease(context.tag);
      if (!release) throw new Error("Release draft disappeared before upload");
      validateRetryDraft(release, context, body, assets);
      const remote = release.assets.find(asset => asset.name === local.name);
      if (remote) await verifyRemoteAsset(remote, local);
      else command("gh", ["release", "upload", context.tag, local.file, "--repo", context.repository]);
    }
    release = await existingRelease(context.tag);
    if (!release) throw new Error("Release draft disappeared before verification");
    validateRetryDraft(release, context, body, assets);
    if (release.assets.length !== assets.length) throw new Error("Release draft is missing an asset");
    for (const local of assets) await verifyRemoteAsset(release.assets.find(asset => asset.name === local.name), local);
    await checkTag(context);
    await assertCI(context);
    release = await existingRelease(context.tag);
    if (!release) throw new Error("Release draft disappeared before publication");
    validateRetryDraft(release, context, body, assets);
    if (release.assets.length !== assets.length) throw new Error("Release draft changed before publication");
    command("gh", ["release", "edit", context.tag, "--repo", context.repository, "--draft=false", "--prerelease", "--latest=false"]);
    const published = await existingRelease(context.tag);
    if (!published || published.draft || !published.prerelease || published.target_commitish !== context.sha || published.author?.login !== "github-actions[bot]") throw new Error("Published release metadata could not be confirmed");
    console.log(`Published https://github.com/${context.repository}/releases/tag/${context.tag}`);
  } finally { await fs.rm(temporary, { recursive: true, force: true }); }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const run = async () => {
    const context = validateReleaseContext(process.env);
    if (process.argv[2] === "gate") { await sourceGate(context, process.argv.includes("--arm64")); console.log("Release source and exact-commit CI verified."); }
    else if (process.argv[2] === "prepare") await prepare(context);
    else if (process.argv[2] === "publish") await publish(context);
    else throw new Error("Unknown release command");
  };
  run().catch(error => { console.error(error.message); process.exitCode = 1; });
}
