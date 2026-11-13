// The title page's Libraries table (docs/design/ui.md, Title page): one row per library the title is in, with columns
// the user chooses (lib/prefs.ts). This file holds the column list, the status words of a row and the small readers
// the table needs, kept out of the component so they are tested. Status words are the poster badge words
// (lib/library.ts, lib/upgrade.ts): Missing, Upcoming, Available, Upgrade found, Best quality known.
import type { Schemas } from "@/api/client";
import type { Version } from "@/api/library";
import { stageWords } from "./copies";
import { fmtAgo } from "./format";
import { fmtDay, upcomingRelease } from "./library";
import { copyTone, copyWords, TONE, type ToneTitle } from "./library-status";
import type { ColumnDef } from "./prefs";
import { upgradeBadge, type Tone } from "./upgrade";

type Acquisition = Schemas["Acquisition"];
type VersionBrief = Schemas["VersionBrief"];

export type LibraryColumn =
  | "status"
  | "quality"
  | "score"
  | "size"
  | "cached_on"
  | "file"
  | "preferred"
  | "minimum_availability"
  | "release"
  | "release_group"
  | "languages"
  | "media"
  | "formats"
  | "added"
  | "last_search"
  | "next_search"
  | "path";

type Kind = "movie" | "series";

/** Every optional column, in the default order, with the kinds it is offered for. */
export const LIBRARY_COLUMNS: readonly (ColumnDef<LibraryColumn> & { kinds: readonly Kind[] })[] = [
  { id: "status", label: "Status", hint: "Missing, downloading, or what the library holds", kinds: ["movie", "series"] },
  { id: "quality", label: "Quality", hint: "Quality", kinds: ["movie", "series"] },
  { id: "score", label: "Score", hint: "Score", kinds: ["movie"] },
  { id: "size", label: "Size", hint: "Size", kinds: ["movie", "series"] },
  { id: "cached_on", label: "Cached on", hint: "Debrid providers that hold the file now", kinds: ["movie", "series"] },
  { id: "file", label: "File", hint: "File", kinds: ["movie"] },
  { id: "preferred", label: "Preferred quality", hint: "The profile's cutoff: upgrades stop there", kinds: ["movie", "series"] },
  { id: "minimum_availability", label: "Minimum availability", hint: "Minimum availability", kinds: ["movie"] },
  { id: "release", label: "Release", hint: "Release", kinds: ["movie"] },
  { id: "release_group", label: "Release group", hint: "Release group", kinds: ["movie"] },
  { id: "languages", label: "Languages", hint: "Languages", kinds: ["movie"] },
  { id: "media", label: "Video / audio", hint: "Video / audio", kinds: ["movie"] },
  { id: "formats", label: "Formats", hint: "Formats", kinds: ["movie"] },
  { id: "added", label: "Added", hint: "Added", kinds: ["movie", "series"] },
  { id: "last_search", label: "Last search", hint: "Last search", kinds: ["movie", "series"] },
  { id: "next_search", label: "Next search", hint: "When Theatarr searches this again", kinds: ["movie"] },
  { id: "path", label: "Path", hint: "Path", kinds: ["movie"] },
];

/** The columns shown until the user chooses. */
export const DEFAULT_LIBRARY_COLUMNS: readonly LibraryColumn[] = ["status", "quality", "score", "size", "cached_on", "file"];

/** The columns offered for a title: its kind's, and Next search only when a row carries the server's time. */
export function libraryColumns(kind: Kind, versions: readonly Version[]): ColumnDef<LibraryColumn>[] {
  const next = versions.some((v) => !!nextSearchOf(v));
  return LIBRARY_COLUMNS.filter((c) => c.kinds.includes(kind) && (c.id !== "next_search" || next));
}

/** The libraries the title is in now: a library it was removed from has no row. */
export function currentLibraries<T extends Pick<Version, "removed">>(versions: readonly T[]): T[] {
  return versions.filter((v) => !v.removed);
}

/** A row's status: the words and tone, a download stage to link to Activity, the upgrade badge (with Why?) for a held
 *  movie, and a second line. */
export type LibraryStatus = {
  label: string;
  tone: Tone;
  /** The download in hand: the words link to Activity. */
  stage?: Acquisition;
  /** A held movie: the upgrade badge with Why? stands for the words. */
  upgrade?: boolean;
  /** A second, smaller line: "6 missing", "Upgrade downloading · 37%". */
  sub?: string;
};

/** A title-page library as the list rows carry it (VersionBrief), so the index's rules (lib/library-status.ts) read
 *  it: the episode counts become its coverage (missing is the same aired, monitored, not held count). */
