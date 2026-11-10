// Date helpers for the calendar: local dates as YYYY-MM-DD and the weeks of a month grid (weeks start on Sunday).

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

/** The days shown for a month: from the Sunday on or before the 1st to the Saturday on or after the last day. */
export function monthDays(year: number, month: number): Date[] {
  const first = new Date(year, month, 1);
  const last = new Date(year, month + 1, 0);
  const start = new Date(year, month, 1 - first.getDay());
  const end = new Date(year, month + 1, 0 + (6 - last.getDay()));
  const out: Date[] = [];
  for (let d = start; d <= end; d = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1)) out.push(d);
  return out;
}

/** "S01E02"; "E12" without a season. */
export function episodeCode(season?: number, number?: number): string {
  if (number === undefined) return "";
  const e = `E${String(number).padStart(2, "0")}`;
  return season === undefined ? e : `S${String(season).padStart(2, "0")}${e}`;
}

const TYPE_LABEL: Record<string, string> = { theatrical: "In cinemas", digital: "Digital", physical: "Physical" };

export function releaseLabel(type: string): string {
  return TYPE_LABEL[type] ?? type;
}
