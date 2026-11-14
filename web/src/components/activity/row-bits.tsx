// Small pieces the Activity tabs share (pages/activity.tsx): the poster thumbnail, a download's title line, the
// episodes in short, the matched formats as pills, and the dash an unknown value shows.
import { Link } from "react-router";
import type { ActivityDownload } from "@/api/activity";
import type { DataColumn } from "@/components/ui/data-table";
import { kindLabel } from "@/components/activity-bits";
import { cn } from "@/lib/utils";
import { nameWithoutYear, plural } from "@/lib/format";

export function Poster({ src, className }: { src: string; className?: string }) {
  return (
    <img
      src={src}
      alt=""
      loading="lazy"
      decoding="async"
      className={cn("aspect-[2/3] shrink-0 rounded-[4px] bg-[var(--bg)] object-cover ring-1 ring-[var(--line)]", className)}
    />
  );
}

export function TitleLine({ t }: { t: ActivityDownload["title"] }) {
  const nameClass = "min-w-0 truncate text-[14px] font-semibold text-[var(--ink)]";
  return (
    <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
      {t.id ? (
        <Link to={`/title/${t.id}`} className={`${nameClass} hover:text-primary`}>{nameWithoutYear(t.name, t.year)}</Link>
      ) : <span className={nameClass}>{nameWithoutYear(t.name, t.year)}</span>}
      {t.year ? <span className="text-sm text-muted-foreground">{t.year}</span> : null}
      <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--ink-3)]">{kindLabel(t.kind)}</span>
    </div>
  );
}

/** The dash a cell shows for a value that is not known (never 0). */
export function Dash() {
  return <span className="text-[var(--ink-3)]">—</span>;
}

/** Episodes in short: "S01E01 S01E02", or the count first and runs as ranges, "11 episodes · S01E01–S01E11", so a
 *  cut-off line still says how many. */
export function episodesWords(eps: readonly string[] | undefined): string {
  if (!eps?.length) return "";
  if (eps.length <= 4) return eps.join(" ");
  const runs: string[][] = [];
  for (const e of eps) {
    const last = runs.at(-1);
    const prev = last ? /^S(\d+)E(\d+)$/i.exec(last[last.length - 1]) : null;
    const cur = /^S(\d+)E(\d+)$/i.exec(e);
    if (last && prev && cur && prev[1] === cur[1] && Number(cur[2]) === Number(prev[2]) + 1) last.push(e);
    else runs.push([e]);
  }
  return `${plural(eps.length, "episode")} · ${runs.map((r) => (r.length > 1 ? `${r[0]}–${r[r.length - 1]}` : r[0])).join(" ")}`;
}

/** The custom formats a release matched, with their scores. */
export function FormatPills({ formats }: { formats?: readonly { name: string; score: number }[] | null }) {
  if (!formats?.length) return <Dash />;
  return (
    <span className="flex flex-wrap gap-1">
      {formats.map((f, i) => (
        <span key={i} className="whitespace-nowrap rounded border border-[var(--line)] px-1 text-[10.5px] text-[var(--ink-3)]">
          {f.name}
          {f.score ? <span className={cn("ml-1 tabular-nums", f.score < 0 ? "text-[var(--bad-ink)]" : "text-[var(--ok-ink)]")}>{f.score > 0 ? `+${f.score}` : f.score}</span> : null}
        </span>
      ))}
    </span>
  );
}

/** Tighter cells than the kit's, so the default columns fit a 1400 px window beside the sidebar. */
export function tight<T>(columns: readonly DataColumn<T>[]): DataColumn<T>[] {
  return columns.map((c) => ({ ...c, headClassName: cn("px-2", c.headClassName), cellClassName: cn("px-2", c.cellClassName) }));
}
