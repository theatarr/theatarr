// The library views' copy helpers (docs/design/ui.md "Library views"): a copy is one version of a title. Each copy has
// a coverage bar (episodes held of those aired; a movie is held or not) with the part in progress striped, and an
// acquisition stage. The table's columns live in lib/index-columns.ts; the poster options and the preferences moved to
// lib/poster-options.ts and lib/prefs.ts and are re-exported here.
import type { Schemas } from "@/api/client";
import { fmtDay } from "@/lib/library";

type VersionBrief = Schemas["VersionBrief"];
type Acquisition = Schemas["Acquisition"];
type Row = { versions?: VersionBrief[]; kind?: string };

export type Stage = Acquisition["stage"];

/** The acquisition stages, in order. */
export const STAGES: Stage[] = ["queued", "starting", "downloading", "complete", "waiting_mount", "linking", "blocked"];

const STAGE_SHORT: Record<Stage, string> = {
  queued: "Queued",
  starting: "Starting",
  downloading: "Downloading",
  complete: "Download complete",
  waiting_mount: "Waiting for mount",
  linking: "Linking",
  blocked: "Blocked",
};

const STAGE_COUNT: Record<Stage, string> = {
  queued: "queued",
  starting: "starting",
  downloading: "downloading",
  complete: "complete",
  waiting_mount: "waiting",
  linking: "linking",
  blocked: "blocked",
};

/** A stage in a few words: "Downloading · 37%", "Waiting for mount", "Upgrade downloading · 37%" (the copy keeps its file). */
export function stageWords(a: Pick<Acquisition, "stage" | "progress" | "upgrade"> | undefined | null): string {
  if (!a) return "";
  let s = STAGE_SHORT[a.stage] ?? a.stage;
  if (a.stage === "downloading" && a.progress !== undefined) s += ` · ${Math.round(Math.max(0, Math.min(a.progress, 100)))}%`;
  if (a.upgrade) s = `Upgrade ${s.charAt(0).toLowerCase()}${s.slice(1)}`;
  return s;
}

/** The poster chip: the stage without the upgrade prefix, short enough for a small poster. */
export function chipWords(a: Pick<Acquisition, "stage" | "progress"> | undefined | null): string {
  if (!a) return "";
  if (a.stage === "downloading" && a.progress !== undefined) return `Downloading ${Math.round(Math.max(0, Math.min(a.progress, 100)))}%`;
  return STAGE_SHORT[a.stage] ?? a.stage;
}

/** The tone a stage shows in: blocked needs attention, the rest are in progress. */
export function stageTone(stage: Stage | undefined): "warning" | "info" {
  return stage === "blocked" ? "warning" : "info";
}

/** "1 downloading · 1 queued · 2 waiting": downloads in hand by stage, in stage order. */
export function stageCounts(stages: Record<string, number> | undefined | null): string {
  if (!stages) return "";
  return STAGES.filter((s) => (stages[s] ?? 0) > 0)
    .map((s) => `${stages[s]} ${STAGE_COUNT[s]}`)
    .join(" · ");
}

/** The Activity page for a stage: blocked under Needs attention, downloading under Downloading, the rest Waiting. */
export function activityHref(stage: Stage | undefined): string {
  switch (stage) {
    case "blocked":
      return "/activity?show=attention";
    case "downloading":
      return "/activity?show=downloading";
    case undefined:
      return "/activity";
  }
  return "/activity?show=waiting";
}

/** Strip old server disambiguators before applying the labels for the current group. */
export function libraryLabel(key: string, label?: string): string {
  const plain = (label || key).replace(/ · (?:followed|Theatarr)$/, "");
  return plain.endsWith(` · ${key}`) ? plain.slice(0, -key.length - 3) : plain;
}

/** Unique labels stay plain; collisions use keys. */
export function distinctLabels(copies: readonly { key?: string; label?: string }[]): Map<string, string> {
  const names = new Map(copies.filter(c => c.key).map(c => [c.key!, libraryLabel(c.key!, c.label)]));
  const counts = new Map<string, number>();
  for (const label of names.values()) counts.set(label, (counts.get(label) ?? 0) + 1);
  return new Map([...names].map(([key, label]) => [key, counts.get(label)! > 1 ? `${label} · ${key}` : label]));
}

/** Library names without their keys, for accounts that do not see files: the plain label, and "(2)", "(3)" after a
 * name already taken. */
export function plainLabels<T extends { key?: string; label?: string }>(vs: readonly T[]): T[] {
  const seen = new Map<string, number>();
  return vs.map((v) => {
    const base = v.label ? libraryLabel(v.key ?? "", v.label) : "Library";
    const n = (seen.get(base) ?? 0) + 1;
    seen.set(base, n);
    return { ...v, label: n > 1 ? `${base} (${n})` : base };
  });
}

/** The copies of one title with their labels made distinct among the libraries it is in now: by key (distinctLabels)
 * for accounts that see files, by number (plainLabels) for the others, who never see keys. A library the title was
 * removed from takes part in no collision and keeps its plain name. */
export function withDistinctLabels<T extends { key?: string; label?: string; removed?: boolean }>(vs: readonly T[], plain = false): T[] {
  const current = vs.filter((v) => !v.removed);
  const names = new Map((plain ? plainLabels(current) : relabel(current, distinctLabels(current))).map((v) => [v.key ?? "", v.label ?? ""]));
  for (const v of vs) if (v.removed && v.key) names.set(v.key, libraryLabel(v.key, v.label));
  return relabel(vs, names);
}

