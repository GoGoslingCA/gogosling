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

function inline(text: string, depth = 0): ReactNode {
  if (depth > 6) return text;
  const pattern = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|__([^_\n]+)__|~~([^~\n]+)~~|\*([^*\n]+)\*|_([^_\n]+)_|(!?)\[([^\]\n]+)\]\(([^)\s]+)\)/g;
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

export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const blocks: ReactNode[] = [];
  let index = 0;
  while (index < lines.length) {
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
      blocks.push(chart ? <ChartBlock key={start} chart={chart} source={content} /> : <div className="message-code" key={start}><div className="message-code-head"><code>{fence[2]}</code><CopyButton text={content} code /></div><pre><code>{content}</code></pre></div>);
      continue;
    }
    const heading = /^ {0,3}(#{1,6})\s+(.+?)\s*#*\s*$/.exec(line);
    if (heading) {
      const children = inline(heading[2] ?? "");
      const level = heading[1]?.length;
      blocks.push(level === 1 ? <h1 key={start}>{children}</h1> : level === 2 ? <h2 key={start}>{children}</h2> : level === 3 ? <h3 key={start}>{children}</h3> : level === 4 ? <h4 key={start}>{children}</h4> : level === 5 ? <h5 key={start}>{children}</h5> : <h6 key={start}>{children}</h6>);
      index++;
      continue;
    }
    if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      blocks.push(<hr key={start} />); index++; continue;
    }
    if (/^ {0,3}>/.test(line)) {
      const quote: string[] = [];
      while (index < lines.length && /^ {0,3}>/.test(lines[index] ?? "")) quote.push((lines[index++] ?? "").replace(/^ {0,3}>\s?/, ""));
      blocks.push(<blockquote key={start}>{inline(quote.join("\n"))}</blockquote>);
      continue;
    }
    const unordered = /^ {0,3}[-+*]\s+/.test(line);
    const ordered = /^ {0,3}(\d+)[.)]\s+/.exec(line);
    if (unordered || ordered) {
      const matcher = unordered ? /^ {0,3}[-+*]\s+(.*)$/ : /^ {0,3}\d+[.)]\s+(.*)$/;
      const items: ReactNode[] = [];
      while (index < lines.length) {
        const item = matcher.exec(lines[index] ?? "");
        if (!item) break;
        items.push(<li key={index++}>{inline(item[1] ?? "")}</li>);
      }
      blocks.push(unordered ? <ul key={start}>{items}</ul> : <ol key={start} start={Number(ordered?.[1] ?? 1)}>{items}</ol>);
      continue;
    }
    if (line.includes("|") && isTableSeparator(lines[index + 1] ?? "")) {
      const headers = tableCells(line);
      index += 2;
      const rows: string[][] = [];
      while (index < lines.length && (lines[index] ?? "").includes("|") && (lines[index] ?? "").trim()) rows.push(tableCells(lines[index++] ?? ""));
      blocks.push(<div className="message-table" key={start}><table><thead><tr>{headers.map((cell, column) => <th key={column}>{inline(cell)}</th>)}</tr></thead><tbody>{rows.map((row, rowIndex) => <tr key={rowIndex}>{headers.map((_cell, column) => <td key={column}>{inline(row[column] ?? "")}</td>)}</tr>)}</tbody></table></div>);
      continue;
    }
    const paragraph = [line];
    index++;
    while (index < lines.length && !startsBlock(lines, index)) paragraph.push(lines[index++] ?? "");
    blocks.push(<p key={start}>{inline(paragraph.join("\n"))}</p>);
  }
  return <div className="message-markdown">{blocks}</div>;
}
