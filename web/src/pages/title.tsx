// The title page (docs/design/ui.md, Title page). A compact hero answers why someone opened the title: poster, title,
// year, essential facts (certification, runtime, genres, language, rating), whether it can be watched ("Ready to
// watch in 4K"), a short synopsis with More, and the primary action the role allows (Add to another library, Request or View
// request, title-request.tsx TitleRequestButton) beside the title; on phones that action also sticks above the bottom tabs. Callers with "see files" get
// the action bar (title-toolbar.tsx) and the Libraries table (library-rows.tsx) right under it, for a movie and a
// series alike, the one place libraries are managed. Then tabs (lib/title-layout.ts): Episodes (series, the default),
// Watch order (anime with a franchise), About (overview, the fact table, links; admins also the technical details)
// and History.
// Family accounts (no "see files") get readiness, languages and their requests, no copy management. The backdrop
// fading into the theme's background and the poster/title arrangement are adapted from Seerr
// (github.com/seerr-team/seerr) src/components/MovieDetails/index.tsx and src/styles/globals.css (.media-page,
// .media-header, .media-poster, .media-title, .media-attributes, .media-facts) @ c167958a, MIT (Copyright (c) 2020
// sct): the classes are inlined as Tailwind and the hero is made shorter.
import { ExternalLink, Plus, Star } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useAddOptions, type AddTarget } from "@/api/add";
import { ApiError } from "@/api/client";
import { usePerms, useTitle, type TitleDetail, type Version } from "@/api/library";
import { PRODUCT_NAME } from "@/brand";
import { Empty, ErrorState, Loading } from "@/components/page";
import { useSearchUI } from "@/components/search/search-context";
import { AnimeOrder } from "@/components/title/anime-order";
import { Details, TitleJournal, outsideLinks } from "@/components/title/details";
import { Seasons } from "@/components/title/seasons";
import { LibraryRows } from "@/components/title/library-rows";
import { TitleToolbar, inEveryLibrary } from "@/components/title/title-toolbar";
import { RequestMoreSeasons, TitleRequest, TitleRequestButton } from "@/components/title/title-request";
import { SeriesTypeControl } from "@/components/title/title-choices";
import { HistoryTab } from "@/components/activity/history-tab";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { fmtAgo, fmtBytes, nameWithoutYear, plural, titleWithYear } from "@/lib/format";
import { fmtDay, fmtRuntime } from "@/lib/library";
import { currentLibraries } from "@/lib/library-rows";
import { airFact, readiness, releasedLabel, resolveTab, titleStatus, titleTabs, type TitleTab } from "@/lib/title-layout";
import { cn } from "@/lib/utils";
import { titleStatusWords } from "@/lib/tmdb-words";

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-[var(--line)] px-3 py-2 last:border-b-0">
      <span className="text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--ink-3)]">{label}</span>
      <span className="min-w-0 text-right text-[var(--ink-2)]">{children}</span>
    </div>
  );
}

/** "English" for "en"; the code itself when the browser has no name for it. */
function languageName(code: string | undefined): string | undefined {
  if (!code) return undefined;
  try {
    return new Intl.DisplayNames(["en"], { type: "language" }).of(code) ?? code.toUpperCase();
  } catch {
    return code.toUpperCase();
  }
}

/** Overviews longer than this are cut to three lines in the hero, with More. */
const SYNOPSIS_CUT = 220;

/** A synopsis cut to three lines with More. */
function Synopsis({ text }: { text: string }) {
  const [all, setAll] = useState(false);
  const long = text.length > SYNOPSIS_CUT;
  return (
    <p className="text-[15px] leading-relaxed text-[var(--ink-2)]" data-testid="title-synopsis">
      <span className={cn(!all && long && "line-clamp-3")}>{text}</span>
      {long ? (
        <button type="button" onClick={() => setAll(!all)} className="mt-0.5 block text-sm font-medium text-[var(--accent-ink)] hover:underline">
          {all ? "Less" : "More"}
        </button>
      ) : null}
    </p>
  );
}

type Target = AddTarget & { title_id: number };

/** The primary action the role allows: Add to another library (add), else Request / View request (request); null
 * when there is none. A title already in every library the add options offer gets none (the Libraries table under the
 * hero has what is left to do), nor while those options load, so no disabled button sits in the hero or over the
 * phone's content. */
