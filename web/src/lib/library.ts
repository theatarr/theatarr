// Library helpers shared by Home, the grids and the title page: kinds, badges, version states in words, and the
// grid's URL parameters.
import type { Availability, GridQuery, TitleRow } from "@/api/library";
import { fmtDay } from "@/lib/format";
import { availableIn, upgradeBadge, type Tone } from "@/lib/upgrade";

/**
 * A title's status word (docs/design/ui.md "Status words"): the poster badge, the table's Status, the title page's
 * status and the filters say the same word for the same state. For those who see files it is the server's
 * availability as it is (one rule, internal/web titleAvailabilitySQL): Available (a file is held), Partly available (a
 * monitored library still lacks one), Missing, Not monitored, Upcoming. Without "see files" it is watchability:
 * Available, Some episodes, Not available, Getting ready, Upcoming. Quality is said apart from it (qualityWord).
 */
export type PosterStatus = "available" | "partly_available" | "wanted" | "unmonitored" | "upcoming" | "some_episodes" | "getting_ready" | "not_available";

export const POSTER_STATUS_LABEL: Record<PosterStatus, string> = {
  available: "Available",
  partly_available: "Partly available",
  wanted: "Missing",
  unmonitored: "Not monitored",
  upcoming: "Upcoming",
  some_episodes: "Some episodes",
  getting_ready: "Getting ready",
  not_available: "Not available",
};

/** Today as YYYY-MM-DD in local time (release dates are calendar days). */
export function today(now: Date = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}

