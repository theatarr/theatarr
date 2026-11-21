// The title page's "what happened and what next" lines (docs/design/ui.md, Title page): the latest relevant event of
// each library (imported, or a failed download with the providers it missed on), why a library or an episode is
// missing (its last targeted search, or never, and whether Theatarr searches by itself), and the hero's word while a
// download is in hand and nothing is held. They read only what the title response carries for callers who see files
// (history, last_search, searching, auto_search, acquisition); accounts without it get none of them.
import type { Schemas } from "@/api/client";
import type { Version } from "@/api/library";
import { PAUSE_STOPS } from "@/lib/version-grab";
import { fmtNumber, localDay } from "./format";
import { fmtDay } from "./library";

type HistoryEvent = Schemas["HistoryEvent"];
type Acquisition = Schemas["Acquisition"];
type LastSearch = Schemas["LastSearch"];
type AutoSearch = Schemas["AutoSearch"];

/** "today", "yesterday" or "Nov 13" for a moment, in this browser's time zone. */
export function dayWords(iso: string, now: Date = new Date()): string {
  const day = localDay(iso);
  const today = localDay(now.toISOString());
  const y = new Date(now);
  y.setDate(y.getDate() - 1);
  if (day === today) return "today";
  if (day === localDay(y.toISOString())) return "yesterday";
  return fmtDay(day, now);
}

/** Why a download failed, in a few words: the server's short reason in lower case ("not cached on RD, AD and TB",
 *  "stalled"). */
export function failureWords(h: Pick<HistoryEvent, "reason">): string {
  const r = h.reason?.trim() ?? "";
  return r ? r.charAt(0).toLowerCase() + r.slice(1) : "";
}

export type LatestEvent = { event: "imported" | "failed"; date: string; words: string; tone: "positive" | "warning"; downloadId?: number };

/**
 * A library's latest relevant event: its newest import or failed download. "Imported today", "Upgraded today" for an
 * import that replaced a file ("Re-downloaded today" when it fetched the same release again: the server's redownload);
 * a failure of a download that was to replace a file held now (the server's upgrade flag, per movie or per
 * episode) "Last upgrade attempt Nov 13: not cached on RD, AD and TB", any other "Last download failed Nov 13: …".
 * Nothing when the library has neither (searches and grabs in flight are said elsewhere).
 */
export function latestEvent(v: Pick<Version, "history">, now: Date = new Date()): LatestEvent | undefined {
  let top: HistoryEvent | undefined;
  for (const h of v.history ?? []) {
    if (h.event !== "imported" && h.event !== "upgraded" && h.event !== "failed") continue;
    if (!top || h.date > top.date) top = h;
  }
  if (!top) return undefined;
  const day = dayWords(top.date, now);
  // An import that replaced a file (the server's upgraded) is an upgrade, as History's Upgraded row.
  if (top.event !== "failed") return { event: "imported", date: top.date, words: `${top.event === "upgraded" ? (top.redownload ? "Re-downloaded" : "Upgraded") : "Imported"} ${day}`, tone: "positive" };
  const why = failureWords(top);
  const what = top.upgrade ? "Last upgrade attempt" : "Last download failed";
  return { event: "failed", date: top.date, words: `${what} ${day}${why ? `: ${why}` : ""}`, tone: "warning", downloadId: top.download_id };
}

/**
 * The last targeted search by its state: "Never searched"; "Last search Nov 12 failed" (no indexer answered, or the
 * search errored) and "Last search Nov 12 was skipped" (paused, a download in flight) say no results; a
 * completed one "Last searched Nov 12: nothing found" or "…: 12 results, none accepted". Without counts (an episode
 * covered by a season's search) only when. scoped names what the run searched ("Last searched Season 2 Nov 12: …").
 */
export function lastSearchWords(l: LastSearch | undefined | null, now: Date = new Date(), scoped = false): string {
  if (!l) return "Never searched";
  const day = dayWords(l.at, now);
  // scoped (a series' Libraries row): one Search series writes a run per season, so the row names the one it reports.
  const what = scoped ? runTargetWords(l.target) : "";
  if (l.state === "failed") return `Last search${what ? ` of ${what}` : ""} ${day} failed`;
  if (l.state === "skipped") return `Last search${what ? ` of ${what}` : ""} ${day} was skipped`;
  if (l.state === "queued" || l.state === "searching") return `Searching since ${day}`;
  const searched = `Last searched${what ? ` ${what}` : ""} ${day}`;
  if (l.results == null) return searched;
  const accepted = l.accepted ?? 0;
  const found = l.results === 0 ? "nothing found" : `${fmtNumber(l.results)} ${l.results === 1 ? "result" : "results"}, ${accepted === 0 ? "none" : fmtNumber(accepted)} accepted`;
  return `${searched}: ${found}`;
}

/** What a run searched, in words: "Season 2", "Specials", "S02E05"; "" for a film's run or an unknown target. */
export function runTargetWords(target: string | undefined | null): string {
  const t = (target ?? "").toLowerCase();
  const season = /^s(\d+)$/.exec(t);
  if (season) return Number(season[1]) === 0 ? "Specials" : `Season ${Number(season[1])}`;
  return /^s\d+e\d+$/.test(t) ? t.toUpperCase() : "";
}

/** A search under way, in words: "Searching now", "Search queued"; "" for none. */
export function searchingWords(s: string | undefined | null): string {
  return s === "searching" ? "Searching now" : s === "queued" ? "Search queued" : "";
}

