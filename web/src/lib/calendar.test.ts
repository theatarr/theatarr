import { describe, expect, it } from "vitest";
import { episodeCode, monthDays, ymd } from "./calendar";

describe("monthDays", () => {
  it("covers whole weeks around the month", () => {
    const days = monthDays(2026, 9); // October 2026: Thursday 1st to Saturday 31st
    expect(ymd(days[0])).toBe("2026-09-27");
    expect(ymd(days[days.length - 1])).toBe("2026-10-31");
    expect(days.length % 7).toBe(0);
    expect(days.length).toBe(35);
  });
  it("spans six weeks when needed", () => {
    const days = monthDays(2026, 7); // August 2026 starts on a Saturday
    expect(days.length).toBe(42);
  });
});

describe("episodeCode", () => {
  it("pads", () => {
    expect(episodeCode(1, 2)).toBe("S01E02");
    expect(episodeCode(undefined, 12)).toBe("E12");
    expect(episodeCode(1, undefined)).toBe("");
  });
});
