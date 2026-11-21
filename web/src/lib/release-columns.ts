// The release grid's columns, sorting and filters (interactive search and the stored "Last search"; docs/design/ui.md
// "Interactive search"): which columns there are and what they mean, the per-user column choice and sort (kept in
// this browser like the library columns, lib/copies.ts prefs), sorting and filtering the rows, the cells' words, and
// when a grab asks first. Title and Grab are fixed columns, not in the list.
import type { Candidate, Releases } from "@/api/add";
import { grabScope, rejectionWarning, unblockGrabScope, type BlockedRelease, type Scope } from "@/lib/ops";
import { wordMatcher } from "@/lib/words";

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
  // Status (accepted, or why not) right after Title, so the verdict stays in view when the table scrolls sideways.
  { id: "status", label: "Status", hint: "Accepted, or why Theatarr rejects it" },
  { id: "indexer", label: "Indexer", hint: "The indexers that returned it; open it for each copy's name" },
  { id: "history", label: "History", hint: "What came of this release before: grabbed, imported, failed or replaced" },
  { id: "size", label: "Size", hint: "Size" },
  { id: "peers", label: "Peers", hint: "Seeders / leechers" },
  { id: "cached", label: "Cached on", hint: "Debrid providers known to have it cached" },
  { id: "episodes", label: "Episodes", hint: "The episodes it covers", series: true },
  { id: "language", label: "Language", hint: "Languages in the release" },
  { id: "quality", label: "Quality", hint: "Quality, with Proper for a second revision" },
  { id: "score", label: "Score", hint: "Custom format score; open it for the formats it matched" },
  { id: "formats", label: "Formats", hint: "The custom formats it matched" },
  { id: "flags", label: "Flags", hint: "Indexer flags such as freeleech or internal" },
];

/** The columns a kind of title has (Episodes only for a series). */
export const releaseColumnsFor = (kind: "movie" | "series") => RELEASE_COLUMNS.filter((c) => kind === "series" || !c.series);

export type ReleaseSortKey =
  | "rank" | "source" | "age" | "title" | "indexer" | "history" | "size" | "peers" | "cached" | "language" | "quality" | "score" | "flags" | "status";
export type ReleaseSort = { key: ReleaseSortKey; dir: "asc" | "desc" };
/** The kept column choice. A sort stored by an earlier version is ignored: the sort lasts one answer. */
export type ReleaseColumnPrefs = { order: ReleaseColumnId[]; shown: ReleaseColumnId[] };

/** The order a grid starts in: the server's ranking. */
export const BEST_MATCH: ReleaseSort = { key: "rank", dir: "asc" };

/** The default order: the verdict and the deciding numbers first (status, quality, score), so they are in view at
 *  1440 px without scrolling sideways; Source (mostly one kind) is offered but hidden until chosen. */
const DEFAULT_ORDER: ReleaseColumnId[] = ["age", "status", "quality", "score", "size", "cached", "indexer", "history", "peers", "episodes", "language", "formats", "flags", "source"];

export const DEFAULT_RELEASE_COLUMNS: ReleaseColumnPrefs = {
  order: DEFAULT_ORDER,
  shown: DEFAULT_ORDER.filter((id) => id !== "formats" && id !== "source"),
};

const isColumn = (x: unknown): x is ReleaseColumnId => RELEASE_COLUMNS.some((c) => c.id === x);

/** Release column prefs read back: unknown ids dropped, columns added since appended (hidden). */
export function normalizeReleaseColumns(p: unknown): ReleaseColumnPrefs {
  if (!p || typeof p !== "object") return DEFAULT_RELEASE_COLUMNS;
  const o = p as { order?: unknown; shown?: unknown };
  const order = Array.isArray(o.order) ? [...new Set(o.order.filter(isColumn))] : [];
  const known = new Set(order);
  for (const c of RELEASE_COLUMNS) if (!order.includes(c.id)) order.push(c.id);
  const shown = Array.isArray(o.shown)
    ? [...new Set(o.shown.filter(isColumn))].filter((id) => known.has(id))
    : DEFAULT_RELEASE_COLUMNS.shown;
  return { order, shown };
}

