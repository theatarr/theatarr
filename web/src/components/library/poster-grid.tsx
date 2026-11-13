// The grid of poster cards, paged as the user scrolls: a sentinel well below the last row asks for the next page
// of GET /titles, and each card skips layout and paint while off screen (content-visibility, .cv-auto-poster), so
// 600+ titles stay fast. Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/views/
// media-content/poster-grid.tsx @ 2ef7757c, GPL-3.0: the responsive grid, the off-screen skipping and the
// end-of-list sentinel.
import { useEffect, useRef, type ReactNode } from "react";
import { Link } from "react-router";
import { usePerms, type TitleRow } from "@/api/library";
import { PosterCard } from "@/components/poster-card";
import { Checkbox } from "@/components/ui/checkbox";
import { activityHref, chipWords, copyBar, nextWords, titleAcquisition, type PosterPrefs } from "@/lib/copies";
import { badgeTone, copyTone, copyWords } from "@/lib/library-status";
import { fmtAgo, fmtBytes } from "@/lib/format";
import { kindOf, posterStatus, utcToday, type PosterStatus } from "@/lib/library";
import { cn } from "@/lib/utils";
import { LibraryPill, StatusBar } from "./library-pill";
import type { Selection } from "./title-table";

export function useEndReached(onEnd: (() => void) | undefined, active: boolean) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el || !active || !onEnd) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && onEnd(), { rootMargin: "1200px 0px" });
    io.observe(el);
    return () => io.disconnect();
  }, [onEnd, active]);
  return ref;
}

/** The library's poster details (Poster options): year is on the card itself; next up, network, each library's
 * quality, the title's size, the day it was added, its rating. */
function PosterDetails({ t, prefs, seeFiles }: { t: TitleRow; prefs: PosterPrefs; seeFiles: boolean }) {
  const has = (d: PosterPrefs["details"][number]) => prefs.details.includes(d);
  const vs = t.versions ?? [];
  const next = has("next") ? nextWords(t.next_up, t.kind) : "";
  const quality =
    has("quality") && seeFiles && vs.length
      ? vs.length === 1
        ? vs[0].quality || "No file"
        : vs.map((v) => `${v.label}: ${v.quality || "no file"}`).join(" · ")
      : "";
  const lines = [
    next,
    has("network") ? t.network : "",
    quality,
    has("size") && seeFiles && t.size_bytes ? fmtBytes(t.size_bytes) : "",
    has("added") && t.title_added_at ? `Added ${fmtAgo(t.title_added_at)}` : "",
    has("rating") && t.rating ? `Rating ${t.rating.toFixed(1)}` : "",
  ].filter(Boolean);
  if (!lines.length) return null;
  return (
    <div className="mt-1 space-y-0.5 text-[11px] leading-snug text-[var(--ink-3)]" data-testid="poster-details">
      {lines.map((l, i) => (
        <div key={i} className="truncate" title={l}>
          {l}
        </div>
      ))}
    </div>
  );
}

/** The status line(s) between the art and the title (accounts that see files). A movie: one bar (in several libraries
 * the bar takes the badge's tone, so it never contradicts it, and one pill per library carries the differences). A
 * series: one bar with its count, or one line per library. words: the same in words, for the card's accessible name.
 * day: today as the server decides "released" (utcToday), once per grid render. */
function statusLines(t: TitleRow, badge: PosterStatus | undefined, day: string, small: boolean): { lines: ReactNode; words: string } | undefined {
  const vs = t.versions ?? [];
  if (!vs.length) return undefined;
  const each = vs.map((v) => ({ v, label: v.label ?? v.key ?? "", tone: copyTone(v, t, day), words: copyWords(v, t, day) }));
  const named = each.map((c) => `${c.label}: ${c.words}`);
  const count = cn("shrink-0 text-right tabular-nums text-[var(--ink-3)]", small ? "text-[9px]" : "text-[10px]");
  if (t.kind === "series") {
    const lines = each.map(({ v, label, tone }, i) => {
      const bar = copyBar(v, "series");
      return (
        <div key={v.key} className="flex min-w-0 items-center gap-1.5" data-testid="library-line">
          {vs.length > 1 ? <span aria-hidden className={cn("w-[38%] shrink truncate text-[var(--ink-2)]", small ? "text-[9px]" : "text-[10px]")}>{label}</span> : null}
          <StatusBar tone={tone} label={named[i]} have={bar.have} flight={bar.flight} total={bar.total} />
          <span aria-hidden className={count}>{bar.count}</span>
        </div>
      );
    });
    return { lines, words: named.join("; ") };
  }
  const whole = vs.length > 1 ? badgeTone(badge) : each[0].tone;
  const lines = (
    <>
      <StatusBar tone={whole} label={vs.length > 1 ? named.join("; ") : named[0]} />
      {vs.length > 1 ? (
        <div className="flex min-w-0 gap-2 overflow-hidden" data-testid="library-pills">
          {each.map((c) => (
            <LibraryPill key={c.v.key} label={c.label} tone={c.tone} words={c.words} className={small ? "text-[9px]" : undefined} />
          ))}
        </div>
      ) : null}
    </>
  );
  return { lines, words: named.join("; ") };
}

