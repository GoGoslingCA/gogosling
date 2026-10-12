// 从已持久化的助手消息派生成果视图；不推断工作区文件，也不执行或渲染消息中的 HTML。

import type { Message } from "../api/types";

/** 可编辑的消息副本，id 随源消息保持稳定。 */
export interface Artifact {
  id: string;
  messageId: string;
  seq: number;
  kind: "document" | "code";
  language: string;
  content: string;
  /** 仅来自正文中实际出现的 Markdown 标题，不推断文件路径。 */
  title?: string;
  index: number;
  partial: boolean;
}

interface Fence {
  start: number;
  end: number;
  language: string;
  content: string;
}

function codeFences(text: string): Fence[] {
  const fences: Fence[] = [];
  let active: { start: number; contentStart: number; language: string; closing: RegExp } | undefined;
  for (const line of text.matchAll(/[^\n]*(?:\n|$)/g)) {
    if (!line[0]) continue;
    if (active) {
      if (active.closing.test(line[0])) {
        fences.push({ start: active.start, end: line.index + line[0].length, language: active.language, content: text.slice(active.contentStart, line.index) });
        active = undefined;
      }
      continue;
    }
    const opening = /^ {0,3}(`{3,}|~{3,})([^\r\n]*)\r?\n$/.exec(line[0]);
    if (!opening || (opening[1][0] === "`" && opening[2].includes("`"))) continue;
    const marker = opening[1];
    const info = opening[2].trim().split(/\s+/)[0] ?? "";
    active = {
      start: line.index,
      contentStart: line.index + line[0].length,
      language: /^[\w+#.-]{1,24}$/.test(info) ? info.toLowerCase() : "",
      closing: new RegExp(`^ {0,3}${marker[0]}{${marker.length},}[ \\t]*(?:\\r?\\n)?$`),
    };
  }
  return fences;
}

/** 读取首个可见 Markdown 标题，跳过代码围栏并去掉基础行内标记。 */
export function firstArtifactHeading(text: string): string | undefined {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  let closing: RegExp | undefined;
  const clean = (value: string) => value
    .replace(/!?\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/<[^>]*>/g, "")
    .replace(/[`*_~]/g, "")
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .replace(/\s+/g, " ").trim().slice(0, 96);
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    if (closing) { if (closing.test(line)) closing = undefined; continue; }
    const fence = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (fence) { closing = new RegExp(`^ {0,3}${fence[1][0]}{${fence[1].length},}[ \\t]*$`); continue; }
    const heading = /^ {0,3}#{1,6}[ \t]+(.+?)(?:[ \t]+#+)?[ \t]*$/.exec(line)?.[1];
    const setext = line.trim() && !/^ {0,3}(?:>|[-+*]\s|\d+[.)]\s)/.test(line) && /^ {0,3}(?:=+|-+)[ \t]*$/.test(lines[index + 1] ?? "");
    const title = heading ?? (setext ? line : undefined);
    if (title) { const value = clean(title); if (value) return value; }
  }
  return undefined;
}

/** 明确打开某条回复时优先完整文档；未指定时只从最新回复中选择，不跳到旧文档。 */
export function preferredArtifact(artifacts: readonly Artifact[], messageId?: string): Artifact | undefined {
  const targetId = messageId ?? artifacts.at(-1)?.messageId;
  const fromReply = artifacts.filter((artifact) => artifact.messageId === targetId);
  return fromReply.find((artifact) => artifact.kind === "document") ?? fromReply[0];
}

const languageLabels: Readonly<Record<string, string>> = {
  js: "JavaScript", javascript: "JavaScript", ts: "TypeScript", typescript: "TypeScript", jsx: "JSX", tsx: "TSX",
  py: "Python", python: "Python", go: "Go", rust: "Rust", ruby: "Ruby", java: "Java", c: "C", cpp: "C++",
  csharp: "C#", "c#": "C#", html: "HTML", css: "CSS", json: "JSON", yaml: "YAML", yml: "YAML", sql: "SQL",
  bash: "Bash", shell: "Shell", sh: "Shell", markdown: "Markdown", md: "Markdown",
};

/** 编程语言是技术标识；未知语言保留原围栏标签，空标签由界面本地化。 */
export function artifactLanguageLabel(language: string): string {
  return languageLabels[language] ?? language;
}

/** 仅提取助手正文；工具输出、用户附件与未闭合代码块不会变成代码成果。 */
export function extractArtifacts(messages: readonly Message[]): Artifact[] {
  const artifacts: Artifact[] = [];
  for (const message of messages) {
    if (message.role !== "assistant") continue;
    const content = message.parts.filter((part) => part.type === "text").map((part) => part.text ?? "").join("\n\n");
    if (!content.trim()) continue;
    const fences = codeFences(content);
    const base = { messageId: message.id, seq: message.seq, partial: message.meta?.partial === true };
    let prose = content;
    for (const fence of [...fences].reverse()) prose = prose.slice(0, fence.start) + prose.slice(fence.end);
    if (prose.trim() || fences.length === 0) {
      artifacts.push({ ...base, id: `${message.id}:document`, kind: "document", language: "markdown", content, title: firstArtifactHeading(content), index: 0 });
    }
    for (const [index, fence] of fences.entries()) {
      artifacts.push({ ...base, id: `${message.id}:code:${index}`, kind: "code", language: fence.language, content: fence.content, index: index + 1 });
    }
  }
  return artifacts;
}

