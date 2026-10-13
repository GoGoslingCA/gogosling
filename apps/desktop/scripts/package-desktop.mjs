// 使用锁文件实际安装的 Electron 版本，避免工作区提升依赖时由 semver 范围猜测运行时。
import { createRequire } from "node:module";
import { desktopRoot, run } from "./runtime-lib.mjs";
const require = createRequire(import.meta.url);
const electronVersion = require("electron/package.json").version;
if (!/^\d+\.\d+\.\d+(?:-[\w.]+)?$/.test(electronVersion)) throw new Error("Installed Electron version must be exact");
run(process.execPath, [require.resolve("electron-builder/cli.js"), "--config", "electron-builder.yml", `--config.electronVersion=${electronVersion}`, "--publish", "never", ...process.argv.slice(2)], { cwd: desktopRoot, stdio: "inherit" });