export function PosterGrid<T extends TitleRow>({
  titles,
  more,
  onMore,
  caption,
  library,
}: {
  titles: T[];
  more: boolean;
  onMore: () => void;
  /** A line under each card (the Home row's note on /discover/{row}). */
  caption?: (t: T) => ReactNode;
  /** The library's posters (Movies, Series, Anime): status bars and library pills, the in-flight chip, the details and select mode. */
  library?: { prefs: PosterPrefs; select?: Selection };
}) {
  const end = useEndReached(onMore, more);
  const perms = usePerms();
  const day = utcToday();
  return (
    <div className="poster-grid-container">
      <div data-testid="poster-grid" className={cn("poster-grid-responsive grid gap-3.5 sm:gap-4", library && `poster-size-${library.prefs.size}`)}>
        {titles.map((t) => {
          if (!library) {
            return (
              <div key={t.id} className="cv-auto-poster">
                <PosterCard
                  to={`/title/${t.id}`}
                  title={t.name ?? "?"}
                  year={t.year}
                  kind={kindOf(t)}
                  status={posterStatus(t.availability, t.upgrade_seen, !perms.seeFiles, t.upgrade_state, t)}
                  image={t.poster}
                  overview={t.overview}
                  caption={caption?.(t)}
                />
              </div>
            );
          }
          const small = library.prefs.size === "small";
          // Without "See files" the badge's watchability is the whole story: no bars, no pills.
          const badge = posterStatus(t.availability, t.upgrade_seen, !perms.seeFiles, t.upgrade_state, t);
          const status = perms.seeFiles ? statusLines(t, badge, day, small) : undefined;
          const flight = titleAcquisition(t);
          const sel = library.select;
          const chosen = !!sel?.chosen.has(t.id ?? 0);
          return (
            <div key={t.id} className={cn("cv-auto-poster relative min-w-0 rounded-xl", chosen && "bg-[color-mix(in_srgb,var(--accent)_10%,transparent)] ring-2 ring-inset ring-[var(--accent)]")} data-selected={chosen || undefined}>
              <PosterCard
                to={`/title/${t.id}`}
                title={t.name ?? "?"}
                year={library.prefs.details.includes("year") ? t.year : undefined}
                unmonitored={perms.seeFiles && t.monitored === false}
                status={badge}
                image={t.poster}
                overview={t.overview}
                extraLabel={status?.words}
                bars={status?.lines}
                details={<PosterDetails t={t} prefs={library.prefs} seeFiles={perms.seeFiles} />}
              />
              {/* Over the art (2:3, the card's full width), as siblings of the card's link: no control inside a link. */}
              <div className="pointer-events-none absolute inset-x-0 top-0 aspect-[2/3]">
                {flight ? (
                  <Link
                    to={activityHref(flight.acquisition.stage)}
                    aria-label={`${t.name}: ${chipWords(flight.acquisition)} · ${flight.version.label}`}
                    title={flight.acquisition.words}
                    data-testid="flight-chip"
                    className={cn(
                      "pointer-events-auto absolute inset-x-2 bottom-2 block truncate rounded-[3px] border border-white/25 bg-[var(--art-scrim-strong)] px-1.5 py-1 text-center font-semibold text-white backdrop-blur-sm",
                      small ? "text-[9px]" : "text-[11px]",
                      flight.acquisition.stage === "blocked" && "dark border-[var(--warn)]",
                    )}
                  >
                    {chipWords(flight.acquisition)}
                  </Link>
                ) : null}
                {sel ? (
                  <label className="pointer-events-auto absolute left-2 top-9 grid size-8 cursor-pointer place-items-center rounded-[5px] bg-[var(--surface)] shadow-md">
                    <Checkbox aria-label={`Select ${t.name}`} className="size-[18px]" checked={chosen} onChange={(e) => sel.toggle(t.id ?? 0, e.target.checked)} />
                  </label>
                ) : null}
              </div>
            </div>
          );
        })}
      </div>
      {more ? <div ref={end} aria-hidden className="h-12" /> : null}
    </div>
  );
}
