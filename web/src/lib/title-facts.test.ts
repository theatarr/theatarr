import { describe, expect, it } from "vitest";
import { acquiringStatus, coverageWords, dayWords, failureWords, latestEvent, missingLine } from "./title-facts";

const now = new Date("2026-11-14T12:00:00Z");
const ev = (event: string, date: string, extra: object = {}) => ({ event, date, ...extra }) as never;

describe("latestEvent", () => {
  it("says the newest import, or a failed attempt with the providers it missed on", () => {
    expect(latestEvent({ has_file: true, history: [ev("grabbed", "2026-11-14T09:00:00Z"), ev("imported", "2026-11-14T10:00:00Z")] }, now)?.words).toBe("Imported today");
    const failed = latestEvent({ has_file: true, history: [ev("imported", "2026-11-01T10:00:00Z"), ev("failed", "2026-11-13T20:07:00Z", { reason: "miss on every provider", missed_on: ["rd", "ad", "tb"] })] }, now);
    expect(failed).toMatchObject({ event: "failed", tone: "warning", words: "Last upgrade attempt yesterday: not cached on RD, AD and TB" });
    // Without a file it is the download that failed; the server's reason when the providers are not known.
    expect(latestEvent({ has_file: false, history: [ev("failed", "2026-11-10T10:00:00Z", { reason: "Stalled" })] }, now)?.words).toBe("Last download failed Nov 10: stalled");
    // Searches and grabs alone are no event here.
    expect(latestEvent({ has_file: false, history: [ev("searched", "2026-11-14T10:00:00Z"), ev("grabbed", "2026-11-14T10:00:00Z")] }, now)).toBeUndefined();
  });

  it("names days plainly", () => {
    expect(dayWords("2026-11-14T08:00:00Z", now)).toBe("today");
    expect(dayWords("2026-11-02T08:00:00Z", now)).toBe("Nov 2");
    expect(failureWords({ missed_on: ["rd"] })).toBe("not cached on RD");
  });
});

describe("missingLine", () => {
  const auto = { missing: false, upgrades: true, enabled: true };
  it("says the last targeted search or never, and whether Theatarr searches by itself", () => {
    expect(missingLine({}, auto, now)).toBe("Never searched. Automatic search for missing items is off; new releases are still picked up from RSS.");
    expect(missingLine({ last_search: { at: "2026-11-12T10:00:00Z", run_id: 1, state: "nothing", results: 12, accepted: 0 } }, { ...auto, missing: true }, now))
      .toBe("Last searched Nov 12: 12 results, none accepted. Theatarr searches for it automatically.");
    expect(missingLine({ searching: "queued" }, { ...auto, enabled: false }, now)).toBe("Search queued. Paused: nothing is searched or downloaded.");
  });
});

describe("acquiringStatus", () => {
  it("is the most advanced download's stage while nothing is held, else a search under way", () => {
    const a = (stage: string, progress?: number) => ({ stage, progress, words: "", download_id: 1, downloads: 1, updated_at: "" }) as never;
    expect(acquiringStatus([{ has_file: false, monitored: true, acquisition: a("queued") }, { has_file: false, monitored: true, acquisition: a("downloading", 37) }])).toBe("Downloading · 37%");
    expect(acquiringStatus([{ has_file: false, monitored: true, acquisition: a("waiting_mount") }])).toBe("Importing");
    // A held file stays Available during an upgrade.
    expect(acquiringStatus([{ has_file: true, monitored: true, acquisition: a("downloading", 10) }, { has_file: false, monitored: true }])).toBeUndefined();
    expect(acquiringStatus([{ has_file: false, monitored: true, searching: "searching" }])).toBe("Searching");
    expect(acquiringStatus([{ has_file: false, monitored: true, acquisition: a("blocked") }])).toBeUndefined();
  });
});

describe("coverageWords", () => {
  it("counts against the monitored episodes and says the rest", () => {
    expect(coverageWords({ total: 76, have: 44, missing: 2, monitored: 46, monitored_have: 44 })).toBe("44 of 46 monitored episodes, 2 missing; 30 not monitored");
    expect(coverageWords({ total: 8, have: 8, missing: 0 })).toBe("8 of 8 episodes");
  });
});
