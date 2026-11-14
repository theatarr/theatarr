import { describe, expect, it } from "vitest";
import type { Episode, EpisodeList } from "@/api/library";
import { cellStatus, cellUpgrade, copiesInView, episodeLayout, episodeNumber, matchesFilter, mergedStatus, mergedUpgrade, plainAvailable, restrictions, seasonBadge, seasonSummary, statusWords, type Cell } from "./seasons";

const today = "2026-10-26";
const cell = (o: Partial<Cell>): Cell => ({ state: "wanted", monitored: true, has_file: false, ...o }) as Cell;
const ep = (n: number, air: string | undefined, versions: Record<string, Cell>, absolute?: number): Episode =>
  ({ id: n, season: 1, number: n, absolute, name: `Ep ${n}`, air_date: air, aired: !!air && air <= today, monitored: true, versions }) as Episode;

const k4 = { key: "tv-2160p", label: "4K" };
const k1 = { key: "tv-1080p", label: "1080p" };

// Season 1: E1-E3 held in both copies (E2 with an upgrade found in 4K), E4 held in 1080p only, E5 missing in both,
// E6 aired but unmonitored, E7 not aired yet.
const season: Episode[] = [
  ep(1, "2026-09-01", { "tv-2160p": cell({ has_file: true, state: "cutoff_met" }), "tv-1080p": cell({ has_file: true }) }),
  ep(2, "2026-09-08", { "tv-2160p": cell({ has_file: true, state: "filled", upgrade_seen: true }), "tv-1080p": cell({ has_file: true }) }),
  ep(3, "2026-09-15", { "tv-2160p": cell({ has_file: true }), "tv-1080p": cell({ has_file: true }) }),
  ep(4, "2026-09-22", { "tv-2160p": cell({}), "tv-1080p": cell({ has_file: true }) }),
  ep(5, "2026-09-29", { "tv-2160p": cell({}), "tv-1080p": cell({}) }),
  ep(6, "2026-10-06", { "tv-2160p": cell({ monitored: false }), "tv-1080p": cell({ monitored: false }) }),
  ep(7, "2026-11-02", { "tv-2160p": cell({}), "tv-1080p": cell({}) }),
];

describe("episode status", () => {
  it("says what a copy has in words, the quality word apart", () => {
    expect(cellStatus(undefined, true)).toBe("none");
    expect(cellStatus(cell({ has_file: true }), true)).toBe("available");
    // An upgrade found is said beside Available, never instead of it.
    expect(cellStatus(cell({ has_file: true, upgrade_seen: true }), true)).toBe("available");
    expect(cellUpgrade(cell({ has_file: true, upgrade_seen: true }), true)).toBe(true);
    // Without "see files" an upgrade is never mentioned (and the API sends no flag anyway).
    expect(cellUpgrade(cell({ has_file: true, upgrade_seen: true }), false)).toBe(false);
    expect(cellStatus(cell({}), false)).toBe("not_aired");
    expect(cellStatus(cell({}), true)).toBe("missing");
    expect(cellStatus(cell({ monitored: false }), true)).toBe("unmonitored");
    // A gap in a library that is not monitored is not missing.
    expect(cellStatus(cell({}), true, true)).toBe("unmonitored");
    expect(cellStatus(cell({ has_file: true }), true, true)).toBe("available");
    expect(statusWords("unmonitored", true).label).toBe("Not monitored");
    expect(statusWords("unmonitored", false).label).toBe("Not available");
    expect((["available", "missing", "not_aired"] as const).map((s) => statusWords(s, true).label)).toEqual(["Available", "Missing", "Not aired"]);
  });
  it("takes aired from the server's flag, not from the day of the air date", () => {
    // Dated today, airs tonight: not aired. Dated tomorrow in the network's zone, already aired: missing.
    const tonight = { ...ep(8, today, { "tv-2160p": cell({}) }), aired: false };
    const overnight = { ...ep(9, "2026-10-27", { "tv-2160p": cell({}) }), aired: true };
    expect(mergedStatus(tonight, [k4.key])).toBe("not_aired");
    expect(mergedStatus(overnight, [k4.key])).toBe("missing");
    expect(matchesFilter(tonight, "missing", [k4.key], true)).toBe(false);
    expect(matchesFilter(overnight, "missing", [k4.key], true)).toBe(true);
    expect(seasonSummary([tonight, overnight], [k4], true).text).toBe("0 of 1 available · 1 missing · 1 not aired");
  });
  it("merges copies by watchability", () => {
    const keys = [k4.key, k1.key];
    expect(mergedStatus(season[3], keys)).toBe("available");
    expect(mergedStatus(season[1], keys)).toBe("available");
    expect(mergedUpgrade(season[1], keys, true)).toBe(true);
    expect(mergedUpgrade(season[1], keys, false)).toBe(false);
    expect(mergedStatus(season[4], keys)).toBe("missing");
    expect(mergedStatus(season[6], keys)).toBe("not_aired");
    expect(mergedStatus(season[5], keys)).toBe("unmonitored");
    // The 4K library not monitored: its gap at E4 is no gap, and E5 is missing only in 1080p.
    expect(mergedStatus(season[4], [k4.key], new Set([k4.key]))).toBe("unmonitored");
    expect(mergedStatus(season[4], keys, new Set([k4.key]))).toBe("missing");
  });
  it("names the best resolution for accounts without see files", () => {
    const e = ep(1, "2026-09-01", { a: cell({ has_file: true, quality: "1080p" }), b: cell({ has_file: true, quality: "2160p" }) });
    expect(plainAvailable(e, ["a", "b"])).toBe("Available in 4K");
    expect(plainAvailable(ep(2, "2026-09-01", { a: cell({ has_file: true }) }), ["a"])).toBe("Available");
  });
});

