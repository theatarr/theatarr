import { describe, expect, it } from "vitest";
import type { Version } from "@/api/library";
import { currentLibraries, fileName, lastSearch, libraryColumns, libraryStatus, nextSearch, nextSearchOf } from "./library-rows";

const v = (o: Partial<Version>): Version => ({ id: 1, key: "movie-web", label: "WEB", monitored: true, has_file: false, state: "wanted", ...o }) as Version;
const downloading = { stage: "downloading" as const, words: "", progress: 37, download_id: 1, downloads: 1, updated_at: "2026-11-12T00:00:00Z" };

describe("library status", () => {
  it("says what a movie without a file is waiting for", () => {
    expect(libraryStatus(v({}), "movie")).toMatchObject({ label: "Missing", tone: "negative" });
    expect(libraryStatus(v({ monitored: false }), "movie").label).toBe("Not monitored");
    const day = `${new Date().getFullYear()}-12-31`;
    expect(libraryStatus(v({}), "movie", day).label).toBe("Upcoming · Dec 31");
    const s = libraryStatus(v({ acquisition: downloading }), "movie", day);
    expect(s.label).toBe("Downloading · 37%");
    expect(s.stage?.stage).toBe("downloading");
  });
  it("uses the poster badge words for a held movie, with an upgrade in hand under them", () => {
    expect(libraryStatus(v({ has_file: true, upgrade: { state: "cutoff_met" } }), "movie")).toMatchObject({ label: "Available", upgrade: true });
    expect(libraryStatus(v({ has_file: true, upgrade: { state: "upgrade_available" } }), "movie").label).toBe("Upgrade found");
    expect(libraryStatus(v({ has_file: true, upgrade: { state: "below_cutoff" } }), "movie").label).toBe("Best quality known");
    expect(libraryStatus(v({ has_file: true, upgrade: { state: "cutoff_met" }, acquisition: downloading }), "movie").sub).toBe("Upgrade downloading · 37%");
    expect(libraryStatus(v({ has_file: true }), "movie")).toMatchObject({ label: "Available", tone: "positive" });
  });
  it("counts a series' episodes", () => {
    expect(libraryStatus(v({ has_file: true, episodes: { have: 42, total: 48, missing: 6 } }), "series")).toEqual({ label: "42 / 48 episodes", tone: "warning", sub: "6 missing" });
    expect(libraryStatus(v({ has_file: true, episodes: { have: 48, total: 48, missing: 0 } }), "series")).toMatchObject({ tone: "positive", sub: undefined });
    expect(libraryStatus(v({ episodes: { have: 0, total: 3, missing: 3 } }), "series").tone).toBe("negative");
    expect(libraryStatus(v({ episodes: { have: 0, total: 0, missing: 0 } }), "series").label).toBe("Nothing aired yet");
    expect(libraryStatus(v({ acquisition: { ...downloading, stage: "queued" }, episodes: { have: 0, total: 3, missing: 3 } }), "series").label).toBe("Queued");
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
  it("words the next search", () => {
    const now = new Date("2026-11-12T12:00:00Z");
    expect(nextSearch({ at: "2026-11-12T15:00:00Z", why: "new" }, now)).toBe("in 3 h (new release)");
    expect(nextSearch({ at: "2026-11-12T11:00:00Z" }, now)).toBe("now");
    expect(nextSearch({ at: "2026-11-12T11:00:00Z", why: "stalled" }, now)).toBe("now (after a failed download)");
    expect(nextSearchOf(v({ control: { mode: "active", can_edit: true, can_search: true, live_releases: true } }))).toBeUndefined();
  });
});
