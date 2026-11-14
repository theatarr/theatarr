import { describe, expect, it } from "vitest";
import type { Version } from "@/api/library";
import { airFact, parseTitleID, readiness, resolveTab, titleReleaseAccess, titleStatus, titleTabs } from "./title-layout";

const v = (o: Partial<Version>): Version => ({ id: 1, key: "movie-remux", label: "4K Remux", has_file: true, state: "filled", ...o }) as Version;

describe("title tabs", () => {
  it("accepts only positive safe title ids", () => {
    expect(parseTitleID("13")).toBe(13);
    expect(parseTitleID("abc")).toBeUndefined();
    expect(parseTitleID("0")).toBeUndefined();
    expect(parseTitleID("9007199254740992")).toBeUndefined();
  });
  it("keeps malformed release links not-found and gates valid ones by management permission", () => {
    expect(titleReleaseAccess("abc", false)).toEqual({ kind: "not-found" });
    expect(titleReleaseAccess("11", false)).toEqual({ kind: "forbidden", id: 11 });
    expect(titleReleaseAccess("11", true)).toEqual({ kind: "page", id: 11 });
  });
  it("opens series on Episodes and gives managers History", () => {
    const t = titleTabs({ series: true, franchise: true, seeFiles: true });
    expect(t.map((x) => x.id)).toEqual(["episodes", "order", "about", "history"]);
    expect(resolveTab(null, t)).toBe("episodes");
  });
  it("keeps the libraries out of the tabs and family accounts out of History", () => {
    expect(titleTabs({ series: false, franchise: false, seeFiles: true }).map((x) => x.id)).toEqual(["about", "history"]);
    expect(titleTabs({ series: false, franchise: false, seeFiles: false }).map((x) => x.id)).toEqual(["about"]);
    expect(titleTabs({ series: true, franchise: false, seeFiles: false }).map((x) => x.id)).toEqual(["episodes", "about"]);
  });
  it("follows old links and ignores tabs the caller lacks", () => {
    const t = titleTabs({ series: true, franchise: false, seeFiles: true });
    expect(resolveTab("seasons", t)).toBe("episodes");
    // The old Libraries tab: the libraries are under the hero now, so the first tab opens.
    expect(resolveTab("versions", t)).toBe("episodes");
    expect(resolveTab("copies", t)).toBe("episodes");
    expect(resolveTab("details", t)).toBe("about");
    expect(resolveTab("order", t)).toBe("episodes");
    const family = titleTabs({ series: false, franchise: false, seeFiles: false });
    expect(resolveTab("history", family)).toBe("about");
    expect(resolveTab("versions", family)).toBe("about");
  });
});

describe("the title's status for those who see files", () => {
  it("says the posters' word, with the best resolution when available", () => {
    const held = [v({ current: { quality: "WEBDL-2160p" }, upgrade: { state: "below_cutoff" } })];
    expect(titleStatus({ availability: "available", kind: "movie" }, held)).toEqual({ label: "Available in 4K", tone: "positive" });
    expect(titleStatus({ availability: "partial", kind: "movie" }, held)).toEqual({ label: "Partly available", tone: "warning" });
    expect(titleStatus({ availability: "missing", kind: "movie" }, [])).toEqual({ label: "Missing", tone: "negative" });
    expect(titleStatus({ availability: "unmonitored", kind: "movie" }, [])).toEqual({ label: "Not monitored", tone: "neutral" });
    expect(titleStatus({ availability: "upcoming", kind: "movie" }, [])).toEqual({ label: "Upcoming", tone: "neutral" });
    // Only a title in no library is "Not in the library yet".
    expect(titleStatus({ availability: "none", kind: "movie" }, [])).toEqual({ label: "Not in the library yet", tone: "neutral" });
  });
});

describe("readiness", () => {
  it("says a movie is ready in its best resolution, whichever copy has it", () => {
    expect(readiness("movie", [v({ current: { quality: "WEBDL-2160p" } }), v({ id: 2, key: "movie-web", has_file: false, state: "wanted" })]).label).toBe("Ready to watch in 4K");
    expect(readiness("movie", [v({ current: { quality: "1080p" } })]).label).toBe("Ready to watch in 1080p");
    // In a library without a file is not "not in the library".
    expect(readiness("movie", [v({ has_file: false, state: "wanted" })])).toEqual({ label: "Not available yet", tone: "neutral" });
    expect(readiness("movie", [])).toEqual({ label: "Not in the library yet", tone: "neutral" });
    // Without See files a missing title with a download in hand is getting ready, as on the posters.
    expect(readiness("movie", [v({ has_file: false, state: "wanted" })], true)).toEqual({ label: "Getting ready", tone: "neutral" });
    expect(readiness("movie", [v({ current: { quality: "1080p" } })], true).label).toBe("Ready to watch in 1080p");
  });
  it("tells complete series from partial ones", () => {
    // Household: a series copy missing aired episodes is partly ready whatever the library's monitoring (the server
    // sends its watchability count).
    expect(readiness("series", [v({ key: "tv", monitored: false, current: { quality: "WEBDL-2160p" }, episodes: { have: 2, total: 3, missing: 1 } })])).toEqual({ label: "2 of 3 episodes ready", tone: "partial" });
    const part = v({ key: "tv-2160p", current: { quality: "WEBDL-2160p" }, episodes: { have: 5, total: 10, missing: 5 } });
    expect(readiness("series", [part])).toEqual({ label: "5 of 10 episodes ready", tone: "partial" });
    const full = v({ key: "anime", current: undefined, upgrade: { state: "below_cutoff", quality: "Bluray-1080p" }, episodes: { have: 12, total: 12, missing: 0 } });
    expect(readiness("series", [part, full]).label).toBe("Ready to watch in 1080p");
  });
});

describe("air fact", () => {
  it("drops a cached next episode day that has passed", () => {
    const m = { next_air_date: "2026-11-08", last_air_date: "2026-11-01" };
    expect(airFact(m, "2026-11-08")).toEqual({ label: "Next episode", day: "2026-11-08" });
    expect(airFact(m, "2026-11-09")).toEqual({ label: "Last aired", day: "2026-11-01" });
    expect(airFact({ next_air_date: "2026-11-08" }, "2026-11-09")).toBeUndefined();
    expect(airFact({ last_air_date: "2026-11-01" }, "2026-11-09")).toEqual({ label: "Last aired", day: "2026-11-01" });
    expect(airFact(undefined, "2026-11-09")).toBeUndefined();
  });
});