describe("season summary", () => {
  it("counts one copy", () => {
    const s = seasonSummary(season, [k4], true);
    expect(s.text).toBe("3 of 6 available · 2 missing · 1 upgrade found · 1 not aired");
    expect([s.available, s.total, s.missing, s.upgrades, s.notAired]).toEqual([3, 6, 2, 1, 1]);
  });
  it("names the copy a missing count belongs to when several are in view", () => {
    expect(seasonSummary(season, [k4, k1], true).text).toBe("4 of 6 available · 2 missing in 4K · 1 missing in 1080p · 1 upgrade found · 1 not aired");
  });
  it("counts across copies as one for accounts without see files, naming no copy and no upgrade", () => {
    expect(seasonSummary(season, [k4, k1], false, true).text).toBe("4 of 6 available · 1 missing · 1 not aired");
    expect(seasonSummary(season, [k4, k1], false).text).toBe("4 of 6 available · 2 missing in 4K · 1 missing in 1080p · 1 not aired");
  });
  it("counts no missing episode in a library that is not monitored, for those who see files only", () => {
    expect(seasonSummary(season, [{ ...k4, monitored: false }], true).text).toBe("3 of 6 available · 1 upgrade found · 1 not aired");
    // Without "see files" it stays watchability: the aired episodes the copy lacks are missing.
    expect(seasonSummary(season, [{ ...k4, monitored: false }], false).text).toBe("3 of 6 available · 2 missing · 1 not aired");
    expect(seasonSummary(season, [{ ...k4, monitored: false }, k1], true).text).toBe("4 of 6 available · 1 missing in 1080p · 1 upgrade found · 1 not aired");
  });
  it("says when nothing has aired", () => {
    expect(seasonSummary([season[6]], [k4], true).text).toBe("1 episode, none aired yet");
    expect(seasonSummary([], [k4], true).text).toBe("No episodes");
  });
});

describe("season filters", () => {
  const ids = (f: "all" | "missing" | "upgrade", keys: string[], seeFiles = true) => season.filter((e) => matchesFilter(e, f, keys, seeFiles)).map((e) => e.number);
  it("filters by the copies in view", () => {
    expect(ids("all", [k4.key])).toEqual([1, 2, 3, 4, 5, 6, 7]);
    expect(ids("missing", [k4.key])).toEqual([4, 5]);
    expect(ids("missing", [k1.key])).toEqual([5]);
    expect(ids("missing", [k4.key, k1.key])).toEqual([4, 5]);
    expect(ids("upgrade", [k4.key, k1.key])).toEqual([2]);
    expect(ids("upgrade", [k1.key])).toEqual([]);
    expect(ids("upgrade", [k4.key], false)).toEqual([]);
  });
  it("matches across copies when merged", () => {
    const merged = (f: "missing" | "upgrade", seeFiles: boolean) => season.filter((e) => matchesFilter(e, f, [k4.key, k1.key], seeFiles, true)).map((e) => e.number);
    expect(merged("missing", false)).toEqual([5]);
    expect(merged("upgrade", false)).toEqual([]);
    expect(merged("upgrade", true)).toEqual([2]);
  });
});

describe("numbering", () => {
  it("labels anime absolute numbers", () => {
    expect(episodeNumber({ number: 1, absolute: 1 }, true)).toBe("E01 · absolute 1");
    expect(episodeNumber({ number: 12, absolute: 37 }, true)).toBe("E12 · absolute 37");
    expect(episodeNumber({ number: 3, absolute: 3 }, false)).toBe("E03");
    expect(episodeNumber({ number: 104 }, true)).toBe("E104");
  });
});

