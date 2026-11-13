// Small pieces the Activity tabs share (pages/activity.tsx): the poster thumbnail, a download's title line, the
// episodes in short, the matched formats as pills, and the dash an unknown value shows.
import { Link } from "react-router";
import type { ActivityDownload } from "@/api/activity";
import { kindLabel } from "@/components/activity-bits";
import { cn } from "@/lib/utils";

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
        <Link to={`/title/${t.id}`} className={`${nameClass} hover:text-primary`}>{t.name}</Link>
      ) : <span className={nameClass}>{t.name}</span>}
      {t.year ? <span className="text-sm text-muted-foreground">{t.year}</span> : null}
      <span className="text-[11px] font-semibold uppercase tracking-wider text-[var(--ink-3)]">{kindLabel(t.kind)}</span>
    </div>
  );
}

/** The dash a cell shows for a value that is not known (never 0). */
export function Dash() {
  return <span className="text-[var(--ink-3)]">—</span>;
}

/** Episodes in short: "S01E01 S01E02", or "S01E01 S01E02 S01E03 … 12 episodes". */
export function episodesWords(eps: readonly string[] | undefined): string {
  if (!eps?.length) return "";
  return eps.length > 4 ? `${eps.slice(0, 3).join(" ")} … ${eps.length} episodes` : eps.join(" ");
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
