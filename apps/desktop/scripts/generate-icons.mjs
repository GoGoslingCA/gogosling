// 将已有品牌图片转换为系统图标；不改变图案。此维护脚本只在 macOS 上手动执行。
import * as fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { desktopRoot, repositoryRoot, run } from "./runtime-lib.mjs";

if (process.platform !== "darwin") throw new Error("Icon regeneration requires macOS sips and iconutil; committed icons work on every build host");
const source = path.join(repositoryRoot, "apps", "web", "public", "brand", "gosli.png");
const output = path.join(desktopRoot, "build");
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "gogosling-icons-"));
try {
  const iconset = path.join(temporary, "Gogosling.iconset");
  await fs.mkdir(iconset); await fs.mkdir(output, { recursive: true });
  const pngs = new Map();
  for (const size of [16, 24, 32, 48, 64, 128, 256, 512, 1024]) {
    const file = path.join(temporary, `${size}.png`);
    run("/usr/bin/sips", ["-z", String(size), String(size), source, "--out", file]);
    pngs.set(size, await fs.readFile(file));
  }
  for (const size of [16, 32, 128, 256, 512]) {
    await fs.writeFile(path.join(iconset, `icon_${size}x${size}.png`), pngs.get(size));
    await fs.writeFile(path.join(iconset, `icon_${size}x${size}@2x.png`), pngs.get(size * 2));
  }
  run("/usr/bin/iconutil", ["-c", "icns", iconset, "-o", path.join(output, "icon.icns")]);
  await fs.writeFile(path.join(output, "icon.png"), pngs.get(512));
  // ICO 用标准目录表封装 PNG 图层；256 在一字节宽高字段中记为零。
  const sizes = [16, 24, 32, 48, 64, 128, 256];
  const header = Buffer.alloc(6 + 16 * sizes.length);
  header.writeUInt16LE(1, 2); header.writeUInt16LE(sizes.length, 4);
  let offset = header.length;
  for (const [index, size] of sizes.entries()) {
    const entry = 6 + index * 16, bytes = pngs.get(size);
    header[entry] = size === 256 ? 0 : size; header[entry + 1] = header[entry];
    header.writeUInt16LE(1, entry + 4); header.writeUInt16LE(32, entry + 6);
    header.writeUInt32LE(bytes.length, entry + 8); header.writeUInt32LE(offset, entry + 12);
    offset += bytes.length;
  }
  await fs.writeFile(path.join(output, "icon.ico"), Buffer.concat([header, ...sizes.map(size => pngs.get(size))]));
  console.log("Generated native icons from the bundled Gosli brand image.");
} finally { await fs.rm(temporary, { recursive: true, force: true }); }
