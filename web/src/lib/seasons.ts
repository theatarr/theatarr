import { downloadReason, downloadSettings } from "@/lib/version-grab";
// The Episodes tab's logic (docs/design/ui.md, Title page / Episodes; components/title/seasons.tsx lays it out): an
// episode's availability in words for each copy or across copies, a season's compact completion summary, the All /
// Missing / Upgrade found filters, the anime numbering label, the read-only and search restrictions said once, and
// which layout a caller gets (the two-copy matrix on desktop, rows with a copy selector on phones, one merged
// availability column for accounts without "see files").
import type { Episode, EpisodeList } from "@/api/library";
import type { VersionControl } from "@/api/manage";
import { fmtNumber } from "./format";
import { availableIn } from "./upgrade";

export type Cell = Episode["versions"][string];

/** An episode's state in one copy, or across copies. */
export type Status = "available" | "upgrade" | "missing" | "not_aired" | "unmonitored" | "none";

/** The season filters; active is an episode with a download in hand for a copy in view (accounts that see files). */
export type EpisodeFilter = "all" | "active" | "missing" | "upgrade";

/** The words for a status. Accounts without "see files" never get "upgrade" (the API sends no upgrade flags). */
export function statusWords(s: Status, seeFiles: boolean): { label: string; tone: "positive" | "info" | "negative" | "neutral" } {
  switch (s) {
    case "available":
      return { label: "Available", tone: "positive" };
    case "upgrade":
      return { label: "Upgrade found", tone: "info" };
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

/** Whether an episode has aired: the server's flag (its air instant has passed; the day of air_date only when it has no instant), never worked out here. */
export function aired(e: Pick<Episode, "aired">): boolean {
  return !!e.aired;
}

/**
 * "4/5 held" for a copy in a season: of the episodes aired or held in that copy (total, the hero's "of N"), so an
 * episode held before it airs counts on both sides. Of all the season's episodes while none has aired or is held.
 */
export function heldOf(c: { have: number; total?: number }, season: { episodes: number }): string {
  return `${fmtNumber(c.have)}/${fmtNumber(c.total || season.episodes)} held`;
}

/** One copy's state for an episode. */
export function cellStatus(c: Cell | undefined, isAired: boolean, seeFiles: boolean): Status {
  if (!c) return "none";
  if (c.has_file) return seeFiles && c.upgrade_seen ? "upgrade" : "available";
  if (!isAired) return "not_aired";
  return c.monitored ? "missing" : "unmonitored";
}

/**
 * An episode across the given copies: watchability first (a file in any copy is Available, Upgrade found when one of
 * those files has a better release seen), then Not aired, Missing (some copy wants it) and Not monitored.
 */
export function mergedStatus(e: Episode, keys: string[], seeFiles: boolean): Status {
  const isAired = aired(e);
  const all = keys.map((k) => cellStatus(e.versions[k], isAired, seeFiles));
  for (const s of ["upgrade", "available", "not_aired", "missing", "unmonitored"] as const) {
    if (all.includes(s)) return s;
  }
  return "none";
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
 * merged (one availability column) they match the episode across copies: Missing only when no copy holds it.
 */
export function matchesFilter(e: Episode, f: EpisodeFilter, keys: string[], seeFiles: boolean, merged = false): boolean {
  if (f === "all") return true;
  if (f === "active") return keys.some((k) => !!e.versions[k]?.acquisition);
  const want: Status = f === "missing" ? "missing" : "upgrade";
  if (merged) return mergedStatus(e, keys, seeFiles) === want;
  const isAired = aired(e);
  return keys.some((k) => cellStatus(e.versions[k], isAired, seeFiles) === want);
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
 * copies as one, naming none: an episode is missing only when no copy holds it.
 */
export function seasonSummary(episodes: Episode[], copies: { key: string; label: string }[], seeFiles: boolean, merged = false): SeasonSummary {
  if (merged && copies.length > 1) {
    const one = { key: "*", label: "" };
    const flat = episodes.map((e) => {
      const s = mergedStatus(e, copies.map((c) => c.key), seeFiles);
      const cell = { state: "wanted", monitored: s !== "unmonitored", has_file: s === "available" || s === "upgrade", upgrade_seen: s === "upgrade" } as Cell;
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
    const states = keys.map((k) => cellStatus(e.versions[k], isAired, seeFiles));
    const held = states.some((s) => s === "available" || s === "upgrade");
    if (held) available++;
    if (isAired || held) total++;
    else notAired++;
    if (states.includes("upgrade")) upgrades++;
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
      const reason = kind === "edit" ? (!c.can_edit ? c.edit_reason : "") : (!c.can_search || c.grab_state?.will_grab === false) ? `${downloadReason(c)} — ${downloadSettings(c.grab_state).label}` : "";
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
  /** A table with a column per copy (desktop), or stacked rows (phones). */
  mode: "table" | "rows";
  /** The copies shown: every copy, the one picked in the selector, or one merged availability column. */
  columns: "each" | "selected" | "merged";
  /** Whether the copy selector shows. */
  selector: boolean;
};

/**
 * Which Episodes layout a caller gets. Accounts without "see files" get one availability column across copies
 * (copies are a household detail). Others get the copy matrix on desktop and, on a phone, rows for one copy at a
 * time with a selector when there are two or more.
 */
export function episodeLayout(o: { wide: boolean; copies: number; seeFiles: boolean }): Layout {
  const mode = o.wide ? "table" : "rows";
  if (!o.seeFiles) return { mode, columns: "merged", selector: false };
  if (o.wide || o.copies < 2) return { mode, columns: "each", selector: false };
  return { mode, columns: "selected", selector: true };
}

/** The copies a layout shows. */
export function copiesInView(l: Layout, versions: EpisodeList["versions"], selected: string | null): EpisodeList["versions"] {
  if (l.columns !== "selected") return versions;
  const v = versions.find((x) => x.key === selected);
  return v ? [v] : versions.slice(0, 1);
}
