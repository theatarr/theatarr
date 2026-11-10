import { afterEach, describe, expect, it, vi } from "vitest";
import { availabilityFilters, isFamily, POSTER_STATUS_LABEL, posterStatus, relDay, titleWatchWords, upcomingRelease, utcToday, watchWords } from "./library";

const label = (...a: Parameters<typeof posterStatus>) => {
  const s = posterStatus(...a);
  return s ? POSTER_STATUS_LABEL[s] : undefined;
};

describe("the poster badge", () => {
  it("says one of eight words, never Upgrade waiting", () => {
    expect(Object.values(POSTER_STATUS_LABEL).sort()).toEqual(["Available", "Best quality known", "Getting ready", "Missing", "Not available", "Some episodes", "Upcoming", "Upgrade found"]);
  });
  const series = (have: number, missing: number) => ({ kind: "series", versions: [{ key: "tv", coverage: { have, missing, total: have + missing } }] }) as Parameters<typeof posterStatus>[4];
  it.each([
    ["Missing", label("missing", true, false, "below_cutoff", series(0, 5))],
    ["Some episodes", label("upgrade", true, false, "below_cutoff", series(3, 2))],
    ["Best quality known", label("upgrade", false, false, "higher_source_exists", series(5, 0))],
    ["Upgrade found", label("available", true, false, "upgrade_available")],
    ["Upgrade found", label("upgrade", true, false, "upgrade_available")],
    ["Best quality known", label("upgrade", false, false, "best_available")],
    ["Best quality known", label("upgrade", false, false, "best_source_seen")],
    // The owner's case: Remux-1080p held, WEBDL-2160p preferred, a search found nothing better.
    ["Best quality known", label("upgrade", false, false, "below_cutoff")],
    ["Available", label("available", false, false, "cutoff_met")],
  ])("picks %s by priority: missing, episode gaps, an upgrade found, best quality known, then available", (want, got) => {
    expect(got).toBe(want);
  });
});

describe("availability for family accounts", () => {
  it("words the poster badge as watchability without see files", () => {
    expect(label("available", false, true)).toBe("Available");
    // The server sends partial only for a series with some episodes and no complete copy.
    expect(label("partial", false, true)).toBe("Some episodes");
    // As the table says (docs/design/library-ui.md).
    expect(label("missing", false, true)).toBe("Not available");
    expect(label("upgrade", true, true)).toBe("Available");
    expect(label("none", false, true)).toBeUndefined();
  });
  it("keeps the copy wording for those who see files", () => {
    expect(label("partial")).toBe("Available");
    expect(label("missing")).toBe("Missing");
    expect(label("upgrade")).toBe("Best quality known");
    expect(label("upgrade", true)).toBe("Upgrade found");
  });
  it("says the title page's one status for family", () => {
    expect(watchWords("available", ["1080p", "2160p", undefined])?.label).toBe("Available in 4K");
    expect(watchWords("available", [])?.label).toBe("Available");
    expect(watchWords("partial", ["1080p"])?.label).toBe("Some episodes available");
    expect(watchWords("missing", [])?.label).toBe("Not available");
    expect(watchWords("none", [])).toBeNull();
  });
  it("names the filters by watchability", () => {
    expect(availabilityFilters(false).map((a) => a.label)).toEqual(["Not available", "Some episodes", "Available"]);
    expect(availabilityFilters(false, "series").map((a) => a.label)).toEqual(["Not available", "Some episodes", "Available"]);
    // A movie has no episodes: its catalog has no "Some episodes".
    expect(availabilityFilters(false, "movie").map((a) => a.label)).toEqual(["Not available", "Available"]);
    expect(availabilityFilters(true, "movie").map((a) => a.id)).toContain("partial");
    expect(availabilityFilters(true).map((a) => a.label)).toContain("Partly");
  });
  it("tells a family account apart", () => {
    expect(isFamily({ seeFiles: false, manageRequests: false })).toBe(true);
    expect(isFamily({ seeFiles: true, manageRequests: true })).toBe(false);
    expect(isFamily({ seeFiles: false, manageRequests: true })).toBe(false);
  });
});

