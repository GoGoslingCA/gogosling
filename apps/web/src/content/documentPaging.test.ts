import { describe, expect, it } from "vitest";
import { DOCUMENT_PAGE_CELLS, documentPages, tablePages } from "./documentPaging";
import type { PreviewBlock } from "./types";

describe("文档 DOM 预算", () => {
  it("多个小表格共享单元格预算且每个原始块只出现一次", () => {
    const blocks: PreviewBlock[] = Array.from({ length: 70 }, () => ({ kind: "table", rows: Array.from({ length: 12 }, () => Array(8).fill("cell")) }));
    const pages = documentPages(blocks);
    expect(pages).toHaveLength(7);
    expect(pages.flatMap(page => blocks.slice(page.start, page.end))).toEqual(blocks);
    for (const page of pages) expect((page.end - page.start) * 96).toBeLessThanOrEqual(DOCUMENT_PAGE_CELLS);
  });

  it("表格翻页覆盖极宽单行、不规则行与空行且不丢失单元格", () => {
    const rows = [[], ["a"], Array.from({ length: 2101 }, (_, index) => `column-${index}`), ["b", "c"], ...Array.from({ length: 102 }, () => ["d"])];
    const pages = tablePages(rows);
    const shown = pages.flatMap(page => rows.slice(page.start, page.end).flatMap(row => row.slice(page.columnStart, page.columnEnd)));
    expect(shown).toEqual(rows.flat());
    for (const page of pages) {
      expect(page.end - page.start).toBeLessThanOrEqual(100);
      expect(rows.slice(page.start, page.end).reduce((count, row) => count + row.slice(page.columnStart, page.columnEnd).length, 0)).toBeLessThanOrEqual(DOCUMENT_PAGE_CELLS);
    }
  });

  it("表格后续页变大也不会挤占其他表格预算，普通文段仍按百段分页", () => {
    const rows = [["first"], Array(1000).fill("second")];
    expect(documentPages([{ kind: "table", rows }, { kind: "table", rows: [["last"]] }])).toHaveLength(2);
    expect(documentPages(Array.from({ length: 201 }, () => ({ kind: "paragraph", text: "text" })))).toEqual([{ start: 0, end: 100 }, { start: 100, end: 200 }, { start: 200, end: 201 }]);
    expect(tablePages([])).toEqual([{ start: 0, end: 0 }]);
    expect(documentPages([])).toEqual([{ start: 0, end: 0 }]);
  });
});
