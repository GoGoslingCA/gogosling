import * as zlib from "node:zlib";
import { describe, expect, it } from "vitest";
import { crc32, encodePng, renderTrayGlyph, trayIconPng } from "../src/main/png.js";

interface Chunk {
  type: string;
  data: Buffer;
  crcOk: boolean;
}

function readChunks(png: Buffer): Chunk[] {
  const chunks: Chunk[] = [];
  let offset = 8;
  while (offset < png.length) {
    const length = png.readUInt32BE(offset);
    const type = png.subarray(offset + 4, offset + 8).toString("ascii");
    const data = png.subarray(offset + 8, offset + 8 + length);
    const crc = png.readUInt32BE(offset + 8 + length);
    chunks.push({ type, data, crcOk: crc === crc32(png.subarray(offset + 4, offset + 8 + length)) });
    offset += 12 + length;
  }
  return chunks;
}

describe("crc32", () => {
  it("与标准向量一致", () => {
    expect(crc32(Buffer.from(""))).toBe(0);
    expect(crc32(Buffer.from("123456789"))).toBe(0xcbf43926);
    expect(crc32(Buffer.from("IEND"))).toBe(0xae426082);
  });
});

describe("encodePng", () => {
  it("输出带签名、IHDR/IDAT/IEND 且 CRC 正确的 PNG", () => {
    const w = 3;
    const h = 2;
    const rgba = new Uint8Array(w * h * 4).map((_, i) => (i * 37) % 256);
    const png = encodePng(w, h, rgba);

    expect(png.subarray(0, 8)).toEqual(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
    const chunks = readChunks(png);
    expect(chunks.map((c) => c.type)).toEqual(["IHDR", "IDAT", "IEND"]);
    expect(chunks.every((c) => c.crcOk)).toBe(true);

    const ihdr = chunks[0]!.data;
    expect(ihdr.readUInt32BE(0)).toBe(w);
    expect(ihdr.readUInt32BE(4)).toBe(h);
    expect([ihdr[8], ihdr[9], ihdr[10], ihdr[11], ihdr[12]]).toEqual([8, 6, 0, 0, 0]);

    const raw = zlib.inflateSync(chunks[1]!.data);
    expect(raw.length).toBe((1 + w * 4) * h);
    for (let y = 0; y < h; y++) {
      expect(raw[y * (1 + w * 4)]).toBe(0);
      expect(raw.subarray(y * (1 + w * 4) + 1, (y + 1) * (1 + w * 4))).toEqual(Buffer.from(rgba.subarray(y * w * 4, (y + 1) * w * 4)));
    }
  });

  it("像素长度不匹配或尺寸非法时抛错", () => {
    expect(() => encodePng(2, 2, new Uint8Array(3))).toThrow(RangeError);
    expect(() => encodePng(0, 2, new Uint8Array(0))).toThrow(RangeError);
    expect(() => encodePng(1.5, 2, new Uint8Array(12))).toThrow(RangeError);
  });
});

describe("renderTrayGlyph", () => {
  const px = (rgba: Uint8Array, size: number, x: number, y: number) => {
    const i = (y * size + x) * 4;
    return [rgba[i]!, rgba[i + 1]!, rgba[i + 2]!, rgba[i + 3]!] as const;
  };

  it("中心不透明、角落透明、眼睛处镂空", () => {
    const size = 32;
    const rgba = renderTrayGlyph(size, { template: false });
    expect(px(rgba, size, 15, 15)[3]).toBe(255);
    expect(px(rgba, size, 0, 0)[3]).toBe(0);
    expect(px(rgba, size, 31, 31)[3]).toBe(0);
    // 眼睛中心（偏右上）
    const eyeX = Math.round((size - 1) / 2 + size * 0.16);
    const eyeY = Math.round((size - 1) / 2 - size * 0.16);
    expect(px(rgba, size, eyeX, eyeY)[3]).toBe(0);
  });

  it("模板图只用黑色，彩色图使用主题色", () => {
    const template = renderTrayGlyph(16, { template: true });
    expect(px(template, 16, 8, 8).slice(0, 3)).toEqual([0, 0, 0]);
    const colored = renderTrayGlyph(16, { template: false });
    expect(px(colored, 16, 8, 8).slice(0, 3)).toEqual([0x2f, 0x6f, 0xed]);
  });

  it("尺寸过小时抛错；trayIconPng 产出合法 PNG", () => {
    expect(() => renderTrayGlyph(4, { template: true })).toThrow(RangeError);
    const png = trayIconPng(16, { template: true });
    const chunks = readChunks(png);
    expect(chunks.every((c) => c.crcOk)).toBe(true);
    expect(chunks[0]!.data.readUInt32BE(0)).toBe(16);
  });
});