it("gives missing and episode gaps precedence over upgrades", () => {
  const series = { kind: "series" as const, versions: [{ coverage: { have: 2, missing: 3, total: 5, aired: 5, upcoming: 0 } }] };
  expect(label("missing", true, false, "upgrade_available", series)).toBe("Missing");
  expect(label("upgrade", true, false, "upgrade_available", series)).toBe("Some episodes");
  expect(label("available", true)).toBe("Upgrade found");
  expect(label("available", false, false, "higher_source_exists")).toBe("Best quality known");
  expect(label("available", false)).toBe("Available");
  expect(Object.values(POSTER_STATUS_LABEL).sort()).toEqual(["Available", "Best quality known", "Getting ready", "Missing", "Not available", "Some episodes", "Upcoming", "Upgrade found"]);
});


it("uses monitored gaps and preserves family watchability", () => {
  const series = { kind: "series" as const, versions: [{ coverage: { have: 12, total: 24, missing: 0, aired: 24, upcoming: 0 } }] };
  expect(label("available", false, false, undefined, series)).toBe("Available");
  expect(label("upgrade", true, false, undefined, series)).toBe("Upgrade found");
  const mixed = { ...series, versions: [...series.versions, { coverage: { have: 1, total: 24, missing: 23, aired: 24, upcoming: 0 } }] };
  expect(label("available", false, true, undefined, mixed)).toBe("Available");
  // A movie playable in one library while another still lacks it: an upgrade found, else what the held copy says.
  expect(label("partial", true, false, undefined, { kind: "movie", versions: [{ state: "wanted" }] })).toBe("Upgrade found");
  expect(label("partial", false, false, undefined, { kind: "movie", versions: [{ state: "wanted" }, { state: "filled" }] })).toBe("Best quality known");
  expect(label("partial", false, false, undefined, { kind: "movie", versions: [{ state: "wanted" }] })).toBe("Available");
  expect(label("partial", false, true, undefined, { kind: "movie" })).toBe("Available");
  expect(label("partial", false, false, undefined, { kind: "movie" })).toBe("Available");
});

describe("a movie before its release", () => {
  const day = "2026-11-07";
  it("is upcoming until its release date, with that date", () => {
    expect(upcomingRelease({ kind: "movie", release_date: "2026-12-01" }, day)).toBe("2026-12-01");
    expect(upcomingRelease({ kind: "movie", release_date: "2026-11-07" }, day)).toBeUndefined();
    expect(upcomingRelease({ kind: "series", release_date: "2026-12-01" }, day)).toBeUndefined();
    expect(upcomingRelease(undefined, day)).toBeUndefined();
  });
  it("without home_release, is not upcoming once released, whatever release next_up shows", () => {
    const next_up = { type: "physical", date: "2026-12-10" } as const;
    expect(upcomingRelease({ kind: "movie", release_date: "2026-10-20", next_up } as Parameters<typeof upcomingRelease>[0], day)).toBeUndefined();
    expect(upcomingRelease({ kind: "movie", next_up: { type: "digital", date: "2026-12-10" } } as Parameters<typeof upcomingRelease>[0], day)).toBeUndefined();
  });
  it("reads Upcoming instead of Missing; still Missing once out", () => {
    const soon = { kind: "movie", release_date: "2999-01-01" } as Parameters<typeof posterStatus>[4];
    expect(label("missing", false, false, undefined, soon)).toBe("Upcoming");
    expect(label("missing", false, true, undefined, soon)).toBe("Upcoming");
    expect(label("missing", false, false, undefined, { kind: "movie", release_date: "2001-01-01" } as Parameters<typeof posterStatus>[4])).toBe("Missing");
  });
});

describe("a movie before its home release", () => {
  const day = "2026-11-07";
  type Row = Parameters<typeof posterStatus>[4];
  it("is upcoming until home_release, with that date, whatever the cinema date", () => {
    // In cinemas, digital still to come.
    expect(upcomingRelease({ kind: "movie", release_date: "2026-10-20", home_release: "2026-12-10" }, day)).toBe("2026-12-10");
    // Digital out, disc still to come: home_release is the digital date.
    expect(upcomingRelease({ kind: "movie", release_date: "2026-08-01", home_release: "2026-10-01" }, day)).toBeUndefined();
    // No home dates: the server sends cinema plus 90 days (30 days ago, then 100 days ago).
    expect(upcomingRelease({ kind: "movie", release_date: "2026-10-08", home_release: "2027-01-06" }, day)).toBe("2027-01-06");
    expect(upcomingRelease({ kind: "movie", release_date: "2026-07-30", home_release: "2026-10-28" }, day)).toBeUndefined();
    // Release day itself is out.
    expect(upcomingRelease({ kind: "movie", release_date: "2026-08-01", home_release: day }, day)).toBeUndefined();
    // No dates at all: today's rule on TMDb's release date.
    expect(upcomingRelease({ kind: "movie", release_date: "2026-12-01" }, day)).toBe("2026-12-01");
  });
  it("reads Upcoming on the poster while not out at home", () => {
    expect(label("missing", false, false, undefined, { kind: "movie", release_date: "2001-01-01", home_release: "2999-01-01" } as Row)).toBe("Upcoming");
    expect(label("missing", false, true, undefined, { kind: "movie", release_date: "2001-01-01", home_release: "2001-04-01" } as Row)).toBe("Not available");
  });
});

