// 发布边界的离线验证：不访问 GitHub，不上传或创建远端发布。
import test from "node:test";
import assert from "node:assert/strict";
import * as fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { assertSuccessfulCI, assetNames, compareAppTrees, existingRelease, releaseBody, releaseMarker, sha256, validateReleaseContext, validateRetryDraft, validateRuntimeManifest, validateVersions, verifyReleaseAssets, waitForReleaseState } from "./release.mjs";

const sha = "a".repeat(40);
const environment = { GITHUB_ACTIONS: "true", GITHUB_EVENT_NAME: "workflow_dispatch", GITHUB_REF: "refs/heads/main", GITHUB_REPOSITORY: "GoGoslingCA/gogosling", GITHUB_SHA: sha, RELEASE_TAG: "v0.0.1104-beta" };
const context = validateReleaseContext(environment);
function manifest() { return { schema: 1, source_commit: sha, source_dirty: false, app_version: context.version, platform: "darwin", arch: "arm64", verified: { relocated: true, isolated_path: true, mock_inference: true, daemon_worker_ready: true } }; }

test("release requires a manual main workflow, strict beta tag, and matching package versions", () => {
  for (const [field, value] of [["GITHUB_ACTIONS", "false"], ["GITHUB_EVENT_NAME", "pull_request"], ["GITHUB_REF", "refs/tags/v0.0.1104-beta"], ["GITHUB_REPOSITORY", "example/fork"], ["RELEASE_TAG", "v0.0.1104-beta;echo bad"], ["RELEASE_TAG", "../../notes"], ["RELEASE_TAG", "v00.0.1104-beta"], ["GITHUB_SHA", "main"]]) {
    assert.throws(() => validateReleaseContext({ ...environment, [field]: value }));
  }
  const pkg = { version: context.version }, lock = { packages: { "apps/desktop": pkg, "apps/web": pkg } };
  assert.doesNotThrow(() => validateVersions(context, pkg, pkg, lock));
  assert.throws(() => validateVersions(context, pkg, { version: "0.0.1-beta" }, lock), /versions/);
  assert.throws(() => validateVersions(context, pkg, pkg, { packages: {} }), /versions/);
});

test("release CI gate rejects missing, foreign, pending, and latest failed runs", () => {
  const good = { id: 10, run_attempt: 1, head_sha: sha, head_branch: "main", event: "push", path: ".github/workflows/ci.yml", status: "completed", conclusion: "success" };
  assert.doesNotThrow(() => assertSuccessfulCI([good], context));
  for (const runs of [[], [{ ...good, head_sha: "b".repeat(40) }], [{ ...good, event: "pull_request" }], [{ ...good, path: ".github/workflows/other.yml" }], [{ ...good, status: "in_progress" }], [good, { ...good, id: 11, conclusion: "failure" }], [good, { ...good, run_attempt: 2, status: "queued", conclusion: null }]]) {
    assert.throws(() => assertSuccessfulCI(runs, context), /CI/);
  }
});

test("runtime identity rejects stale, dirty, wrong-platform, and unverified bundles", () => {
  assert.doesNotThrow(() => validateRuntimeManifest(manifest(), context));
  for (const override of [{ source_commit: "b".repeat(40) }, { source_dirty: true }, { source_dirty: "false" }, { app_version: "0.0.1-beta" }, { platform: "linux" }, { arch: "x64" }, { verified: { relocated: true } }]) {
    assert.throws(() => validateRuntimeManifest({ ...manifest(), ...override }, context));
  }
});

async function assetFixture() {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-release-assets-")));
  const [installer, metadata, checksums] = assetNames(context);
  const writeSums = async () => fs.writeFile(path.join(root, checksums), `${await sha256(path.join(root, installer))}  ${installer}\n${await sha256(path.join(root, metadata))}  ${metadata}\n`);
  await fs.writeFile(path.join(root, installer), "Synthetic installer bytes for offline checksum testing.");
  await fs.writeFile(path.join(root, metadata), JSON.stringify(manifest()));
  await writeSums();
  return { root, installer, metadata, checksums, writeSums };
}

