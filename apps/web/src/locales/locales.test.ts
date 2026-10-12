// 校验 zh、en 与 fr 的键树完全一致，并且占位符一致（避免某种语言漏翻或插值名不同）。

import { describe, expect, it } from "vitest";
import zh from "./zh.json";
import en from "./en.json";
import fr from "./fr.json";

function flatten(obj: unknown, prefix = ""): Record<string, string> {
  const out: Record<string, string> = {};
  if (!obj || typeof obj !== "object") return out;
  for (const [key, value] of Object.entries(obj as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (value && typeof value === "object") Object.assign(out, flatten(value, path));
    else out[path] = String(value);
  }
  return out;
}

function placeholders(text: string): string[] {
  return [...text.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1] ?? "").sort();
}

describe("locales", () => {
  const zhFlat = flatten(zh);
  const enFlat = flatten(en);
  const frFlat = flatten(fr);
  const packs = { zh: zhFlat, en: enFlat, fr: frFlat };

  it("zh、en 与 fr 的键树一致", () => {
    const zhKeys = Object.keys(zhFlat).sort();
    expect(Object.keys(enFlat).sort()).toEqual(zhKeys);
    expect(Object.keys(frFlat).sort()).toEqual(zhKeys);
  });

  it("每个键的插值占位符一致", () => {
    for (const key of Object.keys(zhFlat)) {
      expect(placeholders(enFlat[key] ?? ""), key).toEqual(placeholders(zhFlat[key] ?? ""));
      expect(placeholders(frFlat[key] ?? ""), key).toEqual(placeholders(zhFlat[key] ?? ""));
    }
  });

  it("没有空文案", () => {
    for (const [locale, pack] of Object.entries(packs)) {
      for (const [key, value] of Object.entries(pack)) expect(value.trim(), `${locale}.${key}`).not.toBe("");
    }
  });

  it("法语覆盖主要界面文案", () => {
    expect(fr.settings.locale_fr).toBe("Français");
    expect(fr.work.brandLine).not.toBe(en.work.brandLine);
    expect(fr.errors.not_found).not.toBe(en.errors.not_found);
    expect(fr.chat.queuedHint).not.toBe(en.chat.queuedHint);
  });

  it("错误码文案覆盖契约中的全部错误码", () => {
    const codes = ["invalid", "unauthorized", "forbidden", "not_found", "conflict", "denied", "unavailable", "timeout", "cancelled", "internal", "network", "aborted"];
    for (const code of codes) {
      expect(zhFlat[`errors.${code}`], code).toBeTruthy();
      expect(frFlat[`errors.${code}`], code).toBeTruthy();
    }
  });
});
