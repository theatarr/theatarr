import { afterEach, describe, expect, it } from "vitest";
import { entryDay, episodeCode, monthDays, placeEntries, shiftDay, ymd } from "./calendar";

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

describe("the day an entry sits on", () => {
  const zone = process.env.TZ;
  afterEach(() => {
    if (zone === undefined) delete process.env.TZ;
    else process.env.TZ = zone;
  });
  // Tuesday Nov 3, 21:00 in New York.
  const us = { date: "2026-11-03", at: "2026-11-04T02:00:00Z", n: "us" };
  it("is the local day of the air moment, so the day and the clock time agree", () => {
    process.env.TZ = "Europe/Berlin";
    expect(new Date(us.at).getHours()).toBe(3);
    expect(entryDay(us)).toBe("2026-11-04");
    process.env.TZ = "America/Los_Angeles";
    expect(entryDay(us)).toBe("2026-11-03");
  });
  it("keeps a release date, and an episode with no known air time, on the day as written", () => {
    process.env.TZ = "America/Los_Angeles";
    expect(entryDay({ date: "2026-11-03" })).toBe("2026-11-03");
    process.env.TZ = "Pacific/Auckland";
    expect(entryDay({ date: "2026-11-03" })).toBe("2026-11-03");
    expect(entryDay({ date: "2026-11-03", at: "nonsense" })).toBe("2026-11-03");
  });
  it("keeps the window's own days, from a fetch one day wider", () => {
    process.env.TZ = "Europe/Berlin";
    const items = [
      { date: "2026-10-31", at: "2026-11-01T01:00:00Z", n: "in from the day before" },
      { date: "2026-11-03", n: "movie" },
      us,
      { date: "2026-11-04", at: "2026-11-04T00:30:00Z", n: "early" },
      { date: "2026-11-30", at: "2026-12-01T02:00:00Z", n: "out to the day after" },
      { date: "2026-12-01", n: "outside" },
    ];
    expect(placeEntries(items, "2026-11-01", "2026-11-30").map((x) => `${x.day} ${x.e.n}`)).toEqual([
      "2026-11-01 in from the day before",
      "2026-11-03 movie",
      "2026-11-04 early",
      "2026-11-04 us",
    ]);
    expect(shiftDay("2026-11-01", -1)).toBe("2026-10-31");
    expect(shiftDay("2026-11-30", 1)).toBe("2026-12-01");
  });
});
