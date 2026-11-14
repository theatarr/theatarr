import { downloadReason, downloadSettings } from "@/lib/version-grab";
// The Episodes tab's logic (docs/design/ui.md, Title page / Episodes; components/title/seasons.tsx lays it out): an
// episode's availability in words for each copy or across copies, a season's compact completion summary, the All /
// Missing / Upgrade found filters, the anime numbering label, the read-only and search restrictions said once, a
// season's badge per library, and which layout a caller gets (the library in view: All libraries as the matrix on
// desktop or one library with its chosen columns, rows for one library on phones, one merged availability column for
// accounts without "see files").
import type { Episode, EpisodeList } from "@/api/library";
import type { VersionControl } from "@/api/manage";
import { fmtNumber } from "./format";
import { availableIn } from "./upgrade";

export type Cell = Episode["versions"][string];

/**
 * An episode's status word in one copy, or across copies (docs/design/ui.md "Status words"): Available (a file is
 * held), Missing (aired, monitored, no file), Not aired, Not monitored (aired, no file, and the episode or its whole
 * library is not monitored: a gap there is not missing), or none (not in this library). Quality is said apart from it:
 * Upgrade found beside Available (cellUpgrade).
 */
export type Status = "available" | "missing" | "not_aired" | "unmonitored" | "none";

/** The season filters; active is an episode with a download in hand for a copy in view (accounts that see files). */
export type EpisodeFilter = "all" | "active" | "missing" | "upgrade";

/** The keys of the libraries that are not monitored (EpisodeList versions), whose gaps read Not monitored. For callers
 *  who see files only: everyone else keeps watchability (an aired episode no library holds is missing). */
export type OffCopies = ReadonlySet<string>;

export function offCopies(versions: readonly { key: string; monitored?: boolean }[], seeFiles: boolean): OffCopies {
  return new Set(seeFiles ? versions.filter((v) => v.monitored === false).map((v) => v.key) : []);
}

/** The words for a status. */
export function statusWords(s: Status, seeFiles: boolean): { label: string; tone: "positive" | "info" | "negative" | "neutral" } {
  switch (s) {
    case "available":
      return { label: "Available", tone: "positive" };
    case "missing":
      return { label: "Missing", tone: "negative" };
    case "not_aired":
      return { label: "Not aired", tone: "neutral" };
    case "unmonitored":
      // Monitoring is a manager's notion; to everyone else the episode is simply not there.
      return { label: seeFiles ? "Not monitored" : "Not available", tone: "neutral" };
    default:
      return { label: seeFiles ? "Not in this library" : "Not available", tone: "neutral" };
  }
}

/** The quality word beside an Available episode, for those who see files. */
export const UPGRADE_FOUND = "Upgrade found";

/** Whether an episode has aired: the server's flag (its air instant has passed; the day of air_date only when it has no instant), never worked out here. */
export function aired(e: Pick<Episode, "aired">): boolean {
  return !!e.aired;
}

/** One copy's state for an episode; off: the copy (library) is not monitored. */
export function cellStatus(c: Cell | undefined, isAired: boolean, off = false): Status {
  if (!c) return "none";
  if (c.has_file) return "available";
  if (!isAired) return "not_aired";
  return c.monitored && !off ? "missing" : "unmonitored";
}

/** Whether a copy's file of the episode has a better release found (those who see files). */
export function cellUpgrade(c: Cell | undefined, seeFiles: boolean): boolean {
  return seeFiles && !!c?.has_file && !!c.upgrade_seen;
}

/**
 * An episode across the given copies: watchability first (a file in any copy is Available), then Not aired, Missing
 * (some monitored copy wants it) and Not monitored.
 */
export function mergedStatus(e: Episode, keys: string[], off: OffCopies = new Set()): Status {
  const isAired = aired(e);
  const all = keys.map((k) => cellStatus(e.versions[k], isAired, off.has(k)));
  for (const s of ["available", "not_aired", "missing", "unmonitored"] as const) {
    if (all.includes(s)) return s;
  }
  return "none";
}

/** Whether some copy in view holds the episode with an upgrade found. */
export function mergedUpgrade(e: Episode, keys: string[], seeFiles: boolean): boolean {
  return keys.some((k) => cellUpgrade(e.versions[k], seeFiles));
}