describe("a missing title with a download in hand", () => {
  type Row = Parameters<typeof posterStatus>[4];
  const soon = { kind: "movie", home_release: "2999-01-01", acquiring: true } as Row;
  it("reads Getting ready without see files, over Upcoming", () => {
    expect(label("missing", false, true, undefined, { kind: "movie", acquiring: true } as Row)).toBe("Getting ready");
    expect(label("missing", false, true, undefined, { kind: "series", acquiring: true } as Row)).toBe("Getting ready");
    expect(label("missing", false, true, undefined, soon)).toBe("Getting ready");
  });
  it("keeps the see-files badges and the available ones", () => {
    expect(label("missing", false, false, undefined, { kind: "movie", acquiring: true } as Row)).toBe("Missing");
    expect(label("missing", false, false, undefined, soon)).toBe("Upcoming");
    expect(label("available", false, true, undefined, { kind: "movie", acquiring: true } as Row)).toBe("Available");
    expect(label("partial", false, true, undefined, { kind: "series", acquiring: true } as Row)).toBe("Some episodes");
  });
});

describe("the day Upcoming ends", () => {
  const zone = process.env.TZ;
  afterEach(() => {
    vi.useRealTimers();
    if (zone === undefined) delete process.env.TZ;
    else process.env.TZ = zone;
  });
  it("is the UTC day, the server's, whatever the browser's time zone", () => {
    // A browser in Los Angeles, whatever zone the runner is in.
    process.env.TZ = "America/Los_Angeles";
    expect(new Date("2026-11-08T03:30:00Z").getDate()).toBe(7);
    // 03:30 UTC on Nov 8 is still Nov 7 in Los Angeles; the server already counts the movie out (Home's Missing row).
    const at = new Date("2026-11-08T03:30:00Z");
    expect(utcToday(at)).toBe("2026-11-08");
    expect(utcToday(new Date("2026-11-07T23:59:59Z"))).toBe("2026-11-07");
    vi.useFakeTimers();
    vi.setSystemTime(at);
    expect(upcomingRelease({ kind: "movie", home_release: "2026-11-08" })).toBeUndefined();
    expect(upcomingRelease({ kind: "movie", home_release: "2026-11-09" })).toBe("2026-11-09");
    vi.setSystemTime(new Date("2026-11-07T23:30:00Z"));
    expect(upcomingRelease({ kind: "movie", home_release: "2026-11-08" })).toBe("2026-11-08");
  });
});

describe("a title's watchability line", () => {
  it("says the best resolution, Getting ready while a download is in hand, else Not available", () => {
    expect(titleWatchWords({ kind: "movie", availability: "available", versions: [{ quality: "2160p" }, { quality: "1080p" }] } as Parameters<typeof titleWatchWords>[0])?.label).toBe("Available in 4K");
    expect(titleWatchWords({ kind: "movie", availability: "missing", acquiring: true, home_release: "2999-01-01" })?.label).toBe("Getting ready");
    expect(titleWatchWords({ kind: "movie", availability: "missing", release_date: "2001-01-01" })?.label).toBe("Not available");
  });
});

describe("relDay", () => {
  const now = new Date(2026, 10, 9, 23, 0); // 23:00 on Nov 9, local time

  it("reads a YYYY-MM-DD as the calendar day written", () => {
    expect(relDay("2026-11-09", now)).toBe("today");
    expect(relDay("2026-11-10", now)).toBe("tomorrow");
    expect(relDay("2026-11-06", now)).toBe("3 days ago");
  });

  it("puts a timestamp on the day it falls on in this time zone", () => {
    // An hour ago is today here, whatever day it already is in UTC.
    expect(relDay(new Date(2026, 10, 9, 22, 0).toISOString(), now)).toBe("today");
    expect(relDay(new Date(2026, 10, 8, 23, 30).toISOString(), now)).toBe("yesterday");
  });
});
