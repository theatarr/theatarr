import { afterEach, describe, expect, it, vi } from "vitest";
import { availabilityFilters, filterChips, gridQuery, isFamily, POSTER_STATUS_LABEL, posterStatus, qualityWord, relDay, titleWatchWords, UPGRADE_FILTERS, upcomingRelease, utcToday, watchWords, withoutChip } from "./library";
import { badgeTone, copyTone } from "./library-status";

const label = (...a: Parameters<typeof posterStatus>) => {
  const s = posterStatus(...a);
  return s ? POSTER_STATUS_LABEL[s] : undefined;
};

describe("the status word", () => {
  it("says one of eight words, never a quality word", () => {
    expect(Object.values(POSTER_STATUS_LABEL).sort()).toEqual(["Available", "Getting ready", "Missing", "Not available", "Not monitored", "Partly available", "Some episodes", "Upcoming"]);
  });
  it.each([
    ["available", "Available"],
    ["partial", "Partly available"],
    ["missing", "Missing"],
    ["unmonitored", "Not monitored"],
    ["upcoming", "Upcoming"],
  ] as const)("says the server's availability %s as %s, for a movie and a series alike", (a, want) => {
    expect(label(a, false, { kind: "movie" })).toBe(want);
    expect(label(a, false, { kind: "series", versions: [{ coverage: { have: 2, missing: 3, total: 5, aired: 5, upcoming: 0 } }] })).toBe(want);
  });
  it("says nothing for a title in no library", () => {
    expect(label("none")).toBeUndefined();
  });
  it("matches the filter of the same name and its colour", () => {
    for (const f of availabilityFilters(true)) expect(label(f.id)).toBe(f.label);
    expect(badgeTone(posterStatus("available"))).toBe("ok");
    expect(badgeTone(posterStatus("partial"))).toBe("warn");
    expect(badgeTone(posterStatus("missing"))).toBe("bad");
    expect(badgeTone(posterStatus("unmonitored"))).toBe("idle");
    expect(badgeTone(posterStatus("upcoming"))).toBe("idle");
  });
});

describe("the quality word", () => {
  it("is said beside a held title only: Upgrade found, Best quality known, Preferred quality met", () => {
    expect(qualityWord({ availability: "available", upgrade_seen: true, upgrade_state: "upgrade_available" })?.label).toBe("Upgrade found");
    for (const state of ["below_cutoff", "best_available", "best_source_seen", "higher_source_exists"] as const) {
      expect(qualityWord({ availability: "available", upgrade_state: state })?.label).toBe("Best quality known");
      expect(qualityWord({ availability: "partial", upgrade_state: state })?.label).toBe("Best quality known");
    }
    expect(qualityWord({ availability: "available", upgrade_state: "cutoff_met" })?.label).toBe("Preferred quality met");
    expect(qualityWord({ availability: "available" })?.label).toBe("Preferred quality met");
    expect(qualityWord({ availability: "missing", upgrade_seen: true })).toBeUndefined();
    expect(qualityWord({ availability: "upcoming" })).toBeUndefined();
  });
  it("names the quality filters by the same words", () => {
    expect(UPGRADE_FILTERS.map((u) => u.label)).toEqual(["Upgrade found", "Best quality known"]);
  });
});

describe("the status filters", () => {
  it("are Available, Partly available, Missing, Not monitored and Upcoming; none is about quality", () => {
    expect(availabilityFilters(true).map((a) => a.label)).toEqual(["Available", "Partly available", "Missing", "Not monitored", "Upcoming"]);
    expect(availabilityFilters(true, "movie").map((a) => a.id)).toContain("partial");
  });
  it("reads an old saved Preferred quality not met link as Available", () => {
    expect(gridQuery("movies", new URLSearchParams("availability=upgrade"), true).availability).toBe("available");
    expect(gridQuery("movies", new URLSearchParams("availability=missing&upgrade=found"), true)).toMatchObject({ availability: "missing", upgrade: "found" });
    expect(gridQuery("movies", new URLSearchParams("upgrade=found"), false).upgrade).toBeUndefined();
  });
  it("chips the quality filter", () => {
    expect(filterChips(new URLSearchParams("upgrade=best_known"), { seeFiles: true }).map((c) => c.label)).toEqual(["Best quality known"]);
    expect(filterChips(new URLSearchParams("upgrade=best_known"), { seeFiles: false })).toEqual([]);
  });
});