describe("restrictions", () => {
  const ctl = (o: object) => ({ mode: "shadow", can_edit: true, can_search: true, live_releases: false, ...o }) as const;
  it("says each reason once and names the copies only when not all share it", () => {
    const why = "Your account may not change this";
    expect(
      restrictions([
        { label: "4K", control: ctl({ can_edit: false, edit_reason: why, can_search: false, search_reason: "shadow mode" }) },
        { label: "1080p", control: ctl({ can_edit: false, edit_reason: why, can_search: false, search_reason: "shadow mode" }) },
      ]),
    ).toEqual([`Monitoring is read-only: ${why}`, "4K, 1080p: shadow mode — Settings › Libraries"]);
    expect(restrictions([{ label: "4K", control: ctl({ can_edit: false, edit_reason: why }) }, { label: "1080p", control: ctl({}) }])).toEqual([`Monitoring is read-only (4K): ${why}`]);
    expect(restrictions([{ label: "4K", control: ctl({}) }, { label: "1080p" }])).toEqual([]);
  });
});

describe("layout", () => {
  const versions = [
    { id: 1, key: k4.key, label: k4.label, monitored: true },
    { id: 2, key: k1.key, label: k1.label, monitored: true },
  ] as EpisodeList["versions"];
  it("shows All libraries as the matrix on desktop by default, one picked library at either width, never All on phones", () => {
    const desk = episodeLayout({ wide: true, copies: 2, seeFiles: true, picked: null });
    expect(desk).toEqual({ mode: "table", columns: "each", selector: true });
    expect(episodeLayout({ wide: true, copies: 2, seeFiles: true, picked: "all" })).toEqual(desk);
    expect(copiesInView(desk, versions, null)).toHaveLength(2);
    const deskOne = episodeLayout({ wide: true, copies: 2, seeFiles: true, picked: k1.key });
    expect(deskOne).toEqual({ mode: "table", columns: "selected", selector: true });
    expect(copiesInView(deskOne, versions, k1.key).map((v) => v.key)).toEqual([k1.key]);
    const phone = episodeLayout({ wide: false, copies: 2, seeFiles: true, picked: null });
    expect(phone).toEqual({ mode: "rows", columns: "selected", selector: true });
    expect(copiesInView(phone, versions, null).map((v) => v.key)).toEqual([k4.key]);
    // All picked on the desktop, then the window narrows: the first library, not every library.
    const narrowed = episodeLayout({ wide: false, copies: 2, seeFiles: true, picked: "all" });
    expect(narrowed.columns).toBe("selected");
    expect(copiesInView(narrowed, versions, "all").map((v) => v.key)).toEqual([k4.key]);
    expect(copiesInView(phone, versions, k1.key).map((v) => v.key)).toEqual([k1.key]);
  });
  it("shows the one library with its columns and no selector", () => {
    expect(episodeLayout({ wide: false, copies: 1, seeFiles: true, picked: null })).toEqual({ mode: "rows", columns: "selected", selector: false });
    expect(episodeLayout({ wide: true, copies: 1, seeFiles: true, picked: null })).toEqual({ mode: "table", columns: "selected", selector: false });
  });
  it("merges copies for family accounts at any width", () => {
    expect(episodeLayout({ wide: false, copies: 2, seeFiles: false, picked: null })).toEqual({ mode: "rows", columns: "merged", selector: false });
    expect(episodeLayout({ wide: true, copies: 2, seeFiles: false, picked: k1.key })).toEqual({ mode: "table", columns: "merged", selector: false });
  });
  it("badges a library's season: held of aired, red when something is missing", () => {
    expect(seasonBadge({ have: 8, missing: 2, total: 10 }, { episodes: 12 })).toEqual({ text: "8 / 10", tone: "bad" });
    expect(seasonBadge({ have: 10, missing: 0, total: 10 }, { episodes: 10 })).toEqual({ text: "10 / 10", tone: "ok" });
    expect(seasonBadge({ have: 0, missing: 0, total: 0 }, { episodes: 6 })).toEqual({ text: "0 / 6", tone: "neutral" });
    expect(seasonBadge(undefined, { episodes: 6 })).toEqual({ text: "0 / 6", tone: "neutral" });
    // A season with nothing aired says so.
    expect(seasonBadge({ have: 0, missing: 0, total: 0 }, { episodes: 1, aired: 0 })).toEqual({ text: "Not aired", tone: "neutral" });
  });
});
