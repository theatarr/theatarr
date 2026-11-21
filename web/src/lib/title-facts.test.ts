import { describe, expect, it } from "vitest";
import { acquiringStatus, coverageWords, dayWords, failureWords, latestEvent, missingLine } from "./title-facts";

const now = new Date("2026-11-14T12:00:00Z");
const ev = (event: string, date: string, extra: object = {}) => ({ event, date, ...extra }) as never;

describe("latestEvent", () => {
  it("says the newest import, or a failed attempt with the providers it missed on", () => {
    expect(latestEvent({ history: [ev("grabbed", "2026-11-14T09:00:00Z"), ev("imported", "2026-11-14T10:00:00Z")] }, now)?.words).toBe("Imported today");
    // An import that replaced a file.
    expect(latestEvent({ history: [ev("upgraded", "2026-11-13T10:00:00Z"), ev("imported", "2026-11-01T10:00:00Z")] }, now)?.words).toBe("Upgraded yesterday");
    // The same release fetched again (a lost file).
    expect(latestEvent({ history: [ev("upgraded", "2026-11-13T10:00:00Z", { redownload: true })] }, now)?.words).toBe("Re-downloaded yesterday");
    const failed = latestEvent({ history: [ev("imported", "2026-11-01T10:00:00Z"), ev("failed", "2026-11-13T20:07:00Z", { reason: "Not cached on RD, AD and TB", missed_on: ["rd", "ad", "tb"], upgrade: true, download_id: 9 })] }, now);
    expect(failed).toMatchObject({ event: "failed", tone: "warning", words: "Last upgrade attempt yesterday: not cached on RD, AD and TB", downloadId: 9 });
    // Not an upgrade (nothing it was for was held): the download failed; the server's reason without known providers.
    expect(latestEvent({ history: [ev("failed", "2026-11-10T10:00:00Z", { reason: "Stalled" })] }, now)?.words).toBe("Last download failed Nov 10: stalled");
    // Searches and grabs alone are no event here.
    expect(latestEvent({ history: [ev("searched", "2026-11-14T10:00:00Z"), ev("grabbed", "2026-11-14T10:00:00Z")] }, now)).toBeUndefined();
  });

  it("names days plainly", () => {
    expect(dayWords("2026-11-14T08:00:00Z", now)).toBe("today");
    expect(dayWords("2026-11-02T08:00:00Z", now)).toBe("Nov 2");
    expect(failureWords({ reason: "Not cached on RD" })).toBe("not cached on RD");
    expect(failureWords({ reason: "No provider accepted this release" })).toBe("no provider accepted this release");
  });
});

describe("missingLine", () => {
  const auto = { missing: false, upgrades: true, enabled: true };
  const run = (state: string, results?: number, accepted?: number) => ({ at: "2026-11-12T10:00:00Z", run_id: 1, state, results, accepted }) as never;
  it("says the last targeted search by its state, and whether Theatarr searches by itself", () => {
    expect(missingLine({ rss: true }, auto, now)).toBe("Not searched yet. Automatic backlog searching is off; new releases are still picked up from RSS.");
    // No RSS feed covers the library: the RSS sentence is left out.
    expect(missingLine({}, auto, now)).toBe("Not searched yet. Automatic backlog searching is off.");
    // An account that can search is told what to do, only for an item never searched.
    expect(missingLine({}, auto, now, true)).toBe("Not searched yet. Automatic backlog searching is off. Use Search now to request a search.");
    // In the words of the caller's button: a series' row says Search series (F8.1).
    expect(missingLine({}, auto, now, true, { action: "Search series" })).toBe("Not searched yet. Automatic backlog searching is off. Use Search series to request a search.");
    expect(missingLine({ last_search: run("nothing", 0, 0) }, auto, now, true)).toBe("Last searched Nov 12: nothing found. Automatic backlog searching is off.");
    expect(missingLine({}, { ...auto, enabled: false }, now, true)).toBe("Not searched yet. Paused: searches, new grabs and imports stop for every library; Interactive search can still look up releases.");
    expect(missingLine({ last_search: run("nothing", 12, 0) }, { ...auto, missing: true }, now)).toBe("Last searched Nov 12: 12 results, none accepted. Theatarr searches for it automatically.");
    expect(missingLine({ last_search: run("nothing", 0, 0) }, null, now)).toBe("Last searched Nov 12: nothing found");
    // A failed or skipped search found nothing because it did not search: said so.
    expect(missingLine({ last_search: run("failed", 0, 0) }, null, now)).toBe("Last search Nov 12 failed");
    expect(missingLine({ last_search: run("skipped", 0, 0) }, null, now)).toBe("Last search Nov 12 was skipped");
    // An episode covered by a season's search: when only, no counts.
    expect(missingLine({ last_search: run("routed") }, null, now)).toBe("Last searched Nov 12");
    expect(missingLine({ searching: "queued" }, { ...auto, enabled: false }, now)).toBe("Search queued. Paused: searches, new grabs and imports stop for every library; Interactive search can still look up releases.");
  });
  it("names what the reported run searched on a series' row (scoped), and nothing for a film's run", () => {
    const of = (target: string, state = "nothing", results?: number) => ({ at: "2026-11-12T10:00:00Z", run_id: 1, target, state, results, accepted: 0 }) as never;
    expect(missingLine({ last_search: of("s02", "nothing", 474) }, null, now, false, { scoped: true })).toBe("Last searched Season 2 Nov 12: 474 results, none accepted");
    expect(missingLine({ last_search: of("s00", "nothing", 0) }, null, now, false, { scoped: true })).toBe("Last searched Specials Nov 12: nothing found");
    expect(missingLine({ last_search: of("s02e05", "nothing", 3) }, null, now, false, { scoped: true })).toBe("Last searched S02E05 Nov 12: 3 results, none accepted");
    expect(missingLine({ last_search: of("s2025", "skipped", 0) }, null, now, false, { scoped: true })).toBe("Last search of Season 2025 Nov 12 was skipped");
    expect(missingLine({ last_search: of("movie", "nothing", 3) }, null, now, false, { scoped: true })).toBe("Last searched Nov 12: 3 results, none accepted");
    // Unscoped (the episode window) keeps its words.
    expect(missingLine({ last_search: of("s02", "nothing", 474) }, null, now)).toBe("Last searched Nov 12: 474 results, none accepted");
  });
});