test("release assets are an exact allowlist and every payload checksum must match", async () => {
  const { root, installer, checksums } = await assetFixture();
  try {
    assert.equal((await verifyReleaseAssets(root, context)).length, 3);
    await fs.writeFile(path.join(root, "private-notes.txt"), "Synthetic unwanted file");
    await assert.rejects(() => verifyReleaseAssets(root, context), /exactly/);
    await fs.rm(path.join(root, "private-notes.txt"));
    await fs.appendFile(path.join(root, installer), "changed");
    await assert.rejects(() => verifyReleaseAssets(root, context), /checksum mismatch/);
    await fs.writeFile(path.join(root, checksums), `${"0".repeat(64)}  ../../outside\n`);
    await assert.rejects(() => verifyReleaseAssets(root, context), /checksum entry/);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test("a rehashed artifact still cannot substitute an older or dirty manifest", async () => {
  const { root, metadata, writeSums } = await assetFixture();
  try {
    await fs.writeFile(path.join(root, metadata), JSON.stringify({ ...manifest(), source_dirty: true }));
    await writeSums();
    await assert.rejects(() => verifyReleaseAssets(root, context), /manifest/);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test("disk image verification checks frontend and native bytes rather than only the manifest", async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-image-tree-test-"));
  const expected = path.join(root, "expected"), actual = path.join(root, "actual");
  try {
    for (const directory of [expected, actual]) {
      await fs.mkdir(path.join(directory, "Contents/Resources/web"), { recursive: true });
      await fs.writeFile(path.join(directory, "Contents/Resources/app.asar"), "Synthetic native bundle");
      await fs.writeFile(path.join(directory, "Contents/Resources/web/main.js"), "Synthetic UI bundle");
    }
    await compareAppTrees(expected, actual);
    await fs.writeFile(path.join(actual, "Contents/Resources/web/main.js"), "Changed UI bundle");
    await assert.rejects(() => compareAppTrees(expected, actual), /content differs/);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test("release links point at the immutable tag and cannot escape the repository", async () => {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-release-notes-test-")));
  try {
    await fs.mkdir(path.join(root, "docs/releases"), { recursive: true });
    await fs.writeFile(path.join(root, "CONTRIBUTING.md"), "Synthetic contribution guide");
    const notes = path.join(root, `docs/releases/${context.tag}.md`);
    await fs.writeFile(notes, "Read [Contributing](../../CONTRIBUTING.md). [Website](https://gogosling.ca)\n");
    const body = await releaseBody(root, context);
    assert.ok(body.includes(`https://github.com/GoGoslingCA/gogosling/blob/${context.tag}/CONTRIBUTING.md`));
    assert.ok(body.includes(releaseMarker(context))); assert.equal(body.includes(root), false);
    await fs.writeFile(notes, "[Outside](../../../LICENSE)\n");
    await assert.rejects(() => releaseBody(root, context), /escapes/);
    await fs.writeFile(notes, "[Invalid](javascript:alert)\n");
    await assert.rejects(() => releaseBody(root, context), /unsupported/);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test("draft retry requires bot ownership, identical source and notes, and no conflicting assets", () => {
  const body = `Synthetic release notes\n\n${releaseMarker(context)}\n`;
  const assets = [{ name: "file.dmg", size: 10, sha256: "c".repeat(64) }];
  const draft = { draft: true, prerelease: true, tag_name: context.tag, target_commitish: sha, author: { login: "github-actions[bot]" }, body, assets: [{ name: "file.dmg", size: 10, digest: `sha256:${"c".repeat(64)}`, state: "uploaded" }] };
  assert.doesNotThrow(() => validateRetryDraft(draft, context, body, assets));
  assert.doesNotThrow(() => validateRetryDraft({ ...draft, assets: [] }, context, body, assets));
  for (const override of [{ draft: false }, { target_commitish: "main" }, { author: { login: "synthetic-user" } }, { body: "Unrelated notes" }, { assets: [{ name: "external.txt" }] }, { assets: [{ ...draft.assets[0], size: 11 }] }, { assets: [{ ...draft.assets[0], state: "starter" }] }, { assets: [{ ...draft.assets[0], digest: `sha256:${"d".repeat(64)}` }] }]) {
    assert.throws(() => validateRetryDraft({ ...draft, ...override }, context, body, assets));
  }
  // 检查函数只读：遇到外部文件或失败上传时，不删除、改名或覆盖任何远端文件。
  assert.equal(draft.assets.length, 1); assert.equal(draft.assets[0].name, "file.dmg");
});

test("relative release images use raw tag URLs while document links keep the blob view", async () => {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-release-images-test-")));
  try {
    await fs.mkdir(path.join(root, "docs/releases"), { recursive: true });
    await fs.mkdir(path.join(root, "docs/assets"), { recursive: true });
    await fs.writeFile(path.join(root, "docs/assets/preview-performance.svg"), '<svg xmlns="http://www.w3.org/2000/svg"></svg>');
    await fs.writeFile(path.join(root, "CONTRIBUTING.md"), "Synthetic contribution guide");
    await fs.writeFile(path.join(root, `docs/releases/${context.tag}.md`), "![Local preview timing](../assets/preview-performance.svg)\n[Contributing](../../CONTRIBUTING.md)\n");
    const body = await releaseBody(root, context);
    assert.ok(body.includes(`![Local preview timing](https://raw.githubusercontent.com/${context.repository}/${context.tag}/docs/assets/preview-performance.svg)`));
    assert.ok(body.includes(`[Contributing](https://github.com/${context.repository}/blob/${context.tag}/CONTRIBUTING.md)`));
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test("draft lookup resolves its pending tag through GraphQL and reads the exact REST ID", async () => {
  const calls = [], draft = { id: 123, tag_name: context.tag, draft: true, assets: [] };
  const actual = await existingRelease(context.tag, {
    query: async tag => { calls.push(["pending-tag", tag]); return { databaseId: 123, tagName: tag, isDraft: true }; },
    read: async id => { calls.push(["release-id", id]); return draft; },
  });
  assert.equal(actual, draft);
  assert.deepEqual(calls, [["pending-tag", context.tag], ["release-id", 123]]);
  assert.equal(await existingRelease(context.tag, { query: async () => null, read: async () => { throw new Error("Absent tag must not trigger an ID read"); } }), null);
});

test("release lookup fails closed on API errors or a substituted release identity", async () => {
  await assert.rejects(() => existingRelease(context.tag, { query: async () => { throw new Error("Synthetic authorization failure"); } }), /authorization/);
  await assert.rejects(() => existingRelease(context.tag, { query: async () => ({ databaseId: "../outside", tagName: context.tag }), read: async () => null }), /unexpected release/);
  await assert.rejects(() => existingRelease(context.tag, { query: async () => ({ databaseId: 123, tagName: context.tag }), read: async () => ({ id: 456, tag_name: context.tag }) }), /identity differs/);
});

test("bounded readback waits for creation, uploaded assets, and final publication without repeating writes", async () => {
  const draft = { id: 123, draft: true, assets: [] };
  const uploaded = { ...draft, assets: [{ name: "file.dmg", state: "uploaded" }] };
  const published = { ...uploaded, draft: false };
  const scenarios = [
    { states: [null, null, draft], ready: value => value !== null },
    { states: [draft, draft, uploaded], ready: value => value?.assets?.some(asset => asset.name === "file.dmg" && asset.state === "uploaded") },
    { states: [uploaded, uploaded, published], ready: value => value?.draft === false },
  ];
  for (const scenario of scenarios) {
    let reads = 0; const waits = [];
    const result = await waitForReleaseState(async () => scenario.states[reads++], scenario.ready, { sleep: async delay => { waits.push(delay); } });
    assert.equal(result, scenario.states[2]); assert.equal(reads, 3); assert.deepEqual(waits, [250, 500]);
  }
});

test("readback stops at its limit and does not retry authorization failures", async () => {
  let reads = 0;
  await assert.rejects(() => waitForReleaseState(async () => { reads++; return null; }, value => value !== null, { sleep: async () => {} }), /bounded readback window/);
  assert.equal(reads, 7);
  reads = 0;
  await assert.rejects(() => waitForReleaseState(async () => { reads++; throw new Error("Synthetic forbidden response"); }, () => true, { sleep: async () => {} }), /forbidden/);
  assert.equal(reads, 1);
});
