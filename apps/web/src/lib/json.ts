/** 稳定、可读的 JSON 展示；无法序列化时退回 String()。 */
export function prettyJson(value: unknown): string {
  if (value === undefined) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

/** 截断长文本用于摘要展示。 */
export function truncate(text: string, max = 80): string {
  const oneLine = text.replace(/\s+/g, " ").trim();
  if (oneLine.length <= max) return oneLine;
  return `${oneLine.slice(0, max - 1)}…`;
}

/** 有界的单行 JSON 摘要；只供折叠卡片使用，完整参数仍由 prettyJson 展示。 */
export function previewJson(value: unknown, max = 70): string {
  const chunks: string[] = [];
  let length = 0;
  let clipped = false;
  let nodes = 0;
  const seen = new Set<object>();
  const append = (text: string) => {
    const remaining = Math.max(0, max - 1 - length);
    let end = Math.min(remaining, text.length);
    if (end < text.length && end > 0 && /[\uD800-\uDBFF]/.test(text[end - 1]!)) end--;
    chunks.push(text.slice(0, end));
    length += end;
    if (end < text.length) clipped = true;
  };
  const visit = (item: unknown, depth: number) => {
    if (clipped) return;
    if (++nodes > 64 || depth > 8) { clipped = true; return; }
    if (typeof item === "string") {
      const prefix = item.slice(0, Math.max(0, max - length));
      append(JSON.stringify(prefix));
      if (prefix.length < item.length) clipped = true;
    } else if (item === null || typeof item !== "object") {
      append(JSON.stringify(item) ?? "");
    } else if (seen.has(item)) {
      clipped = true;
    } else {
      seen.add(item);
      if (Array.isArray(item)) {
        append("[");
        for (let index = 0; index < item.length && !clipped; index++) {
          if (index) append(", ");
          visit(item[index] ?? null, depth + 1);
        }
        if (!clipped) append("]");
      } else {
        append("{");
        let first = true;
        for (const key in item) {
          if (clipped) break;
          if (!Object.hasOwn(item, key)) continue;
          const child = (item as Record<string, unknown>)[key];
          if (child === undefined) continue;
          if (!first) append(", ");
          first = false;
          visit(key, depth + 1);
          if (!clipped) append(": ");
          visit(child, depth + 1);
        }
        if (!clipped) append("}");
      }
      seen.delete(item);
    }
  };
  try { visit(value, 0); } catch { clipped = true; }
  return chunks.join("") + (clipped ? "…" : "");
}