const extensions: Readonly<Record<string, string>> = {
  javascript: "js", js: "js", typescript: "ts", ts: "ts", jsx: "jsx", tsx: "tsx",
  python: "py", py: "py", go: "go", rust: "rs", ruby: "rb", java: "java", c: "c", cpp: "cpp",
  csharp: "cs", "c#": "cs", html: "html", css: "css", json: "json", yaml: "yaml", yml: "yml",
  bash: "sh", shell: "sh", sh: "sh", sql: "sql", markdown: "md", md: "md", text: "txt", plaintext: "txt",
};

/** 扩展名只取受控映射，不把模型输出当文件路径。 */
export function artifactExtension(artifact: Pick<Artifact, "kind" | "language">): string {
  return artifact.kind === "document" ? "md" : (extensions[artifact.language] ?? "txt");
}

/** 规范化用户提供的导出名称，去掉目录与控制字符。 */
export function safeArtifactFilename(name: string, fallback: string): string {
  const leaf = name.replace(/\\/g, "/").split("/").pop() ?? "";
  const clean = leaf.replace(/[\u0000-\u001f\u007f<>:"|?*]/g, "_").replace(/[. ]+$/g, "").trim().slice(0, 160);
  return clean && !/^\.+$/.test(clean) ? clean : fallback;
}

/** 行差异：正负号与行号同时传达变化，避免只靠颜色。 */
export interface ArtifactDiffLine {
  kind: "same" | "removed" | "added";
  text: string;
  oldLine?: number;
  newLine?: number;
}

/** 有界 LCS 行差异；大文档退回前后缀对齐，避免编辑时消耗二次方内存。 */
export function diffArtifactLines(original: string, edited: string): ArtifactDiffLine[] {
  const before = original.split("\n");
  const after = edited.split("\n");
  const result: ArtifactDiffLine[] = [];
  let prefix = 0;
  while (prefix < before.length && prefix < after.length && before[prefix] === after[prefix]) {
    result.push({ kind: "same", text: before[prefix], oldLine: prefix + 1, newLine: prefix + 1 });
    prefix += 1;
  }
  let suffix = 0;
  while (suffix < before.length - prefix && suffix < after.length - prefix && before[before.length - 1 - suffix] === after[after.length - 1 - suffix]) suffix += 1;
  const a = before.slice(prefix, before.length - suffix);
  const b = after.slice(prefix, after.length - suffix);
  const removed = (i: number) => result.push({ kind: "removed", text: a[i], oldLine: prefix + i + 1 });
  const added = (j: number) => result.push({ kind: "added", text: b[j], newLine: prefix + j + 1 });
  if (a.length * b.length > 250_000) {
    a.forEach((_, i) => removed(i));
    b.forEach((_, j) => added(j));
  } else {
    const width = b.length + 1;
    const lcs = new Uint32Array((a.length + 1) * width);
    for (let i = a.length - 1; i >= 0; i -= 1) {
      for (let j = b.length - 1; j >= 0; j -= 1) {
        lcs[i * width + j] = a[i] === b[j] ? 1 + lcs[(i + 1) * width + j + 1] : Math.max(lcs[(i + 1) * width + j], lcs[i * width + j + 1]);
      }
    }
    let i = 0;
    let j = 0;
    while (i < a.length || j < b.length) {
      if (i < a.length && j < b.length && a[i] === b[j]) {
        result.push({ kind: "same", text: a[i], oldLine: prefix + i + 1, newLine: prefix + j + 1 });
        i += 1;
        j += 1;
      } else if (i < a.length && (j >= b.length || lcs[(i + 1) * width + j] >= lcs[i * width + j + 1])) removed(i++);
      else added(j++);
    }
  }
  for (let offset = suffix; offset > 0; offset -= 1) {
    result.push({ kind: "same", text: before[before.length - offset], oldLine: before.length - offset + 1, newLine: after.length - offset + 1 });
  }
  return result;
}

/** 高亮只返回文本 token，交由 React 转义，不生成 HTML。 */
export interface CodeToken { text: string; kind: "plain" | "comment" | "string" | "number" | "keyword" }

/** 轻量词法着色，不声称检查语法或运行代码。 */
export function highlightArtifactCode(code: string, language: string): CodeToken[] {
  const hashComment = /^(python|py|ruby|bash|shell|sh|yaml|yml)$/.test(language);
  const sqlComment = language === "sql";
  const comments = hashComment ? "#[^\\n]*" : sqlComment ? "--[^\\n]*|/\\*[\\s\\S]*?\\*/" : "//[^\\n]*|/\\*[\\s\\S]*?\\*/";
  const pattern = new RegExp(`(${comments})|("(?:\\\\.|[^"\\\\])*"|'(?:\\\\.|[^'\\\\])*'|\x60(?:\\\\.|[^\x60\\\\])*\x60)|(\\b\\d+(?:\\.\\d+)?\\b)|(\\b(?:const|let|var|function|return|if|else|for|while|class|import|from|export|default|async|await|new|throw|try|catch|true|false|null|undefined|def|in|is|None|True|False|func|package|type|struct|interface|public|private|select|SELECT|FROM|WHERE)\\b)`, "g");
  const tokens: CodeToken[] = [];
  let cursor = 0;
  for (const match of code.matchAll(pattern)) {
    if (match.index > cursor) tokens.push({ text: code.slice(cursor, match.index), kind: "plain" });
    tokens.push({ text: match[0], kind: match[1] ? "comment" : match[2] ? "string" : match[3] ? "number" : "keyword" });
    cursor = match.index + match[0].length;
  }
  if (cursor < code.length) tokens.push({ text: code.slice(cursor), kind: "plain" });
  return tokens;
}
