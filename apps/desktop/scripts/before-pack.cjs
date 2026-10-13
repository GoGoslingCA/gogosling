// 打包器即使被单独调用，也不能混入不同平台/架构或未通过验证的运行时。
const fs = require("node:fs/promises");
const path = require("node:path");
const { Arch } = require("builder-util");

module.exports = async function beforePack(context) {
  const root = path.resolve(__dirname, "../resources/runtime");
  const manifest = JSON.parse(await fs.readFile(path.join(root, "runtime-manifest.json"), "utf8"));
  if (manifest.platform !== context.electronPlatformName || manifest.arch !== Arch[context.arch]) throw new Error("Staged runtime does not match the Electron target; build on a matching host");
  if (!manifest.verified?.relocated || !manifest.verified?.mock_inference || !manifest.verified?.daemon_worker_ready) throw new Error("Stage and verify the runtime before packaging");
  const daemon = path.join(root, "bin", context.electronPlatformName === "win32" ? "gogoslingd.exe" : "gogoslingd");
  if (!(await fs.stat(daemon)).isFile()) throw new Error("Bundled daemon is missing");
};