function usePrimaryAction({ target, has, held }: { target: Target; has: boolean; held: readonly string[] }): ((className?: string) => ReactNode) | null {
  const perms = usePerms();
  const { openAdd } = useSearchUI();
  // The toolbar's query (no tmdb_id), so one request serves both.
  const opts = useAddOptions(perms.add && has ? { kind: target.kind, name: target.name, title_id: target.title_id } : null, !!target.anime);
  if (perms.add) {
    if (has && (!opts.data || inEveryLibrary(opts.data))) return null;
    const label = !has ? "Add" : monitorOnly(opts.data, held) ? "Monitor again" : "Add to another library";
    return (className) => (
      <Button onClick={() => openAdd(target)} className={className} data-testid="hero-add">
        <Plus /> {label}
      </Button>
    );
  }
  if (perms.request) return (className) => <TitleRequestButton target={target} className={className} />;
  return null;
}

/** Every library that fits the title already has it, some not monitored: the add sheet can only monitor them again,
 *  so the hero says "Monitor again" rather than "Add to another library" (held: the libraries the title is in). */
function monitorOnly(o: Parameters<typeof inEveryLibrary>[0], held: readonly string[]): boolean {
  const fitting = (o?.versions ?? []).filter((v) => v.fits || v.on);
  return fitting.length > 0 && fitting.every((v) => held.includes(v.key ?? ""));
}

function About({ d }: { d: TitleDetail }) {
  const perms = usePerms();
  const m = d.meta;
  const series = d.kind === "series";
  const runtime = fmtRuntime(d.runtime_minutes ?? m?.runtime_minutes);
  const air = series ? airFact(m) : undefined;
  const digital = series ? undefined : d.digital_release;
  // A movie is released on its first cinema date of the release-date choice (as the calendar and the lists), else TMDb's date.
  const releasedDay = (series ? undefined : d.in_cinemas) || d.release_date;
  const links = outsideLinks(d);
  return (
    <div data-testid="title-about">
      <div className="flex flex-col gap-6 lg:flex-row lg:gap-8">
        <div className="min-w-0 flex-1">
          {m?.tagline ? <p className="mb-3 text-lg italic text-muted-foreground">{m.tagline}</p> : null}
          {/* The hero shows a short overview whole; About repeats only a long one. */}
          {!m?.overview || m.overview.length > SYNOPSIS_CUT ? (
            <>
              <h2 className="font-display text-lg font-semibold text-[var(--ink)]">Overview</h2>
              <p className="mt-2 max-w-3xl text-[15px] leading-relaxed text-muted-foreground">{m?.overview || "No overview yet."}</p>
            </>
          ) : null}
          <div className="mt-4 flex flex-wrap gap-2">
            {links.map((l) => (
              <Button key={l.label} asChild variant="outline" size="sm">
                <a href={l.url} target="_blank" rel="noreferrer noopener">
                  {l.label}
                  <ExternalLink />
                </a>
              </Button>
            ))}
          </div>
        </div>
        <div className="w-full lg:w-80 lg:shrink-0">
          <div className="overflow-hidden rounded-[8px] border border-[var(--line)] bg-card text-sm" data-testid="title-facts">
            {m?.status ? <Fact label="Status"><span title={m.status}>{titleStatusWords(m.status)}</span></Fact> : null}
            {series && perms.seeFiles && d.id && d.series_type ? <Fact label="Series type"><SeriesTypeControl titleId={d.id} value={d.series_type} anime={d.anime} versions={currentLibraries((d.versions ?? []) as Version[])} /></Fact> : null}
            {releasedDay ? <Fact label={releasedLabel(series, releasedDay)}>{fmtDay(releasedDay)}</Fact> : null}
            {!series && digital ? <Fact label="Digital">{fmtDay(digital)}</Fact> : null}
            {air ? <Fact label={air.label}>{fmtDay(air.day)}</Fact> : null}
            {runtime ? <Fact label="Runtime">{runtime}</Fact> : null}
            {m?.network ? <Fact label={series ? "Network" : "Studio"}>{m.network}</Fact> : null}
            {m?.original_language ? <Fact label="Language">{languageName(m.original_language)}</Fact> : null}
            {d.rating ? <Fact label="TMDb rating">{d.rating.toFixed(1)} <span className="text-xs">({plural(m?.rating_votes ?? 0, "vote")})</span></Fact> : null}
            {/* The newest current file's time (when the title was added is under Details: Added to Theatarr). */}
            {perms.seeFiles && d.added_at ? <Fact label="Latest file">{fmtAgo(d.added_at)}</Fact> : null}
            {d.size_bytes ? <Fact label="On disk">{fmtBytes(d.size_bytes)}</Fact> : null}
            {perms.seeFiles && !d.monitored ? <Fact label="Monitored">No</Fact> : null}
          </div>
        </div>
      </div>
      {perms.admin ? <Details d={d} /> : null}
    </div>
  );
}

