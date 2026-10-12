// 界面文案：zh（默认）、en 与 fr，键树必须一致（见 locales/locales.test.ts）。
// 服务端 Settings.locale 是用户偏好的来源；未连接时按浏览器语言选择。

import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import zh from "./locales/zh.json";
import en from "./locales/en.json";
import fr from "./locales/fr.json";
import type { Locale } from "./api/types";

export const SUPPORTED_LOCALES: readonly Locale[] = ["zh", "en", "fr"];

/** 把任意语言标签归一到受支持的语言；未知时回落到 zh（项目默认）。 */
export function normalizeLocale(raw: string | null | undefined): Locale {
  const primary = (raw ?? "").trim().toLowerCase().split(/[-_]/)[0];
  if (primary === "fr") return "fr";
  if (primary === "en") return "en";
  return "zh";
}

/** 根据浏览器语言列表挑选初始语言。 */
export function detectBrowserLocale(languages: readonly string[]): Locale {
  for (const lang of languages) {
    const primary = lang.trim().toLowerCase().split(/[-_]/)[0];
    if (primary === "zh" || primary === "fr" || primary === "en") return primary;
  }
  return "zh";
}

let initialized = false;

/** 初始化 i18next（幂等）。 */
export function initI18n(initial?: Locale): typeof i18next {
  if (initialized) return i18next;
  initialized = true;
  const lng = initial ?? (typeof navigator !== "undefined" ? detectBrowserLocale(navigator.languages ?? [navigator.language]) : "zh");
  void i18next.use(initReactI18next).init({
    resources: {
      zh: { translation: zh },
      en: { translation: en },
      fr: { translation: fr },
    },
    lng,
    fallbackLng: "zh",
    supportedLngs: [...SUPPORTED_LOCALES],
    interpolation: { escapeValue: false },
    returnNull: false,
  });
  syncDocumentLang(lng);
  i18next.on("languageChanged", (l) => syncDocumentLang(normalizeLocale(l)));
  return i18next;
}

/** 切换界面语言（幂等）。 */
export async function applyLocale(locale: Locale): Promise<void> {
  if (normalizeLocale(i18next.language) === locale) return;
  await i18next.changeLanguage(locale);
}

/** 当前界面语言，供 Accept-Language 使用。 */
export function currentLocale(): Locale {
  return normalizeLocale(i18next.language);
}

function syncDocumentLang(locale: Locale): void {
  if (typeof document === "undefined") return;
  document.documentElement.lang = locale === "zh" ? "zh-CN" : locale === "fr" ? "fr" : "en";
}

export default i18next;
