import { describe, expect, it } from "vitest";
import { applyLocale, currentLocale, detectBrowserLocale, initI18n, normalizeLocale, SUPPORTED_LOCALES } from "./i18n";

describe("语言归一", () => {
  it("识别法语、英语，未知回落到中文", () => {
    expect(SUPPORTED_LOCALES).toEqual(["zh", "en", "fr"]);
    expect(normalizeLocale("fr-FR")).toBe("fr");
    expect(normalizeLocale("FR_ca")).toBe("fr");
    expect(normalizeLocale(" fr-CA ")).toBe("fr");
    expect(normalizeLocale("french")).toBe("zh");
    expect(normalizeLocale("english")).toBe("zh");
    expect(normalizeLocale("en-US")).toBe("en");
    expect(normalizeLocale("ja")).toBe("zh");
    expect(normalizeLocale(undefined)).toBe("zh");
  });

  it("按浏览器语言列表的顺序选择第一种受支持的语言", () => {
    expect(detectBrowserLocale(["fr-CA", "en"])).toBe("fr");
    expect(detectBrowserLocale(["de", "en-GB"])).toBe("en");
    expect(detectBrowserLocale(["zh-CN", "fr"])).toBe("zh");
    expect(detectBrowserLocale(["ja"])).toBe("zh");
    expect(detectBrowserLocale(["french", " FR_ca ", "en"])).toBe("fr");
  });

  it("语言切换同步文案、请求语言及页面语言，切回后不残留法文", async () => {
    const i18n = initI18n("zh");
    await applyLocale("fr");
    expect(currentLocale()).toBe("fr");
    expect(document.documentElement.lang).toBe("fr");
    expect(i18n.t("common.save")).toBe("Enregistrer");
    await applyLocale("en");
    expect(i18n.t("common.save")).toBe("Save");
    expect(document.documentElement.lang).toBe("en");
    await applyLocale("zh");
    expect(i18n.t("common.save")).toBe("保存");
    expect(document.documentElement.lang).toBe("zh-CN");
  });
});
