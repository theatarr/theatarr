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

/** "Oct 26 18:24" in local time; with the year when it is not the current one. */
export function fmtTime(iso: string | null | undefined, now: Date = new Date()): string {
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
