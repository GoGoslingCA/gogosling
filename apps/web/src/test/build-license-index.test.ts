// @vitest-environment node
import { afterEach, expect, it } from "vitest";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import os from "node:os";
import { createBuildLicenseIndex } from "../../build-license-index";

const roots: string[] = [];
function fixture() {
  const root = mkdtempSync(path.join(os.tmpdir(), "gogosling-web-notices-")); roots.push(root);
  const write = (relative: string, value: string | object) => {
    const file = path.join(root, relative); mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, typeof value === "string" ? value : JSON.stringify(value)); return file;
  };
  write("package-lock.json", { packages: { "node_modules/@scope/reader": { version: "1.2.3" }, "node_modules/pdfjs-dist": { version: "4.5.6" }, "node_modules/vite": { version: "7.8.9" } } });
  write("node_modules/@scope/reader/package.json", { name: "@scope/reader", version: "1.2.3" });
  write("node_modules/@scope/reader/LICENSE", "Synthetic license preserved verbatim.\n");
  write("node_modules/@scope/reader/dist/es/package.json", { type: "module" });
  const reader = write("node_modules/@scope/reader/dist/es/index.js", "export const value = 1;");
  write("node_modules/pdfjs-dist/package.json", { name: "pdfjs-dist", version: "4.5.6" });
  write("node_modules/pdfjs-dist/LICENSE", "Synthetic PDF root license.\n");
  write("node_modules/pdfjs-dist/wasm/LICENSE_DECODER", "Synthetic decoder license.\n");
  return { root, reader, write };
}
afterEach(() => { for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true }); });

it("只记录实际 chunk 依赖及独立 PDF 资源，不输出绝对路径", () => {
  const { root, reader, write } = fixture();
  write("node_modules/unused-native/package.json", { name: "unused-native", version: "9.0.0" });
  const index = createBuildLicenseIndex(root, [reader, reader, "\0virtual-module"], ["pdf-assets/LICENSE", "pdf-assets/wasm/LICENSE_DECODER"]);
  expect(index.packages.map(pkg => pkg.name)).toEqual(["@scope/reader", "pdfjs-dist"]);
  expect(index.packages[0]?.lockPath).toBe("node_modules/@scope/reader");
  expect(index.packages[1]?.notices).toEqual(["LICENSE", "wasm/LICENSE_DECODER"]);
  expect(JSON.stringify(index)).not.toContain(root);
});

it("已安装依赖与锁文件不一致时阻止构建", () => {
  const { root, reader, write } = fixture();
  write("node_modules/@scope/reader/package.json", { name: "@scope/reader", version: "9.0.0" });
  expect(() => createBuildLicenseIndex(root, [reader], [])).toThrow(/package-lock/);
});

it("只在实际分发 Vite 运行时帮助模块时包含其许可", () => {
  const { root, reader, write } = fixture();
  write("node_modules/vite/package.json", { name: "vite", version: "7.8.9" });
  write("node_modules/vite/LICENSE.md", "Synthetic runtime helper license.\n");
  expect(createBuildLicenseIndex(root, [reader], []).packages.map(pkg => pkg.name)).not.toContain("vite");
  const index = createBuildLicenseIndex(root, [reader, "\0vite/preload-helper.js", "\0vite/modulepreload-polyfill.js"], []);
  expect(index.packages.filter(pkg => pkg.name === "vite")).toHaveLength(1);
});

it("拒绝非法包名和试图逃逸的 PDF 声明路径", () => {
  const { root, reader, write } = fixture();
  expect(() => createBuildLicenseIndex(root, [reader], ["pdf-assets/../../LICENSE"])).toThrow(/PDF notice/);
  write("node_modules/@scope/reader/package.json", { name: "../../outside", version: "1.2.3" });
  expect(() => createBuildLicenseIndex(root, [reader], [])).toThrow(/identity/);
});

it("缺失上游许可时阻止构建而不是生成空声明", () => {
  const { root, reader } = fixture();
  rmSync(path.join(root, "node_modules/@scope/reader/LICENSE"));
  expect(() => createBuildLicenseIndex(root, [reader], [])).toThrow(/no license/);
});
