// The release grid's columns, sorting and filters (interactive search and the stored "Last search"; docs/design/ui.md
// "Interactive search"): which columns there are and what they mean, the per-user column choice and sort (kept in
// this browser like the library columns, lib/copies.ts prefs), sorting and filtering the rows, the cells' words, and
// when a grab asks first. Title and Grab are fixed columns, not in the list.
import type { Candidate } from "@/api/add";
import { grabScope, type Scope } from "@/lib/ops";

export type ReleaseColumnId =
  | "source"
  | "age"
  | "indexer"
  | "history"
  | "size"
  | "peers"
  | "cached"
  | "episodes"
  | "language"
  | "quality"
  | "score"
  | "formats"
  | "flags"
  | "status";

export type ReleaseColumn = { id: ReleaseColumnId; label: string; hint: string; series?: true };

export const RELEASE_COLUMNS: ReleaseColumn[] = [
  { id: "source", label: "Source", hint: "Where it came from: a Torznab feed, a built-in indexer, DMM, Torrentio, Zilean or SeaDex" },
  { id: "age", label: "Age", hint: "How long ago the indexer published it" },
  { id: "indexer", label: "Indexer", hint: "The indexer that returned it" },
  { id: "history", label: "History", hint: "What came of this release before: grabbed, imported, failed or replaced" },
  { id: "size", label: "Size", hint: "Size" },
  { id: "peers", label: "Peers", hint: "Seeders / leechers" },
  { id: "cached", label: "Cached on", hint: "Debrid providers known to have it cached" },
  { id: "episodes", label: "Episodes", hint: "The episodes it covers", series: true },
  { id: "language", label: "Language", hint: "Languages in the release" },
  { id: "quality", label: "Quality", hint: "Quality, with Proper for a second revision" },
  { id: "score", label: "Score", hint: "Custom format score; point at it for the formats it matched" },
  { id: "formats", label: "Formats", hint: "The custom formats it matched" },
  { id: "flags", label: "Flags", hint: "Indexer flags such as freeleech or internal" },
  { id: "status", label: "Status", hint: "Accepted, or why Theatarr rejects it" },
];

/** The columns a kind of title has (Episodes only for a series). */
export const releaseColumnsFor = (kind: "movie" | "series") => RELEASE_COLUMNS.filter((c) => kind === "series" || !c.series);

export type ReleaseSortKey = "rank" | "age" | "title" | "indexer" | "size" | "peers" | "cached" | "language" | "quality" | "score";
export type ReleaseSort = { key: ReleaseSortKey; dir: "asc" | "desc" };
export type ReleaseColumnPrefs = { order: ReleaseColumnId[]; shown: ReleaseColumnId[]; sort: ReleaseSort };

export const DEFAULT_RELEASE_COLUMNS: ReleaseColumnPrefs = {
  order: RELEASE_COLUMNS.map((c) => c.id),
  shown: RELEASE_COLUMNS.map((c) => c.id).filter((id) => id !== "formats"),
  sort: { key: "rank", dir: "asc" },
};

const SORT_KEYS: ReleaseSortKey[] = ["rank", "age", "title", "indexer", "size", "peers", "cached", "language", "quality", "score"];
const isColumn = (x: unknown): x is ReleaseColumnId => RELEASE_COLUMNS.some((c) => c.id === x);

/** Release column prefs read back: unknown ids dropped, columns added since appended (hidden), a bad sort is rank. */
export function normalizeReleaseColumns(p: unknown): ReleaseColumnPrefs {
  if (!p || typeof p !== "object") return DEFAULT_RELEASE_COLUMNS;
  const o = p as { order?: unknown; shown?: unknown; sort?: unknown };
  const order = Array.isArray(o.order) ? [...new Set(o.order.filter(isColumn))] : [];
  const known = new Set(order);
  for (const c of RELEASE_COLUMNS) if (!order.includes(c.id)) order.push(c.id);
  const shown = Array.isArray(o.shown)
    ? [...new Set(o.shown.filter(isColumn))].filter((id) => known.has(id))
    : DEFAULT_RELEASE_COLUMNS.shown;
  const s = o.sort as { key?: unknown; dir?: unknown } | undefined;
  const sort: ReleaseSort =
    s && SORT_KEYS.includes(s.key as ReleaseSortKey) && (s.dir === "asc" || s.dir === "desc")
      ? { key: s.key as ReleaseSortKey, dir: s.dir }
      : DEFAULT_RELEASE_COLUMNS.sort;
  return { order, shown, sort };
}

/** The direction a header sorts in on its first click: newest, largest, best, A to Z. */
export function naturalDir(key: ReleaseSortKey): "asc" | "desc" {
  return key === "size" || key === "peers" || key === "cached" || key === "score" ? "desc" : "asc";
}

/** The sort key a column's header sorts by; none for the columns that do not sort. */
export function columnSortKey(id: ReleaseColumnId | "title"): ReleaseSortKey | undefined {
  switch (id) {
    case "title":
    case "age":
    case "indexer":
    case "size":
    case "peers":
    case "cached":
    case "quality":
    case "score":
    case "language":
      return id;
  }
  return undefined;
}

type Value = number | string | undefined;

