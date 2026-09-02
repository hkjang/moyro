// One place for every human-readable timestamp in the UI.
//
// Before this module, message rows, the inbox, work items, and the context
// panel each formatted time on their own, and the results disagreed with each
// other on the same screen ("오후 01:51" beside "오후 1:52").
//
// The clock and day labels are assembled by hand rather than through
// Intl.DateTimeFormat. ICU's Korean day-period output differs between CLDR
// releases ("오후" vs "PM"), so the same code would render differently across
// browsers and the Node test runner; assembling the string keeps it identical
// everywhere. Only the mixed date-and-time list format still uses Intl, where
// the numeric parts are stable across releases.

const LOCALE = "ko-KR";
const DAY_MS = 86_400_000;
const WEEKDAYS = ["일", "월", "화", "수", "목", "금", "토"] as const;

const dateTimeFormatter = new Intl.DateTimeFormat(LOCALE, {
  year: "numeric",
  month: "numeric",
  day: "numeric",
});

/** "오후 1:51" — the time of day, without a leading zero on the hour. */
export function formatClockTime(value: number): string {
  if (!value) return "—";
  const date = new Date(value);
  const hours = date.getHours();
  const period = hours < 12 ? "오전" : "오후";
  const hour12 = hours % 12 === 0 ? 12 : hours % 12;
  return `${period} ${hour12}:${String(date.getMinutes()).padStart(2, "0")}`;
}

/** "2026. 8. 30. 오후 1:52" — date and time together for lists and metadata. */
export function formatDateTime(value: number): string {
  if (!value) return "—";
  return `${dateTimeFormatter.format(value)} ${formatClockTime(value)}`;
}

/** True when both instants fall on the same local calendar day. */
export function isSameDay(left: number, right: number): boolean {
  const a = new Date(left);
  const b = new Date(right);
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/**
 * Label for a date separator: "오늘", "어제", then "8월 28일 (금)" within the
 * current year and "2025년 12월 31일 (수)" beyond it. `now` is injectable so
 * tests and the separator itself do not depend on the wall clock.
 */
export function formatDayLabel(value: number, now: number = Date.now()): string {
  if (!value) return "—";
  if (isSameDay(value, now)) return "오늘";
  if (isSameDay(value, now - DAY_MS)) return "어제";
  const target = new Date(value);
  const monthDay = `${target.getMonth() + 1}월 ${target.getDate()}일 (${WEEKDAYS[target.getDay()]})`;
  return target.getFullYear() === new Date(now).getFullYear()
    ? monthDay
    : `${target.getFullYear()}년 ${monthDay}`;
}

/** "3분 전", "어제", "2일 후" — for freshness hints, never for exact records. */
export function formatRelativeTime(value: number, now: number = Date.now()): string {
  if (!value) return "—";
  const delta = value - now;
  const abs = Math.abs(delta);
  const formatter = new Intl.RelativeTimeFormat(LOCALE, { numeric: "auto" });
  if (abs < 60_000) return formatter.format(Math.round(delta / 1_000), "second");
  if (abs < 3_600_000) return formatter.format(Math.round(delta / 60_000), "minute");
  if (abs < DAY_MS) return formatter.format(Math.round(delta / 3_600_000), "hour");
  return formatter.format(Math.round(delta / DAY_MS), "day");
}

export function isToday(value: number, now: number = Date.now()): boolean {
  return Boolean(value) && isSameDay(value, now);
}