/** Today as YYYY-MM-DD in UTC: the day the server decides "released" by (Home's Missing row, fetching). */
export function utcToday(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

/**
 * The day a movie is out at home when that is still to come (docs/design/ui.md "Library and activity pages"), else
 * undefined: the row's home_release (the earlier of digital and disc, else cinema plus 90 days, the day Theatarr may
 * fetch it) when it is strictly after today; a row without one falls back to its TMDb release date. Such a movie reads
 * Upcoming instead of Missing, as Radarr's "not available" does; it is still wanted, only the word changes. Today is
 * the UTC day, as the server's, so a movie is never Upcoming on a poster and in Home's Missing row at once.
 */
export function upcomingRelease(t: Pick<TitleRow, "kind" | "release_date" | "home_release"> | undefined, day: string = utcToday()): string | undefined {
  if (!t || t.kind !== "movie") return undefined;
  const d = (t.home_release ?? t.release_date)?.slice(0, 10);
  return d && d > day ? d : undefined;
}

export type CardKind = "movie" | "series" | "anime";

export function kindOf(t: { anime?: boolean; kind?: string }): CardKind {
  return t.anime ? "anime" : t.kind === "series" ? "series" : "movie";
}

/** TMDb statuses of a series that has aired, read only when the row has no first air date (a series still to premiere
 *  can already be called Returning Series). */
const AIRED_STATUS = new Set(["Returning Series", "Ended", "Canceled"]);

/**
 * A series none of whose episodes has aired yet: it reads Upcoming, as the bars and pills read "nothing aired yet"
 * (lib/library-status.ts copyTone), never Missing. Empty coverage alone is not proof (the API also sends it before the
 * episodes are loaded), so the row must not say it has aired: its first air date (release_date) is still to come (the
 * UTC day, as upcomingRelease); without that date, its status is not Returning Series, Ended or Canceled. Undefined
 * coverage is not "nothing aired".
 */
export function notAiredYet(t: Pick<TitleRow, "kind" | "versions" | "release_date" | "status"> | undefined, day: string = utcToday()): boolean {
  const vs = t?.versions ?? [];
  if (t?.kind !== "series" || !vs.length || !vs.every((v) => v.coverage !== undefined && v.coverage.total === 0)) return false;
  const first = t.release_date?.slice(0, 10);
  return first ? first > day : !AIRED_STATUS.has(t.status ?? "");
}

/**
 * The status word of a title (PosterStatus). Those who see files get the server's availability word for word. plain
 * (accounts without "see files"): a title with nothing to watch reads Getting ready while a download is in hand, else
 * Upcoming for a movie not out at home yet (upcomingRelease) or a series with nothing aired yet (notAiredYet), else Not
 * available; a series with some episodes reads Some episodes; the rest Available.
 */
export function posterStatus(a: Availability | undefined, plain = false, title?: Pick<TitleRow, "kind" | "versions" | "release_date" | "home_release" | "acquiring" | "status">): PosterStatus | undefined {
  if (plain) {
    if (a === "missing") return title?.acquiring ? "getting_ready" : upcomingRelease(title) || notAiredYet(title) ? "upcoming" : "not_available";
    if (a === "partial") return title?.kind === "movie" ? "available" : "some_episodes";
    return a === "available" ? "available" : undefined;
  }
  switch (a) {
    case "available":
      return "available";
    case "partial":
      return "partly_available";
    case "missing":
      return "wanted";
    case "unmonitored":
      return "unmonitored";
    case "upcoming":
      return "upcoming";
  }
  return undefined;
}

/**
 * The quality word beside a held title's status (those who see files): Upgrade found once a better release was found,
 * Best quality known while a file is below its preferred quality with nothing better found, Preferred quality met when
 * every file is at it (upgradeBadge, the word the title page's libraries, season headers and episodes use). Undefined
 * without a file.
 */
export function qualityWord(t: Pick<TitleRow, "availability" | "upgrade_seen" | "upgrade_state">): { label: string; tone: Tone; found: boolean } | undefined {
  if (t.availability !== "available" && t.availability !== "partial") return undefined;
  const found = !!t.upgrade_seen || t.upgrade_state === "upgrade_available";
  const b = upgradeBadge({ state: found ? "upgrade_available" : t.upgrade_state || "cutoff_met" });
  return b ? { label: b.label, tone: b.tone, found } : undefined;
}

/** A family account: no file detail and no request queue. Home shows it requests, what is available and browsing. */
export function isFamily(p: { seeFiles: boolean; manageRequests: boolean }): boolean {
  return !p.seeFiles && !p.manageRequests;
}

/**
 * A title's watchability in words for accounts without "see files" (the title page's one status pill): "Available in
 * 4K" (the best resolution any copy has), "Some episodes available", "Not available"; null without versions. The API
 * sends these accounts availability as watchability, so a movie missing one of its copies is still available.
 */
export function watchWords(a: Availability | undefined, qualities: readonly (string | undefined | null)[]): { label: string; tone: "positive" | "partial" | "neutral" } | null {
  switch (a) {
    case "available": {
      const best = qualities
        .map((q) => Number(q?.match(/(2160|1080|720|576|480)p?/i)?.[1] ?? 0))
        .reduce((m, n) => Math.max(m, n), 0);
      return { label: availableIn(best ? `${best}p` : undefined), tone: "positive" };
    }
    case "partial":
      return { label: "Some episodes available", tone: "partial" };
    case "missing":
      return { label: "Not available", tone: "neutral" };
  }
  return null;
}

/** A library title's one watchability line for accounts without "see files" (the table's cell, search results): a
 * missing title with a download in hand reads Getting ready, before Upcoming (a movie not out at home, with its day; a
 * series with nothing aired yet, with its first episode's day when known); else watchWords. */
export function titleWatchWords(t: Pick<TitleRow, "kind" | "availability" | "acquiring" | "versions" | "release_date" | "home_release" | "next_up" | "status">): { label: string; tone: "positive" | "partial" | "neutral" } | null {
  const missing = t.availability === "missing";
  const up = missing && !t.acquiring ? upcomingRelease(t) : undefined;
  if (missing && t.acquiring) return { label: "Getting ready", tone: "neutral" };
  if (up) return { label: `Upcoming · ${fmtDay(up)}`, tone: "neutral" };
  if (missing && notAiredYet(t)) return { label: t.next_up?.date ? `Upcoming · ${fmtDay(t.next_up.date)}` : "Upcoming", tone: "neutral" };
  return watchWords(t.availability, (t.versions ?? []).map((v) => v.quality));
}

/**
 * The status filters (availability=), each counting the titles whose status word it is. An old saved link's
 * availability=upgrade (the former "Preferred quality not met") reads as Available (gridQuery).
 */
export const AVAILABILITY: { id: Availability; label: string; hint: string }[] = [
  { id: "available", label: "Available", hint: "A file is held, and every monitored library has its files" },
  { id: "partial", label: "Partly available", hint: "A file is held; a monitored library still lacks one (a movie) or an aired episode (a series)" },
  { id: "missing", label: "Missing", hint: "Monitored, out, and no file" },
  { id: "unmonitored", label: "Not monitored", hint: "No file, and no library monitors it" },
  { id: "upcoming", label: "Upcoming", hint: "Monitored, no file, and not out yet" },
];

/** The quality filters (upgrade=), for those who see files: among titles with a file. */
export const UPGRADE_FILTERS: { id: "found" | "best_known"; label: string; hint: string }[] = [
  { id: "found", label: "Upgrade found", hint: "A better release than a held file has been found" },
  { id: "best_known", label: "Best quality known", hint: "A file is below its preferred quality and nothing better has been found" },
];

/** Without "see files" the filters are watchability, as the server applies them (perms.go plainAvailability). */
const PLAIN_AVAILABILITY: { id: Availability; label: string; hint: string }[] = [
  { id: "missing", label: "Not available", hint: "Nothing to watch yet" },
  { id: "partial", label: "Some episodes", hint: "Series with some episodes available" },
  { id: "available", label: "Available", hint: "Ready to watch" },
];

/** The availability filters for a catalog. Without "see files" a movie is never "Some episodes" (it has none), so
 * the movie catalog leaves that one out. */
export function availabilityFilters(seeFiles: boolean, kind?: string): { id: Availability; label: string; hint: string }[] {
  if (seeFiles) return AVAILABILITY;
  return kind === "movie" ? PLAIN_AVAILABILITY.filter((a) => a.id !== "partial") : PLAIN_AVAILABILITY;
}

export function fmtRuntime(min: number | undefined | null): string | undefined {
  if (!min) return undefined;
  const h = Math.floor(min / 60);
  const m = min % 60;
  return h > 0 ? `${h} h ${m ? `${m} min` : ""}`.trim() : `${m} min`;
}

// fmtDay lives in format.ts with the other formatters; re-exported for the files that import it from here.
export { fmtDay };

/**
 * "today", "tomorrow", "in 4 days" or "3 days ago", against the local day. A YYYY-MM-DD is a calendar day as written;
 * a full timestamp (2026-11-10T06:03:00Z) is a moment, so its day is the one it falls on in this browser's time zone.
 */
export function relDay(d: string | undefined | null, now: Date = new Date()): string {
  if (!d) return "";
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(d);
  if (!m) return "";
  const at = d.length > 10 ? new Date(d) : null;
  const day = at && !Number.isNaN(at.getTime())
    ? Date.UTC(at.getFullYear(), at.getMonth(), at.getDate())
    : Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  const today = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate());
  const n = Math.round((day - today) / 86_400_000);
  if (n === 0) return "today";
  if (n === 1) return "tomorrow";
  if (n === -1) return "yesterday";
  return n > 0 ? `in ${n} days` : `${-n} days ago`;
}

