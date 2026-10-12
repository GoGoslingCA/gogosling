import { describe, expect, it } from "vitest";
import { messageKeys, pickLocale, t } from "../src/main/i18n.js";

describe("壳文案", () => {
  it("zh、en 与 fr 的键集合完全一致", () => {
    expect(messageKeys("en")).toEqual(messageKeys("zh"));
    expect(messageKeys("fr")).toEqual(messageKeys("zh"));
  });

  it("按系统语言主子标签选择语言，默认英文", () => {
    expect(pickLocale("zh-CN")).toBe("zh");
    expect(pickLocale("zh_TW")).toBe("zh");
    expect(pickLocale("ZH")).toBe("zh");
    expect(pickLocale("en-US")).toBe("en");
    expect(pickLocale("fr-FR")).toBe("fr");
    expect(pickLocale("fr_CA")).toBe("fr");
    expect(pickLocale("ja")).toBe("en");
    expect(pickLocale(undefined)).toBe("en");
  });

  it("插值与缺参数", () => {
    expect(t("zh", "daemon.state.restarting", { attempt: 2, max: 5 })).toBe("守护进程重启中（第 2/5 次）");
    expect(t("en", "daemon.state.restarting", { attempt: 2 })).toBe("daemon restarting (2/{max})");
    expect(t("zh", "tray.tooltip", { status: "x" })).toBe("gogosling — x");
  });

  it("所有文案非空且占位符在各语言里一致", () => {
    for (const key of messageKeys("zh")) {
      const k = key as Parameters<typeof t>[1];
      const zh = t("zh", k);
      const en = t("en", k);
      const fr = t("fr", k);
      expect(zh.length).toBeGreaterThan(0);
      expect(en.length).toBeGreaterThan(0);
      expect(fr.length).toBeGreaterThan(0);
      const placeholders = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();
      expect(placeholders(en)).toEqual(placeholders(zh));
      expect(placeholders(fr)).toEqual(placeholders(zh));
    }
  });
});
