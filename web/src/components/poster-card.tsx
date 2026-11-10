// A poster card in Seerr's style: rounded 2:3 card, a type pill top left, a status badge top right, and on hover a
// dark overlay with the overview. Adapted from Seerr (github.com/seerr-team/seerr) src/components/
// TitleCard/index.tsx @ c167958a, MIT (Copyright (c) 2020 sct). Without an image the card draws a generated gradient
// with the title (no inline style: the gradient is one of twelve classes picked by a hash). Under the card, always:
// the title (two lines at most) and the year, as the search's TMDb card has them, so a title reads without hover on a
// phone; then the caption (a Home row's note), which is ours, and in the library the copy bars (between the art and
// the title) and the poster details (docs/design/ui.md "Library views").
import type { ReactNode } from "react";
import { Link } from "react-router";
import { POSTER_STATUS_LABEL, type PosterStatus } from "@/lib/library";
import { cn } from "@/lib/utils";

export type { PosterStatus } from "@/lib/library";

// One restrained badge: a dark chip (the art scrim, the dark theme's marks) with a coloured dot, so the artwork stays the card. On a card narrower than
// 160 px (the Home rows) the chip is the dot alone and the word moves to the year line.
const DOT: Record<PosterStatus, string> = {
  available: "bg-[var(--ok)]",
  some_episodes: "bg-[var(--accent)]",
  upgrade: "bg-[var(--warn)]",
  best_known: "bg-[var(--ok)]",
  wanted: "bg-[var(--bad)]",
  upcoming: "bg-[var(--ink-3)]",
  getting_ready: "bg-[var(--accent)]",
  not_available: "bg-[var(--ink-3)]",
};

function artIndex(s: string): number {
  let h = 2166136261;
  for (const c of s) {
    h ^= c.charCodeAt(0);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0) % 12;
}

export function PosterCard({
  to,
  href,
  title,
  year,
  kind,
  status,
  image,
  overview,
  caption,
  bars,
  details,
  extraLabel,
  className,
}: {
  /** Poster URL on this origin (GET /titles poster); without one the card draws generated art. */
  image?: string;
  to?: string;
  href?: string;
  title: string;
  year?: number;
  kind: "movie" | "series" | "anime";
  status?: PosterStatus;
  /** Shown in the hover overlay. */
  overview?: string;
  /** A line or two under the title and year. */
  caption?: ReactNode;
  /** Between the art and the title: the library's copy bars. */
  bars?: ReactNode;
  /** Under the caption: the library's poster details. */
  details?: ReactNode;
  /** Added to the link's accessible name (what the bars say). */
  extraLabel?: string;
  className?: string;
}) {
  const body = (
    <div className="relative overflow-hidden rounded-xl bg-[var(--bg)] pb-[150%] shadow ring-1 ring-[var(--line)] transition duration-300 group-hover:scale-[1.04] group-hover:shadow-lg group-hover:ring-[var(--line-2)] group-focus-visible:ring-2 group-focus-visible:ring-[var(--line-focus)]">
      {image ? (
        <img src={image} alt="" loading="lazy" decoding="async" className="absolute inset-0 size-full object-cover" />
      ) : (
        <div data-art={artIndex(title)} className="absolute inset-0">
          <div className="absolute inset-x-3 bottom-4 text-[15px] font-extrabold leading-tight text-white/95 [overflow-wrap:anywhere] [text-shadow:0_1px_6px_rgba(0,0,0,0.4)] line-clamp-4">
            {title}
          </div>
        </div>
      )}
      {overview ? (
        <div className="absolute inset-0 flex items-end bg-[linear-gradient(180deg,var(--art-scrim)_0%,var(--art-scrim-strong)_100%)] p-2.5 text-white opacity-0 transition-opacity duration-200 group-hover:opacity-100 group-focus-visible:opacity-100">
          <div className="line-clamp-6 text-xs leading-snug text-white/90">{overview}</div>
        </div>
      ) : null}
      <div className="absolute inset-x-0 top-0 flex items-start justify-between gap-1 p-2">
        <span
          className={cn(
            "pointer-events-none rounded-[3px] bg-[var(--art-scrim)] px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-white",
          )}
        >
          {kind === "movie" ? "Movie" : kind === "anime" ? "Anime" : "Series"}
        </span>
        {status ? (
          <span
            data-status={status}
            className="dark pointer-events-none flex min-w-0 items-center gap-1 rounded-[3px] bg-[var(--art-scrim)] px-1.5 py-0.5 text-[10px] font-semibold text-white backdrop-blur-sm"
          >
            <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", DOT[status])} />
            <span className="hidden truncate @min-[160px]:inline">{POSTER_STATUS_LABEL[status]}</span>
          </span>
        ) : null}
      </div>
    </div>
  );
  const label = [year ? `${title} (${year})` : title, status ? POSTER_STATUS_LABEL[status] : "", extraLabel ?? ""].filter(Boolean).join(", ");
  const inner = (
    <>
      {body}
      {bars ? <div className="mt-2 grid gap-1 px-0.5" data-testid="poster-bars">{bars}</div> : null}
      <div className="mt-2 px-0.5" data-testid="poster-caption">
        <div className="line-clamp-2 text-[13px] font-semibold leading-snug text-[var(--ink)] [overflow-wrap:anywhere]">{title}</div>
        {year || status ? (
          <div className="truncate text-[11.5px] text-[var(--ink-3)]">
            {year}
            {/* A narrow card's badge is a dot only (two pills do not fit 132 px), so its word is here. */}
            {status ? <span className="@min-[160px]:hidden">{year ? " · " : ""}{POSTER_STATUS_LABEL[status]}</span> : null}
          </div>
        ) : null}
        {caption ? <div className="mt-0.5 line-clamp-2 text-[12px] leading-snug text-muted-foreground">{caption}</div> : null}
        {details}
      </div>
    </>
  );
  const cls = cn("group @container block w-full outline-none", className);
  if (to) return <Link to={to} className={cls} aria-label={label}>{inner}</Link>;
  return (
    <a href={href} className={cls} aria-label={label}>
      {inner}
    </a>
  );
}

/** A sideways row of cards (Seerr's Slider): scrolls inside itself, never the page. */
export function Slider({ children, label }: { children: React.ReactNode; label: string }) {
  return (
    <div
      role="list"
      aria-label={label}
      className="-mx-1 grid auto-cols-[132px] grid-flow-col gap-3 overflow-x-auto overscroll-x-contain px-1 pb-3 pt-2 [scrollbar-width:thin] sm:auto-cols-[150px] sm:gap-4 [&>*]:snap-start snap-x"
    >
      {children}
    </div>
  );
}

/** A small poster for table rows in the admin screens: GET /img/t/{id}/poster/w92 (generated art when there is none). */
export function TitleThumb({ id, className }: { id?: number; className?: string }) {
  if (!id) return null;
  return (
    <img
      src={`/img/t/${id}/poster/w92`}
      alt=""
      loading="lazy"
      decoding="async"
      className={cn("aspect-[2/3] w-8 shrink-0 rounded-[5px] bg-[var(--bg)] object-cover ring-1 ring-[var(--line)]", className)}
    />
  );
}