/** The three grids: which titles each shows. Anime is its own grid; Movies and Series leave anime out. */
export const CATALOGS = {
  movies: { title: "Movies", path: "/movies", kind: "movie", anime: false },
  series: { title: "Series", path: "/series", kind: "series", anime: false },
  anime: { title: "Anime", path: "/anime", kind: undefined, anime: true },
} as const;
export type CatalogId = keyof typeof CATALOGS;

/** The Sort menu. Added is the day the title was added (created); Latest file is the newest current file (added). */
export const SORTS = [
  { id: "name", label: "Title" },
  { id: "created", label: "Added" },
  { id: "added", label: "Latest file", seeFiles: true },
  { id: "release", label: "Release date" },
  { id: "year", label: "Year" },
  { id: "rating", label: "Rating" },
  { id: "runtime", label: "Runtime" },
  { id: "size", label: "Size", seeFiles: true },
  { id: "completion", label: "Completion" },
  { id: "next", label: "Next episode / release" },
  { id: "quality", label: "Quality", seeFiles: true },
  { id: "activity", label: "Latest activity", seeFiles: true },
  { id: "network", label: "Network / studio" },
  { id: "certification", label: "Certification" },
] as const;

/** The filter keys the URL carries (the grid's own; kind and anime come from the catalog). */
export const FILTER_KEYS = ["q", "with", "without", "state", "availability", "upgrade", "genre", "quality", "provider", "year_from", "year_to", "monitored"] as const;

/** The sorts the Sort menu offers this account. */
export function offeredSorts(seeFiles: boolean) {
  return SORTS.filter((s) => !("seeFiles" in s) || seeFiles);
}

/** The grid's query from the address. A sort the account is not offered (an old link, Latest file without "see files")
 *  falls back to the default, Title, with its library and order dropped, so the request and the menu agree. The
 *  account is signed in before a list opens, so seeFiles is known. */
