// 安全的 Markdown 子集：只生成 React 元素，原始 HTML 作为正文显示。
// 支持标题、段落、列表、引用、代码、表格和基础行内格式；不执行 HTML、图片或脚本。
import type { ReactNode } from "react";
import { CopyButton } from "./CopyButton";
import { ChartBlock, parseChart } from "./ChartBlock";
import "./Markdown.css";

/** 链接只允许明确的 HTTP(S)、mailto 或当前文档锚点，拒绝控制字符和其他协议。 */
export function safeMarkdownUrl(value: string): string | null {
  if (/[\s\u0000-\u001f\u007f]/u.test(value)) return null;
  if (value.startsWith("#")) return value;
  if (!/^(https?:\/\/|mailto:)/i.test(value)) return null;
  try {
    const url = new URL(value);
    return ["http:", "https:", "mailto:"].includes(url.protocol) ? url.href : null;
  } catch {
    return null;
  }
}

function inlinePattern() {
  return /`([^`\n]+)`|\*\*([^*\n]+)\*\*|__([^_\n]+)__|~~([^~\n]+)~~|\*([^*\n]+)\*|_([^_\n]+)_|(!?)\[([^\]\n]+)\]\(([^)\s]+)\)/g;
}

function inline(text: string, depth = 0): ReactNode {
  if (depth > 6) return text;
  const pattern = inlinePattern();
  const nodes: ReactNode[] = [];
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > cursor) nodes.push(text.slice(cursor, match.index));
    const key = match.index;
    if (match[1] !== undefined) nodes.push(<code key={key}>{match[1]}</code>);
    else if (match[2] !== undefined || match[3] !== undefined) nodes.push(<strong key={key}>{inline(match[2] ?? match[3] ?? "", depth + 1)}</strong>);
    else if (match[4] !== undefined) nodes.push(<del key={key}>{inline(match[4], depth + 1)}</del>);
    else if (match[5] !== undefined || match[6] !== undefined) nodes.push(<em key={key}>{inline(match[5] ?? match[6] ?? "", depth + 1)}</em>);
    else {
      const url = safeMarkdownUrl(match[9] ?? "");
      if (match[7] === "!" || !url) nodes.push(match[0]);
      else nodes.push(<a key={key} href={url} target={url.startsWith("#") ? undefined : "_blank"} rel="noopener noreferrer">{inline(match[8] ?? "", depth + 1)}</a>);
    }
    cursor = match.index + match[0].length;
  }
  if (cursor < text.length) nodes.push(text.slice(cursor));
  return nodes;
}

function tableCells(line: string): string[] {
  return line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((cell) => cell.trim());
}

function isTableSeparator(line: string): boolean {
  const cells = tableCells(line);
  return cells.length > 1 && cells.every((cell) => /^:?-{3,}:?$/.test(cell));
}

function startsBlock(lines: string[], index: number): boolean {
  const line = lines[index] ?? "";
  return /^\s*$|^ {0,3}(?:#{1,6}\s|>|[-+*]\s+|\d+[.)]\s+|`{3,}|~{3,})/.test(line)
    || /^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)
    || (line.includes("|") && isTableSeparator(lines[index + 1] ?? ""));
}

interface MarkdownPart { weight: number; characters: number; render: () => ReactNode }
const PREVIEW_PAGE_WEIGHT = 500;
const PREVIEW_PAGE_CHARACTERS = 20_000;
const PREVIEW_TABLE_ROWS = 100;
const PREVIEW_LIST_ITEMS = 100;

/** 不创建 React 节点，只计入行内标记的结构成本；超限后立即结束估算。 */
function inlineWeight(text: string, depth = 0): number {
  if (depth > 6) return 0;
  let weight = 0;
  for (const match of text.matchAll(inlinePattern())) {
    if (match[1] !== undefined) weight++;
    else if (match[2] !== undefined || match[3] !== undefined) weight += 1 + inlineWeight(match[2] ?? match[3] ?? "", depth + 1);
    else if (match[4] !== undefined) weight += 1 + inlineWeight(match[4], depth + 1);
    else if (match[5] !== undefined || match[6] !== undefined) weight += 1 + inlineWeight(match[5] ?? match[6] ?? "", depth + 1);
    else if (match[7] !== "!" && safeMarkdownUrl(match[9] ?? "")) weight += 1 + inlineWeight(match[8] ?? "", depth + 1);
    if (weight > PREVIEW_PAGE_WEIGHT) return weight;
  }
  return weight;
}

/** 无法在现有格式语义内安全拆分时，要求整个文件显示有界原文。 */
function sourceRequiredPart(): MarkdownPart[] {
  return [{ weight: PREVIEW_PAGE_WEIGHT + 1, characters: 0, render: () => null }];
}

