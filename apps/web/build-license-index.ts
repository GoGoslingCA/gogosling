// 由最终 chunk 和随包 PDF 资源生成分发索引；不扫描未打包的可选 Node 依赖。
import { existsSync, readFileSync, readdirSync, realpathSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";

const noticeName = /^(?:licen[sc]es?|notice|copying)(?:$|[._-])/i;
const packageName = /^(?:@[a-z0-9._-]+\/)?[a-z0-9._-]+$/i;
function inside(root: string, file: string): boolean {
  const relative = path.relative(root, file);
  return relative === "" || (!relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative));
}
function packageAt(root: string, module: string): string | null {
  const file = module.split("?")[0]!;
  if (!path.isAbsolute(file) || !file.split(path.sep).includes("node_modules")) return null;
  let directory = path.dirname(realpathSync(file));
  if (!inside(root, directory)) throw new Error("Bundled dependency escapes the repository");
  while (inside(root, directory)) {
    if (existsSync(path.join(directory, "package.json"))) {
      const metadata = JSON.parse(readFileSync(path.join(directory, "package.json"), "utf8")) as { name?: string; version?: string };
      // 某些包在 dist/es 中仅放置 type=module 标记；它不是独立 npm 包。
      if (metadata.name && metadata.version) return directory;
    }
    if (directory === root) break;
    directory = path.dirname(directory);
  }
  throw new Error("Bundled dependency package metadata is missing");
}
function rootNotices(directory: string, relative = ""): string[] {
  return readdirSync(path.join(directory, relative), { withFileTypes: true }).flatMap(entry => {
    if (!relative && !noticeName.test(entry.name)) return [];
    const file = path.join(relative, entry.name);
    if (entry.isDirectory()) return rootNotices(directory, file);
    if (entry.isFile()) return [file.split(path.sep).join("/")];
    throw new Error("Distribution notices must be regular files");
  });
}

// 索引只含锁文件相对位置和包身份；供桌面构建再次校验后复制上游原文。
export function createBuildLicenseIndex(repositoryRoot: string, moduleIds: Iterable<string>, pdfNoticeAssets: string[]) {
  const root = realpathSync(repositoryRoot);
  const lock = JSON.parse(readFileSync(path.join(root, "package-lock.json"), "utf8")) as { packages?: Record<string, { version?: string }> };
  const directories = new Set<string>();
  for (const module of moduleIds) {
    // 这两个 Vite 虚拟模块会把运行时帮助代码写入最终浏览器 chunk。
    if (module === "\0vite/preload-helper.js" || module === "\0vite/modulepreload-polyfill.js") {
      directories.add(realpathSync(path.join(root, "node_modules", "vite")));
      continue;
    }
    const directory = packageAt(root, module); if (directory) directories.add(directory);
  }
  // pdf-assets 由单独插件复制，不能仅依赖 JS chunk 的 module 列表。
  if (pdfNoticeAssets.length) directories.add(realpathSync(path.join(root, "node_modules", "pdfjs-dist")));
  const packages = [...directories].map(directory => {
    if (!inside(root, directory)) throw new Error("Bundled dependency escapes the repository");
    const pkg = JSON.parse(readFileSync(path.join(directory, "package.json"), "utf8")) as { name?: string; version?: string };
    const lockPath = path.relative(root, directory).split(path.sep).join("/");
    if (!packageName.test(pkg.name ?? "") || !/^[a-z0-9.+-]+$/i.test(pkg.version ?? "")) throw new Error("Invalid bundled dependency identity");
    if (lock.packages?.[lockPath]?.version !== pkg.version) throw new Error(`Bundled dependency differs from package-lock.json: ${pkg.name}`);
    const notices = rootNotices(directory);
    if (pkg.name === "pdfjs-dist") for (const asset of pdfNoticeAssets) {
      if (!/^pdf-assets\/(?:[a-z0-9_-]+\/)?(?:LICENSE|LICENCE|COPYING|NOTICE)[a-z0-9_.-]*$/i.test(asset)) throw new Error("Invalid PDF notice asset");
      const relative = asset.slice("pdf-assets/".length), file = realpathSync(path.join(directory, relative));
      if (!inside(directory, file) || !statSync(file).isFile()) throw new Error("PDF notice asset escapes its package");
      notices.push(relative);
    }
    if (!notices.length) throw new Error(`Bundled dependency has no license: ${pkg.name}`);
    return { name: pkg.name!, version: pkg.version!, lockPath, notices: [...new Set(notices)].sort() };
  }).sort((a, b) => a.lockPath.localeCompare(b.lockPath));
  if (!packages.length) throw new Error("No bundled dependency license metadata was generated");
  return { schema: 1, packages };
}

export function buildLicenseIndex(): Plugin {
  const root = fileURLToPath(new URL("../../", import.meta.url));
  return {
    name: "bundled-license-index", apply: "build", enforce: "post",
    generateBundle(_options, bundle) {
      const modules = Object.values(bundle).flatMap(output => output.type === "chunk" ? Object.keys(output.modules) : []);
      const pdfNotices = Object.keys(bundle).filter(file => file.startsWith("pdf-assets/") && noticeName.test(path.basename(file)));
      this.emitFile({ type: "asset", fileName: "licenses/bundled-dependencies.json", source: JSON.stringify(createBuildLicenseIndex(root, modules, pdfNotices), null, 2) + "\n" });
    },
  };
}
