import { describe, expect, it } from "vitest";
import type { Candidate } from "@/api/add";
import {
  DEFAULT_RELEASE_COLUMNS,
  RELEASE_COLUMNS,
  noneOfTitle,
  scopeTarget,
  searchTargetWords,
  NO_FILTER,
  CACHED_ANY,
  columnSortKey,
  episodesText,
  filterChoices,
  filterReleases,
  fmtAge,
  indexersText,
  indexersTitle,
  rowIndexers,
  blockedGrabScope,
  grabConfirmScope,
  needsConfirm,
  normalizeReleaseColumns,
  peersText,
  sortReleases,
} from "./release-columns";

const c = (rank: number, more: Partial<Candidate> = {}): Candidate => ({ rank, release: `R${rank}`, accepted: true, ...more });
const ranks = (xs: Candidate[]) => xs.map((x) => x.rank);

describe("normalizeReleaseColumns", () => {
  it("drops unknown ids, appends new columns hidden and ignores a stored sort", () => {
    const p = normalizeReleaseColumns({ order: ["age", "bogus", "title", "size"], shown: ["age", "bogus", "size"], sort: { key: "nope", dir: "asc" } });
    expect(p.order.slice(0, 2)).toEqual(["age", "size"]);
    expect(p.order).toHaveLength(DEFAULT_RELEASE_COLUMNS.order.length);
    expect(p.order).toContain("flags");
    // By default the verdict follows Title (the leading Source and Age columns come before Title).
    expect(scopeTarget(2)).toBe("s02");
    expect(scopeTarget(undefined, "S02E05")).toBe("s02e05");
    expect(searchTargetWords("s01")).toBe("a search for S01");
    // Age leads; the verdict, quality and score follow Title; Source is offered but hidden.
    expect(DEFAULT_RELEASE_COLUMNS.order.slice(0, 4)).toEqual(["age", "status", "quality", "score"]);
    expect(DEFAULT_RELEASE_COLUMNS.shown).not.toContain("source");
    // A phone has no pointer: no hint asks to point at a cell.
    expect(RELEASE_COLUMNS.filter((c) => /point at/.test(c.hint ?? "")).map((c) => c.id)).toEqual([]);
    expect(p.shown).toEqual(["age", "size"]);
    expect(p).not.toHaveProperty("sort");
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
    expect(ranks(filterReleases(items, { ...NO_FILTER, cached: CACHED_ANY }))).toEqual([1]);
    expect(ranks(filterReleases(items, { ...NO_FILTER, packs: true }))).toEqual([1]);
  });
  it("keeps one indexer, quality, language or provider, and combines them", () => {
    const more = [...items, c(4, { indexer: "Nyaa", quality: "WEBDL-2160p", cached_on: ["ad", "tb"], languages: ["English", "Japanese"] })];
    expect(ranks(filterReleases(more, { ...NO_FILTER, indexer: "Nyaa" }))).toEqual([1, 4]);
    expect(ranks(filterReleases(more, { ...NO_FILTER, quality: "WEBDL-2160p" }))).toEqual([2, 4]);
    expect(ranks(filterReleases(more, { ...NO_FILTER, language: "Japanese" }))).toEqual([2, 4]);
    expect(ranks(filterReleases(more, { ...NO_FILTER, cached: "tb" }))).toEqual([4]);
    expect(ranks(filterReleases(more, { ...NO_FILTER, cached: CACHED_ANY }))).toEqual([1, 4]);
    expect(ranks(filterReleases(more, { ...NO_FILTER, indexer: "Nyaa", quality: "WEBDL-2160p" }))).toEqual([4]);
  });
  it("offers the values in every release, qualities best first", () => {
    const xs = [
      c(1, { indexer: "prowlarr-zeta", quality: "WEBDL-1080p", quality_rank: 5, languages: ["Japanese"], cached_on: ["rd"] }),
      c(2, { indexer: "alpha", quality: "Remux-2160p", quality_rank: 9, languages: ["English", "Japanese"], cached_on: ["tb", "rd"] }),
      c(3, { quality: "WEBDL-1080p", quality_rank: 5 }),
    ];
    expect(filterChoices(xs)).toEqual({
      indexer: ["alpha", "prowlarr-zeta"],
      quality: ["Remux-2160p", "WEBDL-1080p"],
      language: ["English", "Japanese"],
      cached: ["rd", "tb"],
    });
    // One value: offered when some release lacks it, not when every release has it.
    expect(filterChoices([c(1, { languages: ["English"] }), c(2)]).language).toEqual(["English"]);
    expect(filterChoices([c(1, { indexer: "Nyaa" }), c(2, { indexer: "Nyaa" })]).indexer).toEqual([]);
  });
});