interface InlineDecoration { tag: "code" | "strong" | "del" | "em" | "a"; url?: string }
interface InlineFragment { text: string; decorations: InlineDecoration[] }

/** 只有超长段落需要拆分行内格式；拆分后仍保留链接、强调与代码的原始含义。 */
function inlineFragments(text: string, decorations: InlineDecoration[] = [], depth = 0): InlineFragment[] {
  if (depth > 6) return [{ text, decorations }];
  const fragments: InlineFragment[] = [];
  const append = (value: string, decoration: InlineDecoration) => {
    for (const fragment of inlineFragments(value, [...decorations, decoration], depth + 1)) fragments.push(fragment);
  };
  let cursor = 0;
  for (const match of text.matchAll(inlinePattern())) {
    if (match.index > cursor) fragments.push({ text: text.slice(cursor, match.index), decorations });
    if (match[1] !== undefined) fragments.push({ text: match[1], decorations: [...decorations, { tag: "code" }] });
    else if (match[2] !== undefined || match[3] !== undefined) append(match[2] ?? match[3] ?? "", { tag: "strong" });
    else if (match[4] !== undefined) append(match[4], { tag: "del" });
    else if (match[5] !== undefined || match[6] !== undefined) append(match[5] ?? match[6] ?? "", { tag: "em" });
    else {
      const url = safeMarkdownUrl(match[9] ?? "");
      if (match[7] === "!" || !url) fragments.push({ text: match[0], decorations });
      else append(match[8] ?? "", { tag: "a", url });
    }
    cursor = match.index + match[0].length;
  }
  if (cursor < text.length) fragments.push({ text: text.slice(cursor), decorations });
  return fragments;
}

function renderFragment(fragment: InlineFragment, key: number): ReactNode {
  let node: ReactNode = fragment.text;
  for (const decoration of [...fragment.decorations].reverse()) {
    const Tag = decoration.tag;
    node = Tag === "a" ? <a key={key} href={decoration.url} target={decoration.url?.startsWith("#") ? undefined : "_blank"} rel="noopener noreferrer">{node}</a> : <Tag key={key}>{node}</Tag>;
  }
  return node;
}

function textChunks(text: string) {
  const chunks: string[] = [];
  for (let start = 0; start < text.length;) {
    let end = Math.min(text.length, start + PREVIEW_PAGE_CHARACTERS);
    if (end < text.length && text.charCodeAt(end) >= 0xdc00 && text.charCodeAt(end) <= 0xdfff) end--;
    chunks.push(text.slice(start, end));
    start = end;
  }
  return chunks.length ? chunks : [""];
}

function paragraphParts(text: string, start: number, preview: boolean, quote = false): MarkdownPart[] {
  const Tag = quote ? "blockquote" : "p";
  if (!preview || text.length <= PREVIEW_PAGE_CHARACTERS) return [{ weight: 2 + (preview ? inlineWeight(text) : 0), characters: text.length, render: () => <Tag key={start}>{inline(text)}</Tag> }];
  const chunks: { fragments: InlineFragment[]; characters: number; weight: number }[] = [{ fragments: [], characters: 0, weight: 2 }];
  for (const fragment of inlineFragments(text)) {
    if (fragment.decorations.some(decoration => (decoration.url?.length ?? 0) > PREVIEW_PAGE_CHARACTERS)) return sourceRequiredPart();
    let offset = 0;
    while (offset < fragment.text.length) {
      let chunk = chunks[chunks.length - 1];
      const weight = fragment.decorations.length + 1;
      if (chunk.characters === PREVIEW_PAGE_CHARACTERS || chunk.weight + weight > PREVIEW_PAGE_WEIGHT) {
        chunk = { fragments: [], characters: 0, weight: 2 }; chunks.push(chunk);
      }
      let end = Math.min(fragment.text.length, offset + PREVIEW_PAGE_CHARACTERS - chunk.characters);
      if (end < fragment.text.length && fragment.text.charCodeAt(end) >= 0xdc00 && fragment.text.charCodeAt(end) <= 0xdfff) end--;
      if (end === offset) { chunks.push({ fragments: [], characters: 0, weight: 2 }); continue; }
      chunk.fragments.push({ text: fragment.text.slice(offset, end), decorations: fragment.decorations });
      chunk.characters += end - offset;
      chunk.weight += weight;
      offset = end;
    }
  }
  return chunks.map((chunk, index) => ({ weight: chunk.weight, characters: chunk.characters, render: () => <Tag key={`${start}:${index}`}>{chunk.fragments.map(renderFragment)}</Tag> }));
}

