import { describe, expect, it } from "vitest";
import type { Version } from "@/api/library";
import { copyTone, copyWords, TONE } from "./library-status";
import { currentLibraries, fileName, renameSides, versionBrief, nextSearchWhy, lastSearch, libraryColumns, libraryStatus, nextSearch, nextSearchOf } from "./library-rows";

const v = (o: Partial<Version>): Version => ({ id: 1, key: "movie-web", label: "WEB", monitored: true, has_file: false, state: "wanted", ...o }) as Version;
const downloading = { stage: "downloading" as const, words: "", progress: 37, download_id: 1, downloads: 1, updated_at: "2026-11-12T00:00:00Z" };

const movie = { kind: "movie" as const };
const series = { kind: "series" as const, release_date: "2019-03-01", status: "Ended" };
const DAY = "2026-11-12";

describe("library status", () => {
  it("says what a movie without a file is waiting for", () => {
    expect(libraryStatus(v({}), movie, DAY)).toMatchObject({ label: "Missing", tone: "negative" });
    expect(libraryStatus(v({ monitored: false }), movie, DAY)).toMatchObject({ label: "Not monitored", tone: "neutral" });
    expect(libraryStatus(v({}), { ...movie, home_release: "2026-12-31" }, DAY)).toMatchObject({ label: "Upcoming · Dec 31", tone: "neutral" });
    const s = libraryStatus(v({ acquisition: downloading }), movie, DAY);
    expect(s).toMatchObject({ label: "Downloading · 37%", tone: "info" });
    expect(s.stage?.stage).toBe("downloading");
  });
  it("says Available for a held movie, the quality word beside it, with an upgrade in hand under them", () => {
    // The status word is Available whatever the quality; the quality word (with Why?) follows it (upgrade).
    for (const state of ["cutoff_met", "upgrade_available", "below_cutoff", "best_source_seen"] as const) {
      expect(libraryStatus(v({ has_file: true, upgrade: { state } }), movie, DAY)).toMatchObject({ label: "Available", tone: "positive", upgrade: true });
    }
    expect(libraryStatus(v({ has_file: true, upgrade: { state: "cutoff_met" }, acquisition: downloading }), movie, DAY).sub).toBe("Upgrade downloading · 37%");
    expect(libraryStatus(v({ has_file: true }), movie, DAY)).toMatchObject({ label: "Available", tone: "positive" });
  });
  it("reads a series library as the index does: same words, same colour", () => {
    const cases: [Partial<Version>, string, string][] = [
      // Monitored aired episodes missing: red, as on the index pill.
      [{ has_file: true, episodes: { have: 42, total: 48, missing: 6 } }, "42 of 48 episodes linked", "negative"],
      // Gaps nobody monitors: green.
      [{ has_file: true, episodes: { have: 42, total: 48, missing: 0 } }, "42 of 48 episodes linked", "positive"],
      // Nothing aired yet only when the series has not started; an older series without episode data says so.
      [{ episodes: { have: 0, total: 0, missing: 0 } }, "No episode information yet", "neutral"],
      [{ episodes: undefined }, "No episode information yet", "neutral"],
    ];
    for (const [o, words, tone] of cases) {
      const lib = v(o);
      const s = libraryStatus(lib, series, DAY);
      expect(s.label).toBe(words);
      expect(s.tone).toBe(tone);
      const pill = copyWords(versionBrief(lib), series, DAY);
      expect(pill.charAt(0).toUpperCase() + pill.slice(1)).toBe(s.label);
      expect(TONE[copyTone(versionBrief(lib), series, DAY)]).toBe(s.tone);
    }
    expect(libraryStatus(v({ episodes: { have: 0, total: 0, missing: 0 } }), { ...series, release_date: "2027-02-01", status: "Planned" }, DAY).label).toBe("Nothing aired yet");
    expect(libraryStatus(v({ has_file: true, episodes: { have: 42, total: 48, missing: 6 } }), series, DAY).sub).toBe("6 missing");
  });
});

describe("library columns", () => {
  it("offers a column only for the kinds it is for", () => {
    const movie = libraryColumns("movie", [v({})]).map((c) => c.id);
    const series = libraryColumns("series", [v({})]).map((c) => c.id);
    expect(movie).toContain("score");
    expect(movie).toContain("file");
    expect(series).not.toContain("score");
    expect(series).not.toContain("file");
    expect(series).not.toContain("minimum_availability");
    expect(series).toContain("cached_on");
  });
  it("offers Next search only when a row carries the server's time", () => {
    expect(libraryColumns("movie", [v({})]).map((c) => c.id)).not.toContain("next_search");
    const timed = v({ control: { mode: "active", can_edit: true, can_search: true, live_releases: true, next_search_at: "2026-11-12T15:00:00Z" } as Version["control"] });
    expect(libraryColumns("movie", [v({}), timed]).map((c) => c.id)).toContain("next_search");
  });
});

describe("row readers", () => {
  it("drops a library the title was removed from", () => {
    expect(currentLibraries([v({}), v({ id: 2, key: "gone", removed: true })]).map((x) => x.key)).toEqual(["movie-web"]);
  });
  it("names the file and the newest search", () => {
    expect(fileName("/media/movies/Alpha (2020)/Alpha.2020.2160p.mkv")).toBe("Alpha.2020.2160p.mkv");
    expect(fileName(undefined)).toBe("");
    expect(lastSearch({ history: [
      { event: "grabbed", date: "2026-11-03T00:00:00Z" },
      { event: "searched", date: "2026-11-01T00:00:00Z" },
      { event: "searched", date: "2026-11-02T00:00:00Z" },
    ] })).toBe("2026-11-02T00:00:00Z");
    expect(lastSearch({ history: [] })).toBeUndefined();
  });
  it("shows a rename's names, or the changed folders when the names are equal", () => {
    expect(renameSides("/media/tv/A/Season 1/a.mkv", "/media/tv/A/Season 1/b.mkv")).toEqual(["a.mkv", "b.mkv"]);
    expect(renameSides("/media/tv/A/Season 1/a.mkv", "/media/tv/A (2020)/Season 01/a.mkv")).toEqual(["A/Season 1/a.mkv", "A (2020)/Season 01/a.mkv"]);
  });
  it("gives Wanted and the title page one reason wording", () => {
    expect(["new", "asked", "added", "stalled", undefined].map(nextSearchWhy)).toEqual(["new release", "Search now", "Added by a list", "after a failed download", ""]);
  });
  it("words the next search", () => {
    const now = new Date("2026-11-12T12:00:00Z");
    expect(nextSearch({ at: "2026-11-12T15:00:00Z", why: "new" }, now)).toBe("in 3 h (new release)");
    expect(nextSearch({ at: "2026-11-12T11:00:00Z" }, now)).toBe("now");
    expect(nextSearch({ at: "2026-11-12T11:00:00Z", why: "stalled" }, now)).toBe("now (after a failed download)");
    expect(nextSearchOf(v({ control: { mode: "active", can_edit: true, can_search: true, live_releases: true } }))).toBeUndefined();
  });
});
