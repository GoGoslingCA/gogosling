import type { PreviewBlock } from "./types";

export const DOCUMENT_PAGE_BLOCKS = 100;
export const DOCUMENT_PAGE_CELLS = 1_000;
const TABLE_PAGE_ROWS = 100;

export interface TablePage { start: number; end: number; columnStart?: number; columnEnd?: number }

/** 每页限制单元格总数；极宽的单行也分段展示，不丢弃任何列。 */
export function tablePages(rows: string[][]): TablePage[] {
  const pages: TablePage[] = [];
  let start = 0;
  let cells = 0;
  const flush = (end: number) => {
    if (end > start) pages.push({ start, end });
    start = end;
    cells = 0;
  };
  rows.forEach((row, index) => {
    const size = Math.max(1, row.length);
    if (size > DOCUMENT_PAGE_CELLS) {
      flush(index);
      for (let columnStart = 0; columnStart < row.length; columnStart += DOCUMENT_PAGE_CELLS) {
        pages.push({ start: index, end: index + 1, columnStart, columnEnd: columnStart + DOCUMENT_PAGE_CELLS });
      }
      start = index + 1;
    } else {
      if (cells + size > DOCUMENT_PAGE_CELLS || index - start >= TABLE_PAGE_ROWS) flush(index);
      cells += size;
    }
  });
  flush(rows.length);
  return pages.length ? pages : [{ start: 0, end: 0 }];
}

/** 单个文档页同时受段落与表格单元格预算约束，避免许多小表格挤在同一页。 */
export function documentPages(blocks: PreviewBlock[]) {
  const pages: { start: number; end: number }[] = [];
  let start = 0;
  let cells = 0;
  blocks.forEach((block, index) => {
    // 按表格任意一页的上限预留预算，切换表格页后总 DOM 仍然有界。
    const size = block.kind === "table"
      ? Math.min(DOCUMENT_PAGE_CELLS, (block.rows ?? []).reduce((count, row) => count + Math.max(1, row.length), 0))
      : 0;
    if (index > start && (index - start >= DOCUMENT_PAGE_BLOCKS || cells + size > DOCUMENT_PAGE_CELLS)) {
      pages.push({ start, end: index });
      start = index;
      cells = 0;
    }
    cells += size;
  });
  if (start < blocks.length) pages.push({ start, end: blocks.length });
  return pages.length ? pages : [{ start: 0, end: 0 }];
}