/** "Available in 4K" for accounts without "see files": the best resolution any copy holds, else "Available". */
export function plainAvailable(e: Episode, keys: string[]): string {
  const order = ["2160", "1080", "720", "576", "480"];
  const rank = (q: string | undefined) => {
    const i = order.findIndex((r) => q?.includes(r));
    return i < 0 ? order.length : i;
  };
  let best: string | undefined;
  for (const k of keys) {
    const c = e.versions[k];
    if (c?.has_file && rank(c.quality) < rank(best)) best = c.quality;
  }
  return availableIn(best);
}

/**
 * Whether an episode belongs under a filter: Missing and Upgrade found match when any of the copies in view is. With
 * merged (one availability column) Missing matches the episode across copies: only when no copy holds it.
 */
export function matchesFilter(e: Episode, f: EpisodeFilter, keys: string[], seeFiles: boolean, merged = false, off: OffCopies = new Set()): boolean {
  if (f === "all") return true;
  if (f === "active") return keys.some((k) => !!e.versions[k]?.acquisition);
  if (f === "upgrade") return mergedUpgrade(e, keys, seeFiles);
  if (merged) return mergedStatus(e, keys, off) === "missing";
  const isAired = aired(e);
  return keys.some((k) => cellStatus(e.versions[k], isAired, off.has(k)) === "missing");
}

export type SeasonSummary = {
  /** Episodes aired or already held: what "of N" counts. */
  total: number;
  available: number;
  /** Episodes missing in any copy in view, and per copy (in the keys' order). */
  missing: number;
  missingBy: { key: string; label: string; n: number }[];
  upgrades: number;
  notAired: number;
  /** "8 of 10 available · 2 missing · 1 upgrade found". */
  text: string;
};

/**
 * A season's completion for the copies in view (one copy on a phone, every copy on the desktop matrix). With several
 * copies a missing count names its copy: "2 missing in 4K". merged (accounts without "see files") counts across
 * copies as one, naming none: an episode is missing only when no copy holds it. A copy that is not monitored has no
 * missing episodes (off).
 */
export function seasonSummary(episodes: Episode[], copies: { key: string; label: string; monitored?: boolean }[], seeFiles: boolean, merged = false): SeasonSummary {
  // Library monitoring is a manager's notion: without "see files" the counts stay watchability.
  const off = offCopies(copies, seeFiles);
  if (merged && copies.length > 1) {
    const one = { key: "*", label: "" };
    const flat = episodes.map((e) => {
      const keys = copies.map((c) => c.key);
      const s = mergedStatus(e, keys, off);
      const cell = { state: "wanted", monitored: s !== "unmonitored", has_file: s === "available", upgrade_seen: mergedUpgrade(e, keys, seeFiles) } as Cell;
      const versions: Record<string, Cell> = s === "none" ? {} : { "*": cell };
      return { ...e, versions };
    });
    return seasonSummary(flat, [one], seeFiles);
  }
  const keys = copies.map((c) => c.key);
  let total = 0;
  let available = 0;
  let upgrades = 0;
  let notAired = 0;
  let missing = 0;
  const by = new Map<string, number>();
  for (const e of episodes) {
    const isAired = aired(e);
    const states = keys.map((k) => cellStatus(e.versions[k], isAired, off.has(k)));
    const held = states.includes("available");
    if (held) available++;
    if (isAired || held) total++;
    else notAired++;
    if (mergedUpgrade(e, keys, seeFiles)) upgrades++;
    if (states.includes("missing")) missing++;
    keys.forEach((k, i) => {
      if (states[i] === "missing") by.set(k, (by.get(k) ?? 0) + 1);
    });
  }
  const missingBy = copies.map((c) => ({ key: c.key, label: c.label, n: by.get(c.key) ?? 0 })).filter((m) => m.n > 0);
  const parts: string[] = [];
  if (total === 0) {
    parts.push(episodes.length === 0 ? "No episodes" : `${fmtNumber(episodes.length)} episode${episodes.length === 1 ? "" : "s"}, none aired yet`);
  } else {
    parts.push(`${fmtNumber(available)} of ${fmtNumber(total)} available`);
    if (copies.length > 1) {
      for (const m of missingBy) parts.push(`${fmtNumber(m.n)} missing in ${m.label}`);
    } else if (missing) {
      parts.push(`${fmtNumber(missing)} missing`);
    }
    if (upgrades) parts.push(`${fmtNumber(upgrades)} upgrade${upgrades === 1 ? "" : "s"} found`);
    if (notAired) parts.push(`${fmtNumber(notAired)} not aired`);
  }
  return { total, available, missing, missingBy, upgrades, notAired, text: parts.join(" · ") };
}

