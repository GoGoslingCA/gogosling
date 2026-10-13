// 仅在构建机执行：固定版本运行时、锁定依赖、原生 Go 构建、重定位验证后才替换暂存产物。
import * as fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import { assertOutputDirectory, copyRelocatable, desktopRoot, nativeTarget, pythonRelative, pythonVersion, repositoryRoot, resourcesRoot, run, stagedRoot, within } from "./runtime-lib.mjs";
import { verifyRuntime } from "./verify-runtime.mjs";

async function collectLicenses(root, goEnv) {
  const licenseRoot = path.join(root, "licenses");
  await fs.mkdir(licenseRoot);
  await fs.copyFile(path.join(repositoryRoot, "LICENSE"), path.join(licenseRoot, "Gogosling-MIT.txt"));
  const modules = run("go", ["list", "-m", "-f", "{{.Path}}\t{{.Version}}\t{{.Dir}}", "all"], { env: goEnv });
  for (const row of modules.split("\n")) {
    const [module, version, directory] = row.split("\t");
    if (!directory || !version) continue;
    const output = path.join(licenseRoot, "go", `${module}@${version}`);
    for (const file of await fs.readdir(directory, { withFileTypes: true })) {
      if (!/^(licen[sc]e|copying|notice)/i.test(file.name)) continue;
      await fs.mkdir(output, { recursive: true });
      await fs.cp(path.join(directory, file.name), path.join(output, file.name), { recursive: true, dereference: true });
    }
  }
}

