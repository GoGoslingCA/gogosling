import { describe, expect, it } from "vitest";
import { prettyJson, previewJson } from "./json";

describe("工具参数有界摘要", () => {
  it("短参数保留可读键值、数组和转义字符", () => {
    expect(previewJson({ path: "a.md", lines: [1, 2], value: null })).toBe('{"path": "a.md", "lines": [1, 2], "value": null}');
    expect(previewJson({ line: "a\nb" })).toContain('"a\\nb"');
  });
  it("长值只读取摘要所需字段，完整显示仍保留末尾内容", () => {
    let laterReads = 0;
    const args = { text: "x".repeat(500_000), get later() { laterReads++; return "完整末尾"; } };
    const preview = previewJson(args);
    expect(preview.length).toBeLessThanOrEqual(70);
    expect(preview.endsWith("…")).toBe(true);
    expect(laterReads).toBe(0);
    expect(prettyJson(args)).toContain("完整末尾");
    expect(laterReads).toBe(1);
  });
  it("循环、极深结构和 emoji 不能使摘要阻塞或留下不成对的代理字符", () => {
    const value: Record<string, unknown> = {}; value.self = value;
    expect(previewJson(value)).toBe('{"self": …');
    let deep: unknown = "end";
    for (let index = 0; index < 1000; index++) deep = [deep];
    expect(previewJson(deep).length).toBeLessThan(70);
    const emoji = previewJson({ text: "😊".repeat(100) });
    expect(emoji).not.toMatch(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/);
  });
});
