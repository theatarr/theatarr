// Date helpers for the calendar: local dates as YYYY-MM-DD and the weeks of a month grid (weeks start on the browser locale's first day).
import type { Stage } from "@/lib/copies";
import { stageWords } from "@/lib/copies";
import { utcToday, watchWords } from "@/lib/library";
import { releaseTypeWords } from "./tmdb-words";

export function ymd(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export function parseYmd(s: string): Date {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y, (m ?? 1) - 1, d ?? 1);
}

/** The day n days after a YYYY-MM-DD (before it, for a negative n). */
export function shiftDay(s: string, n: number): string {
  const d = parseYmd(s);
  return ymd(new Date(d.getFullYear(), d.getMonth(), d.getDate() + n));
}

type Dated = { date: string; at?: string };

/**
 * The day an entry sits on. An episode with an air moment (at) sits on the day that moment falls on in this browser's
 * time zone, so its day and its clock time agree: 21:00 Tuesday in New York is 03:00 Wednesday in Berlin. A release
 * date, or an episode whose air time is not known, is a calendar day and stays as written.
 */
export function entryDay(e: Dated): string {
  if (e.at) {
    const d = new Date(e.at);
    if (!Number.isNaN(d.getTime())) return ymd(d);
  }
  return e.date;
}

/**
 * The entries whose own day (entryDay) is in from..to, by day and then by air moment (entries without one first, in
 * the server's order). The page fetches one day wider on each side, since a moment's local day can be the day before
 * or after the network's.
 */
export function placeEntries<T extends Dated>(items: T[], from: string, to: string): { day: string; e: T }[] {
  const ms = (e: T) => (e.at ? new Date(e.at).getTime() || 0 : 0);
  return items
    .map((e, i) => ({ day: entryDay(e), e, i }))
    .filter((x) => x.day >= from && x.day <= to)
    .sort((a, b) => a.day.localeCompare(b.day) || ms(a.e) - ms(b.e) || a.i - b.i)
    .map(({ day, e }) => ({ day, e }));
}

type WeekInfo = { firstDay?: number };
/** Intl.Locale's week data: getWeekInfo() in newer browsers, the weekInfo property in older ones. */
export type WeekLocale = { getWeekInfo?: () => WeekInfo | undefined; weekInfo?: WeekInfo };

/** A locale's first day of the week as Date.getDay() numbers it (0 Sunday; Intl says 7 for Sunday); Sunday when the
 *  locale cannot say. */
export function firstDayOf(loc: WeekLocale): number {
  const first = loc.getWeekInfo?.()?.firstDay ?? loc.weekInfo?.firstDay;
  return first ? first % 7 : 0;
}

/** The first day of the week in this browser's language (0 Sunday); Sunday when the browser cannot say. */
export function weekStart(): number {
  try {
    return firstDayOf(new Intl.Locale(navigator.language) as WeekLocale);
  } catch {
    return 0;
  }
}

/** The days shown for a month: whole weeks starting on weekday start (0 Sunday), from the one holding the 1st to the
 *  one holding the last day. */
export function monthDays(year: number, month: number, start = 0): Date[] {
  const first = new Date(year, month, 1);
  const last = new Date(year, month + 1, 0);
  const begin = new Date(year, month, 1 - ((first.getDay() - start + 7) % 7));
  const end = new Date(year, month + 1, (start + 6 - last.getDay() + 7) % 7);
  const out: Date[] = [];
  for (let d = begin; d <= end; d = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1)) out.push(d);
  return out;
}

/** "S01E02"; "E12" without a season. */
export function episodeCode(season?: number, number?: number): string {
  if (number === undefined) return "";
  const e = `E${String(number).padStart(2, "0")}`;
  return season === undefined ? e : `S${String(season).padStart(2, "0")}${e}`;
}

/**
 * The month grid's slots for one day: a series' episodes of the same season fold into one slot, in the place of the
 * first, so a season dropped at once takes one slot, not three (foldCode words it). Everything else is a slot of its
 * own, in order. The day's list keeps every episode.
 */
export function foldEpisodes<T extends { kind: string; title: { id: number }; season?: number; number?: number }>(day: readonly T[]): T[][] {
  const slots: T[][] = [];
  const bySeason = new Map<string, T[]>();
  for (const e of day) {
    if (e.kind !== "episode" || e.number === undefined) { slots.push([e]); continue; }
    const key = `${e.title.id}:${e.season ?? ""}`;
    const group = bySeason.get(key);
    if (group) group.push(e);
    else { const g = [e]; bySeason.set(key, g); slots.push(g); }
  }
  return slots.map((slot) => (slot.length < 2 ? slot : [...slot].sort((a, b) => a.number! - b.number!)));
}

/** A folded slot's code: "S02E01–E03" for a run, "S02E01, E03" with a gap, "S02 · 6 episodes" when that would be long;
 * the one episode's code for one. */
