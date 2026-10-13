import { describe, expect, it } from "vitest";
import { readSchedule, writeSchedule } from "./jobSchedule";

describe("周期控件与现有调度表达式", () => {
  it("识别基本周期与别名，不误读复杂或无效表达式", () => {
    expect(readSchedule("@hourly").mode).toBe("hourly");
    expect(readSchedule("@daily")).toMatchObject({ mode: "daily", time: "00:00" });
    expect(readSchedule("@weekly")).toMatchObject({ mode: "weekly", weekday: "0" });
    expect(readSchedule("15 * * * *")).toMatchObject({ mode: "hourly", minute: "15" });
    expect(readSchedule("30 9 * * 1-5")).toMatchObject({ mode: "weekdays", time: "09:30" });
    expect(readSchedule("0 20 * * 6")).toMatchObject({ mode: "weekly", time: "20:00", weekday: "6" });
    for (const expression of ["*/30 * * * *", "0 9 1 * *", "CRON_TZ=UTC 0 9 * * *", "0 25 * * *", "60 9 * * *", "@every 1h", "0 9 * * 7"]) expect(readSchedule(expression).mode).toBe("custom");
  });
  it("只生成合法时间且不静默修复未完成的输入", () => {
    expect(writeSchedule({ mode: "daily", time: "09:30", weekday: "1", minute: "0" })).toBe("30 9 * * *");
    expect(writeSchedule({ mode: "weekdays", time: "00:00", weekday: "1", minute: "0" })).toBe("0 0 * * 1-5");
    expect(writeSchedule({ mode: "weekly", time: "23:59", weekday: "0", minute: "0" })).toBe("59 23 * * 0");
    expect(writeSchedule({ mode: "hourly", time: "", weekday: "1", minute: "05" })).toBe("5 * * * *");
    expect(writeSchedule({ mode: "daily", time: "", weekday: "1", minute: "0" })).toBeUndefined();
    expect(writeSchedule({ mode: "daily", time: "24:00", weekday: "1", minute: "0" })).toBeUndefined();
    expect(writeSchedule({ mode: "hourly", time: "", weekday: "1", minute: "1.5" })).toBeUndefined();
    expect(writeSchedule({ mode: "weekly", time: "09:00", weekday: "7", minute: "0" })).toBeUndefined();
  });
});
