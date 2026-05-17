// PUL-154 «Wake up in N» smart-preset utility.
//
// Picks the four most-likely "wake me up at..." offsets for the current
// wall-clock hour, so the dropdown's first three taps cover the 80% of
// real cases (no date picker needed). The browser's local time drives the
// selection; the resulting fire_at is sent to the server in UTC ISO form
// (the caller calls .toISOString() on the produced Date).
//
// Time bands chosen to match the user's actual brainstorm-on-phone
// workflow (see PUL-154 office-hours session):
//   06–11 (morning):       2h, 14:00, 18:00, tomorrow 09:00
//   12–17 (workday):       1h, 18:00, tomorrow 09:00, in 3 days
//   18–22 (evening):       30m, tomorrow 09:00, tomorrow 14:00, in 3 days
//   23–05 (night):         tomorrow 09:00, tomorrow 14:00, day after 09:00, in 1 week
//
// All preset *values* are absolute Date instances (in browser local TZ),
// so DST transitions are handled correctly by the Date constructor: setting
// hours on a Date object respects the local timezone rules. The dropdown
// shows the label, the createReminder mutation sends ISO UTC.

export type ReminderPreset = {
  /** Human label shown in the dropdown row. */
  label: string;
  /** Stable identifier for tests / analytics. */
  key: string;
  /** Absolute fire time, browser local TZ. Caller converts to UTC ISO. */
  fireAt: Date;
};

/** Time bands the dropdown is keyed on, exported for test introspection. */
export type TimeOfDayBand = "morning" | "workday" | "evening" | "night";

export function bandForHour(hour: number): TimeOfDayBand {
  if (hour >= 6 && hour <= 11) return "morning";
  if (hour >= 12 && hour <= 17) return "workday";
  if (hour >= 18 && hour <= 22) return "evening";
  return "night"; // 23..05
}

/** Set hours/minutes on a copy of `base`, keeping the same calendar day. */
function atSameDay(base: Date, hour: number, minute = 0): Date {
  const d = new Date(base);
  d.setHours(hour, minute, 0, 0);
  return d;
}

function addMinutes(base: Date, minutes: number): Date {
  return new Date(base.getTime() + minutes * 60_000);
}

function addDays(base: Date, days: number): Date {
  const d = new Date(base);
  d.setDate(d.getDate() + days);
  return d;
}

/**
 * Generate four contextual presets plus the canonical "Custom..." spot
 * (the caller renders Custom separately; it's not in this array).
 *
 * The `now` parameter is injectable so tests can pin time without mocking
 * Date globally. Real callers pass `new Date()`.
 */
export function presetsForNow(now: Date): ReminderPreset[] {
  const hour = now.getHours();
  const band = bandForHour(hour);

  switch (band) {
    case "morning":
      return [
        { key: "in_2h", label: "In 2 hours", fireAt: addMinutes(now, 120) },
        { key: "after_lunch", label: "After lunch (14:00)", fireAt: atSameDay(now, 14) },
        { key: "end_of_day", label: "End of day (18:00)", fireAt: atSameDay(now, 18) },
        { key: "tomorrow_morning", label: "Tomorrow 09:00", fireAt: atSameDay(addDays(now, 1), 9) },
      ];
    case "workday":
      return [
        { key: "in_1h", label: "In 1 hour", fireAt: addMinutes(now, 60) },
        { key: "end_of_day", label: "End of day (18:00)", fireAt: atSameDay(now, 18) },
        { key: "tomorrow_morning", label: "Tomorrow 09:00", fireAt: atSameDay(addDays(now, 1), 9) },
        { key: "in_3_days", label: "In 3 days", fireAt: atSameDay(addDays(now, 3), 9) },
      ];
    case "evening":
      return [
        { key: "in_30m", label: "In 30 minutes", fireAt: addMinutes(now, 30) },
        { key: "tomorrow_morning", label: "Tomorrow 09:00", fireAt: atSameDay(addDays(now, 1), 9) },
        { key: "tomorrow_afternoon", label: "Tomorrow 14:00", fireAt: atSameDay(addDays(now, 1), 14) },
        { key: "in_3_days", label: "In 3 days", fireAt: atSameDay(addDays(now, 3), 9) },
      ];
    case "night":
      return [
        { key: "tomorrow_morning", label: "Tomorrow 09:00", fireAt: atSameDay(addDays(now, 1), 9) },
        { key: "tomorrow_afternoon", label: "Tomorrow 14:00", fireAt: atSameDay(addDays(now, 1), 14) },
        { key: "day_after_morning", label: "Day after tomorrow 09:00", fireAt: atSameDay(addDays(now, 2), 9) },
        { key: "in_1_week", label: "In 1 week", fireAt: atSameDay(addDays(now, 7), 9) },
      ];
  }
}

/**
 * Format a human-readable "Remind in 2h 15m" string for the pending chip.
 * Skips zero units (no "0m" on the end) and caps granularity at minutes.
 */
export function humanizeRelative(target: Date, now: Date = new Date()): string {
  const deltaMs = target.getTime() - now.getTime();
  if (deltaMs <= 0) return "now";
  const totalMin = Math.round(deltaMs / 60_000);

  const days = Math.floor(totalMin / 1440);
  const hours = Math.floor((totalMin % 1440) / 60);
  const mins = totalMin % 60;

  const parts: string[] = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0) parts.push(`${hours}h`);
  if (mins > 0 && days === 0) parts.push(`${mins}m`);
  return parts.length > 0 ? parts.join(" ") : "<1m";
}
