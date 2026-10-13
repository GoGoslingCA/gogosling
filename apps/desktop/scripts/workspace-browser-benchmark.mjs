// 独立桌面基准：配置在子进程退出后清理，报告不包含用户路径、网址正文或进程参数。
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const args = process.argv.slice(2);
const output = args[args.indexOf("--output") + 1];
if (!args.includes("--output") || !output) throw new Error("Pass --output with a JSON report destination");
const profile = mkdtempSync(join(tmpdir(), "gogosling-browser-performance-"));
try {
  const electron = createRequire(import.meta.url)("electron");
  const result = spawnSync(electron, [join(dirname(fileURLToPath(import.meta.url)), "workspace-browser-benchmark.cjs"), ...args], {
    env: { ...process.env, ELECTRON_RUN_AS_NODE: undefined, GOGOSLING_BROWSER_TEST_PROFILE: profile, GOGOSLING_BROWSER_PERFORMANCE_OUTPUT: output },
    stdio: "inherit", timeout: 180_000,
  });
  process.exitCode = result.status ?? 1;
} finally { rmSync(profile, { recursive: true, force: true }); }
