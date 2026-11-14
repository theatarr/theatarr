import { describe, expect, it } from "vitest";
import {
  activityHref,
  bulkExpect,
  chipWords,
  copyBar,
  copyColumns,
  defaultDesc,
  distinctLabels,
  moveColumn,
  swapColumns,
  nextWords,
  normalizePosters,
  prefKey,
  relabel,
  stageCounts,
  stageWords,
  titleAcquisition,
  withDistinctLabels,
} from "./copies";

const control = { mode: "active" as const, can_edit: true, can_search: true, live_releases: true };
const acq = (stage: "queued" | "downloading" | "waiting_mount" | "blocked", extra = {}) => ({ stage, words: stage, download_id: 1, downloads: 1, updated_at: "2026-10-29T00:00:00Z", ...extra });

describe("stage words", () => {
  it("words each stage, with progress and the upgrade prefix", () => {
    expect(stageWords(acq("downloading", { progress: 37.4 }))).toBe("Downloading · 37%");
    expect(stageWords(acq("downloading", { progress: 37, upgrade: true }))).toBe("Upgrade downloading · 37%");
    expect(stageWords(acq("waiting_mount"))).toBe("Waiting for mount");
    expect(stageWords(undefined)).toBe("");
    expect(chipWords(acq("downloading", { progress: 99.6 }))).toBe("Downloading 100%");
    expect(chipWords(acq("queued"))).toBe("Queued");
  });

  it("counts downloads by stage in stage order", () => {
    expect(stageCounts({ waiting_mount: 2, queued: 1, downloading: 1, blocked: 0 })).toBe("1 queued · 1 downloading · 2 waiting");
    expect(stageCounts(undefined)).toBe("");
  });

  it("links each stage to its Activity filter", () => {
    expect(activityHref("blocked")).toBe("/activity?show=attention");
    expect(activityHref("downloading")).toBe("/activity?show=downloading");
    expect(activityHref("queued")).toBe("/activity?show=waiting");
    expect(activityHref(undefined)).toBe("/activity");
  });

  it("takes the most advanced copy for the title chip", () => {
    const t = { versions: [{ key: "a", acquisition: acq("queued") }, { key: "b" }, { key: "c", acquisition: acq("waiting_mount") }] };
    expect(titleAcquisition(t)?.version.key).toBe("c");
    expect(titleAcquisition({ versions: [{ key: "b" }] })).toBeUndefined();
  });
});

describe("copy columns", () => {
  it("lists each copy column once, in order of first appearance", () => {
    const cols = copyColumns([
      { versions: [{ key: "tv-2160p", label: "4K", control }, { key: "tv-web", label: "1080p WEB", control }] },
      { versions: [{ key: "tv-2160p", label: "4K", control }, { key: "anime-tv", label: "Anime" }] },
    ]);
    expect(cols).toEqual([{ key: "tv-2160p", label: "4K" }, { key: "tv-web", label: "1080p WEB" }, { key: "anime-tv", label: "Anime" }]);
  });
});

describe("distinct labels", () => {
  it("keeps labels that do not collide", () => {
    expect([...distinctLabels([{ key: "tv-2160p", label: "4K" }, { key: "tv-web", label: "1080p WEB" }])]).toEqual([
      ["tv-2160p", "4K"],
      ["tv-web", "1080p WEB"],
    ]);
  });

  it("numbers colliding labels, never by key", () => {
    const n = distinctLabels([{ key: "tv-2160p", label: "4K" }, { key: "tv-2160p-theatarr", label: "4K" }, { key: "tv-web", label: "WEB" }]);
    expect([...n.values()]).toEqual(["4K", "4K (2)", "WEB"]);
    expect([...distinctLabels([{ key: "a", label: "4K" }, { key: "b", label: "4K" }, { key: "c", label: "4K" }]).values()]).toEqual([
      "4K",
      "4K (2)",
      "4K (3)",
    ]);
  });

  it("normalizes server suffixes before detecting collisions", () => {
    expect([...distinctLabels([{ key: "a", label: "Anime · followed" }, { key: "b", label: "Anime · Theatarr" }, { key: "c", label: "WEB · c" }]).values()]).toEqual(["Anime", "Anime (2)", "WEB"]);
  });

  it("names each key once, an empty label by its key", () => {
    const n = distinctLabels([{ key: "a", label: "4K" }, { key: "a", label: "4K" }, { key: "b", label: "" }, { label: "x" }]);
    expect([...n]).toEqual([["a", "4K"], ["b", "b"]]);
  });

  it("relabels a title's copies, and anything keyed from a map", () => {
    const vs = withDistinctLabels([{ key: "anime-tv", label: "Anime", control }, { key: "anime-tv-theatarr", label: "Anime", control }]);
    expect(vs.map((v) => v.label)).toEqual(["Anime", "Anime (2)"]);
    // A caller without control (users) gets the same.
    expect(withDistinctLabels([{ key: "anime-tv", label: "Anime" }, { key: "anime-tv-theatarr", label: "Anime" }]).map((v) => v.label)).toEqual(["Anime", "Anime (2)"]);
    const same = [{ key: "x", label: "X" }];
    expect(relabel(same, new Map([["x", "X"]]))[0]).toBe(same[0]);
    expect(relabel([{ key: "x", label: "X", titles: 3 }], new Map([["x", "X · x"]]))).toEqual([{ key: "x", label: "X · x", titles: 3 }]);
  });
});

