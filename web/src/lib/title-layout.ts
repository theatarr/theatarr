// The title page's layout logic (docs/design/ui.md, Title page): which tabs a caller gets and which opens first, and the
// readiness line in the hero. Kept out of the components so it is tested.
import type { Version } from "@/api/library";
import { fmtNumber } from "./format";
import { today } from "./library";
import { resolutionWords } from "./upgrade";

export type TitleTab = "episodes" | "order" | "about" | "history";

/** A positive safe title id from a route segment, or undefined when the link is malformed. */
export function parseTitleID(raw: string): number | undefined {
  if (!/^[1-9]\d*$/.test(raw)) return undefined;
  const id = Number(raw);
  return Number.isSafeInteger(id) ? id : undefined;
}

export function titleReleaseAccess(raw: string, manage: boolean):
  | { kind: "not-found" }
  | { kind: "forbidden"; id: number }
  | { kind: "page"; id: number } {
  const id = parseTitleID(raw);
  if (id === undefined) return { kind: "not-found" };
  return manage ? { kind: "page", id } : { kind: "forbidden", id };
}

/**
 * The tabs, in order. Series and anime open on Episodes; the libraries sit right under the hero for both kinds (no
 * tab). Watch order only for anime with a franchise; History only with "see files" (the API sends history to no one
 * else).
 */
export function titleTabs(o: { series: boolean; franchise: boolean; seeFiles: boolean }): { id: TitleTab; label: string }[] {
  const tabs: { id: TitleTab; label: string }[] = [];
  if (o.series) tabs.push({ id: "episodes", label: "Episodes" });
  if (o.franchise) tabs.push({ id: "order", label: "Watch order" });
  tabs.push({ id: "about", label: "About" });
  if (o.seeFiles) tabs.push({ id: "history", label: "History" });
  return tabs;
}

/** Old ?tab= values (bookmarks, links) and what they are now; the old Libraries tab (versions, copies) opens the
 *  first tab, as the libraries are under the hero now. */
const LEGACY: Record<string, TitleTab | ""> = { seasons: "episodes", versions: "", copies: "", details: "about" };

/** The open tab for a ?tab= value: the first tab when it names none the caller has. */
export function resolveTab(param: string | null | undefined, tabs: { id: TitleTab }[]): TitleTab {
  const want = param ? (LEGACY[param] ?? param) : "";
  return tabs.find((t) => t.id === want)?.id ?? tabs[0]?.id ?? "about";
}

/** The best resolution among qualities, in words ("4K"), or "". */
function bestResolution(qualities: (string | undefined | null)[]): string {
  const n = qualities.map((q) => Number(q?.match(/(2160|1080|720|576|480)p?/i)?.[1] ?? 0)).reduce((m, x) => Math.max(m, x), 0);
  return n ? resolutionWords(`${n}p`) : "";
}

/**
 * Whether the title can be watched, for everyone (the hero): "Ready to watch in 4K" (the best resolution a usable
 * copy has), "Some episodes ready" (a series with no complete copy), or "Not in the library yet". A movie missing one of
 * its copies is still ready: watchability, not the household's copy policy. gettingReady (an account without See
 * files, the title's acquiring: a download in hand) reads "Getting ready" for a title with nothing held, as the
 * posters do; a failed download is not in hand.
 */
export function readiness(kind: string | undefined, versions: Version[], gettingReady = false): { label: string; tone: "positive" | "partial" | "neutral" } {
  const held = versions.filter((v) => v.has_file);
  if (held.length === 0) return { label: gettingReady ? "Getting ready" : "Not in the library yet", tone: "neutral" };
  const complete = kind === "series" ? held.filter((v) => !v.episodes || (v.episodes.have > 0 && v.episodes.missing === 0)) : held;
  if (complete.length === 0) {
    const best = held.map((v) => v.episodes).filter((e) => !!e).sort((a, b) => b.have - a.have)[0];
    return { label: best ? `${fmtNumber(best.have)} of ${fmtNumber(best.total)} episodes ready` : "Some episodes ready", tone: "partial" };
  }
  const r = bestResolution(complete.map((v) => v.current?.quality ?? v.upgrade?.quality));
  return { label: r ? `Ready to watch in ${r}` : "Ready to watch", tone: "positive" };
}

/**
 * The Facts line about a series' airing: "Next episode" while the day TMDb gave is today or later, else "Last aired".
 * The next day is a cached value, so one that has passed is dropped. Air dates are calendar days: today is the local day.
 */
export function airFact(m: { next_air_date?: string; last_air_date?: string } | undefined | null, day: string = today()): { label: string; day: string } | undefined {
  if (m?.next_air_date && m.next_air_date.slice(0, 10) >= day) return { label: "Next episode", day: m.next_air_date };
  return m?.last_air_date ? { label: "Last aired", day: m.last_air_date } : undefined;
}
