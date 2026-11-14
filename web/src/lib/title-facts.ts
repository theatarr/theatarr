// The title page's "what happened and what next" lines (docs/design/ui.md, Title page): the latest relevant event of
// each library (imported, or a failed download with the providers it missed on), why a library or an episode is
// missing (its last targeted search, or never, and whether Theatarr searches by itself), and the hero's word while a
// download is in hand and nothing is held. They read only what the title response carries for callers who see files
// (history, last_search, searching, auto_search, acquisition); accounts without it get none of them.
import type { Schemas } from "@/api/client";
import type { Version } from "@/api/library";
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

/** "RD, AD and TB" for cache codes. */
function providerList(codes: readonly string[]): string {
  const up = codes.map((c) => c.toUpperCase());
  return up.length < 2 ? up.join("") : `${up.slice(0, -1).join(", ")} and ${up[up.length - 1]}`;
}

/** Why a download failed, in a few words: "not cached on RD, AD and TB" for a miss on every provider whose providers
 *  are known, else the server's short reason in lower case ("miss on every provider", "stalled"). */
export function failureWords(h: Pick<HistoryEvent, "missed_on" | "reason">): string {
  if (h.missed_on?.length) return `not cached on ${providerList(h.missed_on)}`;
  const r = h.reason?.trim() ?? "";
  return r ? r.charAt(0).toLowerCase() + r.slice(1) : "";
}

export type LatestEvent = { event: "imported" | "failed"; date: string; words: string; tone: "positive" | "warning" };

/**
 * A library's latest relevant event: its newest import (an upgrade counts) or failed download. "Imported today";
 * a failure with a file held "Last upgrade attempt Nov 13: not cached on RD, AD and TB", without one "Last download
 * failed Nov 13: …". Nothing when the library has neither (searches and grabs in flight are said elsewhere).
 */
export function latestEvent(v: Pick<Version, "history" | "has_file">, now: Date = new Date()): LatestEvent | undefined {
  let top: HistoryEvent | undefined;
  for (const h of v.history ?? []) {
    if (h.event !== "imported" && h.event !== "upgraded" && h.event !== "failed") continue;
    if (!top || h.date > top.date) top = h;
  }
  if (!top) return undefined;
  const day = dayWords(top.date, now);
  if (top.event !== "failed") return { event: "imported", date: top.date, words: `Imported ${day}`, tone: "positive" };
  const why = failureWords(top);
  const what = v.has_file ? "Last upgrade attempt" : "Last download failed";
  return { event: "failed", date: top.date, words: `${what} ${day}${why ? `: ${why}` : ""}`, tone: "warning" };
}

/** "Never searched" or "Last searched Nov 12: 12 results, none accepted". */
export function lastSearchWords(l: LastSearch | undefined | null, now: Date = new Date()): string {
  if (!l) return "Never searched";
  const found = l.results === 0 ? "nothing found" : `${fmtNumber(l.results)} ${l.results === 1 ? "result" : "results"}, ${l.accepted === 0 ? "none" : fmtNumber(l.accepted)} accepted`;
  return `Last searched ${dayWords(l.at, now)}: ${found}`;
}

/** A search under way, in words: "Searching now", "Search queued"; "" for none. */
export function searchingWords(s: string | undefined | null): string {
  return s === "searching" ? "Searching now" : s === "queued" ? "Search queued" : "";
}

/**
 * Why a missing library (or episode) is missing and what comes next, in one line: the search under way, else the last
 * targeted search or "Never searched", then whether Theatarr searches for missing items by itself. With it off, RSS
 * still picks up new releases, and the line says so. Search now sits beside the line (the caller's button).
 */
export function missingLine(f: { last_search?: LastSearch | null; searching?: string | null }, auto: AutoSearch | undefined | null, now: Date = new Date()): string {
  const first = searchingWords(f.searching) || lastSearchWords(f.last_search, now);
  if (!auto) return first;
  if (!auto.enabled) return `${first}. Paused: nothing is searched or downloaded.`;
  if (!auto.missing) return `${first}. Automatic search for missing items is off; new releases are still picked up from RSS.`;
  return `${first}. Theatarr searches for it automatically.`;
}

/** The hero's word for an acquisition in hand: "Waiting to start", "Downloading · 37%" or "Importing". */
export function heroStageWords(a: Pick<Acquisition, "stage" | "progress">): string {
  switch (a.stage) {
    case "queued":
    case "starting":
      return "Waiting to start";
    case "downloading":
      return a.progress !== undefined ? `Downloading · ${Math.round(Math.max(0, Math.min(a.progress, 100)))}%` : "Downloading";
    case "blocked":
      return "Blocked";
    default:
      return "Importing";
  }
}

const STAGE_ORDER: Acquisition["stage"][] = ["queued", "starting", "downloading", "complete", "waiting_mount", "linking"];

/**
 * The hero's status while a library is getting its first file: the most advanced download in hand (not blocked) of a
 * library that holds no file, else "Searching" / "Search queued" when the server says a search of such a library is
 * under way; undefined when neither (the title's status stands). A held file stays Available during an upgrade, so a
 * title with any file held gets none.
 */
export function acquiringStatus(versions: readonly Pick<Version, "has_file" | "acquisition" | "searching" | "monitored">[]): string | undefined {
  if (versions.some((v) => v.has_file)) return undefined;
  let best: Acquisition | undefined;
  for (const v of versions) {
    const a = v.acquisition;
    if (!a || a.stage === "blocked") continue;
    if (!best || STAGE_ORDER.indexOf(a.stage) > STAGE_ORDER.indexOf(best.stage)) best = a;
  }
  if (best) return heroStageWords(best);
  if (versions.some((v) => v.monitored && v.searching === "searching")) return "Searching";
  if (versions.some((v) => v.monitored && v.searching === "queued")) return "Search queued";
  return undefined;
}

/**
 * A series library's coverage in words: "44 of 46 monitored episodes, 2 missing; 30 not monitored" (monitored counts
 * from the server); without them, "44 of 76 episodes". Specials are left out, as the counts are.
 */
export function coverageWords(e: { total: number; have: number; missing: number; monitored?: number; monitored_have?: number } | undefined | null, libraryMonitored = true): string {
  if (!e) return "";
  if (e.monitored === undefined && e.monitored_have === undefined) return `${fmtNumber(e.have)} of ${fmtNumber(e.total)} episodes`;
  const mon = e.monitored ?? 0;
  const off = Math.max(0, e.total - mon);
  const parts = [`${fmtNumber(e.monitored_have ?? 0)} of ${fmtNumber(mon)} monitored ${mon === 1 ? "episode" : "episodes"}`];
  if (libraryMonitored && e.missing > 0) parts[0] += `, ${fmtNumber(e.missing)} missing`;
  return off > 0 ? `${parts[0]}; ${fmtNumber(off)} not monitored` : parts[0];
}