/** "E01", and for anime with an absolute number "E01 · absolute 1". */
export function episodeNumber(e: Pick<Episode, "number" | "absolute">, anime: boolean): string {
  const n = `E${String(e.number).padStart(2, "0")}`;
  return anime && e.absolute ? `${n} · absolute ${e.absolute}` : n;
}

/** "<reason> — <where to change it>", unless the reason already names it (forced Pause names THEATARR_FORCE_OFF). */
const withFix = (reason: string, fix: string) => (reason.includes(fix) ? reason : `${reason} — ${fix}`);

/**
 * Read-only monitoring and disabled search, said once for the whole tab rather than on every season line: one line
 * per distinct reason, naming the copies it applies to when not all of them.
 */
export function restrictions(versions: { label: string; control?: VersionControl | null }[], opts: { globalSaid?: boolean } = {}): string[] {
  const out: string[] = [];
  // globalSaid: the page notice already says Pause is on (or forced) for every library.
  const global = (c: VersionControl) => !!opts.globalSaid && c.grab_state?.mode === "active" && (c.grab_state.pause_forced || c.grab_state.paused);
  const add = (kind: "edit" | "search") => {
    const groups = new Map<string, string[]>();
    for (const v of versions) {
      const c = v.control;
      if (!c || (kind === "search" && global(c))) continue;
      const reason = kind === "edit" ? (!c.can_edit ? c.edit_reason : "") : (!c.can_search || c.grab_state?.will_grab === false) ? withFix(downloadReason(c), downloadSettings(c.grab_state).label) : "";
      if (!reason) continue;
      groups.set(reason, [...(groups.get(reason) ?? []), v.label]);
    }
    for (const [reason, labels] of groups) {
      const who = labels.length < versions.length ? ` (${labels.join(", ")})` : "";
      out.push(kind === "edit" ? `Monitoring is read-only${who}: ${reason}` : `${labels.join(", ")}: ${reason}`);
    }
  };
  add("edit");
  add("search");
  return out;
}

export type Layout = {
  /** A table (desktop), or stacked rows (phones). */
  mode: "table" | "rows";
  /** Every copy as a column (All libraries), the one library in view with its chosen columns, or one merged
   *  availability column. */
  columns: "each" | "selected" | "merged";
  /** Whether the library selector shows. */
  selector: boolean;
};

/** The library in view: "all" or a library key; null until the user picks (the default for the width). */
export type Picked = "all" | string | null;

/**
 * Which Episodes layout a caller gets. Accounts without "see files" get one availability column across copies
 * (copies are a household detail). Others see one library at a time, or all of them as the matrix: a selector with
 * two or more; All libraries is the default from the md width, phones have no All and show the first library.
 */
export function episodeLayout(o: { wide: boolean; copies: number; seeFiles: boolean; picked: Picked }): Layout {
  const mode = o.wide ? "table" : "rows";
  if (!o.seeFiles) return { mode, columns: "merged", selector: false };
  if (o.copies < 2) return { mode, columns: "selected", selector: false };
  const all = o.wide && (o.picked === null || o.picked === "all");
  return { mode, columns: all ? "each" : "selected", selector: true };
}

/** The copies a layout shows: every copy, or the picked one (the first when the pick is All or gone). */
export function copiesInView(l: Layout, versions: EpisodeList["versions"], picked: Picked): EpisodeList["versions"] {
  if (l.columns !== "selected") return versions;
  const v = versions.find((x) => x.key === picked);
  return v ? [v] : versions.slice(0, 1);
}

/**
 * A library's badge on a season header: "8 / 10" held of aired-or-held (the whole season while none has aired or is
 * held); bad when something is missing, ok when nothing is, neutral while there is nothing to hold yet.
 */
export function seasonBadge(c: { have: number; missing: number; total?: number } | undefined, season: { episodes: number; aired?: number }): { text: string; tone: "ok" | "bad" | "neutral" } {
  const have = c?.have ?? 0;
  // A season with nothing aired and nothing held says so, rather than "0 / 1".
  if (season.aired === 0 && have === 0) return { text: "Not aired", tone: "neutral" };
  const total = c?.total || season.episodes;
  const tone = (c?.missing ?? 0) > 0 ? "bad" : have > 0 ? "ok" : "neutral";
  return { text: `${fmtNumber(have)} / ${fmtNumber(total)}`, tone };
}