/** Copies (or facets, columns) with each label replaced from names (distinctLabels). */
export function relabel<T extends { key?: string; label?: string }>(vs: readonly T[], names: ReadonlyMap<string, string>): T[] {
  return vs.map((v) => (v.key && names.has(v.key) && names.get(v.key) !== v.label ? { ...v, label: names.get(v.key) } : v));
}

export type CopyColumn = {
  key: string;
  label: string;
};

/** The copy columns for the titles in view: every version key, in order of first appearance. */
export function copyColumns(titles: readonly Row[]): CopyColumn[] {
  const cols = new Map<string, string>();
  for (const t of titles) {
    for (const v of t.versions ?? []) {
      if (v.key && !cols.has(v.key)) cols.set(v.key, v.label || v.key);
    }
  }
  return [...cols].map(([key, label]) => ({ key, label }));
}

export type CopyBar = {
  have: number;
  total: number;
  /** In progress, not held yet (striped). */
  flight: number;
  /** "13/17"; a movie "1/1" or "0/1". */
  count: string;
  /** For screen readers: "13 of 17 episodes linked, 2 in progress". */
  label: string;
};

/** A copy's bar: a series copy's episodes held of those aired (or held), a movie copy held or not, and the part in progress. */
export function copyBar(v: VersionBrief, kind: string | undefined): CopyBar {
  if (kind === "series") {
    const total = v.coverage?.total ?? 0;
    const have = Math.min(v.coverage?.have ?? 0, total);
    const flight = Math.min(v.coverage?.in_flight ?? (v.acquisition && !v.acquisition.upgrade ? v.acquisition.episodes ?? 0 : 0), total - have);
    return {
      have,
      total,
      flight,
      count: `${have}/${total}`,
      label: `${have} of ${total} episode${total === 1 ? "" : "s"} linked${flight ? `, ${flight} in progress` : ""}`,
    };
  }
  const have = v.has_file ? 1 : 0;
  const flight = !have && v.acquisition ? 1 : 0;
  return {
    have,
    total: 1,
    flight,
    count: `${have}/1`,
    label: `${have ? "Linked" : "Not linked yet"}${flight ? ", in progress" : v.acquisition?.upgrade ? ", upgrade in progress" : ""}`,
  };
}

/** Percent of total, for the 5 % width classes. */
export function pct(n: number, total: number): number {
  return total > 0 ? (100 * n) / total : 0;
}

/** The copy whose acquisition a title's poster chip shows: the most advanced stage (blocked first), any copy. */
export function titleAcquisition(t: Row): { version: VersionBrief; acquisition: Acquisition } | undefined {
  let best: { version: VersionBrief; acquisition: Acquisition } | undefined;
  for (const v of t.versions ?? []) {
    const a = v.acquisition;
    if (!a) continue;
    if (!best || STAGES.indexOf(a.stage) > STAGES.indexOf(best.acquisition.stage)) best = { version: v, acquisition: a };
  }
  return best;
}

// ---- Sort order, poster options, preferences (moved; kept here for the existing imports) ----

export { defaultDesc } from "./index-columns";
export { DEFAULT_POSTERS, POSTER_DETAILS, normalizePosters, type PosterDetail, type PosterPrefs, type PosterSize } from "./poster-options";
export { moveColumn, prefKey, readPref, swapColumns, writePref } from "./prefs";

// ---- Bulk review ----

type BulkItem = Schemas["BulkMonitorResult"]["items"][number];

const OUTCOME_WORDS: Record<BulkItem["outcome"], { preview: string; done: string }> = {
  changed: { preview: "Will change", done: "Changed" },
  unchanged: { preview: "Already set", done: "Already set" },
  missing: { preview: "Excluded: the title has no such library", done: "Skipped: no such library" },
  stale: { preview: "Changed since the preview", done: "Skipped: changed since the preview" },
};

export function outcomeWords(o: BulkItem["outcome"], dryRun: boolean): string {
  const w = OUTCOME_WORDS[o];
  return w ? (dryRun ? w.preview : w.done) : o;
}

/** The apply's expect list: the preview's from values of the items that will change. */
export function bulkExpect(items: readonly BulkItem[]): { title_id: number; version: string; monitored: boolean }[] {
  return items.filter((i) => i.outcome === "changed" && i.from !== undefined).map((i) => ({ title_id: i.title_id, version: i.version, monitored: !!i.from }));
}

// ---- Next up ----

/** "S03E06 · Nov 4" for a series' next episode, "Digital release · Nov 4" for a movie's coming release. */
export function nextWords(n: { date: string; season?: number; number?: number; type?: string } | undefined | null, kind: string | undefined, now: Date = new Date()): string {
  if (!n?.date) return "";
  const day = fmtDay(n.date, now);
  if (kind === "series") {
    const ep = n.season !== undefined && n.number !== undefined ? `S${String(n.season).padStart(2, "0")}E${String(n.number).padStart(2, "0")}` : "Next episode";
    return `${ep} · ${day}`;
  }
  const t = n.type ? `${n.type.charAt(0).toUpperCase()}${n.type.slice(1)} release` : "Release";
  return `${t} · ${day}`;
}

/** A copy's bar in one season: episodes held of those aired (or of all, before any airs), with the part in flight. */
export function seasonBar(c: { have: number; in_flight?: number } | undefined, season: { episodes: number; aired: number }): CopyBar {
  const total = Math.max(season.aired || season.episodes, c?.have ?? 0);
  const have = Math.min(c?.have ?? 0, total);
  const flight = Math.min(c?.in_flight ?? 0, total - have);
  return {
    have,
    total,
    flight,
    count: `${have}/${total}`,
    label: `${have} of ${total} episode${total === 1 ? "" : "s"} linked${flight ? `, ${flight} in progress` : ""}`,
  };
}