describe("acquiringStatus", () => {
  const a = (stage: string, progress?: number) => ({ stage, progress, words: "", download_id: 1, downloads: 1, updated_at: "" }) as never;
  it("is the most advanced download's stage among libraries without a file, else a search under way", () => {
    expect(acquiringStatus([{ key: "r", has_file: false, monitored: true, acquisition: a("queued") }])).toBe("Waiting to start");
    expect(acquiringStatus([{ key: "r", label: "4K Remux", has_file: false, monitored: true, acquisition: a("queued") }, { key: "w", label: "4K WEB", has_file: false, monitored: true, acquisition: a("downloading", 37) }])).toBe("4K WEB: Downloading · 37%");
    expect(acquiringStatus([{ key: "r", has_file: false, monitored: true, acquisition: a("waiting_mount") }])).toBe("Waiting for the mount");
    expect(acquiringStatus([{ key: "r", has_file: false, monitored: true, acquisition: a("linking") }])).toBe("Importing");
    // A held library's upgrade does not count; another library's first download does.
    expect(acquiringStatus([{ key: "r", label: "4K Remux", has_file: true, monitored: true, acquisition: a("downloading", 10) }])).toBeUndefined();
    expect(acquiringStatus([{ key: "r", label: "4K Remux", has_file: true, monitored: true }, { key: "w", label: "4K WEB", has_file: false, monitored: true, acquisition: a("downloading", 40) }])).toBe("4K WEB: Downloading · 40%");
    expect(acquiringStatus([{ key: "r", has_file: false, monitored: true, searching: "searching" }])).toBe("Searching");
    // Two libraries searching are counted; one searching and one queued names the one searching.
    expect(acquiringStatus([{ key: "r", label: "4K Remux", has_file: false, monitored: true, searching: "searching" }, { key: "w", label: "4K WEB", has_file: false, monitored: true, searching: "searching" }])).toBe("Searching 2 libraries");
    expect(acquiringStatus([{ key: "r", label: "4K Remux", has_file: false, monitored: true, searching: "queued" }, { key: "w", label: "4K WEB", has_file: false, monitored: true, searching: "searching" }])).toBe("4K WEB: Searching");
    expect(acquiringStatus([{ key: "r", has_file: false, monitored: true, acquisition: a("blocked") }])).toBeUndefined();
  });
});

describe("coverageWords", () => {
  it("counts against the monitored episodes and says the rest", () => {
    expect(coverageWords({ total: 76, have: 44, missing: 2, monitored: 46, monitored_have: 44 })).toBe("44 of 46 monitored episodes, 2 missing; 30 not monitored");
    expect(coverageWords({ total: 8, have: 8, missing: 0 })).toBe("8 of 8 episodes");
  });
  it("says once that nothing is monitored", () => {
    expect(coverageWords({ total: 37, have: 37, missing: 0, monitored: 0, monitored_have: 0 })).toBe("37 of 37 episodes · none monitored");
    expect(coverageWords({ total: 291, have: 291, missing: 0, monitored: 291, monitored_have: 291 }, false)).toBe("291 of 291 episodes available, not monitored");
  });
});
