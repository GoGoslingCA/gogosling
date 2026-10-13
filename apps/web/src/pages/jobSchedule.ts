// 周期控件只生成现有调度表达式；未识别的表达式不解析、不改写。
export type ScheduleMode = "hourly" | "daily" | "weekdays" | "weekly" | "custom";
export interface ScheduleFields { mode: ScheduleMode; time: string; weekday: string; minute: string }

/** 仅识别能无歧义表达的基本周期；调用方保留原始 schedule 直到用户修改控件。 */
export function readSchedule(schedule: string): ScheduleFields {
  const defaults: ScheduleFields = { mode: "custom", time: "09:00", weekday: "1", minute: "0" };
  const raw = schedule.trim();
  if (raw === "@hourly") return { ...defaults, mode: "hourly" };
  if (raw === "@daily" || raw === "@midnight") return { ...defaults, mode: "daily", time: "00:00" };
  if (raw === "@weekly") return { ...defaults, mode: "weekly", time: "00:00", weekday: "0" };
  const parts = raw.split(/\s+/);
  if (parts.length !== 5 || parts[2] !== "*" || parts[3] !== "*" || !/^\d{1,2}$/.test(parts[0]!) || Number(parts[0]) > 59) return defaults;
  const minute = String(Number(parts[0]));
  if (parts[1] === "*" && parts[4] === "*") return { ...defaults, mode: "hourly", minute };
  if (!/^\d{1,2}$/.test(parts[1]!) || Number(parts[1]) > 23) return defaults;
  const time = `${parts[1]!.padStart(2, "0")}:${parts[0]!.padStart(2, "0")}`;
  if (parts[4] === "*") return { ...defaults, mode: "daily", time, minute };
  if (parts[4] === "1-5") return { ...defaults, mode: "weekdays", time, minute };
  if (/^[0-6]$/.test(parts[4]!)) return { ...defaults, mode: "weekly", time, minute, weekday: parts[4]! };
  return defaults;
}

/** 无效或未填完的控件返回空值，禁止提交而不静默回落到另一个时间。 */
export function writeSchedule(fields: ScheduleFields): string | undefined {
  if (fields.mode === "custom") return undefined;
  if (fields.mode === "hourly") return /^\d{1,2}$/.test(fields.minute) && Number(fields.minute) <= 59 ? `${Number(fields.minute)} * * * *` : undefined;
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(fields.time)) return undefined;
  const [hour, minute] = fields.time.split(":").map(Number);
  if (fields.mode === "weekly" && !/^[0-6]$/.test(fields.weekday)) return undefined;
  const days = fields.mode === "weekdays" ? "1-5" : fields.mode === "weekly" ? fields.weekday : "*";
  return `${minute} ${hour} * * ${days}`;
}
