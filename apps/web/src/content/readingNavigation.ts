// 阅读导航仅处理当前已加载的目录或当前显示页，不扫描其他文件。
export const TEXT_PAGE_CHARS = 60_000;
export const MAX_FIND_QUERY = 200;
export const MAX_FIND_MATCHES = 200;
export const MAX_OUTLINE_HEADINGS = 200;

/** 按 UTF-16 位置分页，同时保持代理对完整。 */
export function textPage(all: string, requested: number) {
  const pages = Math.max(1, Math.ceil(all.length / TEXT_PAGE_CHARS));
  const page = Math.max(0, Math.min(pages - 1, requested));
  let start = page * TEXT_PAGE_CHARS;
  let end = Math.min(all.length, (page + 1) * TEXT_PAGE_CHARS);
  const lowSurrogate = (at: number) => all.charCodeAt(at) >= 0xdc00 && all.charCodeAt(at) <= 0xdfff;
  if (start > 0 && lowSurrogate(start)) start--;
  if (end < all.length && lowSurrogate(end)) end--;
  return { text: all.slice(start, end), page, pages };
}

/** 目录来源于现有列表响应，面包屑只复用已知相对路径。 */
export function folderBreadcrumbs(folder: string) {
  const parts = folder.split("/").filter(Boolean);
  return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join("/") }));
}

export interface ReadingHeading { id: string; title: string; level: number }
/** 从实际渲染结果提取标题，代码块中的井号不参与目录。 */
export function readingHeadings(root: HTMLElement, prefix: string) {
  const headings: ReadingHeading[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT, {
    acceptNode: node => /^H[1-6]$/.test((node as Element).tagName) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_SKIP,
  });
  let node = walker.nextNode();
  while (node) {
    if (headings.length === MAX_OUTLINE_HEADINGS) return { headings, truncated: true };
    const heading = node as HTMLHeadingElement;
    const id = `${prefix}-heading-${headings.length + 1}`;
    heading.id = id;
    heading.tabIndex = -1;
    headings.push({ id, title: heading.textContent ?? "", level: Number(heading.tagName.slice(1)) });
    node = walker.nextNode();
  }
  return { headings, truncated: false };
}

export interface TextSegment { node: Text; start: number; end: number }
export interface ReadingText { text: string; segments: TextSegment[] }
/** 只索引当前挂载页的可读文本，排除代码复制按钮等控件。 */
export function readingText(root: HTMLElement): ReadingText {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const segments: TextSegment[] = [];
  let text = "";
  let previousBlock: Element | null = null;
  let node = walker.nextNode();
  while (node && text.length <= TEXT_PAGE_CHARS) {
    const parent = node.parentElement;
    if (parent && !parent.closest('button,[aria-hidden="true"]')) {
      const block = parent.closest("p,pre,h1,h2,h3,h4,h5,h6,li,td,th,blockquote");
      if (previousBlock && block !== previousBlock) text += "\n";
      previousBlock = block;
      const remaining = TEXT_PAGE_CHARS + 1 - text.length;
      const value = (node.textContent ?? "").slice(0, remaining);
      segments.push({ node: node as Text, start: text.length, end: text.length + value.length });
      text += value;
    }
    node = walker.nextNode();
  }
  return { text, segments };
}

export interface ReadingMatch { start: number; end: number }
/** 字面匹配不构造正则；保留大小写转换扩展字符到原始 UTF-16 位置的映射。 */
export function findReadingMatches(text: string, query: string) {
  const needle = query.slice(0, MAX_FIND_QUERY).toLowerCase();
  const matches: ReadingMatch[] = [];
  if (!needle) return { matches, truncated: false };
  let folded = "";
  const starts: number[] = [];
  const ends: number[] = [];
  let offset = 0;
  for (const character of text) {
    const part = character.toLowerCase();
    for (let index = 0; index < part.length; index++) { starts.push(offset); ends.push(offset + character.length); }
    folded += part;
    offset += character.length;
  }
  let cursor = 0;
  while (cursor < folded.length) {
    const found = folded.indexOf(needle, cursor);
    if (found < 0) break;
    if (matches.length === MAX_FIND_MATCHES) return { matches, truncated: true };
    matches.push({ start: starts[found], end: ends[found + needle.length - 1] });
    cursor = found + needle.length;
  }
  return { matches, truncated: false };
}

/** 选择并滚动到当前命中，不重写 Markdown 或代码的 DOM。 */
export function revealReadingMatch(root: HTMLElement, snapshot: ReadingText, match: ReadingMatch) {
  const first = snapshot.segments.find(segment => segment.end > match.start);
  const last = snapshot.segments.find(segment => segment.end >= match.end);
  if (!first || !last) return;
  const range = document.createRange();
  // 段落分隔符是索引补入的换行，不属于文本节点，边界必须收敛到真实节点范围。
  range.setStart(first.node, Math.max(0, Math.min(first.node.length, match.start - first.start)));
  range.setEnd(last.node, Math.max(0, Math.min(last.node.length, match.end - last.start)));
  const selection = window.getSelection();
  selection?.removeAllRanges();
  selection?.addRange(range);
  const scroller = root.closest<HTMLElement>(".content-preview-body");
  if (scroller && typeof range.getBoundingClientRect === "function") {
    const bounds = range.getBoundingClientRect();
    scroller.scrollTop += bounds.top - scroller.getBoundingClientRect().top - scroller.clientHeight / 2;
  }
}

/** 当前阅读器只清理自己的选区，不能清掉其他对话或编辑器中的选择。 */
export function clearReadingSelection(root: HTMLElement | null) {
  const selection = window.getSelection();
  if (root && selection && (root.contains(selection.anchorNode) || root.contains(selection.focusNode))) {
    selection.removeAllRanges();
  }
}
