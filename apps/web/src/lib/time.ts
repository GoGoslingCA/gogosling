// 时间格式化：服务端一律给 RFC 3339（UTC），展示时按设置里的时区转换。
// 时区无效或为空时回落到浏览器时区，而不是抛错或静默显示 UTC。
//
// 统一接收 ISO 字符串、毫秒时间戳或 Date，按界面语言和指定时区格式化。

export interface FormatOptions {
  timeZone?: string;
  locale?: string;
  now?: Date;
}

function toDate(input: string | number | Date | undefined | null): Date | null {
  if (input === undefined || input === null || input === "") return null;
  const d = input instanceof Date ? input : new Date(input);
  return Number.isNaN(d.getTime()) ? null : d;
}

function isValidTimeZone(tz: string | undefined): tz is string {
  if (!tz) return false;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/** 解析出可用的时区；无效时返回 undefined（即浏览器时区）。 */
export function resolveTimeZone(tz: string | undefined): string | undefined {
  return isValidTimeZone(tz) ? tz : undefined;
}

function intlLocale(locale?: string): string {
  if (!locale) return "zh-CN";
  const primary = locale.trim().toLowerCase().split(/[-_]/)[0];
  if (primary === "en") return "en-US";
  if (primary === "fr") return "fr-FR";
  return "zh-CN";
}

/** 日期部分 YYYY-MM-DD（给定时区）。 */
export function formatYmd(input: string | number | Date, opts: FormatOptions = {}): string {
  const d = toDate(input);
  if (!d) return "";
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: resolveTimeZone(opts.timeZone),
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(d);
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "";
  return `${get("year")}-${get("month")}-${get("day")}`;
}

/** 完整日期时间，例如 2026-10-10 18:30:05。 */
export function formatDateTime(input: string | number | Date | undefined | null, opts: FormatOptions = {}): string {
  const d = toDate(input);
  if (!d) return "—";
  return new Intl.DateTimeFormat(intlLocale(opts.locale), {
    timeZone: resolveTimeZone(opts.timeZone),
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  })
    .format(d)
    .replace(/\//g, "-");
}

/** 聊天消息时间：当天只显示 HH:mm，否则显示 MM-DD HH:mm。 */
export function formatMessageTime(input: string | number | Date | undefined | null, opts: FormatOptions = {}): string {
  const d = toDate(input);
  if (!d) return "";
  const tz = resolveTimeZone(opts.timeZone);
  const now = opts.now ?? new Date();
  const hm = new Intl.DateTimeFormat("en-GB", { timeZone: tz, hour: "2-digit", minute: "2-digit", hour12: false }).format(d);
  if (formatYmd(d, { timeZone: tz }) === formatYmd(now, { timeZone: tz })) return hm;
  const ymd = formatYmd(d, { timeZone: tz });
  return `${ymd.slice(5)} ${hm}`;
}

/** 相对时间（用于列表），精确到分钟/小时/天；超过 7 天显示日期。 */
export function formatRelative(input: string | number | Date | undefined | null, labels: { justNow: string; minutes: (n: number) => string; hours: (n: number) => string; days: (n: number) => string }, opts: FormatOptions = {}): string {
  const d = toDate(input);
  if (!d) return "";
  const now = opts.now ?? new Date();
  const diffMs = now.getTime() - d.getTime();
  const minutes = Math.floor(diffMs / 60_000);
  if (minutes < 1) return labels.justNow;
  if (minutes < 60) return labels.minutes(minutes);
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return labels.hours(hours);
  const days = Math.floor(hours / 24);
  if (days <= 7) return labels.days(days);
  return formatYmd(d, opts);
}
