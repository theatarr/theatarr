// The library views' copy helpers (docs/design/ui.md "Library views"): a copy is one version of a title. Each copy has
// an ownership (followed: an arr still imports it, so its monitoring is read-only here; else Theatarr manages it),
// a coverage bar (episodes held of those aired; a movie is held or not) with the part in progress striped, and an
// acquisition stage. Plus the table's columns, the poster options and the per-user preferences that keep them.
import type { Schemas } from "@/api/client";
import { fmtDay, stateWords } from "@/lib/library";

type VersionBrief = Schemas["VersionBrief"];
type Acquisition = Schemas["Acquisition"];
type VersionControl = Schemas["VersionControl"];
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

/** A copy is followed when an arr still imports it: its monitoring is read-only here. */
export function isFollowed(c: VersionControl | undefined | null): boolean {
  return !!c?.managed_by;
}

/** "Followed · sonarr_4k · read-only" or "Theatarr-managed"; "" for callers who get no control. */
export function ownershipWords(c: VersionControl | undefined | null): string {
  if (!c) return "";
  return c.managed_by ? `mirrors ${c.managed_by}` : "Theatarr-managed";
}

/** "Followed · read-only" or "Theatarr-managed": the poster's short ownership line (the instance goes in a tooltip). */
export function shortOwnershipWords(c: VersionControl | undefined | null): string {
  if (!c) return "";
  return c.managed_by ? "Followed · read-only" : "Theatarr-managed";
}

type Owner = "followed" | "managed" | "mixed" | undefined;

/** A copy's ownership as a label suffix needs it: undefined when the caller gets no control. */
export function ownerOf(c: VersionControl | undefined | null): Owner {
  return c ? (c.managed_by ? "followed" : "managed") : undefined;
}

/** Strip old server disambiguators before applying the labels for the current group. */
export function libraryLabel(key: string, label?: string): string {
  const plain = (label || key).replace(/ · (?:followed|Theatarr)$/, "");
  return plain.endsWith(` · ${key}`) ? plain.slice(0, -key.length - 3) : plain;
}

/** Unique labels stay plain; collisions use keys, independent of ownership. */
export function distinctLabels(copies: readonly { key?: string; label?: string; owner?: Owner }[]): Map<string, string> {
  const names = new Map(copies.filter(c => c.key).map(c => [c.key!, libraryLabel(c.key!, c.label)]));
  const counts = new Map<string, number>();
  for (const label of names.values()) counts.set(label, (counts.get(label) ?? 0) + 1);
  return new Map([...names].map(([key, label]) => [key, counts.get(label)! > 1 ? `${label} · ${key}` : label]));
}

/** The copies of one title with their labels made distinct (distinctLabels, ownership from each copy's control). */
export function withDistinctLabels<T extends { key?: string; label?: string; control?: VersionControl | null }>(vs: readonly T[]): T[] {
  const names = distinctLabels(vs.map((v) => ({ key: v.key, label: v.label, owner: ownerOf(v.control) })));
  return relabel(vs, names);
}

/** Copies (or facets, columns) with each label replaced from names (distinctLabels). */
export function relabel<T extends { key?: string; label?: string }>(vs: readonly T[], names: ReadonlyMap<string, string>): T[] {
  return vs.map((v) => (v.key && names.has(v.key) && names.get(v.key) !== v.label ? { ...v, label: names.get(v.key) } : v));
}

export type CopyColumn = {
  key: string;
  label: string;
  /** followed: every copy of this key in view is followed; managed: none is; mixed: some are. undefined: no control (users). */
  owner?: "followed" | "managed" | "mixed";
  /** The arr instances that follow copies of this key. */
  by: string[];
};

