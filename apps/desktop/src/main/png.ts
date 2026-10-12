// 极简 PNG 编码器与托盘图标绘制。
//
// 托盘需要一张位图，而 nativeImage 不接受 SVG；与其把二进制图片提交进仓库，不如在运行时
// 画一个几十像素的图形并编码成 PNG（标准库 zlib 即可），顺便让图标逻辑可测试。
// 发行版的应用图标（dmg/nsis/AppImage 用）仍需设计稿，见 electron-builder.yml 的 buildResources。

import * as zlib from "node:zlib";

const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

const CRC_TABLE: Uint32Array = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
})();

/** PNG 块使用的 CRC-32（与 zlib.crc32 相同多项式）。 */
export function crc32(data: Uint8Array): number {
  let c = 0xffffffff;
  for (let i = 0; i < data.length; i++) c = (CRC_TABLE[(c ^ data[i]!) & 0xff]! ^ (c >>> 8)) >>> 0;
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type: string, body: Uint8Array): Buffer {
  const typeBytes = Buffer.from(type, "ascii");
  const length = Buffer.alloc(4);
  length.writeUInt32BE(body.length, 0);
  const crcInput = Buffer.concat([typeBytes, body]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(crcInput), 0);
  return Buffer.concat([length, typeBytes, body, crc]);
}

/**
 * 把 RGBA 像素（行优先，每像素 4 字节）编码为 8 位真彩 + alpha 的 PNG。
 * rgba.length 必须等于 width*height*4。
 */
export function encodePng(width: number, height: number, rgba: Uint8Array): Buffer {
  if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0) {
    throw new RangeError("png: 宽高必须是正整数");
  }
  if (rgba.length !== width * height * 4) {
    throw new RangeError(`png: 像素数据长度 ${rgba.length} 与 ${width}x${height} RGBA 不符`);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // colour type: RGBA
  ihdr[10] = 0; // compression
  ihdr[11] = 0; // filter
  ihdr[12] = 0; // interlace

  // 每行前置一个过滤类型字节（0 = None）。
  const stride = width * 4;
  const raw = Buffer.alloc((stride + 1) * height);
  for (let y = 0; y < height; y++) {
    raw[y * (stride + 1)] = 0;
    raw.set(rgba.subarray(y * stride, (y + 1) * stride), y * (stride + 1) + 1);
  }
  const idat = zlib.deflateSync(raw, { level: 9 });
  return Buffer.concat([PNG_SIGNATURE, chunk("IHDR", ihdr), chunk("IDAT", idat), chunk("IEND", new Uint8Array(0))]);
}

export interface GlyphOptions {
  /** true：黑色带透明度（macOS 模板图，由系统按菜单栏明暗着色）；false：彩色实心图。 */
  template: boolean;
}

/** 托盘图标用的彩色（非模板）前景色。 */
export const TRAY_ACCENT: readonly [number, number, number] = [0x2f, 0x6f, 0xed];

/**
 * 绘制 size×size 的托盘图标：一个实心圆（抗锯齿边缘）里挖出一个偏右上的小圆，
 * 像一只侧视的鹅头。模板图只用 alpha 通道表达形状。
 */
export function renderTrayGlyph(size: number, opts: GlyphOptions): Uint8Array {
  if (!Number.isInteger(size) || size < 8) throw new RangeError("png: 图标尺寸至少 8 像素");
  const rgba = new Uint8Array(size * size * 4);
  const center = (size - 1) / 2;
  const radius = size * 0.42;
  const eyeX = center + size * 0.16;
  const eyeY = center - size * 0.16;
  const eyeRadius = size * 0.1;
  const [r, g, b] = opts.template ? [0, 0, 0] : TRAY_ACCENT;

  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const d = Math.hypot(x - center, y - center);
      let coverage = Math.min(1, Math.max(0, radius - d + 0.5));
      const de = Math.hypot(x - eyeX, y - eyeY);
      const hole = Math.min(1, Math.max(0, eyeRadius - de + 0.5));
      coverage *= 1 - hole;
      const i = (y * size + x) * 4;
      rgba[i] = r;
      rgba[i + 1] = g;
      rgba[i + 2] = b;
      rgba[i + 3] = Math.round(coverage * 255);
    }
  }
  return rgba;
}

/** 生成某个尺寸的托盘图标 PNG。 */
export function trayIconPng(size: number, opts: GlyphOptions): Buffer {
  return encodePng(size, size, renderTrayGlyph(size, opts));
}