/** A row's value for a key, ascending; undefined is unknown and always sorts last. Age ascends from the newest. */
function valueOf(c: Candidate, key: ReleaseSortKey): Value {
  switch (key) {
    case "rank":
      return c.rank ?? undefined;
    case "age": {
      const t = c.published ? Date.parse(c.published) : NaN;
      return Number.isNaN(t) ? undefined : -t;
    }
    case "title":
      return (c.release ?? "").toLowerCase();
    case "indexer":
      return c.indexer ? c.indexer.toLowerCase() : undefined;
    case "size":
      return c.size || undefined;
    case "peers": {
      const s = c.seeders ?? 0;
      return s < 0 ? undefined : s;
    }
    case "cached":
      return c.cached_on?.length ?? 0;
    case "language":
      return c.languages?.[0]?.toLowerCase();
    case "quality":
      return c.quality_rank && c.quality_rank > 0 ? c.quality_rank : undefined;
    case "score":
      return c.score ?? 0;
  }
}

/** The rows in the asked order. Stable: ties keep rank order; unknown values go last either way. */
export function sortReleases(items: Candidate[], sort: ReleaseSort): Candidate[] {
  const byRank = items.map((c, i) => ({ c, i })).sort((a, b) => (a.c.rank ?? a.i) - (b.c.rank ?? b.i) || a.i - b.i);
  const sign = sort.dir === "desc" ? -1 : 1;
  return byRank
    .map((x, i) => ({ c: x.c, i, v: valueOf(x.c, sort.key) }))
    .sort((a, b) => {
      if (a.v === undefined || b.v === undefined) return (a.v === undefined ? 1 : 0) - (b.v === undefined ? 1 : 0) || a.i - b.i;
      const d = typeof a.v === "string" ? a.v.localeCompare(b.v as string) : a.v - (b.v as number);
      return d * sign || a.i - b.i;
    })
    .map((x) => x.c);
}

export type ReleaseFilter = { text: string; accepted: boolean; cached: boolean; packs: boolean };

export const NO_FILTER: ReleaseFilter = { text: "", accepted: false, cached: false, packs: false };

/** The rows the filters keep: text in the name, indexer, quality or languages; accepted only; cached only; packs only. */
export function filterReleases(items: Candidate[], f: ReleaseFilter): Candidate[] {
  const q = f.text.trim().toLowerCase();
  return items.filter((c) => {
    if (f.accepted && !c.accepted) return false;
    if (f.cached && !c.cached_on?.length) return false;
    if (f.packs && (c.episodes?.length ?? 0) <= 1) return false;
    if (!q) return true;
    return [c.release, c.indexer, c.quality, ...(c.languages ?? [])].some((s) => !!s && s.toLowerCase().includes(q));
  });
}

/** How long ago it was published: "12 min", "5 h", "468 d"; "—" when unknown. */
export function fmtAge(published: string | undefined, now: Date | number = Date.now()): string {
  const t = published ? Date.parse(published) : NaN;
  if (Number.isNaN(t)) return "—";
  const min = Math.max(0, Math.floor((+now - t) / 60_000));
  if (min < 60) return `${min} min`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h} h`;
  return `${Math.floor(h / 24)} d`;
}

/** Seeders / leechers: "120 / 8", "120" without leechers, "—" when the indexer gave no count. */
export function peersText(c: Pick<Candidate, "seeders" | "leechers">): string {
  const s = c.seeders ?? 0;
  if (s < 0) return "—";
  return c.leechers !== undefined && c.leechers !== null ? `${s} / ${c.leechers}` : String(s);
}

/** The episodes a release covers: "S02E05", or "S02E01–E12 (12)" for several. */
export function episodesText(eps: string[] | undefined): string {
  if (!eps?.length) return "—";
  if (eps.length === 1) return eps[0];
  const first = eps[0];
  const last = eps[eps.length - 1];
  const sameSeason = first.slice(0, 3) === last.slice(0, 3) && /^S\d{2}E/.test(last);
  return `${first}–${sameSeason ? last.slice(3) : last} (${eps.length})`;
}

export const SOURCE_LABEL: Record<string, string> = {
  torznab: "Torznab",
  cardigann: "Built-in",
  dmm: "DMM",
  torrentio: "Torrentio",
  zilean: "Zilean",
  seadex: "SeaDex",
};

export const FLAG_LABEL: Record<string, string> = {
  freeleech: "Freeleech",
  internal: "Internal",
  scene: "Scene",
  seadex: "SeaDex",
  "seadex-best": "SeaDex best",
};

/** Whether an accepted release would replace a better-scored file. Only for a release of one file (a film or one
 *  episode): a candidate's existing is the first held episode's file only (decide), so for a pack it says nothing
 *  about the other episodes' files, and a pack gets no score-based question. */
function replacesBetter(c: Candidate): boolean {
  return (c.episodes?.length ?? 0) <= 1 && !!c.existing && (c.existing.score ?? 0) > (c.score ?? 0);
}

/** Whether a grab asks first: a rejected release, or a single-file one that would replace a better-scored file. */
export function needsConfirm(c: Candidate): boolean {
  return !c.accepted || replacesBetter(c);
}

/** The grab confirmation: grabScope, and for an accepted release that replaces a better-scored file, a warning that
 *  it does. late: the server's reasons when it rejected the release at grab time. */
export function grabConfirmScope(c: Candidate, library: string, late?: string[]): Scope {
  const base = grabScope(c, library, late);
  const replaces = c.accepted && !late?.length && replacesBetter(c);
  if (!replaces) return base;
  return {
    ...base,
    title: "Replace a better-scored file",
    does: [...base.does, `It replaces the current file (${c.existing?.quality || "unknown quality"}, score ${c.existing?.score ?? 0}), which scores higher`],
    confirm: "Grab anyway",
  };
}