export function gridQuery(c: CatalogId, p: URLSearchParams, seeFiles: boolean): GridQuery {
  const cat = CATALOGS[c];
  const asked = p.get("sort") || "name";
  const sortOK = offeredSorts(seeFiles).some((s) => s.id === asked);
  const num = (k: string) => {
    const v = Number(p.get(k));
    return Number.isFinite(v) && v > 0 ? v : undefined;
  };
  const str = (k: string) => p.get(k) || undefined;
  const mon = p.get("monitored");
  return {
    kind: cat.kind,
    anime: cat.anime,
    q: str("q"),
    with: str("with"),
    without: str("without"),
    state: str("state"),
    // The former "Preferred quality not met" (availability=upgrade) held titles with a file: Available now.
    availability: p.get("availability") === "upgrade" ? "available" : str("availability"),
    upgrade: seeFiles ? str("upgrade") : undefined,
    genre: str("genre"),
    quality: str("quality"),
    provider: str("provider"),
    year_from: num("year_from"),
    year_to: num("year_to"),
    monitored: mon === "true" ? true : mon === "false" ? false : undefined,
    sort: sortOK ? asked : "name",
    sort_version: sortOK ? str("sort_version") : undefined,
    order: sortOK ? str("order") : undefined,
  };
}

export function activeFilters(p: URLSearchParams): number {
  return FILTER_KEYS.filter((k) => p.get(k)).length;
}

/** One active filter as a removable chip: its words and the URL value left once it is removed (undefined: the key goes). */
export type FilterChip = { key: string; label: string; next?: string };

/**
 * The active filters as chips (the line under the toolbar): "Title: “dune”", "Monitored", the availability label,
 * "Has 4K", "Lacks 4K", "Quality: WEBDL-2160p", "Provider: RD", "2019–2024" (or "From 2019", "Up to 2024"), one chip
 * per genre, "State: wanted" for an old link that carries state. names: library labels by key; quality and provider
 * word their values as the filter sheet does.
 */
export function filterChips(
  p: URLSearchParams,
  o: { seeFiles: boolean; kind?: string; names?: ReadonlyMap<string, string>; quality?: (v: string) => string; provider?: (v: string) => string },
): FilterChip[] {
  const out: FilterChip[] = [];
  const get = (k: string) => p.get(k) || undefined;
  const q = get("q");
  if (q) out.push({ key: "q", label: `Title: “${q}”` });
  const mon = get("monitored");
  if (mon === "true") out.push({ key: "monitored", label: "Monitored" });
  if (mon === "false") out.push({ key: "monitored", label: "Not monitored" });
  const av = get("availability");
  if (av) out.push({ key: "availability", label: availabilityFilters(o.seeFiles, o.kind).find((a) => a.id === (av === "upgrade" ? "available" : av))?.label ?? AVAILABILITY.find((a) => a.id === av)?.label ?? av });
  const up = o.seeFiles && get("upgrade");
  if (up) out.push({ key: "upgrade", label: UPGRADE_FILTERS.find((u) => u.id === up)?.label ?? up });
  // Libraries, providers and copy states are file detail: no chip for them without "see files".
  const files = o.seeFiles;
  const lib = (k: string) => o.names?.get(k) ?? k;
  const w = files && get("with");
  if (w) out.push({ key: "with", label: `Has ${lib(w)}` });
  const wo = files && get("without");
  if (wo) out.push({ key: "without", label: `Lacks ${lib(wo)}` });
  const ql = get("quality");
  if (ql) out.push({ key: "quality", label: `Quality: ${o.quality?.(ql) || ql}` });
  const pr = files && get("provider");
  if (pr) out.push({ key: "provider", label: `Provider: ${o.provider?.(pr) ?? pr}` });
  const from = get("year_from");
  const to = get("year_to");
  if (from && to) out.push({ key: "years", label: `${from}–${to}` });
  else if (from) out.push({ key: "year_from", label: `From ${from}` });
  else if (to) out.push({ key: "year_to", label: `Up to ${to}` });
  const genres = (get("genre") ?? "").split(",").filter(Boolean);
  for (const g of genres) {
    const rest = genres.filter((x) => x !== g).join(",");
    out.push({ key: "genre", label: g, next: rest || undefined });
  }
  const st = files && get("state");
  if (st) out.push({ key: "state", label: `State: ${st}` });
  return out;
}

/** The URL after removing a chip: its key set to what is left, or gone ("years" removes both years). */
export function withoutChip(p: URLSearchParams, c: FilterChip): URLSearchParams {
  const n = new URLSearchParams(p);
  if (c.key === "years") {
    n.delete("year_from");
    n.delete("year_to");
  } else if (c.next) n.set(c.key, c.next);
  else n.delete(c.key);
  return n;
}
