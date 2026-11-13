// Small pieces the Activity tabs share (pages/activity.tsx): the poster thumbnail and a download's title line.
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