/**
 * Why a missing library (or episode) is missing and what comes next, in one line: the search under way, else the last
 * targeted search or "Not searched yet", then whether Theatarr searches for missing items by itself. With it off, RSS
 * still picks up new releases where the library's RSS read covers it, and the line says so; an item never searched
 * also says what to do ("Use Search now to request a search.") when the account can search (canSearch). Search now
 * sits beside the line (the caller's button).
 */
export function missingLine(f: { last_search?: LastSearch | null; searching?: string | null; rss?: boolean }, auto: AutoSearch | undefined | null, now: Date = new Date(), canSearch = false, opts: { scoped?: boolean } = {}): string {
  const never = !f.searching && !f.last_search;
  const first = searchingWords(f.searching) || (never ? "Not searched yet" : lastSearchWords(f.last_search, now, opts.scoped));
  if (!auto) return first;
  if (!auto.enabled) return `${first}. Paused: ${PAUSE_STOPS}.`;
  if (!auto.missing) {
    const ask = never && canSearch ? " Use Search now to request a search." : "";
    return `${first}. Automatic backlog searching is off${f.rss ? "; new releases are still picked up from RSS" : ""}.${ask}`;
  }
  return `${first}. Theatarr searches for it automatically.`;
}

/** The episode window's line for a library that does not monitor the title, in place of its download state. */
export function libraryOffWords(library: string): string {
  return `${library} is not monitored for this title: nothing is searched by itself. The switch keeps your choice.`;
}

/** The hero's word for an acquisition in hand: "Waiting to start", "Downloading · 37%", "Waiting for the mount" (the
 *  queue's and the row's word) or "Importing". */
export function heroStageWords(a: Pick<Acquisition, "stage" | "progress">): string {
  switch (a.stage) {
    case "queued":
    case "starting":
      return "Waiting to start";
    case "downloading":
      return a.progress !== undefined ? `Downloading · ${Math.round(Math.max(0, Math.min(a.progress, 100)))}%` : "Downloading";
    case "blocked":
      return "Blocked";
    case "waiting_mount":
      return "Waiting for the mount";
    default:
      return "Importing";
  }
}

const STAGE_ORDER: Acquisition["stage"][] = ["queued", "starting", "downloading", "complete", "waiting_mount", "linking"];

/**
 * What the libraries without a file are getting, for the hero: the most advanced download in hand (not blocked)
 * among them, "Searching" / "Search queued" when the server says a search of such a library is under way, else
 * undefined. A held library stays Available during an upgrade (its download is not counted); with several libraries
 * the words name the library ("4K WEB: Downloading · 40%"), and several searching at once are counted ("Searching 2
 * libraries").
 */
export function acquiringStatus(versions: readonly Pick<Version, "has_file" | "acquisition" | "searching" | "monitored" | "label" | "key">[]): string | undefined {
  const empty = versions.filter((v) => !v.has_file);
  const named = (v: (typeof versions)[number], words: string) => (versions.length > 1 ? `${v.label || v.key}: ${words}` : words);
  let best: (typeof versions)[number] | undefined;
  for (const v of empty) {
    const a = v.acquisition;
    if (!a || a.stage === "blocked") continue;
    if (!best || STAGE_ORDER.indexOf(a.stage) > STAGE_ORDER.indexOf(best.acquisition!.stage)) best = v;
  }
  if (best) return named(best, heroStageWords(best.acquisition!));
  // Libraries really searching are counted ("Searching 2 libraries"); one is named as before.
  const now = empty.filter((v) => v.monitored && v.searching === "searching");
  if (now.length > 1) return `Searching ${now.length} libraries`;
  const searching = now[0] ?? empty.find((v) => v.monitored && v.searching === "queued");
  return searching ? named(searching, searching.searching === "searching" ? "Searching" : "Search queued") : undefined;
}

/**
 * A series library that monitors none of the episodes it has, said once: "291 of 291 episodes available, not
 * monitored" (the library itself is not monitored), "37 of 37 episodes · none monitored" (no episode is); "" otherwise.
 */
export function unmonitoredCoverage(e: { total: number; have: number; monitored?: number } | undefined | null, libraryMonitored: boolean): string {
  if (!e || e.total <= 0) return "";
  const count = `${fmtNumber(e.have)} of ${fmtNumber(e.total)} ${e.total === 1 ? "episode" : "episodes"}`;
  if (!libraryMonitored) return `${count} available, not monitored`;
  return e.monitored === 0 ? `${count} · none monitored` : "";
}

/**
 * A series library's coverage in words: "44 of 46 monitored episodes, 2 missing; 30 not monitored" (monitored counts
 * from the server); without them, "44 of 76 episodes"; a library monitoring none of them unmonitoredCoverage.
 * Specials are left out, as the counts are.
 */
export function coverageWords(e: { total: number; have: number; missing: number; monitored?: number; monitored_have?: number } | undefined | null, libraryMonitored = true): string {
  if (!e) return "";
  const none = unmonitoredCoverage(e, libraryMonitored);
  if (none) return none;
  if (e.monitored === undefined && e.monitored_have === undefined) return `${fmtNumber(e.have)} of ${fmtNumber(e.total)} episodes`;
  const mon = e.monitored ?? 0;
  const off = Math.max(0, e.total - mon);
  const parts = [`${fmtNumber(e.monitored_have ?? 0)} of ${fmtNumber(mon)} monitored ${mon === 1 ? "episode" : "episodes"}`];
  if (libraryMonitored && e.missing > 0) parts[0] += `, ${fmtNumber(e.missing)} missing`;
  return off > 0 ? `${parts[0]}; ${fmtNumber(off)} not monitored` : parts[0];
}
