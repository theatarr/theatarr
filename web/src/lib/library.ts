// Library helpers shared by Home, the grids and the title page: kinds, badges, version states in words, and the
// grid's URL parameters.
import type { Availability, GridQuery, TitleRow } from "@/api/library";
import { availableIn } from "@/lib/upgrade";

/** A poster card's availability badge. */
export type PosterStatus = "available" | "wanted" | "upgrade" | "some_episodes" | "upcoming" | "getting_ready" | "not_available";

export const POSTER_STATUS_LABEL: Record<PosterStatus, string> = {
  available: "Available",
  wanted: "Missing",
  upgrade: "Upgrade waiting",
  some_episodes: "Some episodes",
  upcoming: "Upcoming",
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

/** Missing takes priority (Getting ready without "see files" while a download is in hand, else Upcoming for a movie
 * not out at home yet, upcomingRelease; else Missing, which reads Not available without "see files" as in the table),
 * then episode gaps, then a wanted upgrade, then available. */
export function posterStatus(a: Availability | undefined, upgradeSeen?: boolean, plain = false, upgradeState?: string, title?: Pick<TitleRow, "kind" | "versions" | "release_date" | "home_release" | "next_up" | "acquiring">): PosterStatus | undefined {
  if (a === "missing") return plain && title?.acquiring ? "getting_ready" : upcomingRelease(title) ? "upcoming" : plain ? "not_available" : "wanted";
  if (plain) return a === "partial" && title?.kind !== "movie" ? "some_episodes" : a === "available" || a === "upgrade" || a === "partial" ? "available" : undefined;
  const episodes = title?.kind === "series" && title.versions?.some(v => (v.coverage?.missing ?? 0) > 0);
  if (episodes && title?.versions?.some(v => (v.coverage?.have ?? 0) > 0)) return "some_episodes";
  if (a === "partial") {
    return upgradeSeen || title?.versions?.some(v => v.state === "wanted" || v.state === "filled") ? "upgrade" : "available";
  }
  if (a === "upgrade") return plain || upgradeState === "best_available" || upgradeState === "best_source_seen" || upgradeState === "cutoff_met" ? "available" : "upgrade";
  if (a === "available") return !plain && (upgradeSeen || upgradeState === "higher_source_exists" || upgradeState === "upgrade_available" || upgradeState === "below_cutoff") ? "upgrade" : "available";
  return undefined;
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
    case "available":
    case "upgrade": {
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
 * missing title with a download in hand reads Getting ready, before Upcoming; else watchWords. */
export function titleWatchWords(t: Pick<TitleRow, "kind" | "availability" | "acquiring" | "versions" | "release_date" | "home_release">): { label: string; tone: "positive" | "partial" | "neutral" } | null {
  const missing = t.availability === "missing";
  const up = missing && !t.acquiring ? upcomingRelease(t) : undefined;
  if (missing && t.acquiring) return { label: "Getting ready", tone: "neutral" };
  if (up) return { label: `Upcoming · ${fmtDay(up)}`, tone: "neutral" };
  return watchWords(t.availability, (t.versions ?? []).map((v) => v.quality));
}

/**
 * The availability filters. The key "upgrade" stays (saved URLs); it reads "Preferred quality not met". Accounts
 * without "see files" do not get it (the API counts those titles as available).
 */
export const AVAILABILITY: { id: Availability; label: string; hint: string }[] = [
  { id: "missing", label: "Missing", hint: "No library has a file" },
  { id: "partial", label: "Partly", hint: "Some libraries have no file" },
  { id: "upgrade", label: "Preferred quality not met", hint: "Every library has a file; some file is below its preferred quality (including best available ones)" },
  { id: "available", label: "Available", hint: "Every library has a file at its preferred quality" },
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

/**
 * A version's state in plain words. plain (accounts without "see files") leaves the preferred quality out: filled is
 * "Available" (the API sends them cutoff_met as filled).
 */
export function stateWords(state: string | undefined, hasFile?: boolean, plain = false): { label: string; tone: "positive" | "warning" | "negative" | "neutral" } {
  switch (state) {
    case "cutoff_met":
      return { label: "Available", tone: "positive" };
    case "filled":
      return plain ? { label: "Available", tone: "positive" } : { label: "Available · preferred quality not met", tone: "warning" };
    case "wanted":
      return hasFile ? { label: "Partly available", tone: "warning" } : { label: "Missing", tone: "negative" };
  }
  return { label: state ?? "—", tone: "neutral" };
}

export function fmtRuntime(min: number | undefined | null): string | undefined {
  if (!min) return undefined;
  const h = Math.floor(min / 60);
  const m = min % 60;
  return h > 0 ? `${h} h ${m ? `${m} min` : ""}`.trim() : `${m} min`;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "Oct 27" for a YYYY-MM-DD this year, "Oct 27, 2024" otherwise. */
export function fmtDay(d: string | undefined | null, now: Date = new Date()): string {
  if (!d) return "—";
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(d);
  if (!m) return d;
  const y = Number(m[1]);
  const s = `${MONTHS[Number(m[2]) - 1]} ${Number(m[3])}`;
  return y === now.getFullYear() ? s : `${s}, ${y}`;
}

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

export const SORTS = [
  { id: "name", label: "Title" },
  { id: "added", label: "Date added" },
  { id: "release", label: "Release date" },
  { id: "year", label: "Year" },
  { id: "rating", label: "Rating" },
  { id: "size", label: "Size", seeFiles: true },
  { id: "completion", label: "Completion" },
  { id: "next", label: "Next episode / release" },
  { id: "quality", label: "Quality", seeFiles: true },
  { id: "activity", label: "Latest activity", seeFiles: true },
] as const;

/** The filter keys the URL carries (the grid's own; kind and anime come from the catalog). */
export const FILTER_KEYS = ["q", "with", "without", "state", "availability", "genre", "quality", "provider", "year_from", "year_to", "monitored"] as const;

export function gridQuery(c: CatalogId, p: URLSearchParams): GridQuery {
  const cat = CATALOGS[c];
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
    availability: str("availability"),
    genre: str("genre"),
    quality: str("quality"),
    provider: str("provider"),
    year_from: num("year_from"),
    year_to: num("year_to"),
    monitored: mon === "true" ? true : mon === "false" ? false : undefined,
    sort: str("sort") ?? "name",
    sort_version: str("sort_version"),
    order: str("order"),
  };
}

export function activeFilters(p: URLSearchParams): number {
  return FILTER_KEYS.filter((k) => p.get(k)).length;
}