describe("one row per torrent", () => {
  const merged = c(1, {
    release: "Show.S02E01.2160p-A", indexer: "prowlarr-tpb", indexers: ["prowlarr-tpb", "therarbg", "dmm"],
    copies: [{ indexer: "therarbg", release: "Show S02E01 2160p A" }, { indexer: "dmm", release: "show.s02e01.2160p.a" }],
  });
  const single = c(2, { release: "Show.S02E01.720p", indexer: "nyaa" });
  it("names the first two indexers, then +N, with each copy in the tooltip", () => {
    expect(indexersText(merged)).toBe("tpb, therarbg +1");
    expect(indexersTitle(merged)).toBe("prowlarr-tpb: Show.S02E01.2160p-A\ntherarbg: Show S02E01 2160p A\ndmm: show.s02e01.2160p.a");
    expect(indexersText(single)).toBe("nyaa");
    expect(indexersTitle(single)).toBe("nyaa");
    expect(rowIndexers(c(3))).toEqual([]);
  });
  it("filters and offers every indexer of a row, and sorts by the first", () => {
    expect(ranks(filterReleases([merged, single], { ...NO_FILTER, indexer: "dmm" }))).toEqual([1]);
    expect(ranks(filterReleases([merged, single], { ...NO_FILTER, text: "therarbg" }))).toEqual([1]);
    expect(filterChoices([merged, single]).indexer).toEqual(["dmm", "nyaa", "therarbg", "prowlarr-tpb"]);
    expect(ranks(sortReleases([merged, single], { key: "indexer", dir: "asc" }))).toEqual([2, 1]);
  });
});

describe("sorting every column Sonarr sorts", () => {
  it("sorts Source, Status, Flags and History; Episodes and Formats do not sort", () => {
    expect(["source", "age", "title", "indexer", "history", "size", "peers", "language", "quality", "score", "flags", "status", "cached"].every((id) => columnSortKey(id as never))).toBe(true);
    expect(columnSortKey("episodes")).toBeUndefined();
    expect(columnSortKey("formats")).toBeUndefined();
    const items = [
      c(1, { accepted: false, rejections: [{ reason: "A" }, { reason: "B" }], source: "torznab", flags: ["freeleech"], info_hash: "AA" }),
      c(2, { source: "dmm", info_hash: "bb" }),
      c(3, { accepted: false, rejections: [{ reason: "A" }], flags: ["freeleech", "internal"] }),
    ];
    expect(ranks(sortReleases(items, { key: "status", dir: "asc" }))).toEqual([2, 3, 1]);
    expect(ranks(sortReleases(items, { key: "source", dir: "asc" }))).toEqual([2, 1, 3]);
    expect(ranks(sortReleases(items, { key: "flags", dir: "desc" }))).toEqual([3, 1, 2]);
    const outcomes = { aa: { outcome: "failed" }, bb: { outcome: "imported" } } as never;
    expect(ranks(sortReleases(items, { key: "history", dir: "asc" }, outcomes))).toEqual([2, 1, 3]);
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

describe("blockedGrabScope", () => {
  it("asks the blocklist question, then names each other thing the grab overrides", () => {
    const b = { library: "WEB", reason: "Removed in Activity by admin" };
    const q = "This release is on WEB's blocklist: Removed in Activity by admin. Grab it anyway? It is taken off the blocklist.";
    expect(blockedGrabScope(c(1, { score: 10 }), b).does).toEqual([q]);
    const better = c(1, { score: 10, existing: { quality: "Bluray-1080p", score: 50 } });
    expect(blockedGrabScope(better, b).does).toEqual([q, "It replaces the current file (Bluray-1080p, score 50), which scores higher"]);
    const rejected = c(2, { accepted: false, rejections: [{ reason: "q", message: "720p is not wanted" }] });
    expect(blockedGrabScope(rejected, b).does).toEqual([q, "Theatarr rejects it: 720p is not wanted. Grabbing overrides that"]);
    expect(blockedGrabScope(c(1), b, ["late reason"]).does).toEqual([q, "Theatarr rejects it: late reason. Grabbing overrides that"]);
    expect(blockedGrabScope(c(1), b).confirm).toBe("Grab anyway");
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

describe("rejection reason filter and other-title answers", () => {
  const wrong = { rank: 1, accepted: false, rejections: [{ reason: "UnknownMovie" }] } as Candidate;
  const held = { rank: 2, accepted: false, rejections: [{ reason: "DiskHigherPreference" }, { reason: "QualityNotWanted" }] } as Candidate;
  const ok = { rank: 3, accepted: true } as Candidate;
  it("keeps the releases rejected for the picked reason", () => {
    expect(filterReleases([wrong, held, ok], { ...NO_FILTER, reason: "QualityNotWanted" }).map((c) => c.rank)).toEqual([2]);
  });
  it("says nothing of the title was found only when every release is another title and none is accepted", () => {
    expect(noneOfTitle([wrong], 0)).toBe(true);
    expect(noneOfTitle([wrong, held], 0)).toBe(false);
    expect(noneOfTitle([], 0)).toBe(false);
  });
});
