// The Movies, Series and Anime tables' columns (docs/design/ui.md "Library views"): which columns each list offers and
// under which label, which are on by default, which the account may see, the grid sort a header asks for, and the
// words some cells read (release and series status, series type, minimum availability, language). Title is fixed and
// not in this list, nor is the managers' last column of row actions (title-table.tsx RowActions). The choice is kept per list and per user (lib/prefs.ts useColumnPrefs, key columns.<list>).
import type { ColumnDef } from "@/lib/prefs";
import { HELD_ON, HELD_ON_TIP } from "@/lib/held-on";

export type ListId = "movies" | "series" | "anime";

export type IndexColumnId =
  | "status"
  | "libraries"
  | "network"
  | "next"
  | "seasons"
  | "size"
  | "added"
  | "latest_file"
  | "previous"
  | "quality"
  | "profile"
  | "year"
  | "release"
  | "in_cinemas"
  | "digital"
  | "physical"
  | "title_status"
  | "series_type"
  | "runtime"
  | "certification"
  | "rating"
  | "genres"
  | "tags"
  | "language"
  | "min_availability"
  | "provider"
  | "folder";

type Def = {
  id: IndexColumnId;
  /** The label on Movies; absent: Movies does not offer it. */
  movie?: string;
  /** The label on Series and Anime; absent: they do not offer it. */
  series?: string;
  /** The label on Anime when it differs from Series': Anime mixes movies and series. */
  anime?: string;
  hint: string;
  /** The hint on Movies, or on Series, when it differs: Anime keeps hint, which speaks of both. */
  movieHint?: string;
  seriesHint?: string;
  /** The grid sort its header asks for. */
  sort?: string;
  /** File detail: not offered to an account without "see files". */
  seeFiles?: true;
  /** On by default on Movies (m), on Series and Anime (s). */
  on?: "m" | "s" | "ms";
};

const both = (label: string) => ({ movie: label, series: label });

const DEFS: Def[] = [
  { id: "status", ...both("Status"), hint: "The poster badge's word; a download in hand, its stage", on: "ms" },
  { id: "libraries", ...both("Libraries"), hint: "One pill per library: its quality, or what it lacks", seeFiles: true, on: "ms" },
  { id: "network", movie: "Studio", series: "Network", anime: "Network / studio", hint: "From the title's metadata", sort: "network", on: "s" },
  { id: "next", movie: "Next release", series: "Next airing", anime: "Next episode / release", hint: "The next episode or coming release", movieHint: "The coming cinema, digital or disc release", seriesHint: "The next episode's air date", sort: "next", on: "s" },
  { id: "seasons", series: "Seasons", hint: "Seasons with episodes, specials left out", on: "s" },
  { id: "size", ...both("Size"), hint: "The title's current files in total", sort: "size", seeFiles: true, on: "ms" },
  { id: "added", ...both("Added"), hint: "The day the title was added to Theatarr", sort: "created", on: "m" },
  { id: "latest_file", ...both("Latest file"), hint: "When the newest current file came", sort: "added", seeFiles: true },
  { id: "previous", series: "Previous airing", hint: "The latest aired episode's day" },
  { id: "quality", ...both("Quality"), hint: "Each library's current quality", sort: "quality", seeFiles: true },
  { id: "profile", ...both("Quality profile"), hint: "Each library's quality profile", seeFiles: true },
  { id: "year", ...both("Year"), hint: "The release year", sort: "year" },
  { id: "release", movie: "Release date", series: "First aired", anime: "Released / first aired", hint: "A movie's earliest cinema release or TMDb's date, whichever is earlier; a series' first air date", movieHint: "The earliest cinema release or TMDb's date, whichever is earlier", seriesHint: "The first episode's air date", sort: "release" },
  { id: "in_cinemas", movie: "In cinemas", hint: "The first cinema release" },
  { id: "digital", movie: "Digital release", hint: "The first digital release" },
  { id: "physical", movie: "Physical release", hint: "The first disc release" },
  { id: "title_status", movie: "Release status", series: "Series status", hint: "Announced, In cinemas, Released / Continuing, Ended, Cancelled, Upcoming", movieHint: "Announced, In cinemas or Released", seriesHint: "Continuing, Ended, Cancelled or Upcoming" },
  { id: "series_type", series: "Series type", hint: "Standard, Daily or Anime" },
  { id: "runtime", ...both("Runtime"), hint: "From TMDb", sort: "runtime" },
  { id: "certification", ...both("Certification"), hint: "The age rating", sort: "certification" },
  { id: "rating", ...both("Rating"), hint: "TMDb's rating", sort: "rating" },
  { id: "genres", ...both("Genres"), hint: "The first three" },
  { id: "tags", ...both("Tags"), hint: "Labels compatible clients set on the title's libraries", seeFiles: true },
  { id: "language", ...both("Original language"), hint: "From TMDb" },
  { id: "min_availability", movie: "Minimum availability", hint: "When a movie may be fetched; per library when they differ", seeFiles: true },
  { id: "provider", ...both(HELD_ON), hint: HELD_ON_TIP, seeFiles: true },
  { id: "folder", ...both("Folder"), hint: "The title's folder in each library", seeFiles: true },
];

/** The kind a list shows: Anime's Movies or Series segment (?kind=) shows one kind; undefined: both (All). */
export type ShownKind = "movie" | "series" | undefined;

