import { describe, expect, it } from "vitest";

import { formatClockTime, formatDateTime, formatDayLabel, formatRelativeTime, isSameDay } from "./time";

// A fixed local instant: 2026-08-30 13:51 local time.
const base = new Date(2026, 7, 30, 13, 51).getTime();

describe("formatClockTime", () => {
  it("drops the leading zero the old per-component formatter produced", () => {
    expect(formatClockTime(base)).toBe("오후 1:51");
    expect(formatClockTime(new Date(2026, 7, 30, 9, 5).getTime())).toBe("오전 9:05");
  });

  it("renders an em dash for an absent timestamp", () => {
    expect(formatClockTime(0)).toBe("—");
  });
});

describe("formatDayLabel", () => {
  it("names today and yesterday instead of repeating the date", () => {
    expect(formatDayLabel(base, base)).toBe("오늘");
    expect(formatDayLabel(base - 86_400_000, base)).toBe("어제");
  });

  it("omits the year inside the current year and includes it beyond", () => {
    expect(formatDayLabel(new Date(2026, 7, 28).getTime(), base)).toBe("8월 28일 (금)");
    expect(formatDayLabel(new Date(2025, 11, 31).getTime(), base)).toBe("2025년 12월 31일 (수)");
  });
});

describe("formatDateTime", () => {
  it("matches the list format the inbox already used", () => {
    expect(formatDateTime(base)).toBe("2026. 8. 30. 오후 1:51");
    expect(formatDateTime(new Date(2026, 0, 5, 0, 0).getTime())).toBe("2026. 1. 5. 오전 12:00");
  });
});

describe("isSameDay", () => {
  it("compares local calendar days, not 24-hour windows", () => {
    const lateNight = new Date(2026, 7, 30, 23, 59).getTime();
    const earlyMorning = new Date(2026, 7, 31, 0, 1).getTime();
    expect(isSameDay(base, lateNight)).toBe(true);
    expect(isSameDay(lateNight, earlyMorning)).toBe(false);
  });
});

describe("formatRelativeTime", () => {
  it("scales from seconds to days", () => {
    expect(formatRelativeTime(base - 30_000, base)).toBe("30초 전");
    expect(formatRelativeTime(base - 5 * 60_000, base)).toBe("5분 전");
    expect(formatRelativeTime(base - 2 * 3_600_000, base)).toBe("2시간 전");
    expect(formatRelativeTime(base + 86_400_000, base)).toBe("내일");
  });
});
