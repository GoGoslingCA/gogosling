import { describe, expect, it } from "vitest";
import { formatDateTime, formatMessageTime, formatRelative, formatYmd, resolveTimeZone } from "./time";

describe("time", () => {
  const iso = "2026-07-09T07:16:37Z";

  it("按服务端时区格式化，而不是浏览器时区", () => {
    expect(formatYmd(iso, { timeZone: "Asia/Shanghai" })).toBe("2026-07-09");
    expect(formatMessageTime(iso, { timeZone: "Asia/Shanghai", now: new Date("2026-07-10T00:00:00Z") })).toBe("07-09 15:16");
    expect(formatMessageTime(iso, { timeZone: "UTC", now: new Date("2026-07-09T23:00:00Z") })).toBe("07:16");
    expect(formatDateTime(iso, { timeZone: "Asia/Tokyo", locale: "en" })).toContain("16:16:37");
  });

  it("无效时区回落到浏览器时区而不是抛错", () => {
    expect(resolveTimeZone("Mars/Olympus")).toBeUndefined();
    expect(resolveTimeZone("")).toBeUndefined();
    expect(() => formatDateTime(iso, { timeZone: "Mars/Olympus" })).not.toThrow();
  });

  it("法语日期使用日月年格式，切回英文保留月日年格式", () => {
    expect(formatDateTime(iso, { timeZone: "UTC", locale: "fr-CA" })).toContain("09-07-2026");
    expect(formatDateTime(iso, { timeZone: "UTC", locale: "en" })).toContain("07-09-2026");
  });

  it("空值与非法日期给出占位", () => {
    expect(formatDateTime(undefined)).toBe("—");
    expect(formatDateTime("not a date")).toBe("—");
    expect(formatMessageTime(null)).toBe("");
  });

  it("相对时间", () => {
    const labels = { justNow: "刚刚", minutes: (n: number) => `${n}m`, hours: (n: number) => `${n}h`, days: (n: number) => `${n}d` };
    const now = new Date("2026-07-10T12:00:00Z");
    expect(formatRelative("2026-07-10T11:59:40Z", labels, { now })).toBe("刚刚");
    expect(formatRelative("2026-07-10T11:30:00Z", labels, { now })).toBe("30m");
    expect(formatRelative("2026-07-10T09:00:00Z", labels, { now })).toBe("3h");
    expect(formatRelative("2026-07-08T12:00:00Z", labels, { now })).toBe("2d");
    expect(formatRelative("2026-06-01T12:00:00Z", labels, { now, timeZone: "UTC" })).toBe("2026-06-01");
  });
});
