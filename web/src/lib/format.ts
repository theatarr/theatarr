// Formatting helpers shared by the screens.

export function fmtNumber(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  return n.toLocaleString("en-US");
}

export function fmtBytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i === 0 ? `${v} ${units[i]}` : `${v.toFixed(1)} ${units[i]}`;
}

export function fmtPercent(r: number | null | undefined, digits = 1): string {
  if (r === null || r === undefined) return "—";
  return `${(r * 100).toFixed(digits).replace(/\.0+$/, "")}%`;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "Oct 26 18:24" in local time; with the year when it is not the current one. A number is milliseconds. */
export function fmtTime(iso: string | number | null | undefined, now: Date = new Date()): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return "—";
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const year = d.getFullYear() === now.getFullYear() ? "" : ` ${d.getFullYear()}`;
  return `${MONTHS[d.getMonth()]} ${d.getDate()}${year} ${hh}:${mm}`;
}

/**
 * The day (YYYY-MM-DD) a moment falls on in this browser's time zone. A bare YYYY-MM-DD is a calendar day and stays as
 * written.
 */
export function localDay(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = iso.length > 10 ? new Date(iso) : null;
  if (!d || Number.isNaN(d.getTime())) return iso.slice(0, 10);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** "12 s ago", "5 min ago", "3 h ago", "2 d ago"; "in 4 min" for the future. */
export function fmtAgo(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "—";
  const s = Math.round((now.getTime() - t) / 1000);
  const a = Math.abs(s);
  let v: string;
  if (a < 60) v = `${a} s`;
  else if (a < 3600) v = `${Math.round(a / 60)} min`;
  else if (a < 86400) v = `${Math.round(a / 3600)} h`;
  else v = `${Math.round(a / 86400)} d`;
  return s >= 0 ? `${v} ago` : `in ${v}`;
}

/** Duration between two instants, "1.5 s", "38 s", "1 min 44 s". */
export function fmtDuration(from: string | undefined, to: string | undefined): string {
  if (!from || !to) return "—";
  const ms = new Date(to).getTime() - new Date(from).getTime();
  if (Number.isNaN(ms) || ms < 0) return "—";
  if (ms < 10_000) return `${(ms / 1000).toFixed(1)} s`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  return `${Math.floor(s / 60)} min ${s % 60} s`;
}

/** A Go duration string ("1m0s", "1s", "1h0m0s") in plain words: "1 min", "1 s", "1 h". */
export function fmtGoDuration(d: string | undefined): string {
  if (!d) return "—";
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:([\d.]+)s)?$/.exec(d);
  if (!m) return d;
  const parts: string[] = [];
  if (m[1] && m[1] !== "0") parts.push(`${m[1]} h`);
  if (m[2] && m[2] !== "0") parts.push(`${m[2]} min`);
  if (m[3] && Number(m[3]) !== 0) parts.push(`${m[3]} s`);
  return parts.join(" ") || "0 s";
}

/** "30 / 1 min" for a budget rate. */
export function fmtRate(requests: number | undefined, per: string | undefined): string {
  if (!requests) return "—";
  const p = fmtGoDuration(per);
  return `${requests} / ${p.replace(/^1 /, "")}`;
}

/** "1 title", "2 titles", "1,128 titles": the count through fmtNumber and the noun in the number it needs. many
 *  defaults to one + "s". */
export function plural(n: number, one: string, many: string = `${one}s`): string {
  return `${fmtNumber(n)} ${pluralWord(n, one, many)}`;
}

/** The word alone, for a verb or a noun the count is not printed with: pluralWord(1, "matches", "match"). */
export function pluralWord(n: number, one: string, many: string = `${one}s`): string {
  return n === 1 ? one : many;
}

/** The name with its year in brackets, "Alien (1979)"; once only when the name already ends with that year, as
 *  "Foundation (2021)" does. A different year at the end ("Dune (1984)" of 2021) is kept and the year added. */
export function titleWithYear(name: string | null | undefined, year?: number | null): string {
  const n = nameWithoutYear(name, year);
  return year ? `${n} (${year})` : n;
}

/** The name without a trailing "(year)" equal to year: for a screen that shows the year beside or under the name. */
export function nameWithoutYear(name: string | null | undefined, year?: number | null): string {
  const n = name ?? "";
  if (!year) return n;
  const tail = ` (${year})`;
  return n.endsWith(tail) && n.length > tail.length ? n.slice(0, -tail.length) : n;
}

/** "Oct 27" for a YYYY-MM-DD this year, "Oct 27, 2024" otherwise. */
export function fmtDay(d: string | undefined | null, now: Date = new Date()): string {
  if (!d) return "—";
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(d);
  if (!m) return d;
  const y = Number(m[1]);
  const s = `${MONTHS[Number(m[2]) - 1]} ${Number(m[3])}`;
  return y === now.getFullYear() ? s : `${s}, ${y}`;
}

/** "Oct 27, 2025" for a YYYY-MM-DD (or the day part of a timestamp), always with the year: for a list whose dates
 *  span more than one year (dayFormatter). */
export function fmtDayYear(d: string | null | undefined): string {
  if (!d) return "—";
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(d);
  if (!m) return d;
  return `${MONTHS[Number(m[2]) - 1]} ${Number(m[3])}, ${m[1]}`;
}

/** The day format for one list: "Oct 27" when every date is in the current year, else every date with its year, so
 *  "Jan 1" never follows "Dec 25, 2025". */
export function dayFormatter(dates: readonly (string | null | undefined)[], now: Date = new Date()): (d: string | null | undefined) => string {
  const y = String(now.getFullYear());
  const mixed = dates.some((d) => !!d && /^\d{4}-/.test(d) && d.slice(0, 4) !== y);
  return mixed ? fmtDayYear : (d) => fmtDay(d, now);
}