/** History: the title's History rows (the same rows Activity › History shows for it; see files), and the title's
 *  journal for admins. */
function TitleHistory({ d }: { d: TitleDetail }) {
  const perms = usePerms();
  return (
    <div className="space-y-5" data-testid="title-history">
      <HistoryTab admin={perms.seeFiles} titleId={d.id ?? 0} />
      {perms.admin ? <TitleJournal id={d.id ?? 0} /> : null}
    </div>
  );
}

export function TitlePage() {
  const { id = "" } = useParams();
  const q = useTitle(id);
  const perms = usePerms();
  const [params, setParams] = useSearchParams();
  const d = q.data;
  const target: Target = { kind: d?.kind === "series" ? "series" : "movie", name: d?.name ?? `Title ${id}`, year: d?.year ?? undefined, poster: d?.poster ?? undefined, title_id: Number(id), tmdb_id: d?.tmdb_id ?? undefined, anime: !!d?.anime };
  const current = currentLibraries((d?.versions ?? []) as Version[]);
  // A hook, so it runs before the early returns.
  const action = usePrimaryAction({ target, has: current.length > 0, held: current.map((v) => v.key ?? "") });
  useEffect(() => {
    if (d?.name) document.title = `${titleWithYear(d.name, d.year)} · ${PRODUCT_NAME}`;
  }, [d?.name, d?.year]);

  if (q.isPending) return <Loading />;
  if (q.error || !d) {
    const nf = q.error instanceof ApiError && q.error.status === 404;
    return nf ? (
      <Card className="mt-6"><Empty>There is no title {id}. <Link to="/" className="text-primary hover:underline">Home</Link></Empty></Card>
    ) : (
      <ErrorState error={q.error} />
    );
  }

  // The libraries the title is in now: one it was removed from counts nowhere on the page (its leftover links are
  // named under the Libraries table).
  const all = (d.versions ?? []) as Version[];
  const versions = currentLibraries(all);
  const m = d.meta;
  const series = d.kind === "series";
  const franchises = d.franchises ?? [];
  const tabs = titleTabs({ series, franchise: franchises.length > 0, seeFiles: perms.seeFiles });
  const tab: TitleTab = resolveTab(params.get("tab"), tabs);
  // Those who see files get the status word the posters and lists use; everyone else watchability.
  const watch = readiness(d.kind, versions, !perms.seeFiles && !!d.acquiring);
  const ready = perms.seeFiles ? titleStatus(d, versions) : watch;
  const runtime = fmtRuntime(d.runtime_minutes ?? m?.runtime_minutes);
  const lang = languageName(m?.original_language);
  const attrs = [
    d.certification ? <span key="c" className="rounded-[3px] px-1 py-px text-[11px] font-semibold leading-none shadow-[inset_0_0_0_1px_var(--line-2)]">{d.certification}</span> : null,
    runtime ? <span key="r">{runtime}{series ? " per episode" : ""}</span> : null,
    (d.genres ?? []).length ? <span key="g">{(d.genres ?? []).slice(0, 3).join(", ")}</span> : null,
    lang ? <span key="l">{lang}</span> : null,
  ].filter(Boolean);
  const audio = [...new Set(versions.flatMap((v) => (v.has_file ? (v.current?.languages ?? []) : [])))];

  return (
    <div className={cn("relative isolate", action && "max-md:pb-16")}>
      {/* The backdrop sits behind the hero (z -10 inside this stacking context), full bleed. */}
      <div className="absolute -left-4 -right-4 -top-5 -z-10 h-[300px] overflow-hidden sm:-left-6 sm:-right-6 lg:-left-8 lg:-right-8 lg:h-[340px]">
        {d.backdrop ? <img src={d.backdrop} alt="" className="size-full object-cover object-top opacity-80" /> : null}
        <div className="absolute inset-0 bg-[linear-gradient(180deg,color-mix(in_srgb,var(--background)_45%,transparent)_0%,color-mix(in_srgb,var(--background)_80%,transparent)_55%,var(--background)_100%)]" />
      </div>

      {/* Hero */}
      <div className="grid grid-cols-[6rem_1fr] gap-x-4 gap-y-3 pt-2 sm:grid-cols-[8.5rem_1fr] sm:gap-x-6 lg:grid-cols-[10rem_1fr] lg:pt-6" data-testid="title-hero">
        <div className="row-span-1 overflow-hidden rounded-[6px] shadow-lg ring-1 ring-[var(--line)] sm:row-span-2">
          <img src={d.poster} alt="" className="aspect-[2/3] w-full bg-[var(--sunken)] object-cover" />
        </div>
        <div className="flex min-w-0 flex-col justify-end gap-1.5 sm:justify-start">
          <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
            <h1 className="min-w-0 font-display text-2xl font-semibold tracking-[-0.01em] text-[var(--ink)] [overflow-wrap:anywhere] sm:text-[34px] sm:leading-tight" data-testid="title-name">
              {nameWithoutYear(d.name, d.year)}
              {d.year ? <span className="ml-2 text-base font-normal text-muted-foreground sm:text-2xl">({d.year})</span> : null}
            </h1>
            {action?.("max-md:hidden")}
          </div>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
            {attrs.map((a, i) => (
              <span key={i} className="flex items-center gap-2">
                {i > 0 ? <span aria-hidden>·</span> : null}
                {a}
              </span>
            ))}
            {d.rating ? (
              <span className="flex items-center gap-1">
                {attrs.length ? <span aria-hidden className="mr-1">·</span> : null}
                <Star className="size-4 fill-[var(--warn)] text-[var(--warn)]" />
                <span className="font-medium text-[var(--ink)]">{d.rating.toFixed(1)}</span>
              </span>
            ) : null}
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <Chip data-testid="title-readiness" tone={ready.tone === "positive" ? "ok" : ready.tone === "partial" ? "info" : ready.tone === "warning" ? "warn" : ready.tone === "negative" ? "bad" : "ghost"} data-tone={ready.tone}>
              {ready.label}
            </Chip>
            {audio.length > 0 ? <span className="text-xs text-muted-foreground">Audio: {audio.join(", ")}</span> : null}
            {perms.seeFiles && !d.monitored && d.availability !== "unmonitored" ? <Chip tone="ghost">Not monitored</Chip> : null}
          </div>
        </div>
        <div className="col-span-2 max-w-3xl sm:col-span-1 sm:col-start-2">
          {m?.overview ? <Synopsis text={m.overview} /> : null}
        </div>
      </div>

      {/* Requests (accounts that request rather than add): their status for this title. */}
      {!perms.add && perms.request ? (
        <div className="mt-5">
          <TitleRequest target={target} versions={versions} heroTone={watch.tone} />
        </div>
      ) : null}

      {/* The action bar and the Libraries table right under the hero, for a movie and a series alike. */}
      {perms.seeFiles ? (
        <div className="mt-6 space-y-4">
          <TitleToolbar titleId={Number(id)} name={target.name} year={target.year} kind={target.kind} poster={target.poster} anime={!!d.anime} versions={versions}
            removed={all.filter((v) => v.removed)} onHistory={() => setParams({ tab: "history" }, { replace: true })} />
          <LibraryRows title={d} versions={all} />
        </div>
      ) : null}

      {/* Tabs */}
      <div className="-mx-1 mb-5 mt-7 flex gap-1 overflow-x-auto border-b border-[var(--line)] px-1 [scrollbar-width:none]" role="tablist">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            data-active={tab === t.id}
            data-tab={t.id}
            onClick={() => setParams(t.id === tabs[0].id ? {} : { tab: t.id }, { replace: true })}
            className="-mb-px whitespace-nowrap border-b-2 border-transparent px-3 py-2.5 text-sm font-medium text-[var(--ink-3)] transition hover:text-[var(--ink)] data-[active=true]:border-[var(--accent)] data-[active=true]:text-[var(--ink)]"
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "episodes" ? (
        <>
          {!perms.add && perms.request ? (
            <div className="mb-3">
              <RequestMoreSeasons target={target} />
            </div>
          ) : null}
          <Seasons id={id} anime={!!d.anime} franchises={franchises} />
        </>
      ) : null}
      {tab === "order" ? <AnimeOrder franchises={franchises} admin={perms.admin} /> : null}
      {tab === "about" ? <About d={d} /> : null}
      {tab === "history" ? <TitleHistory d={d} /> : null}

      {/* Phones: the primary action stays reachable above the bottom tabs. */}
      {action ? (
        <div className="fixed inset-x-0 bottom-[calc(3.75rem+env(safe-area-inset-bottom))] pointer-events-none z-30 flex justify-center px-4 pb-2 md:hidden" data-testid="title-sticky-action">
          {action("pointer-events-auto w-full max-w-md shadow-lg")}
        </div>
      ) : null}
    </div>
  );
}