describe("copy bars", () => {
  it("counts a series copy's linked episodes and the part in flight", () => {
    const b = copyBar({ key: "a", coverage: { have: 13, total: 17, aired: 17, upcoming: 1, in_flight: 2 } }, "series");
    expect(b).toMatchObject({ have: 13, total: 17, flight: 2, count: "13/17" });
    expect(b.label).toBe("13 of 17 episodes linked, 2 in progress");
    // In flight never runs past what is missing.
    expect(copyBar({ coverage: { have: 16, total: 17, aired: 17, upcoming: 0, in_flight: 3 } }, "series").flight).toBe(1);
  });

  it("shows a movie copy held or not, striped while it is fetched", () => {
    expect(copyBar({ has_file: true }, "movie")).toMatchObject({ have: 1, flight: 0, count: "1/1" });
    expect(copyBar({ has_file: false, acquisition: acq("downloading") }, "movie")).toMatchObject({ have: 0, flight: 1, count: "0/1" });
    expect(copyBar({ has_file: true, acquisition: acq("downloading", { upgrade: true }) }, "movie").label).toBe("Linked, upgrade in progress");
  });
});

describe("columns and prefs", () => {
  it("moves columns", () => {
    const p = { order: ["size", "next"], shown: ["size"] };
    expect(moveColumn(p, "next", -1).order).toEqual(["next", "size"]);
    expect(moveColumn(p, "size", -1)).toBe(p);
    // The picker moves past the next column it lists, over one it hides.
    expect(swapColumns({ order: ["size", "quality", "next"] }, "next", "size").order).toEqual(["next", "quality", "size"]);
  });

  it("reads poster options back safely", () => {
    expect(normalizePosters({ size: "huge", details: ["year", "nope", "network"] })).toEqual({ size: "medium", details: ["year", "network"] });
    expect(normalizePosters({ size: "small" }).size).toBe("small");
  });

  it("keys prefs per user", () => {
    expect(prefKey("columns", 7)).toBe("theatarr.library.columns.7");
    expect(prefKey("posters", undefined)).toBe("theatarr.library.posters.anon");
  });
});

describe("sorting and next up", () => {
  it("starts names and next dates ascending, the rest descending", () => {
    expect(defaultDesc("name")).toBe(false);
    expect(defaultDesc("next")).toBe(false);
    expect(defaultDesc("completion")).toBe(true);
  });

  it("words what comes next", () => {
    const now = new Date(2026, 9, 29);
    expect(nextWords({ date: "2026-11-04", season: 3, number: 6 }, "series", now)).toBe("S03E06 · Nov 4");
    expect(nextWords({ date: "2027-01-10", type: "digital" }, "movie", now)).toBe("Digital release · Jan 10, 2027");
    expect(nextWords(undefined, "movie", now)).toBe("");
  });
});

describe("bulk expect", () => {
  it("sends the preview's from values of the copies that change", () => {
    const items = [
      { title_id: 1, title: "A", version: "a", label: "A", outcome: "changed" as const, from: false, to: true },
      { title_id: 2, title: "B", version: "a", label: "A", outcome: "stale" as const, from: false, to: true },
      { title_id: 3, title: "C", version: "a", label: "A", outcome: "unchanged" as const, from: true, to: true },
    ];
    expect(bulkExpect(items)).toEqual([{ title_id: 1, version: "a", monitored: false }]);
  });
});

describe("library labels on the title page", () => {
  const vs = [{ key: "tv-2160p", label: "4K" }, { key: "tv-2160p-theatarr", label: "4K" }];
  it("tells same-named libraries apart by number, never by key, for every account", () => {
    expect(withDistinctLabels(vs).map((v) => v.label)).toEqual(["4K", "4K (2)"]);
  });
  it("leaves a library alone once the other was removed", () => {
    const removed = [vs[0], { ...vs[1], removed: true }];
    expect(withDistinctLabels(removed).map((v) => v.label)).toEqual(["4K", "4K"]);
  });
});
