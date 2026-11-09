// Date helpers for the calendar: local dates as YYYY-MM-DD and the weeks of a month grid (weeks start on Sunday).

export function ymd(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export function parseYmd(s: string): Date {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y, (m ?? 1) - 1, d ?? 1);
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