export async function stageRuntime() {
  const target = nativeTarget();
  if ((process.env.GOOS && process.env.GOOS !== target.goos) || (process.env.GOARCH && process.env.GOARCH !== target.goarch)) throw new Error("Cross-compilation is not supported by native runtime staging");
  if (target.platform === "linux" && !process.report.getReport().header.glibcVersionRuntime) throw new Error("Runtime staging currently requires a glibc Linux build host");
  await assertOutputDirectory();
  const temporary = await fs.mkdtemp(path.join(resourcesRoot, ".runtime-stage-"));
  const output = path.join(temporary, "bundle");
  try {
    await fs.mkdir(path.join(output, "bin"), { recursive: true });
    const managed = path.join(temporary, "managed");
    const buildEnv = { ...process.env, UV_PYTHON_INSTALL_DIR: managed, UV_PYTHON_BIN_DIR: path.join(temporary, "unused-bin"), PYTHONDONTWRITEBYTECODE: "1" };
    delete buildEnv.UV_COMPILE_BYTECODE;
    console.log(`Staging ${target.platform}/${target.arch} CPython ${pythonVersion}…`);
    // 使用独立下载目录，绝不把开发机现有 site-packages 或虚拟环境打进安装包。
    run("uv", ["python", "install", "--no-bin", "--no-registry", pythonVersion], { env: buildEnv });
    const sourcePython = run("uv", ["python", "find", "--no-project", "--managed-python", "--resolve-links", pythonVersion], { env: buildEnv });
    const source = JSON.parse(run(sourcePython, ["-B", "-I", "-c", 'import sys,platform,json; print(json.dumps({"prefix":sys.prefix,"version":platform.python_version(),"machine":platform.machine()}))'], { env: buildEnv }));
    const machine = source.machine.toLowerCase();
    if (!within(managed, await fs.realpath(source.prefix)) || source.version !== pythonVersion || (target.arch === "arm64" ? !["arm64", "aarch64"].includes(machine) : !["amd64", "x86_64"].includes(machine))) throw new Error("Downloaded Python does not match the build target");
    const runtimeRoot = target.platform === "win32" ? path.join(output, "worker", "Scripts") : path.join(output, "worker");
    await copyRelocatable(source.prefix, runtimeRoot);
    const python = path.join(output, pythonRelative(target.platform));
    const purelib = run(python, ["-B", "-I", "-c", 'import sysconfig; print(sysconfig.get_path("purelib"))'], { env: buildEnv });
    if (!within(runtimeRoot, purelib)) throw new Error("Python package directory escapes the staged runtime");
    const requirements = path.join(temporary, "requirements.txt");
    run("uv", ["export", "--project", "worker", "--locked", "--no-dev", "--no-emit-project", "--no-header", "--no-annotate", "--format", "requirements.txt", "--output-file", requirements], { env: buildEnv });
    run("uv", ["pip", "install", "--python", python, "--target", purelib, "--require-hashes", "--only-binary", ":all:", "--no-deps", "--link-mode", "copy", "--requirements", requirements], { env: buildEnv });
    // --target 可能生成带绝对 shebang 的可选 CLI；工作进程只导入模块，不分发这些入口。
    for (const name of ["bin", "Scripts"]) await fs.rm(path.join(purelib, name), { recursive: true, force: true });
    // 模块可直接通过 -m 运行，不生成含本机构建路径的控制台脚本或 editable 引用。
    await fs.cp(path.join(repositoryRoot, "worker", "src", "gogosling_worker"), path.join(purelib, "gogosling_worker"), { recursive: true, filter: name => !name.split(path.sep).includes("__pycache__") });
    // 清理随 CPython 分发的开发工具入口；保留解释器、标准库和所有许可证。
    const bin = path.dirname(python);
    for (const file of await fs.readdir(bin, { withFileTypes: true })) {
      if (target.platform !== "win32" && !/^python(?:3(?:\.12)?)?$/.test(file.name)) await fs.rm(path.join(bin, file.name), { recursive: true, force: true });
    }
    // uv 会写入安装前缀；运行时重新定位时只保留动态前缀，不保留构建机路径。
    const stdlib = run(python, ["-B", "-I", "-c", 'import sysconfig; print(sysconfig.get_path("stdlib"))'], { env: buildEnv });
    for (const filename of await fs.readdir(stdlib)) {
      if (!filename.startsWith("_sysconfigdata_") || !filename.endsWith(".py")) continue;
      const file = path.join(stdlib, filename);
      let text = await fs.readFile(file, "utf8");
      for (const prefix of [source.prefix, JSON.stringify(source.prefix).slice(1, -1)]) text = text.replaceAll(prefix, "/gogosling-runtime");
      text += '\nimport sys as _gogosling_sys\nbuild_time_vars = {key: value.replace("/gogosling-runtime", _gogosling_sys.base_prefix) if isinstance(value, str) else value for key, value in build_time_vars.items()}\n';
      await fs.writeFile(file, text);
    }
    if (target.platform === "darwin") {
      const library = path.join(runtimeRoot, "lib", `libpython${pythonVersion.split(".").slice(0, 2).join(".")}.dylib`);
      run("/usr/bin/install_name_tool", ["-id", "@rpath/" + path.basename(library), library]);
      run("/usr/bin/codesign", ["--force", "--sign", "-", library]);
    }
    // 删除原始安装，防止重定位验证意外依赖下载目录。
    await fs.rm(managed, { recursive: true, force: true });
    const pkg = JSON.parse(await fs.readFile(path.join(desktopRoot, "package.json"), "utf8"));
    const commit = run("git", ["rev-parse", "HEAD"]);
    const dirty = run("git", ["status", "--porcelain", "--untracked-files=no"]) !== "";
    const builtAt = new Date(process.env.SOURCE_DATE_EPOCH ? Number(process.env.SOURCE_DATE_EPOCH) * 1000 : Date.now()).toISOString();
    const versionPackage = "github.com/GoGoslingCA/gogosling/internal/version";
    const goEnv = { ...process.env, GOOS: target.goos, GOARCH: target.goarch, CGO_ENABLED: "0" };
    const ldflags = `-s -w -X ${versionPackage}.Version=${pkg.version} -X ${versionPackage}.Commit=${commit}${dirty ? "-dirty" : ""} -X ${versionPackage}.Date=${builtAt}`;
    run("go", ["build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", path.join(output, "bin") + path.sep, "./cmd/gogoslingd", "./cmd/gogosling"], { env: goEnv });
    await collectLicenses(output, goEnv);
    const manifest = { schema: 1, platform: target.platform, arch: target.arch, app_version: pkg.version, python_version: pythonVersion, source_commit: commit, source_dirty: dirty, built_at: builtAt, worker_lock_sha256: createHash("sha256").update(await fs.readFile(path.join(repositoryRoot, "worker", "uv.lock"))).digest("hex") };
    await fs.writeFile(path.join(output, "runtime-manifest.json"), JSON.stringify(manifest, null, 2) + "\n");
    console.log("Verifying relocated runtime with an empty PATH and a local mock model…");
    async function checkBuildPaths(directory) {
      for (const entry of await fs.readdir(directory, { withFileTypes: true })) {
        const file = path.join(directory, entry.name);
        if (entry.isDirectory()) await checkBuildPaths(file);
        else if (entry.isFile()) {
          if (entry.name.endsWith(".pyc")) throw new Error("Bytecode must not retain staging paths");
          const bytes = await fs.readFile(file);
          for (const privatePath of [repositoryRoot, os.homedir()]) {
            if (bytes.includes(Buffer.from(privatePath)) || bytes.includes(Buffer.from(JSON.stringify(privatePath).slice(1, -1)))) throw new Error(`Build path found in ${path.relative(output, file)}`);
          }
        }
      }
    }
    await checkBuildPaths(output);
    const verified = await verifyRuntime(output);
    await fs.writeFile(path.join(output, "runtime-manifest.json"), JSON.stringify({ ...manifest, verified }, null, 2) + "\n");
    // 只有整套验证成功才替换固定暂存目录；失败保留上一次可用产物。
    await assertOutputDirectory();
    const backup = path.join(resourcesRoot, "runtime.previous");
    await fs.rm(backup, { recursive: true, force: true });
    let hadPrevious = false;
    try { await fs.rename(stagedRoot, backup); hadPrevious = true; } catch (error) { if (error.code !== "ENOENT") throw error; }
    try { await fs.rename(output, stagedRoot); } catch (error) { if (hadPrevious) await fs.rename(backup, stagedRoot); throw error; }
    await fs.rm(backup, { recursive: true, force: true });
    console.log(JSON.stringify({ staged: "apps/desktop/resources/runtime", ...verified }));
  } finally { await fs.rm(temporary, { recursive: true, force: true }); }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) stageRuntime().catch(error => { console.error(error.message); process.exitCode = 1; });
