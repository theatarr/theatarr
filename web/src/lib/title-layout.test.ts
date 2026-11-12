import { describe, expect, it } from "vitest";
import type { Version } from "@/api/library";
import { airFact, copyFields, parseTitleID, readiness, resolveTab, titleReleaseAccess, titleTabs } from "./title-layout";

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
  it("opens series on Episodes and gives managers Copies and History", () => {
    const t = titleTabs({ series: true, franchise: true, seeFiles: true });
    expect(t.map((x) => x.id)).toEqual(["episodes", "copies", "order", "about", "history"]);
    expect(resolveTab(null, t)).toBe("episodes");
  });
  it("keeps a movie's copies out of the tabs and family accounts out of management", () => {
    expect(titleTabs({ series: false, franchise: false, seeFiles: true }).map((x) => x.id)).toEqual(["about", "history"]);
    expect(titleTabs({ series: false, franchise: false, seeFiles: false }).map((x) => x.id)).toEqual(["about"]);
    expect(titleTabs({ series: true, franchise: false, seeFiles: false }).map((x) => x.id)).toEqual(["episodes", "about"]);
  });
  it("follows old links and ignores tabs the caller lacks", () => {
    const t = titleTabs({ series: true, franchise: false, seeFiles: true });
    expect(resolveTab("seasons", t)).toBe("episodes");
    expect(resolveTab("versions", t)).toBe("copies");
    expect(resolveTab("details", t)).toBe("about");
    expect(resolveTab("order", t)).toBe("episodes");
    const family = titleTabs({ series: false, franchise: false, seeFiles: false });
    expect(resolveTab("history", family)).toBe("about");
    expect(resolveTab("versions", family)).toBe("about");
  });
});

describe("readiness", () => {
  it("says a movie is ready in its best resolution, whichever copy has it", () => {
    expect(readiness("movie", [v({ current: { quality: "WEBDL-2160p" } }), v({ id: 2, key: "movie-web", has_file: false, state: "wanted" })]).label).toBe("Ready to watch in 4K");
    expect(readiness("movie", [v({ current: { quality: "1080p" } })]).label).toBe("Ready to watch in 1080p");
    expect(readiness("movie", [v({ has_file: false, state: "wanted" })])).toEqual({ label: "Not in the library yet", tone: "neutral" });
    // Without See files a missing title with a download in hand is getting ready, as on the posters.
    expect(readiness("movie", [v({ has_file: false, state: "wanted" })], true)).toEqual({ label: "Getting ready", tone: "neutral" });
    expect(readiness("movie", [v({ current: { quality: "1080p" } })], true).label).toBe("Ready to watch in 1080p");
  });
  it("tells complete series from partial ones", () => {
    const part = v({ key: "tv-2160p", current: { quality: "WEBDL-2160p" }, episodes: { have: 5, total: 10, missing: 5 } });
    expect(readiness("series", [part])).toEqual({ label: "5 of 10 episodes ready", tone: "partial" });
    const full = v({ key: "anime", current: undefined, upgrade: { state: "below_cutoff", quality: "Bluray-1080p" }, episodes: { have: 12, total: 12, missing: 0 } });
    expect(readiness("series", [part, full]).label).toBe("Ready to watch in 1080p");
  });
});

describe("copy fields", () => {
  it("names the held file, never the copy's lane, as availability", () => {
    const f = copyFields(v({ label: "4K Remux", current: { quality: "WEBDL-2160p" }, upgrade: { state: "best_available", quality: "WEBDL-2160p", cutoff: { quality: "Remux-2160p", upgrades: true } } }));
    expect(f).toEqual({ copy: "4K Remux", current: "4K WEB", preferred: "4K Remux", availability: "Available in 4K WEB" });
    expect(f.availability).not.toContain("Remux");
  });
  it("covers missing copies and series", () => {
    expect(copyFields(v({ has_file: false, state: "wanted", current: undefined }))).toMatchObject({ current: "None yet", availability: "Not in the library yet", preferred: "" });
    expect(copyFields(v({ label: "Anime", upgrade: { state: "below_cutoff", quality: "Bluray-1080p" }, episodes: { have: 3, total: 12, missing: 9 } })).availability).toBe("3 of 12 episodes");
    expect(copyFields(v({ label: "Anime", upgrade: { state: "below_cutoff", quality: "Bluray-1080p" }, episodes: { have: 12, total: 12, missing: 0 } })).availability).toBe("All 12 episodes · mostly 1080p Blu-ray");
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