/** The direction a header sorts in on its first click: newest, largest, best, A to Z. */
export function naturalDir(key: ReleaseSortKey): "asc" | "desc" {
  return key === "size" || key === "peers" || key === "cached" || key === "score" || key === "flags" ? "desc" : "asc";
}

/** The sort key a column's header sorts by (the columns Sonarr's interactive search sorts); none for Episodes and
 *  Formats. */
export function columnSortKey(id: ReleaseColumnId | "title"): ReleaseSortKey | undefined {
  switch (id) {
    case "title":
    case "source":
    case "history":
    case "flags":
    case "status":
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

/** History sorts the current file first, then imported, grabbed, replaced and failed; no history last. */
const OUTCOME_ORDER = ["current", "imported", "grabbed", "replaced", "failed"];

/** A row's value for a key, ascending; undefined is unknown and always sorts last. Age ascends from the newest;
 *  Status puts the accepted first, then the fewest reasons. */
function valueOf(c: Candidate, key: ReleaseSortKey, outcomes?: Releases["outcomes"]): Value {
  switch (key) {
    case "rank":
      return c.rank ?? undefined;
    case "source":
      return c.source || undefined;
    case "history": {
      const o = c.info_hash ? outcomes?.[c.info_hash.toLowerCase()] : undefined;
      const i = o ? OUTCOME_ORDER.indexOf(o.outcome ?? "") : -1;
      return i < 0 ? undefined : i;
    }
    case "flags":
      return c.flags?.length ?? 0;
    case "status":
      return c.accepted ? 0 : Math.max(1, c.rejections?.length ?? 0);
    case "age": {
      const t = c.published ? Date.parse(c.published) : NaN;
      return Number.isNaN(t) ? undefined : -t;
    }
    case "title":
      return (c.release ?? "").toLowerCase();
    case "indexer": {
      const first = rowIndexers(c)[0];
      return first ? first.toLowerCase() : undefined;
    }
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

/** The rows in the asked order, all of them (the grid pages after sorting). Stable: ties keep rank order; unknown
 *  values go last either way. outcomes: the History column's answers. */
export function sortReleases(items: Candidate[], sort: ReleaseSort, outcomes?: Releases["outcomes"]): Candidate[] {
  const byRank = items.map((c, i) => ({ c, i })).sort((a, b) => (a.c.rank ?? a.i) - (b.c.rank ?? b.i) || a.i - b.i);
  const sign = sort.dir === "desc" ? -1 : 1;
  return byRank
    .map((x, i) => ({ c: x.c, i, v: valueOf(x.c, sort.key, outcomes) }))
    .sort((a, b) => {
      if (a.v === undefined || b.v === undefined) return (a.v === undefined ? 1 : 0) - (b.v === undefined ? 1 : 0) || a.i - b.i;
      const d = typeof a.v === "string" ? a.v.localeCompare(b.v as string) : a.v - (b.v as number);
      return d * sign || a.i - b.i;
    })
    .map((x) => x.c);
}

/** cached: "" any release, CACHED_ANY cached on some provider, else one provider's code. indexer, quality,
 *  language: one value from the list ("" any). reason: a rejection reason code picked from the summary chips
 *  (interactive search); "" none. */
export type ReleaseFilter = {
  text: string;
  accepted: boolean;
  cached: string;
  packs: boolean;
  indexer: string;
  quality: string;
  language: string;
  reason?: string;
};

export const CACHED_ANY = "*";

export const NO_FILTER: ReleaseFilter = { text: "", accepted: false, cached: "", packs: false, indexer: "", quality: "", language: "" };

/** The rows the filters keep: every word in the name, any of its indexers, quality or languages; accepted only; cached (anywhere
 *  or on one provider); packs only; the picked indexer, quality or language; rejected for the picked reason. */
export function filterReleases(items: Candidate[], f: ReleaseFilter): Candidate[] {
  const match = wordMatcher(f.text);
  return items.filter((c) => {
    if (f.accepted && !c.accepted) return false;
    if (f.reason && (c.accepted || !(c.rejections ?? []).some((r) => r.reason === f.reason))) return false;
    if (f.cached === CACHED_ANY ? !c.cached_on?.length : f.cached && !(c.cached_on ?? []).includes(f.cached)) return false;
    if (f.indexer && !rowIndexers(c).includes(f.indexer)) return false;
    if (f.quality && c.quality !== f.quality) return false;
    if (f.language && !(c.languages ?? []).includes(f.language)) return false;
    if (f.packs && (c.episodes?.length ?? 0) <= 1) return false;
    return match(c.release, ...rowIndexers(c), c.quality, ...(c.languages ?? []));
  });
}

/** The values the indexer, quality, language and cached-on filters offer: those in this search's releases (all of
 *  them, not only the shown page); qualities best first, the rest A to Z. A filter that cannot narrow the list (one
 *  value, which every release has) offers none. */
export function filterChoices(items: Candidate[]): { indexer: string[]; quality: string[]; language: string[]; cached: string[] } {
  const indexer = new Set<string>();
  const quality = new Map<string, number>();
  const language = new Set<string>();
  const cached = new Set<string>();
  for (const c of items) {
    for (const i of rowIndexers(c)) indexer.add(i);
    if (c.quality) quality.set(c.quality, Math.max(quality.get(c.quality) ?? 0, c.quality_rank ?? 0));
    for (const l of c.languages ?? []) language.add(l);
    for (const p of c.cached_on ?? []) cached.add(p);
  }
  const az = (s: Set<string>) => [...s].sort((a, b) => a.localeCompare(b));
  const narrows = (vs: string[], has: (c: Candidate) => boolean) => (vs.length > 1 || items.some((c) => !has(c)) ? vs : []);
  return {
    indexer: narrows([...indexer].sort((a, b) => indexerWords(a).localeCompare(indexerWords(b))), (c) => rowIndexers(c).length > 0),
    quality: narrows([...quality].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([q]) => q), (c) => !!c.quality),
    language: narrows(az(language), (c) => !!c.languages?.length),
    cached: narrows(az(cached), (c) => !!c.cached_on?.length),
  };
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
/** An indexer by its key, without the "prowlarr-" prefix a linked Prowlarr indexer's key carries ("prowlarr-therarbg"
 *  reads "therarbg"); the key stays in the tooltip. */
export function indexerWords(key: string | null | undefined): string {
  return (key ?? "").replace(/^prowlarr-/, "");
}

/** The indexers that returned a row's torrent, the shown copy's first (an answer without the list: its one indexer). */
export function rowIndexers(c: Pick<Candidate, "indexer" | "indexers">): string[] {
  if (c.indexers?.length) return c.indexers;
  return c.indexer ? [c.indexer] : [];
}

/** The Indexer cell: the first two indexers' short names, then "+N" for the rest ("therarbg, dmm +1"). */
export function indexersText(c: Pick<Candidate, "indexer" | "indexers">): string {
  const all = rowIndexers(c).map(indexerWords);
  return all.slice(0, 2).join(", ") + (all.length > 2 ? ` +${all.length - 2}` : "");
}

/** Every indexer of a row by its short name, for the narrow cards, which have no tooltip ("tpb, therarbg, dmm"). */
export function allIndexersText(c: Pick<Candidate, "indexer" | "indexers">): string {
  return rowIndexers(c).map(indexerWords).join(", ");
}

/** The Indexer cell's tooltip: each copy's indexer and release name, the shown copy first; the indexer's key alone
 *  when there is one copy. */
export function indexersTitle(c: Pick<Candidate, "indexer" | "release" | "copies">): string | undefined {
  if (!c.copies?.length) return c.indexer || undefined;
  return [{ indexer: c.indexer, release: c.release }, ...c.copies].map((x) => `${x.indexer || "—"}: ${x.release ?? ""}`).join("\n");
}

/** What a stored decision's search target was, after "from": "RSS", "a search", "a search for S01" (the server's
 *  sourceWord, internal/web/releases.go). */
export function searchTargetWords(target: string | null | undefined): string {
  const t = (target ?? "").toLowerCase();
  if (t === "rss") return "RSS";
  if (t === "" || t === "movie") return "a search";
  return `a search for ${t.toUpperCase()}`;
}

/** The target a search of this scope stores: "s02e05" for an episode, "s02" for a season, "movie" otherwise (a
 *  series is always searched per season or episode). */
export function scopeTarget(season?: number, episodeCode?: string): string {
  if (episodeCode) return episodeCode.toLowerCase();
  if (season !== undefined) return `s${String(season).padStart(2, "0")}`;
  return "movie";
}

export function peersText(c: Pick<Candidate, "seeders" | "leechers">): string {
  const s = c.seeders ?? 0;
  if (s < 0) return "—";
  return c.leechers !== undefined && c.leechers !== null ? `${s} / ${c.leechers}` : String(s);
}

/** The episodes a release covers: "S02E05", or "S02E01–E12 (12)" for several. */
export function episodesText(eps: string[] | undefined): string {
  if (!eps?.length) return "—";
  if (eps.length === 1) return eps[0];
  // A pack of several seasons names its seasons: "Seasons 1 to 5" (a list when they are not in a row).
  const seasons = [...new Set(eps.map((e) => /^S(\d+)E\d+$/.exec(e)?.[1]).filter((n): n is string => n !== undefined).map(Number))].sort((a, b) => a - b);
  if (seasons.length > 1 && seasons.length <= eps.length) {
    const inRow = seasons.every((n, i) => i === 0 || n === seasons[i - 1] + 1);
    return inRow ? `Seasons ${seasons[0]} to ${seasons[seasons.length - 1]}` : `Seasons ${seasons.join(", ")}`;
  }
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
  const replaces = replaceWarning(c, late);
  if (!replaces) return base;
  return {
    ...base,
    title: "Replace a better-scored file",
    does: [...base.does, replaces],
    confirm: "Grab anyway",
  };
}

/** The warning that an accepted release replaces a better-scored file; undefined when it does not (or is rejected:
 *  late, the server's reasons at grab time). */
export function replaceWarning(c: Candidate, late?: string[]): string | undefined {
  if (!c.accepted || late?.length || !replacesBetter(c)) return undefined;
  return `It replaces the current file (${c.existing?.quality || "unknown quality"}, score ${c.existing?.score ?? 0}), which scores higher`;
}

/** The grab confirmation of a release on the library's blocklist (b): the blocklist question, then what else the
 *  grab overrides (a rejection, a better-scored file it replaces). */
export function blockedGrabScope(c: Candidate, b: BlockedRelease, late?: string[]): Scope {
  const s = unblockGrabScope(b, "Grab");
  const also = [rejectionWarning(c, late), replaceWarning(c, late)].filter((w): w is string => !!w);
  return { ...s, does: [...s.does, ...also] };
}

/** The reasons that say a release is of another title, not a worse copy of this one. */
const OTHER_TITLE = new Set(["UnknownMovie", "UnknownSeries", "WrongMovie", "WrongSeries", "MatchesAnotherSeries"]);

/** Whether a search found nothing of the title itself: results came back, none was accepted, and every release was
 *  rejected as another title (A Life Illuminated's 50 results, all other films). */
export function noneOfTitle(items: Candidate[], accepted: number): boolean {
  return items.length > 0 && accepted === 0 && items.every((c) => !c.accepted && (c.rejections ?? []).some((r) => OTHER_TITLE.has(r.reason ?? "")));
}

/** How many release rows the grid renders at first and adds with each Show more: hundreds at once froze the tab. */
export const RELEASE_PAGE = 50;