const offered = (d: Def, list: ListId) => (list === "movies" ? d.movie : list === "anime" ? (d.series && (d.anime ?? d.series)) : d.series);
const hintOf = (d: Def, list: ListId) => (list === "movies" ? d.movieHint : list === "series" ? d.seriesHint : undefined) ?? d.hint;

/** A column's label where kind is shown: Anime's Movies segment has the Movies columns under the movie words
 *  ("Studio", In cinemas, Digital release), its Series segment the Series columns under the series words, All the mixed
 *  ones; the choice is kept under Anime's key (listColumns holds every column of both). */
function shownLabel(d: Def, list: ListId, kind: ShownKind): string | undefined {
  if (list !== "anime" || !kind) return offered(d, list);
  return kind === "movie" ? d.movie : d.series;
}
const shownHint = (d: Def, list: ListId, kind: ShownKind) =>
  (list === "anime" && kind ? (kind === "movie" ? d.movieHint : d.seriesHint) : undefined) ?? hintOf(d, list);

/** Every column a list has, in the default order (for the stored choice: lib/prefs.ts normalizeColumnPrefs). */
export function listColumns(list: ListId): ColumnDef<IndexColumnId>[] {
  // Anime also keeps the movie-only columns its Movies segment offers.
  const label = (d: Def) => offered(d, list) ?? (list === "anime" ? d.movie : undefined);
  return DEFS.filter((d) => label(d)).map((d) => ({ id: d.id, label: label(d)!, hint: hintOf(d, list) }));
}

/** The columns a list offers this account: without "see files" no file column, and Status is headed Availability.
 *  kind: the kind Anime shows (shownLabel). */
export function indexColumns(list: ListId, seeFiles: boolean, kind?: ShownKind): ColumnDef<IndexColumnId>[] {
  return DEFS.filter((d) => shownLabel(d, list, kind) && (seeFiles || !d.seeFiles)).map((d) => ({
    id: d.id,
    label: d.id === "status" && !seeFiles ? "Availability" : shownLabel(d, list, kind)!,
    hint: d.id === "status" && !seeFiles ? "Whether it can be watched" : shownHint(d, list, kind),
  }));
}

/** The columns a list shows before anything is chosen (the Title column carries the year). Movies: Status, Libraries,
 *  Size, Added. Series and
 *  Anime: Status, Libraries, Network, Next airing, Seasons, Size. */
export function defaultColumns(list: ListId): IndexColumnId[] {
  const k = list === "movies" ? "m" : "s";
  return DEFS.filter((d) => offered(d, list) && d.on?.includes(k)).map((d) => d.id);
}

/** The chosen columns this account sees, in order. */
export function visibleIndexColumns(p: { order: IndexColumnId[]; shown: IndexColumnId[] }, list: ListId, seeFiles: boolean, kind?: ShownKind): IndexColumnId[] {
  const ok = new Set(indexColumns(list, seeFiles, kind).map((c) => c.id));
  return p.order.filter((id) => p.shown.includes(id) && ok.has(id));
}

/** The grid sort a column's header asks for; undefined: the header does not sort. Title sorts by name. */
export function columnSort(id: IndexColumnId | "title"): string | undefined {
  return id === "title" ? "name" : DEFS.find((d) => d.id === id)?.sort;
}

/** The column whose header shows the grid's sort, if one does. */
export function sortColumn(sort: string): IndexColumnId | "title" | undefined {
  return sort === "name" ? "title" : DEFS.find((d) => d.sort === sort)?.id;
}

/** The order a sort starts in, as the server defaults: names, next dates, certification and network ascending, the rest
 *  descending. */
export function defaultDesc(sort: string): boolean {
  return !["name", "next", "certification", "network"].includes(sort);
}

// ---- Cell words ----

const SERIES_TYPE: Record<string, string> = { standard: "Standard", daily: "Daily", anime: "Anime" };

export function seriesTypeWords(t: string | undefined | null): string {
  return t ? (SERIES_TYPE[t] ?? t) : "";
}

const MIN_AVAIL: Record<string, string> = { tba: "TBA", announced: "Announced", inCinemas: "In cinemas", released: "Released" };

export function minAvailabilityWords(v: string | undefined | null): string {
  return v ? (MIN_AVAIL[v] ?? v) : "";
}

const LANGUAGES = typeof Intl.DisplayNames === "function" ? new Intl.DisplayNames(["en"], { type: "language" }) : undefined;

/** "Japanese" for "ja"; the code as given when the browser cannot name it. */
export function languageWords(code: string | undefined | null): string {
  if (!code) return "";
  try {
    return LANGUAGES?.of(code) ?? code;
  } catch {
    return code;
  }
}

/** One value per library: once when they are all the same, else "4K: … · WEB: …" (libraries without one left out). */
export function perLibrary(vs: readonly { label?: string; key?: string }[], value: (i: number) => string | undefined | null): string {
  const each = vs.map((v, i) => ({ label: v.label ?? v.key ?? "", value: value(i) || "" })).filter((x) => x.value);
  if (!each.length) return "";
  if (each.every((x) => x.value === each[0].value) && each.length === vs.length) return each[0].value;
  return each.map((x) => `${x.label}: ${x.value}`).join(" · ");
}
