// 在子进程退出后清理原生测试配置，避免与 Chromium 关闭缓存的过程竞争。
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const profile = mkdtempSync(join(tmpdir(), "gogosling-web-runtime-"));
try {
  const electron = createRequire(import.meta.url)("electron");
  const modes = process.argv.length > 2 ? [process.argv.slice(2)] : [[], ["--quit-empty"], ["--quit-confirm"]];
  for (const mode of modes) {
    const result = spawnSync(electron, [join(dirname(fileURLToPath(import.meta.url)), "workspace-browser-runtime.cjs"), ...mode], {
      env: { ...process.env, ELECTRON_RUN_AS_NODE: undefined, GOGOSLING_BROWSER_TEST_PROFILE: profile },
      stdio: "inherit", timeout: 60_000,
    });
    process.exitCode = result.status ?? 1;
    if (process.exitCode !== 0) break;
  }
} finally { rmSync(profile, { recursive: true, force: true }); }