/** The copy columns for the titles in view: every version key, in order of first appearance, with an ownership summary. */
export function copyColumns(titles: readonly Row[]): CopyColumn[] {
  const cols = new Map<string, { label: string; followed: number; managed: number; by: Set<string> }>();
  for (const t of titles) {
    for (const v of t.versions ?? []) {
      if (!v.key) continue;
      let c = cols.get(v.key);
      if (!c) {
        c = { label: v.label || v.key, followed: 0, managed: 0, by: new Set() };
        cols.set(v.key, c);
      }
      if (v.control) {
        if (v.control.managed_by) {
          c.followed++;
          c.by.add(v.control.managed_by);
        } else c.managed++;
      }
    }
  }
  return [...cols].map(([key, c]) => ({
    key,
    label: c.label,
    owner: c.followed && c.managed ? "mixed" : c.followed ? "followed" : c.managed ? "managed" : undefined,
    by: [...c.by].sort(),
  }));
}

/** A copy column header's ownership line: "Followed · sonarr_4k · read-only", "Theatarr-managed", "Some followed (read-only)". */
export function columnOwnerWords(c: CopyColumn): string {
  switch (c.owner) {
    case "followed":
      return `Followed${c.by.length === 1 ? ` · ${c.by[0]}` : ""} · read-only`;
    case "managed":
      return "Theatarr-managed";
    case "mixed":
      return "Some followed (read-only)";
  }
  return "";
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

// ---- Table columns ----

export type ColumnId = "next" | "quality" | "size" | "added" | "network";

export const TABLE_COLUMNS: { id: ColumnId; label: string; hint: string; seeFiles?: boolean }[] = [
  { id: "next", label: "Next / activity", hint: "What is in progress, else the next episode or release" },
  { id: "quality", label: "Quality per library", hint: "Each library's current quality", seeFiles: true },
  { id: "size", label: "Size", hint: "The title's library size", seeFiles: true },
  { id: "added", label: "Added", hint: "When the title was added" },
  { id: "network", label: "Network / studio", hint: "From the title's metadata" },
];

export type ColumnPrefs = { order: ColumnId[]; shown: ColumnId[] };

export const DEFAULT_COLUMNS: ColumnPrefs = { order: TABLE_COLUMNS.map((c) => c.id), shown: ["next", "size"] };

const isColumn = (x: unknown): x is ColumnId => TABLE_COLUMNS.some((c) => c.id === x);

/** Column prefs read back: unknown ids dropped, columns added since appended (hidden). */
export function normalizeColumns(p: unknown): ColumnPrefs {
  if (!p || typeof p !== "object") return DEFAULT_COLUMNS;
  const o = p as { order?: unknown; shown?: unknown };
  const order = Array.isArray(o.order) ? [...new Set(o.order.filter(isColumn))] : [];
  for (const c of TABLE_COLUMNS) if (!order.includes(c.id)) order.push(c.id);
  const shown = Array.isArray(o.shown) ? [...new Set(o.shown.filter(isColumn))] : DEFAULT_COLUMNS.shown;
  return { order, shown };
}

/** Move a column one place up (-1) or down (+1) in the order. */
export function moveColumn<P extends { order: string[] }>(p: P, id: P["order"][number], by: -1 | 1): P {
  const order = [...p.order];
  const i = order.indexOf(id);
  const j = i + by;
  if (i < 0 || j < 0 || j >= order.length) return p;
  [order[i], order[j]] = [order[j], order[i]];
  return { ...p, order };
}

/** The extra columns shown, in order, for this account. */
export function visibleColumns(p: ColumnPrefs, seeFiles: boolean): ColumnId[] {
  return p.order.filter((id) => p.shown.includes(id) && (seeFiles || !TABLE_COLUMNS.find((c) => c.id === id)?.seeFiles));
}

// ---- Poster options ----

export type PosterSize = "small" | "medium" | "large";
export type PosterDetail = "year" | "next" | "network" | "quality" | "copy_size" | "added";

export const POSTER_DETAILS: { id: PosterDetail; label: string; seeFiles?: boolean }[] = [
  { id: "year", label: "Year" },
  { id: "next", label: "Next episode / release" },
  { id: "network", label: "Network / studio" },
  { id: "quality", label: "Each library's current quality", seeFiles: true },
  { id: "copy_size", label: "Each library's size", seeFiles: true },
  { id: "added", label: "Added to Theatarr" },
];

export type PosterPrefs = { size: PosterSize; details: PosterDetail[] };

export const DEFAULT_POSTERS: PosterPrefs = { size: "medium", details: ["year", "next", "quality"] };

export function normalizePosters(p: unknown): PosterPrefs {
  if (!p || typeof p !== "object") return DEFAULT_POSTERS;
  const o = p as { size?: unknown; details?: unknown };
  const size = o.size === "small" || o.size === "large" || o.size === "medium" ? o.size : DEFAULT_POSTERS.size;
  const details = Array.isArray(o.details)
    ? [...new Set(o.details.filter((d): d is PosterDetail => POSTER_DETAILS.some((x) => x.id === d)))]
    : DEFAULT_POSTERS.details;
  return { size, details };
}

// ---- Preferences ----

/** The localStorage key of a per-user library preference: theatarr.library.<what>.<user>. */
export function prefKey(what: string, user: number | string | undefined): string {
  return `theatarr.library.${what}.${user ?? "anon"}`;
}

export function readPref<T>(what: string, user: number | string | undefined, normalize: (v: unknown) => T): T {
  try {
    const raw = window.localStorage.getItem(prefKey(what, user));
    return normalize(raw ? JSON.parse(raw) : undefined);
  } catch {
    return normalize(undefined);
  }
}

export function writePref(what: string, user: number | string | undefined, value: unknown) {
  try {
    window.localStorage.setItem(prefKey(what, user), JSON.stringify(value));
  } catch {
    // not kept
  }
}

// ---- Header sorting ----

/** The grid sort a table header asks for: a copy column sorts by that library's completion. */
export function headerSort(col: "title" | "copy" | ColumnId, key?: string): { sort: string; sort_version?: string } | undefined {
  switch (col) {
    case "title":
      return { sort: "name" };
    case "copy":
      return key ? { sort: "completion", sort_version: key } : undefined;
    case "next":
      return { sort: "next" };
    case "quality":
      return { sort: "quality" };
    case "size":
      return { sort: "size" };
    case "added":
      return { sort: "added" };
  }
  return undefined;
}

/** The order a sort starts in: names and next dates ascending, the rest descending (as the server defaults). */
export function defaultDesc(sort: string): boolean {
  return sort !== "name" && sort !== "next";
}

// ---- Bulk review ----

type BulkItem = Schemas["BulkMonitorResult"]["items"][number];

const OUTCOME_WORDS: Record<BulkItem["outcome"], { preview: string; done: string }> = {
  changed: { preview: "Will change", done: "Changed" },
  unchanged: { preview: "Already set", done: "Already set" },
  read_only: { preview: "Excluded: followed, read-only", done: "Skipped: followed, read-only" },
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

/** A copy cell's headline: a series copy's "13 / 17 available", a movie copy's state in words (lib/library.ts
 * stateWords). upcoming: the movie's release day when it is not released yet (upcomingRelease), so a copy without a
 * file reads "Upcoming · Jan 14" instead of Missing. */
export function copyStatus(v: VersionBrief, kind: string | undefined, plain: boolean, upcoming?: string): { label: string; tone: "positive" | "warning" | "negative" | "neutral" } {
  if (kind === "series" && v.coverage) {
    const { have, total } = v.coverage;
    if (total === 0) return { label: "Nothing aired yet", tone: "neutral" };
    return { label: `${have} / ${total} available`, tone: have >= total ? "positive" : have === 0 ? "negative" : "warning" };
  }
  if (kind === "movie" && upcoming && v.state === "wanted" && !v.has_file) return { label: `Upcoming · ${fmtDay(upcoming)}`, tone: "neutral" };
  return stateWords(v.state, v.has_file, plain);
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