describe("availability for family accounts", () => {
  it("words the poster badge as watchability without see files", () => {
    expect(label("available", true)).toBe("Available");
    // The server sends partial only for a series with some episodes and no complete copy.
    expect(label("partial", true, { kind: "series" })).toBe("Some episodes");
    expect(label("partial", true, { kind: "movie" })).toBe("Available");
    // As the table says (docs/design/library-ui.md).
    expect(label("missing", true)).toBe("Not available");
    expect(label("none", true)).toBeUndefined();
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
  });
  it("keeps a series with a complete copy and a part-way one Available for family", () => {
    const series = { kind: "series" as const, versions: [{ coverage: { have: 12, total: 24, missing: 0, aired: 24, upcoming: 0 } }] };
    const mixed = { ...series, versions: [...series.versions, { coverage: { have: 1, total: 24, missing: 23, aired: 24, upcoming: 0 } }] };
    expect(label("available", true, mixed)).toBe("Available");
  });
  it("tells a family account apart", () => {
    expect(isFamily({ seeFiles: false, manageRequests: false })).toBe(true);
    expect(isFamily({ seeFiles: true, manageRequests: true })).toBe(false);
    expect(isFamily({ seeFiles: false, manageRequests: true })).toBe(false);
  });
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
  it("reads Upcoming instead of Missing without see files; still Not available once out", () => {
    const soon = { kind: "movie", release_date: "2999-01-01" } as Parameters<typeof posterStatus>[2];
    expect(label("missing", true, soon)).toBe("Upcoming");
    expect(label("missing", true, { kind: "movie", release_date: "2001-01-01" } as Parameters<typeof posterStatus>[2])).toBe("Not available");
    // Those who see files get the server's word: it decides Upcoming by the same days (titleAvailabilitySQL).
    expect(label("upcoming", false, soon)).toBe("Upcoming");
    expect(label("missing", false, soon)).toBe("Missing");
  });
});

describe("a movie before its home release", () => {
  const day = "2026-11-07";
  type Row = Parameters<typeof posterStatus>[2];
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
    expect(label("missing", true, { kind: "movie", release_date: "2001-01-01", home_release: "2999-01-01" } as Row)).toBe("Upcoming");
    expect(label("missing", true, { kind: "movie", release_date: "2001-01-01", home_release: "2001-04-01" } as Row)).toBe("Not available");
  });
});

