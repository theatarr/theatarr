import { afterEach, describe, expect, it } from "vitest";
import { dayFormatter, fmtAgo, fmtBytes, fmtDayYear, fmtDuration, fmtGoDuration, fmtPercent, fmtRate, fmtTime, localDay, nameWithoutYear, plural, pluralWord, titleWithYear } from "./format";

describe("format", () => {
  const now = new Date("2026-10-26T19:00:00Z");
  it("bytes", () => {
    expect(fmtBytes(512)).toBe("512 B");
    expect(fmtBytes(50_000_000_000)).toBe("46.6 GiB");
    expect(fmtBytes(undefined)).toBe("—");
  });
  it("percent", () => {
    expect(fmtPercent(1)).toBe("100%");
    expect(fmtPercent(0.6667)).toBe("66.7%");
    expect(fmtPercent(null)).toBe("—");
  });
  it("ago", () => {
    expect(fmtAgo("2026-10-26T18:59:00Z", now)).toBe("1 min ago");
    expect(fmtAgo("2026-10-26T18:59:48Z", now)).toBe("12 s ago");
    expect(fmtAgo("2026-10-26T16:00:00Z", now)).toBe("3 h ago");
    expect(fmtAgo("2026-10-26T19:05:00Z", now)).toBe("in 5 min");
  });
  it("durations", () => {
    expect(fmtDuration("2026-10-26T18:59:39.39Z", "2026-10-26T18:59:40.865Z")).toBe("1.5 s");
    expect(fmtDuration("2026-10-26T18:57:57Z", "2026-10-26T18:59:41Z")).toBe("1 min 44 s");
    expect(fmtGoDuration("1m0s")).toBe("1 min");
    expect(fmtGoDuration("1h30m0s")).toBe("1 h 30 min");
    expect(fmtRate(30, "1m0s")).toBe("30 / min");
  });
  it("time", () => {
    expect(fmtTime("0001-01-01T00:00:00Z", now)).toBe("—");
    expect(fmtTime(undefined, now)).toBe("—");
  });
});

describe("the day of a moment", () => {
  const zone = process.env.TZ;
  afterEach(() => {
    if (zone === undefined) delete process.env.TZ;
    else process.env.TZ = zone;
  });
  it("is the day in the browser's time zone, not the UTC day", () => {
    process.env.TZ = "America/Los_Angeles";
    expect(localDay("2026-10-27T03:00:00Z")).toBe("2026-10-26");
    process.env.TZ = "Pacific/Auckland";
    expect(localDay("2026-10-26T20:00:00Z")).toBe("2026-10-27");
  });
  it("leaves a calendar day as written", () => {
    process.env.TZ = "America/Los_Angeles";
    expect(localDay("2026-10-27")).toBe("2026-10-27");
    expect(localDay(undefined)).toBe("—");
  });
});

describe("titleWithYear", () => {
  it("adds the year once", () => {
    expect(titleWithYear("Alien", 1979)).toBe("Alien (1979)");
    expect(titleWithYear("Foundation (2021)", 2021)).toBe("Foundation (2021)");
    expect(titleWithYear("Dune (1984)", 2021)).toBe("Dune (1984) (2021)");
    expect(titleWithYear("Alien", undefined)).toBe("Alien");
    expect(titleWithYear("(2021)", 2021)).toBe("(2021) (2021)");
  });
  it("drops only a trailing year equal to the year", () => {
    expect(nameWithoutYear("Foundation (2021)", 2021)).toBe("Foundation");
    expect(nameWithoutYear("Dune (1984)", 2021)).toBe("Dune (1984)");
    expect(nameWithoutYear("Foundation (2021)", undefined)).toBe("Foundation (2021)");
    expect(nameWithoutYear(undefined, 2021)).toBe("");
  });
});

describe("plural", () => {
  it("picks the word by the count and formats the number", () => {
    expect(plural(1, "title")).toBe("1 title");
    expect(plural(2, "title")).toBe("2 titles");
    expect(plural(0, "result")).toBe("0 results");
    expect(plural(1128, "search", "searches")).toBe("1,128 searches");
    expect(plural(1, "frozen copy", "frozen copies")).toBe("1 frozen copy");
    expect(`${plural(1, "title")} ${pluralWord(1, "matches", "match")}`).toBe("1 title matches");
    expect(`${plural(2, "title")} ${pluralWord(2, "matches", "match")}`).toBe("2 titles match");
  });
});

describe("days", () => {
  const now = new Date("2026-11-13T12:00:00Z");
  it("always with the year", () => {
    expect(fmtDayYear("2026-01-01")).toBe("Jan 1, 2026");
    expect(fmtDayYear("2025-12-25T08:00:00Z")).toBe("Dec 25, 2025");
    expect(fmtDayYear(undefined)).toBe("—");
  });
  it("one format per list: the year on every date once a date is in another year", () => {
    const mixed = dayFormatter(["2025-12-25", "2026-01-01"], now);
    expect(mixed("2025-12-25")).toBe("Dec 25, 2025");
    expect(mixed("2026-01-01")).toBe("Jan 1, 2026");
    const thisYear = dayFormatter(["2026-01-01", undefined, "2026-11-20"], now);
    expect(thisYear("2026-01-01")).toBe("Jan 1");
    expect(thisYear("2026-11-20")).toBe("Nov 20");
  });
});
