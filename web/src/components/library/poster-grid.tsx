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
import { fmtAgo, fmtBytes } from "@/lib/format";
import { kindOf, posterStatus } from "@/lib/library";
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { CopyBarLine, CopyDot } from "./copy-bits";
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

/** The library's poster details (Poster options): next up, network, each copy's quality and size, added, and
 * always each copy's monitoring and ownership (short; the following instance in the tooltip). */
function PosterDetails({ t, prefs, seeFiles }: { t: TitleRow; prefs: PosterPrefs; seeFiles: boolean }) {
  const has = (d: PosterPrefs["details"][number]) => prefs.details.includes(d);
  const vs = t.versions ?? [];
  const next = has("next") ? nextWords(t.next_up, t.kind) : "";
  const perCopy = (has("quality") || has("copy_size")) && seeFiles;
  return (
    <div className="mt-1 space-y-1 text-[11px] leading-snug text-[var(--ink-3)]" data-testid="poster-details">
      {next ? <div className="truncate">{next}</div> : null}
      {has("network") && t.network ? <div className="truncate">{t.network}</div> : null}
      {perCopy ? (
        <div className="space-y-0.5 border-t border-[var(--line)] pt-1">
          {vs.map((v) => (
            <div key={v.key} className="flex min-w-0 items-center gap-1">
              <CopyDot vkey={v.key} className="size-1.5" />
              <span className="truncate">
                {[has("quality") ? (seeFiles ? v.quality : resolutionWords(v.quality)) || "No file" : v.label, has("copy_size") && v.size_bytes ? fmtBytes(v.size_bytes) : ""].filter(Boolean).join(" · ")}
              </span>
            </div>
          ))}
        </div>
      ) : null}
      {has("added") && t.added_at ? <div className="truncate">Added {fmtAgo(t.added_at)}</div> : null}
      {vs.some((v) => v.control) ? (
        <div className="space-y-0.5 text-[10px]" data-testid="poster-copies">
          {vs.map((v) => (
            <div key={v.key} className="flex min-w-0 items-center gap-1">
              <CopyDot vkey={v.key} className="size-1.5" />
              <span aria-hidden>{v.monitored ? "●" : "○"}</span>
              <span className="truncate">{v.label}</span>
              <span className="sr-only">{v.monitored ? ", monitored" : ", not monitored"}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
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
  /** The library's posters (Movies, Series, Anime): copy bars, the in-flight chip, the details and select mode. */
  library?: { prefs: PosterPrefs; select?: Selection };
}) {
  const end = useEndReached(onMore, more);
  const perms = usePerms();
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
          const vs = t.versions ?? [];
          // Without "See files" the badge's watchability is the whole story: no per-library bars.
          const bars = perms.seeFiles ? vs.map((v) => ({ v, bar: copyBar(v, t.kind) })) : [];
          const flight = titleAcquisition(t);
          const sel = library.select;
          const chosen = !!sel?.chosen.has(t.id ?? 0);
          const small = library.prefs.size === "small";
          return (
            <div key={t.id} className={cn("cv-auto-poster relative min-w-0 rounded-xl", chosen && "bg-[color-mix(in_srgb,var(--accent)_10%,transparent)] ring-2 ring-inset ring-[var(--accent)]")} data-selected={chosen || undefined}>
              <PosterCard
                to={`/title/${t.id}`}
                title={t.name ?? "?"}
                year={library.prefs.details.includes("year") ? t.year : undefined}
                kind={kindOf(t)}
                status={posterStatus(t.availability, t.upgrade_seen, !perms.seeFiles, t.upgrade_state, t)}
                image={t.poster}
                overview={t.overview}
                extraLabel={bars.length ? bars.map(({ v, bar }) => `${v.label}: ${bar.label}`).join("; ") : undefined}
                bars={bars.length ? bars.map(({ v, bar }) => <CopyBarLine key={v.key} vkey={v.key} label={v.label ?? v.key ?? ""} bar={bar} compact={small} />) : undefined}
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