describe("a missing title with a download in hand", () => {
  type Row = Parameters<typeof posterStatus>[2];
  const soon = { kind: "movie", home_release: "2999-01-01", acquiring: true } as Row;
  it("reads Getting ready without see files, over Upcoming", () => {
    expect(label("missing", true, { kind: "movie", acquiring: true } as Row)).toBe("Getting ready");
    expect(label("missing", true, { kind: "series", acquiring: true } as Row)).toBe("Getting ready");
    expect(label("missing", true, soon)).toBe("Getting ready");
  });
  it("keeps the see-files badges and the available ones", () => {
    expect(label("missing", false, { kind: "movie", acquiring: true } as Row)).toBe("Missing");
    expect(label("upcoming", false, soon)).toBe("Upcoming");
    expect(label("available", true, { kind: "movie", acquiring: true } as Row)).toBe("Available");
    expect(label("partial", true, { kind: "series", acquiring: true } as Row)).toBe("Some episodes");
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

describe("a series with nothing aired yet", () => {
  const coming = { kind: "series" as const, versions: [{ key: "tv", monitored: true, coverage: { have: 0, total: 0, aired: 0, upcoming: 10 } }], next_up: { date: "2099-03-02", season: 1, number: 1 } };
  // Those who see files get the server's word (titleAvailabilitySQL, the same evidence); without "see files" the
  // poster decides it here, by the same rule.
  it("reads Upcoming, as its bar and pills read nothing aired yet, not Not available", () => {
    expect(label("missing", true, coming)).toBe("Upcoming");
    expect(label("upcoming", false, coming)).toBe("Upcoming");
    expect(copyTone(coming.versions[0], coming)).toBe("idle");
    expect(titleWatchWords({ ...coming, availability: "missing" })?.label).toBe("Upcoming · Mar 2, 2099");
    // Once an episode has aired and is missing, it is Not available again; a library with aired episodes keeps it so.
    const aired = { kind: "series" as const, versions: [{ coverage: { have: 0, total: 3, missing: 3, aired: 3, upcoming: 7 } }] };
    expect(label("missing", true, aired)).toBe("Not available");
    expect(label("missing", true, { kind: "series", versions: [...coming.versions, ...aired.versions] })).toBe("Not available");
    // Empty coverage on a series that has aired (episodes not loaded yet) stays Not available: an old first air date, or a
    // status that says it aired.
    const loading = { kind: "series" as const, versions: [{ key: "tv", coverage: { have: 0, total: 0, aired: 0, upcoming: 0 } }] };
    expect(label("missing", true, { ...loading, release_date: "2009-04-01", status: "Ended" })).toBe("Not available");
    expect(label("missing", true, { ...loading, release_date: "2009-04-01" })).toBe("Not available");
    expect(label("missing", true, { ...loading, status: "Returning Series" })).toBe("Not available");
    // A premiere later today: dated today, but its episode is known and still to come.
    expect(label("missing", true, { kind: "series", release_date: "2000-01-01", status: "Returning Series", versions: [{ key: "tv", coverage: { have: 0, total: 0, aired: 0, upcoming: 1 } }] })).toBe("Upcoming");
    // A first air date still to come wins over the status (TMDb can call a series not premiered yet Returning).
    expect(label("missing", true, { ...loading, release_date: "2099-01-01", status: "Returning Series" })).toBe("Upcoming");
    expect(label("missing", true, { ...loading, status: "In Production" })).toBe("Upcoming");
    expect(titleWatchWords({ ...loading, availability: "missing", release_date: "2009-04-01", status: "Ended" })?.label).toBe("Not available");
    // Without coverage (nothing known) it stays Not available.
    expect(label("missing", true, { kind: "series", versions: [{ key: "tv" }] })).toBe("Not available");
  });
});

describe("a movie held in one library and missing in another", () => {
  // Listed under Partly (the server's partial: a monitored library has no file); the badge says so in amber and the
  // missing library's pill stays red.
  const t = { kind: "movie" as const, release_date: "2020-01-01", versions: [
    { key: "4k", label: "4K", monitored: true, has_file: true, state: "cutoff_met" as const },
    { key: "web", label: "WEB", monitored: true, has_file: false, state: "wanted" as const },
  ] };
  const word = (s: ReturnType<typeof posterStatus>) => (s ? POSTER_STATUS_LABEL[s] : undefined);
  it("reads Partly available in amber, with a red pill for the missing library", () => {
    const s = posterStatus("partial", false, t);
    expect(word(s)).toBe("Partly available");
    expect(badgeTone(s)).toBe("warn");
    expect(t.versions.map((v) => copyTone(v, t))).toEqual(["ok", "bad"]);
  });
  it("reads Available when the empty library is not monitored (the server sends the held file's availability)", () => {
    const off = { ...t, versions: [t.versions[0], { ...t.versions[1], monitored: false }] };
    const s = posterStatus("available", false, off);
    expect(word(s)).toBe("Available");
    expect(badgeTone(s)).toBe("ok");
    expect(off.versions.map((v) => copyTone(v, off))).toEqual(["ok", "idle"]);
  });
  it("reads Available without see files", () => {
    expect(word(posterStatus("available", true, t))).toBe("Available");
  });
});

describe("filter chips", () => {
  const p = new URLSearchParams("q=dune&monitored=false&availability=upgrade&with=tv-4k&without=tv-web&quality=WEBDL-2160p&provider=rd&year_from=2019&genre=Drama,Crime&state=wanted");
  const names = new Map([["tv-4k", "4K"], ["tv-web", "WEB"]]);
  it("words each active filter", () => {
    expect(filterChips(p, { seeFiles: true, names, provider: (v) => v.toUpperCase() }).map((c) => c.label)).toEqual([
      // An old saved link's availability=upgrade (Preferred quality not met) reads as Available.
      "Title: “dune”", "Not monitored", "Available", "Has 4K", "Lacks WEB", "Quality: WEBDL-2160p", "Provider: RD", "From 2019", "Drama", "Crime", "State: wanted",
    ]);
    expect(filterChips(new URLSearchParams("year_to=2024"), { seeFiles: true }).map((c) => c.label)).toEqual(["Up to 2024"]);
    // Libraries, providers and copy states are file detail: no chip without see files.
    expect(filterChips(p, { seeFiles: false, names }).map((c) => c.label)).toEqual([
      "Title: “dune”", "Not monitored", "Available", "Quality: WEBDL-2160p", "From 2019", "Drama", "Crime",
    ]);
    expect(filterChips(new URLSearchParams("availability=missing"), { seeFiles: false, kind: "movie" }).map((c) => c.label)).toEqual(["Not available"]);
  });
  it("removes one chip's value only", () => {
    const chips = filterChips(p, { seeFiles: true });
    const drama = chips.find((c) => c.label === "Drama")!;
    expect(withoutChip(p, drama).get("genre")).toBe("Crime");
    const years = filterChips(new URLSearchParams("year_from=2019&year_to=2024"), { seeFiles: true });
    expect(years.map((c) => c.label)).toEqual(["2019–2024"]);
    expect(withoutChip(new URLSearchParams("year_from=2019&year_to=2024&q=x"), years[0]).toString()).toBe("q=x");
  });
});