export function versionBrief(v: Version): VersionBrief {
  const e = v.episodes;
  return {
    key: v.key, label: v.label, state: v.state, monitored: v.monitored, has_file: v.has_file, acquisition: v.acquisition,
    coverage: e ? { have: e.have, total: e.total, aired: e.total, upcoming: 0, missing: e.missing } : undefined,
  };
}

/**
 * The status of a library row, in the index's colours (copyTone) for every row. A series: the index's words
 * (copyWords: "20 of 24 episodes linked", "nothing aired yet" or "no episode information yet"), with "6 missing" under
 * them. A movie without a file: the stage words ("Downloading · 37%"), else "Not monitored", else "Upcoming · Jan 14"
 * when not out at home yet, else "Missing" (the poster badge words). A held movie: the upgrade badge ("Available",
 * "Upgrade found", "Best quality known"), with "Upgrade downloading · 37%" under it when one is in hand.
 */
export function libraryStatus(v: Version, t: ToneTitle, day?: string): LibraryStatus {
  const a = v.acquisition;
  const b = versionBrief(v);
  const tone = TONE[copyTone(b, t, day)];
  if (t.kind === "series") {
    const words = copyWords(b, t, day);
    const missing = v.episodes?.missing ?? 0;
    return { label: words.charAt(0).toUpperCase() + words.slice(1), tone, sub: missing > 0 ? `${missing} missing` : undefined };
  }
  if (!v.has_file) {
    if (a) return { label: stageWords(a), tone, stage: a };
    if (!v.monitored) return { label: "Not monitored", tone };
    const upcoming = upcomingRelease(t, day);
    if (upcoming) return { label: `Upcoming · ${fmtDay(upcoming)}`, tone };
    return { label: "Missing", tone };
  }
  const badge = upgradeBadge(v.upgrade);
  const sub = a ? stageWords({ ...a, upgrade: true }) : undefined;
  if (!badge) return { label: "Available", tone, sub };
  return { label: badge.label, tone: badge.tone, upgrade: true, sub };
}

/** A rename's two sides as shown: the file names, or, when only folders change (equal names), each path from the first
 *  folder that differs, so the change is visible. */
export function renameSides(from: string, to: string): [string, string] {
  const a = from.split("/");
  const b = to.split("/");
  if (a[a.length - 1] !== b[b.length - 1]) return [a[a.length - 1], b[b.length - 1]];
  let i = 0;
  while (i < a.length - 1 && i < b.length - 1 && a[i] === b[i]) i++;
  return [a.slice(i).join("/"), b.slice(i).join("/")];
}

/** The file name of a path (the last segment). */
export function fileName(path: string | undefined | null): string {
  if (!path) return "";
  return path.split("/").filter(Boolean).pop() ?? "";
}

/** When the library was last searched: the newest search in its history. */
export function lastSearch(v: Pick<Version, "history">): string | undefined {
  let newest: string | undefined;
  for (const h of v.history ?? []) if (h.event === "searched" && h.date && (!newest || h.date > newest)) newest = h.date;
  return newest;
}

/** The next automatic search (phase E): on the copy's control object, read defensively since older servers do not
 *  send it. */
type NextSearchFields = { next_search_at?: unknown; next_search_why?: unknown };

export function nextSearchOf(v: Pick<Version, "control">): { at: string; why?: string } | undefined {
  const c = v.control as NextSearchFields | undefined;
  if (!c || typeof c.next_search_at !== "string" || !c.next_search_at) return undefined;
  return { at: c.next_search_at, why: typeof c.next_search_why === "string" && c.next_search_why ? c.next_search_why : undefined };
}

const WHY: Record<string, string> = { new: "new release", asked: "Search now" };

/** Why a search is early, in the words the title page and Wanted share: "new release", "Search now", "after a failed
 *  download"; "" on the regular schedule. */
export function nextSearchWhy(why: string | undefined | null): string {
  return why ? WHY[why] ?? "after a failed download" : "";
}

/** "now" when due, else "in 3 h", with why it is early: "(new release)", "(Search now)", "(after a failed download)". */
export function nextSearch(x: { at: string; why?: string } | undefined, now: Date = new Date()): string {
  if (!x) return "";
  const t = new Date(x.at).getTime();
  if (Number.isNaN(t)) return "";
  const when = t <= now.getTime() ? "now" : fmtAgo(x.at, now);
  const why = nextSearchWhy(x.why);
  return why ? `${when} (${why})` : when;
}
