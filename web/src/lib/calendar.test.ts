import { afterEach, describe, expect, it } from "vitest";
import { calendarHref, calendarLegend, calendarTone, entryDay, episodeCode, monthDays, placeEntries, shiftDay, ymd } from "./calendar";

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

describe("calendarTone", () => {
  const now = new Date(2026, 10, 13, 12, 0);
  const ep = { kind: "episode", type: "episode", date: "2026-11-10", monitored: true, available: false, acquiring: false };
  const movie = (type: string, date: string) => ({ ...ep, kind: "movie", type, date });
  const tone = (e: Parameters<typeof calendarTone>[0], plain = false) => calendarTone(e, now, plain);

  it("gives each status its colour, in the rule's order", () => {
    expect(tone({ ...ep, available: true, acquiring: true })).toEqual({ tone: "ok", label: "Downloaded" });
    expect(tone({ ...ep, acquiring: true, monitored: false })).toEqual({ tone: "progress", label: "Downloading" });
    expect(tone({ ...ep, monitored: false, date: "2026-12-01" })).toEqual({ tone: "idle", label: "Not monitored" });
    expect(tone({ ...ep, date: "2026-12-01" })).toEqual({ tone: "neutral", label: "Upcoming" });
    expect(tone(ep)).toEqual({ tone: "bad", label: "Missing" });
  });

  it("uses the air time when known, else the day (today is still upcoming)", () => {
    expect(tone({ ...ep, date: "2026-11-13", at: new Date(2026, 10, 13, 11, 0).toISOString() }).tone).toBe("bad");
    expect(tone({ ...ep, date: "2026-11-13", at: new Date(2026, 10, 13, 21, 0).toISOString() }).tone).toBe("neutral");
    expect(tone({ ...ep, date: "2026-11-13" }).tone).toBe("neutral");
    expect(tone({ ...ep, date: "2026-11-12" }).tone).toBe("bad");
  });

  it("never calls a past cinema date missing; the digital and physical dates are", () => {
    expect(tone(movie("theatrical", "2026-10-01"))).toEqual({ tone: "neutral", label: "Released in cinemas" });
    expect(tone(movie("digital", "2026-10-01")).tone).toBe("bad");
    expect(tone(movie("physical", "2026-10-01")).tone).toBe("bad");
    expect(tone(movie("digital", "2026-12-01")).tone).toBe("neutral");
  });

  it("gives accounts without see files the title page's words, never a red missing", () => {
    expect(tone({ ...ep, available: true }, true)).toEqual({ tone: "ok", label: "Available" });
    expect(tone({ ...ep, acquiring: true }, true)).toEqual({ tone: "neutral", label: "Getting ready" });
    expect(tone({ ...ep, date: "2026-12-01" }, true)).toEqual({ tone: "neutral", label: "Upcoming" });
    expect(tone(ep, true)).toEqual({ tone: "neutral", label: "Not available" });
    expect(tone({ ...ep, monitored: false }, true)).toEqual({ tone: "neutral", label: "Not available" });
    expect(tone(movie("digital", "2026-10-01"), true)).toEqual({ tone: "neutral", label: "Not available" });
  });

  it("has a legend per account", () => {
    expect(calendarLegend(false).map((l) => l.label)).toEqual(["Downloaded", "Downloading", "Missing", "Upcoming", "Not monitored"]);
    expect(calendarLegend(true).map((l) => l.tone)).toEqual(["ok", "neutral"]);
  });

  it("opens an episode in its series' episode window, a movie on its page", () => {
    expect(calendarHref({ kind: "episode", title: { id: 3 }, episode_id: 41 })).toBe("/title/3?tab=episodes&episode=41");
    expect(calendarHref({ kind: "movie", title: { id: 1 } })).toBe("/title/1");
  });
});
