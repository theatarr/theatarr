import { describe, expect, it } from "vitest";
import type { Candidate } from "@/api/add";
import {
  DEFAULT_RELEASE_COLUMNS,
  NO_FILTER,
  episodesText,
  filterReleases,
  fmtAge,
  grabConfirmScope,
  needsConfirm,
  normalizeReleaseColumns,
  peersText,
  sortReleases,
} from "./release-columns";

const c = (rank: number, more: Partial<Candidate> = {}): Candidate => ({ rank, release: `R${rank}`, accepted: true, ...more });
const ranks = (xs: Candidate[]) => xs.map((x) => x.rank);

describe("normalizeReleaseColumns", () => {
  it("drops unknown ids, appends new columns hidden and resets a bad sort to rank", () => {
    const p = normalizeReleaseColumns({ order: ["age", "bogus", "title", "size"], shown: ["age", "bogus", "size"], sort: { key: "nope", dir: "asc" } });
    expect(p.order.slice(0, 2)).toEqual(["age", "size"]);
    expect(p.order).toHaveLength(DEFAULT_RELEASE_COLUMNS.order.length);
    expect(p.order).toContain("flags");
    // By default the verdict follows Title (the leading Source and Age columns come before Title).
    expect(DEFAULT_RELEASE_COLUMNS.order.slice(0, 3)).toEqual(["source", "age", "status"]);
    expect(p.shown).toEqual(["age", "size"]);
    expect(p.sort).toEqual({ key: "rank", dir: "asc" });
    expect(normalizeReleaseColumns({ order: [], shown: [], sort: { key: "score", dir: "desc" } }).sort).toEqual({ key: "score", dir: "desc" });
    expect(normalizeReleaseColumns(undefined).shown).not.toContain("formats");
  });
});

describe("sortReleases", () => {
  it("by quality puts rank 0 or absent last, either way", () => {
    const items = [c(1, { quality_rank: 0 }), c(2, { quality_rank: 3 }), c(3), c(4, { quality_rank: 1 })];
    expect(ranks(sortReleases(items, { key: "quality", dir: "asc" }))).toEqual([4, 2, 1, 3]);
    expect(ranks(sortReleases(items, { key: "quality", dir: "desc" }))).toEqual([2, 4, 1, 3]);
  });
  it("by peers puts unknown last and ties keep rank order", () => {
    const items = [c(1, { seeders: -1 }), c(2, { seeders: 5 }), c(3, { seeders: 50 }), c(4), c(5, { seeders: 5 })];
    expect(ranks(sortReleases(items, { key: "peers", dir: "desc" }))).toEqual([3, 2, 5, 4, 1]);
  });
  it("by age puts the newest first and unknown last", () => {
    const items = [c(1, { published: "2026-01-01T00:00:00Z" }), c(2), c(3, { published: "2026-11-01T00:00:00Z" })];
    expect(ranks(sortReleases(items, { key: "age", dir: "asc" }))).toEqual([3, 1, 2]);
  });
});

describe("filterReleases", () => {
  const items = [
    c(1, { release: "Show.S02.1080p.WEB", indexer: "Nyaa", episodes: ["S02E01", "S02E02"], cached_on: ["rd"] }),
    c(2, { release: "Show.S02E01.2160p", accepted: false, quality: "WEBDL-2160p", languages: ["Japanese"], episodes: ["S02E01"] }),
    c(3, { release: "Show.S02E01.720p", indexer: "Other" }),
  ];
  it("matches text in the name, indexer, quality or languages, case-insensitive", () => {
    expect(ranks(filterReleases(items, { ...NO_FILTER, text: "nyaa" }))).toEqual([1]);
    expect(ranks(filterReleases(items, { ...NO_FILTER, text: "2160P" }))).toEqual([2]);
    expect(ranks(filterReleases(items, { ...NO_FILTER, text: "japanese" }))).toEqual([2]);
    expect(ranks(filterReleases(items, { ...NO_FILTER, text: "web nyaa" }))).toEqual([1]); // every word, any field
  });
  it("keeps accepted, cached or packs only", () => {
    expect(ranks(filterReleases(items, { ...NO_FILTER, accepted: true }))).toEqual([1, 3]);
    expect(ranks(filterReleases(items, { ...NO_FILTER, cached: true }))).toEqual([1]);
    expect(ranks(filterReleases(items, { ...NO_FILTER, packs: true }))).toEqual([1]);
  });
});

describe("cell words", () => {
  it("fmtAge", () => {
    const now = Date.parse("2026-11-12T12:00:00Z");
    expect(fmtAge("2026-11-12T11:48:00Z", now)).toBe("12 min");
    expect(fmtAge("2026-11-12T07:00:00Z", now)).toBe("5 h");
    expect(fmtAge("2025-08-01T12:00:00Z", now)).toBe("468 d");
    expect(fmtAge(undefined, now)).toBe("—");
  });
  it("peersText and episodesText", () => {
    expect(peersText({ seeders: 120, leechers: 8 })).toBe("120 / 8");
    expect(peersText({ seeders: 120 })).toBe("120");
    expect(peersText({})).toBe("0");
    expect(peersText({ seeders: -1 })).toBe("—");
    expect(episodesText(["S02E05"])).toBe("S02E05");
    expect(episodesText(Array.from({ length: 12 }, (_, i) => `S02E${String(i + 1).padStart(2, "0")}`))).toBe("S02E01–E12 (12)");
  });
});

describe("needsConfirm", () => {
  it("asks for a rejected release and one that replaces a better-scored file, not for an accepted one", () => {
    expect(needsConfirm(c(1, { accepted: false }))).toBe(true);
    expect(needsConfirm(c(1, { score: 10 }))).toBe(false);
    expect(needsConfirm(c(1, { score: 10, existing: { quality: "Bluray-1080p", score: 5 } }))).toBe(false);
    const better = c(1, { score: 10, existing: { quality: "Bluray-1080p", score: 50 } });
    expect(needsConfirm(better)).toBe(true);
    const scope = grabConfirmScope(better, "WEB");
    expect(scope.title).toBe("Replace a better-scored file");
    expect(scope.confirm).toBe("Grab anyway");
    expect(scope.does.at(-1)).toBe("It replaces the current file (Bluray-1080p, score 50), which scores higher");
  });
});

describe("a pack", () => {
  it("gets no score-based question: its existing is only the first episode's file", () => {
    const pack = c(1, { score: 10, episodes: ["S01E01", "S01E02"], existing: { quality: "WEBDL-1080p", score: 50 } });
    expect(needsConfirm(pack)).toBe(false);
    expect(grabConfirmScope(pack, "WEB").title).toBe("Grab this release");
    expect(needsConfirm({ ...pack, accepted: false })).toBe(true);
    expect(needsConfirm({ ...pack, episodes: ["S01E01"] })).toBe(true);
  });
});
