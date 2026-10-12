import { describe, expect, it } from "vitest";
import type { Message } from "../api/types";
import { artifactExtension, artifactLanguageLabel, diffArtifactLines, extractArtifacts, firstArtifactHeading, highlightArtifactCode, preferredArtifact, safeArtifactFilename } from "./artifacts";

function message(text: string, role: Message["role"] = "assistant"): Message {
  return { id: "message-1", session_id: "s", seq: 2, role, parts: [{ type: "text", text }], created_at: "2026-10-12T00:00:00Z" };
}

describe("extractArtifacts", () => {
  it("只从已持久化助手正文提取文档与闭合代码块，保留代码换行", () => {
    const result = extractArtifacts([message("忽略用户消息", "user"), message("# 计划\n\n```typescript\nconst answer = 42;\n```\n说明")]);
    expect(result.map((artifact) => artifact.kind)).toEqual(["document", "code"]);
    expect(result[1]).toMatchObject({ id: "message-1:code:0", language: "typescript", content: "const answer = 42;\n", index: 1 });
    expect(result[0].content).toContain("# 计划");
  });

  it("纯代码不重复创建文档，支持更长关闭围栏与波浪围栏", () => {
    const result = extractArtifacts([message("~~~~py\nprint('ok')\n~~~~~\n\n```go\npackage main\n```\n")]);
    expect(result).toHaveLength(2);
    expect(result.map((artifact) => artifact.language)).toEqual(["py", "go"]);
  });

  it("未闭合块保持为文档并标记中断，工具文本不能成为成果", () => {
    const source = message("```js\nconst pending = true;");
    source.meta = { partial: true };
    source.parts.push({ type: "tool_result", tool_result: { call_id: "call", content: "do not extract" } });
    const result = extractArtifacts([source, message("tool text", "tool")]);
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({ kind: "document", partial: true });
    expect(result[0].content).not.toContain("do not extract");
  });

  it("不把模型语言标识和用户目录用作本地路径", () => {
    expect(artifactExtension({ kind: "code", language: "../../evil" })).toBe("txt");
    expect(safeArtifactFilename("../../docs/review.md", "result.md")).toBe("review.md");
    expect(safeArtifactFilename("C:\\private\\file.ts", "result.md")).toBe("file.ts");
    expect(safeArtifactFilename("..", "result.md")).toBe("result.md");
  });

  it("未闭合外层围栏中的其他围栏仍是正文，不误提取嵌套代码", () => {
    const result = extractArtifacts([message("```markdown\n~~~py\nprint('example')\n~~~\n")]);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("document");
  });
});

describe("diffArtifactLines", () => {
  it("插入、删除和相同行均保留正确的原稿及当前行号", () => {
    expect(diffArtifactLines("a\nb\nc", "a\nnew\nc\nend")).toEqual([
      { kind: "same", text: "a", oldLine: 1, newLine: 1 },
      { kind: "removed", text: "b", oldLine: 2 },
      { kind: "added", text: "new", newLine: 2 },
      { kind: "same", text: "c", oldLine: 3, newLine: 3 },
      { kind: "added", text: "end", newLine: 4 },
    ]);
  });

  it("大段完全改写时仍保留头尾匹配且能重建两版内容", () => {
    const original = `head\n${Array.from({ length: 600 }, (_, i) => `old-${i}`).join("\n")}\ntail`;
    const edited = `head\n${Array.from({ length: 600 }, (_, i) => `new-${i}`).join("\n")}\ntail`;
    const lines = diffArtifactLines(original, edited);
    expect(lines.filter((line) => line.kind !== "added").map((line) => line.text).join("\n")).toBe(original);
    expect(lines.filter((line) => line.kind !== "removed").map((line) => line.text).join("\n")).toBe(edited);
    expect(lines[lines.length - 1]).toEqual({ kind: "same", text: "tail", oldLine: 602, newLine: 602 });
  });

  it("能识别末尾换行变化", () => {
    expect(diffArtifactLines("hello", "hello\n")).toContainEqual({ kind: "added", text: "", newLine: 2 });
  });
});

describe("成果名称与默认版本", () => {
  it("标题来自首个可见 Markdown 标题，跳过代码并去掉行内格式", () => {
    expect(firstArtifactHeading("```md\n# 代码中的标题\n```\n## **需求整理**与[计划](https://example.test)\n正文")).toBe("需求整理与计划");
    expect(firstArtifactHeading("每周计划\n========\n正文")).toBe("每周计划");
    expect(firstArtifactHeading("普通说明，没有标题")).toBeUndefined();
    expect(firstArtifactHeading("```md\n# 未闭合代码中的标题")).toBeUndefined();
  });

  it("同一回复优先文档，最新回复只有代码时不退回旧文档", () => {
    const old = message("# 旧计划\n正文");
    const current = { ...message("# 新计划\n```ts\nconst n = 1;\n```"), id: "new", seq: 4 };
    expect(preferredArtifact(extractArtifacts([old, current]))).toMatchObject({ id: "new:document", title: "新计划" });
    const codeOnly = { ...current, parts: [{ type: "text" as const, text: "```ts\nconst n = 1;\n```" }] };
    expect(preferredArtifact(extractArtifacts([old, codeOnly]))?.id).toBe("new:code:0");
    expect(preferredArtifact(extractArtifacts([old, current]), old.id)?.id).toBe(`${old.id}:document`);
    expect(artifactLanguageLabel("ts")).toBe("TypeScript");
  });
});

describe("highlightArtifactCode", () => {
  it("高亮结果可无损重建正文，不把 HTML 包装为可执行代码", () => {
    const code = 'const text = "<img src=x onerror=alert(1)>"; // 说明\nreturn 42;';
    const tokens = highlightArtifactCode(code, "js");
    expect(tokens.map((token) => token.text).join("")).toBe(code);
    expect(tokens).toContainEqual({ kind: "string", text: '"<img src=x onerror=alert(1)>"' });
    expect(tokens).toContainEqual({ kind: "comment", text: "// 说明" });
  });
});
