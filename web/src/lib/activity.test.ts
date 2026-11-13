import { describe, expect, it } from "vitest";
import { activityHref } from "./copies";
import { filterDownloads, fmtSpan, isWaiting, needsAttention, parseShow, showCounts, STAGE_MEANINGS, stageLabel, stageMeaning, timing } from "./activity";

const NOW = Date.parse("2026-10-26T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const H = 3_600_000;
type D = Parameters<typeof needsAttention>[0];
const d = (x: Partial<D> & { id?: number }): D & { id: number } => ({ id: 1, state: "downloading", steps: [], updated_at: ago(60_000), ...x });

const items = [
  d({ id: 1, state: "downloading" }),
  d({ id: 2, state: "queued", waiting_since: ago(H) }),
  d({ id: 3, state: "queued", waiting_since: ago(7 * H), next_retry: new Date(NOW + 10 * 60_000).toISOString() }),
  d({ id: 4, state: "ready", import_state: "waiting_mount", waiting_since: ago(8 * H) }),
  d({ id: 5, state: "importing" }),
  d({ id: 6, state: "importing", import_state: "partial", waiting_since: ago(60_000) }),
  d({ id: 7, state: "downloading", updated_at: ago(7 * H) }),
  d({ id: 8, state: "added", waiting_since: ago(30 * 60_000) }),
  d({ id: 13, state: "adding" }),
];

describe("activity filters", () => {
  it("parses ?show=, defaulting to All", () => {
    expect(parseShow("attention")).toBe("attention");
    expect(parseShow("waiting")).toBe("waiting");
    expect(parseShow("bogus")).toBe("all");
    expect(parseShow(null)).toBe("all");
  });
  it("splits waiting from downloading", () => {
    expect(items.filter((x) => isWaiting(x)).map((x) => x.id)).toEqual([2, 3, 4, 5, 6, 8, 13]);
    expect(filterDownloads(items, "downloading", NOW).map((x) => x.id)).toEqual([1, 7]);
    expect(filterDownloads(items, "all", NOW)).toHaveLength(items.length);
  });
  it("needs attention: long waits, refused or partial links, failures, no progress for hours", () => {
    expect(filterDownloads(items, "attention", NOW).map((x) => x.id)).toEqual([3, 4, 6, 7]);
    expect(needsAttention(d({ state: "failed" }), NOW)).toBe(true);
    expect(needsAttention(d({ state: "failed", not_started: true }), NOW)).toBe(false);
    expect(needsAttention(d({ steps: [{ key: "ready", label: "Download complete", state: "failed" }] }), NOW)).toBe(true);
    expect(needsAttention(d({ state: "ready", import_state: "rejected", waiting_since: ago(60_000) }), NOW)).toBe(true);
    // A wait without a known start is not flagged.
    expect(needsAttention(d({ state: "queued" }), NOW)).toBe(false);
  });
  it("counts each filter", () => {
    expect(showCounts(items, NOW)).toEqual({ attention: 4, downloading: 2, waiting: 7, all: 9 });
  });
  it("counts only real transfers as Downloading: failures go under Needs attention", () => {
    const failed = [
      d({ id: 9, state: "failed" }),
      d({ id: 10, state: "downloading", steps: [{ key: "downloading", label: "Download", state: "failed" }] }),
      d({ id: 11, state: "ready", import_state: "rejected" }),
      d({ id: 12, state: "imported" }),
    ];
    expect(filterDownloads([...items, ...failed], "downloading", NOW).map((x) => x.id)).toEqual([1, 7]);
    expect(filterDownloads([...items, ...failed], "attention", NOW).map((x) => x.id)).toEqual([3, 4, 6, 7, 9, 10, 11]);
    expect(showCounts(failed, NOW)).toEqual({ attention: 3, downloading: 0, waiting: 1, all: 4 });
  });
  it("puts starting and linking under Waiting, the filter their library stage links open", () => {
    const one = [d({ id: 1, state: "downloading" }), d({ id: 2, state: "importing" }), d({ id: 3, state: "adding" })];
    expect(showCounts(one, NOW)).toMatchObject({ downloading: 1, waiting: 2 });
    expect(activityHref("starting")).toBe("/activity?show=waiting");
    expect(activityHref("linking")).toBe("/activity?show=waiting");
    // A linking row that has not moved for hours still needs a look.
    expect(needsAttention(d({ state: "importing", updated_at: ago(7 * H) }), NOW)).toBe(true);
  });
});

describe("stage words", () => {
  it("calls a finished provider download Download complete, never ready", () => {
    expect(stageLabel(d({ state: "ready" }))).toBe("Download complete");
    expect(stageLabel(d({ state: "ready", import_state: "waiting_mount" }))).toBe("Waiting for the mount");
    expect(stageLabel(d({ state: "importing" }))).toBe("Linking");
    expect(stageLabel(d({ state: "queued", next_retry: ago(-60_000) }))).toBe("Waiting for a provider");
    expect(stageLabel(d({ state: "queued" }))).toBe("Queued");
    expect(stageLabel(d({ state: "failed" }))).toBe("Failed");
    expect(stageLabel(d({ state: "failed", not_started: true }))).toBe("Not started");
    for (const s of ["queued", "adding", "added", "downloading", "ready", "importing", "imported", "failed"]) {
      expect(stageLabel(d({ state: s as D["state"] })).toLowerCase()).not.toContain("ready");
    }
  });
  it("times the wait, the last update and the next try", () => {
    expect(timing(items[2], NOW)).toEqual({ waited: "7 h", updated: "1 min ago", retry: "in 10 min" });
    expect(timing(d({ next_retry: ago(60_000), updated_at: undefined as unknown as string }), NOW)).toEqual({});
    expect(fmtSpan(45_000)).toBe("45 s");
    expect(fmtSpan(3 * 86_400_000)).toBe("3 d");
  });
});

describe("status meanings", () => {
  it("has a meaning for every status stageLabel can return", () => {
    const states = ["queued", "adding", "added", "downloading", "ready", "importing", "imported", "failed"];
    const imports = [undefined, "waiting_mount", "linked", "partial", "rejected", "dry_run", "failed"];
    const labels = new Set<string>();
    for (const state of states) {
      for (const import_state of imports) {
        for (const extra of [{}, { next_retry: "2026-10-26T12:10:00Z" }, { not_started: true }]) {
          labels.add(stageLabel({ ...d({ state: state as D["state"], import_state: import_state as D["import_state"] }), ...extra }));
        }
      }
    }
    for (const l of labels) expect(stageMeaning(l), l).not.toBe("");
    expect(STAGE_MEANINGS.map((m) => m.label).sort()).toEqual([...labels].sort());
  });
});
