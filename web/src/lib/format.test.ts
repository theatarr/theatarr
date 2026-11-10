import { afterEach, describe, expect, it } from "vitest";
import { fmtAgo, fmtBytes, fmtDuration, fmtGoDuration, fmtPercent, fmtRate, fmtTime, localDay } from "./format";

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