/** 预览先建立纯文字片段，只有当前页生成 React 节点；普通对话保持完整渲染。 */
function markdownParts(text: string, preview: boolean): MarkdownPart[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const blocks: MarkdownPart[] = [];
  let index = 0;
  while (index < lines.length) {
    if (preview && blocks.length >= 20_000) return sourceRequiredPart();
    const start = index;
    const line = lines[index] ?? "";
    if (!line.trim()) { index++; continue; }
    const fence = /^ {0,3}(`{3,}|~{3,})([^\s`~]*)\s*$/.exec(line);
    if (fence) {
      index++;
      const code: string[] = [];
      const marker = fence[1] ?? "```";
      const closing = new RegExp("^ {0,3}" + marker[0] + "{" + marker.length + ",}\\s*$");
      while (index < lines.length && !closing.test(lines[index] ?? "")) code.push(lines[index++] ?? "");
      if (index < lines.length) index++;
      const content = code.join("\n");
      const chart = fence[2]?.toLowerCase() === "chart" ? parseChart(content) : null;
      if (chart && preview && content.length > PREVIEW_PAGE_CHARACTERS) return sourceRequiredPart();
      if (chart) blocks.push({ weight: PREVIEW_PAGE_WEIGHT, characters: content.length, render: () => <ChartBlock key={start} chart={chart} source={content} /> });
      else for (const [part, shown] of (preview ? textChunks(content) : [content]).entries()) blocks.push({ weight: 10, characters: shown.length, render: () => <div className="message-code" key={`${start}:${part}`}><div className="message-code-head"><code>{fence[2]}</code><CopyButton text={content} code /></div><pre><code>{shown}</code></pre></div> });
      continue;
    }
    const heading = /^ {0,3}(#{1,6})\s+(.+?)\s*#*\s*$/.exec(line);
    if (heading) {
      const level = heading[1]?.length;
      const characters = (heading[2] ?? "").length;
      if (preview && characters > PREVIEW_PAGE_CHARACTERS) return sourceRequiredPart();
      const weight = 2 + (preview ? inlineWeight(heading[2] ?? "") : 0);
      if (preview && weight > PREVIEW_PAGE_WEIGHT) return sourceRequiredPart();
      blocks.push({ weight, characters, render: () => {
        const children = inline(heading[2] ?? "");
        return level === 1 ? <h1 key={start}>{children}</h1> : level === 2 ? <h2 key={start}>{children}</h2> : level === 3 ? <h3 key={start}>{children}</h3> : level === 4 ? <h4 key={start}>{children}</h4> : level === 5 ? <h5 key={start}>{children}</h5> : <h6 key={start}>{children}</h6>;
      } });
      index++;
      continue;
    }
    if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      blocks.push({ weight: 1, characters: 0, render: () => <hr key={start} /> }); index++; continue;
    }
    if (/^ {0,3}>/.test(line)) {
      const quote: string[] = [];
      while (index < lines.length && /^ {0,3}>/.test(lines[index] ?? "")) quote.push((lines[index++] ?? "").replace(/^ {0,3}>\s?/, ""));
      const parts = paragraphParts(quote.join("\n"), start, preview, true);
      if (preview && parts.some(part => part.weight > PREVIEW_PAGE_WEIGHT)) return sourceRequiredPart();
      blocks.push(...parts);
      continue;
    }
    const unordered = /^ {0,3}[-+*]\s+/.test(line);
    const ordered = /^ {0,3}(\d+)[.)]\s+/.exec(line);
    if (unordered || ordered) {
      const matcher = unordered ? /^ {0,3}[-+*]\s+(.*)$/ : /^ {0,3}\d+[.)]\s+(.*)$/;
      const items: string[] = [];
      while (index < lines.length) {
        const item = matcher.exec(lines[index] ?? "");
        if (!item) break;
        items.push(item[1] ?? "");
        index++;
      }
      for (let offset = 0; offset < items.length;) {
        let count = preview ? 0 : items.length;
        let characters = 0;
        let weight = 1;
        if (preview) for (const item of items.slice(offset, offset + PREVIEW_LIST_ITEMS)) {
          if (item.length > PREVIEW_PAGE_CHARACTERS) return sourceRequiredPart();
          const itemWeight = 2 + inlineWeight(item);
          if (1 + itemWeight > PREVIEW_PAGE_WEIGHT) return sourceRequiredPart();
          if (count && (characters + item.length > PREVIEW_PAGE_CHARACTERS || weight + itemWeight > PREVIEW_PAGE_WEIGHT)) break;
          count++; characters += item.length; weight += itemWeight;
        }
        const shown = items.slice(offset, offset + count);
        const listStart = Number(ordered?.[1] ?? 1) + offset;
        const partKey = `${start}:${offset}`;
        blocks.push({ weight, characters, render: () => {
          const children = shown.map((item, itemIndex) => <li key={itemIndex}>{inline(item)}</li>);
          return unordered ? <ul key={partKey}>{children}</ul> : <ol key={partKey} start={listStart}>{children}</ol>;
        } });
        offset += count;
      }
      continue;
    }
    if (line.includes("|") && isTableSeparator(lines[index + 1] ?? "")) {
      const headers = tableCells(line);
      index += 2;
      const rows: string[][] = [];
      while (index < lines.length && (lines[index] ?? "").includes("|") && (lines[index] ?? "").trim()) rows.push(tableCells(lines[index++] ?? ""));
      const columnsPerPart = preview ? Math.min(headers.length, 50) : headers.length;
      for (let columnStart = 0; columnStart < headers.length; columnStart += columnsPerPart) {
        const shownHeaders = headers.slice(columnStart, columnStart + columnsPerPart);
        const headerCharacters = preview ? shownHeaders.reduce((size, cell) => size + cell.length, 0) : 0;
        if (preview && headerCharacters > PREVIEW_PAGE_CHARACTERS) return sourceRequiredPart();
        const headerWeight = shownHeaders.length + 1 + (preview ? shownHeaders.reduce((size, cell) => size + inlineWeight(cell), 0) : 0);
        if (preview && headerWeight > PREVIEW_PAGE_WEIGHT) return sourceRequiredPart();
        for (let rowStart = 0; rowStart < Math.max(1, rows.length);) {
          let count = preview ? 0 : rows.length;
          let characters = headerCharacters;
          let weight = headerWeight;
          if (preview) for (const row of rows.slice(rowStart, rowStart + PREVIEW_TABLE_ROWS)) {
            const cells = row.slice(columnStart, columnStart + columnsPerPart);
            const rowCharacters = cells.reduce((size, cell) => size + cell.length, 0);
            if (headerCharacters + rowCharacters > PREVIEW_PAGE_CHARACTERS) return sourceRequiredPart();
            const rowWeight = shownHeaders.length + 1 + cells.reduce((size, cell) => size + inlineWeight(cell), 0);
            if (headerWeight + rowWeight > PREVIEW_PAGE_WEIGHT) return sourceRequiredPart();
            if (count && (characters + rowCharacters > PREVIEW_PAGE_CHARACTERS || weight + rowWeight > PREVIEW_PAGE_WEIGHT)) break;
            count++; characters += rowCharacters; weight += rowWeight;
          }
          const shownRows = rows.slice(rowStart, rowStart + count);
          const partKey = `${start}:${columnStart}:${rowStart}`;
          blocks.push({ weight, characters, render: () => <div className="message-table" key={partKey}><table><thead><tr>{shownHeaders.map((cell, column) => <th key={column}>{inline(cell)}</th>)}</tr></thead><tbody>{shownRows.map((row, rowIndex) => <tr key={rowIndex}>{shownHeaders.map((_cell, column) => <td key={column}>{inline(row[columnStart + column] ?? "")}</td>)}</tr>)}</tbody></table></div> });
          rowStart += Math.max(1, count);
        }
      }
      continue;
    }
    const paragraph = [line];
    index++;
    while (index < lines.length && !startsBlock(lines, index)) paragraph.push(lines[index++] ?? "");
    const parts = paragraphParts(paragraph.join("\n"), start, preview);
    if (preview && parts.some(part => part.weight > PREVIEW_PAGE_WEIGHT)) return sourceRequiredPart();
    blocks.push(...parts);
  }
  return blocks;
}

/** 文件阅读分页保留完整列表与重复表头，不生成未显示页的行内元素。 */
export function prepareMarkdownPreview(text: string) {
  const pages: MarkdownPart[][] = [[]];
  let weight = 0;
  let characters = 0;
  for (const part of markdownParts(text, true)) {
    if (part.weight > PREVIEW_PAGE_WEIGHT || part.characters > PREVIEW_PAGE_CHARACTERS) return { sourceRequired: true, pageCount: 0, renderPage: (_page: number) => null };
    if (weight && (weight + part.weight > PREVIEW_PAGE_WEIGHT || characters + part.characters > PREVIEW_PAGE_CHARACTERS)) { pages.push([]); weight = 0; characters = 0; }
    pages[pages.length - 1].push(part);
    weight += part.weight;
    characters += part.characters;
  }
  return {
    sourceRequired: false,
    pageCount: pages.length,
    renderPage(page: number) { return <div className="message-markdown">{pages[Math.max(0, Math.min(pages.length - 1, page))].map(part => part.render())}</div>; },
  };
}

export function Markdown({ text }: { text: string }) {
  return <div className="message-markdown">{markdownParts(text, false).map(part => part.render())}</div>;
}