export function foldCode(slot: readonly { season?: number; number?: number }[]): string {
  const first = slot[0];
  if (slot.length < 2) return episodeCode(first.season, first.number);
  const runs: number[][] = [];
  for (const { number: n } of slot) {
    const run = runs.at(-1);
    if (run && n === run.at(-1)! + 1) run.push(n!);
    else runs.push([n!]);
  }
  const season = first.season === undefined ? "" : `S${String(first.season).padStart(2, "0")}`;
  if (runs.length > 3) return `${season || "Episodes"} · ${slot.length} episodes`;
  const words = runs.map((r) => (r.length > 1 ? `${episodeCode(undefined, r[0])}–${episodeCode(undefined, r.at(-1))}` : episodeCode(undefined, r[0])));
  return `${season}${words.join(", ")}`;
}

/** A release type in words (tmdb-words.ts). */
export const releaseLabel = (type: string) => releaseTypeWords(type);

type Toned = { kind: string; type: string; date: string; at?: string; monitored: boolean; available: boolean; partial?: boolean; acquiring: boolean; stage?: Stage; types?: string[] };

/** A calendar entry's status colour: the theme's status tokens (calendar.css theatarr-cal-tone-*). */
export type CalendarTone = "ok" | "progress" | "bad" | "neutral" | "idle";

/**
 * What a calendar entry's status is, as a colour and a word (docs/design/ui.md "Status words"): Available or Partly
 * available (a file is held), the stage of a download in hand ("Queued", "Downloading", "Waiting for the mount"; "Getting
 * ready" without one), not monitored, upcoming, else missing. Upcoming is the rule the title page and the lists use: an
 * air moment still ahead; a date alone strictly after today's UTC day (upcomingRelease, the server's aired rule). A
 * movie's past cinema date is never missing: a film is missing from its digital or physical date (also when it joins
 * the cinema date on one entry). plain (accounts
 * without "see files") gets the title page's household words (titleWatchWords): Available in green; Getting ready,
 * Upcoming and Not available in grey.
 */
export function calendarTone(e: Toned, now: Date, plain = false): { tone: CalendarTone; label: string } {
  const notAvailable = watchWords("missing", [])?.label ?? "Not available";
  if (e.available) return { tone: "ok", label: !plain && e.partial ? "Partly available" : "Available" };
  if (e.acquiring) return plain ? { tone: "neutral", label: "Getting ready" } : { tone: "progress", label: e.stage ? stageWords({ stage: e.stage }) : "Getting ready" };
  // Household accounts are not told about monitoring: an unmonitored entry still reads Upcoming before its date.
  if (!e.monitored && !plain) return { tone: "idle", label: "Not monitored" };
  const ahead = e.at && !Number.isNaN(new Date(e.at).getTime()) ? new Date(e.at) > now : e.date > utcToday(now);
  if (ahead) return { tone: "neutral", label: "Upcoming" };
  if (plain) return { tone: "neutral", label: notAvailable };
  // A cinema date alone is never missing; joined with a digital or disc date of the day (joinReleases), it is.
  if (e.kind === "movie" && (e.types ?? [e.type]).every((t) => t === "theatrical")) return { tone: "neutral", label: "Released in cinemas" };
  return { tone: "bad", label: "Missing" };
}

/** The legend under the calendar's header, per account: the tones its chips can take, in order. */
export function calendarLegend(plain: boolean): { tone: CalendarTone; label: string }[] {
  if (plain) {
    return [
      { tone: "ok", label: "Available" },
      { tone: "neutral", label: "Getting ready, upcoming or not available yet" },
    ];
  }
  return [
    { tone: "ok", label: "Available" },
    { tone: "progress", label: "Getting ready" },
    { tone: "bad", label: "Missing" },
    { tone: "neutral", label: "Upcoming or in cinemas" },
    { tone: "idle", label: "Not monitored" },
  ];
}

/**
 * One entry per movie and day: a movie's release types on the same day (digital and disc on Nov 17) join into the
 * first entry, which lists them all in types. Other entries pass through, in order.
 */
export function joinReleases<T extends { kind: string; type: string; title: { id: number } }>(placed: { day: string; e: T }[]): { day: string; e: T & { types: string[] } }[] {
  const out: { day: string; e: T & { types: string[] } }[] = [];
  const seen = new Map<string, T & { types: string[] }>();
  for (const { day, e } of placed) {
    const key = e.kind === "movie" ? `${day}:${e.title.id}` : "";
    const first = key ? seen.get(key) : undefined;
    if (first) {
      first.types.push(e.type);
      continue;
    }
    const joined = { ...e, types: [e.type] };
    if (key) seen.set(key, joined);
    out.push({ day, e: joined });
  }
  return out;
}

/** Where a calendar entry opens: an episode opens its series with that episode's window; a movie its title page. */
export function calendarHref(e: { kind: string; title: { id: number }; episode_id?: number }): string {
  return e.kind === "episode" && e.episode_id ? `/title/${e.title.id}?tab=episodes&episode=${e.episode_id}` : `/title/${e.title.id}`;
}
