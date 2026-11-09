// The title page (docs/design/ui.md, Title page). A compact hero answers why someone opened the title: poster, title,
// year, essential facts (certification, runtime, genres, language, rating), whether it can be watched ("Ready to
// watch in 4K"), a short synopsis with More, and the primary action the role allows (Add another library, Request or View
// request, title-request.tsx TitleRequestButton) beside the title; on phones that action also sticks above the bottom tabs. Callers with "see files" get
// the copies right under it (a movie; a series has them on its Copies tab), the one place copies are managed
// (versions.tsx). Then tabs (lib/title-layout.ts): Episodes (series, the default), Copies (series), Watch order (anime
// with a franchise), About (overview, the fact table, links; admins also the technical details) and History.
// Family accounts (no "see files") get readiness, languages and their requests, no copy management. The backdrop
// fading into the theme's background and the poster/title arrangement are adapted from Seerr
// (github.com/seerr-team/seerr) src/components/MovieDetails/index.tsx and src/styles/globals.css (.media-page,
// .media-header, .media-poster, .media-title, .media-attributes, .media-facts) @ c167958a, MIT (Copyright (c) 2020
// sct): the classes are inlined as Tailwind and the hero is made shorter.
import { ExternalLink, Plus, RefreshCw, Star } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import type { AddTarget } from "@/api/add";
import { ApiError } from "@/api/client";
import { usePerms, useRefreshTitle, useTitle, type TitleDetail, type Version } from "@/api/library";
import { useRename, useRenamePreview } from "@/api/rename";
import { renameButton } from "@/lib/rename";
import { PRODUCT_NAME } from "@/brand";
import { Empty, ErrorState, Loading } from "@/components/page";
import { useSearchUI } from "@/components/search/search-context";
import { AnimeOrder } from "@/components/title/anime-order";
import { Details, TitleJournal, outsideLinks } from "@/components/title/details";
import { Seasons } from "@/components/title/seasons";
import { TitleRemove } from "@/components/title/copy-actions";
import { ShowReleases } from "@/components/title/show-releases";
import { TitleAddActions } from "@/components/title/title-add-actions";
import { RequestMoreSeasons, TitleRequest, TitleRequestButton } from "@/components/title/title-request";
import { CopyCards, History } from "@/components/title/versions";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { fmtAgo, fmtBytes } from "@/lib/format";
import { fmtDay, fmtRuntime } from "@/lib/library";
import { readiness, resolveTab, titleTabs, type TitleTab } from "@/lib/title-layout";
import { cn } from "@/lib/utils";

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-[var(--line)] px-3 py-2 last:border-b-0">
      <span className="text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--ink-3)]">{label}</span>
      <span className="min-w-0 text-right text-[var(--ink-2)]">{children}</span>
    </div>
  );
}

function firstDate(d: TitleDetail, type: string): string | undefined {
  const all = (d.release_dates ?? []).filter((r) => r.type === type && (r.country === "US" || r.country === "")).map((r) => r.date ?? "");
  return all.sort()[0];
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

/** The primary action the role allows: Add another library (add), else Request / View request (request). */
function PrimaryAction({ target, has, className }: { target: Target; has: boolean; className?: string }) {
  const perms = usePerms();
  const { openAdd } = useSearchUI();
  if (perms.add) {
    return (
      <Button onClick={() => openAdd(target)} className={className} data-testid="hero-add">
        <Plus /> {has ? "Add another library" : "Add"}
      </Button>
    );
  }
  if (perms.request) return <TitleRequestButton target={target} className={className} />;
  return null;
}

function About({ d }: { d: TitleDetail }) {
  const perms = usePerms();
  const refresh = useRefreshTitle(String(d.id ?? ""));
  const renamePreview = useRenamePreview(d.id ?? 0, perms.admin && !!d.id);
  const rename = useRename(d.id ?? 0);
  const renameState = renameButton(renamePreview, rename);
  const m = d.meta;
  const series = d.kind === "series";
  const runtime = fmtRuntime(d.runtime_minutes ?? m?.runtime_minutes);
  const digital = firstDate(d, "digital");
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
            {perms.manage ? (
              <Button variant="outline" size="sm" onClick={() => refresh.mutate()} disabled={refresh.isPending} title="Fetch the TMDb metadata and art again">
                <RefreshCw className={cn(refresh.isPending && "animate-spin")} />
                {refresh.isSuccess ? "Queued" : "Refresh metadata"}
              </Button>
            ) : null}
            {perms.manage && refresh.error ? (
              <p role="alert" className="w-full rounded-[5px] border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-3 py-2 text-xs" data-testid="title-refresh-error">
                Refresh failed: {refresh.error.message}
              </p>
            ) : null}
            {perms.manage && d.id && (d.versions?.length ?? 0) > 0 ? <TitleRemove titleId={d.id} /> : null}
            {perms.admin && d.id ? (
              <>
                <Button variant="outline" size="sm" onClick={() => rename.mutate()} disabled={renameState.disabled} title={renameState.title}
                  data-testid="title-rename">
                  {renameState.label}
                </Button>
                {renameState.error ? (
                  <p role="alert" className="w-full rounded-[5px] border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-3 py-2 text-xs" data-testid="title-rename-error">
                    {renameState.error}
                  </p>
                ) : null}
              </>
            ) : null}
          </div>
        </div>
        <div className="w-full lg:w-80 lg:shrink-0">
          <div className="overflow-hidden rounded-[8px] border border-[var(--line)] bg-card text-sm" data-testid="title-facts">
            {m?.status ? <Fact label="Status">{m.status}</Fact> : null}
            {d.release_date ? <Fact label={series ? "First aired" : "Released"}>{fmtDay(d.release_date)}</Fact> : null}
            {!series && digital ? <Fact label="Digital">{fmtDay(digital)}</Fact> : null}
            {series && m?.next_air_date ? <Fact label="Next episode">{fmtDay(m.next_air_date)}</Fact> : null}
            {series && !m?.next_air_date && m?.last_air_date ? <Fact label="Last aired">{fmtDay(m.last_air_date)}</Fact> : null}
            {runtime ? <Fact label="Runtime">{runtime}</Fact> : null}
            {m?.network ? <Fact label={series ? "Network" : "Studio"}>{m.network}</Fact> : null}
            {m?.original_language ? <Fact label="Language">{languageName(m.original_language)}</Fact> : null}
            {d.rating ? <Fact label="TMDb rating">{d.rating.toFixed(1)} <span className="text-xs">({m?.rating_votes} votes)</span></Fact> : null}
            {d.added_at ? <Fact label="Added">{fmtAgo(d.added_at)}</Fact> : null}
            {d.size_bytes ? <Fact label="On disk">{fmtBytes(d.size_bytes)}</Fact> : null}
            {perms.seeFiles && !d.monitored ? <Fact label="Monitored">No</Fact> : null}
          </div>
        </div>
      </div>
      {perms.admin ? <Details d={d} /> : null}
    </div>
  );
}

/** History: each copy's release history (see files), and the title's journal for admins. */
function TitleHistory({ d, versions }: { d: TitleDetail; versions: Version[] }) {
  const perms = usePerms();
  return (
    <div className="space-y-5" data-testid="title-history">
      {versions.length === 0 ? <Card><Empty>No libraries yet.</Empty></Card> : null}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {versions.map((v) => (
          <Card key={v.id}>
            <h3 className="mb-3 flex items-center gap-2 font-semibold text-[var(--ink)]">
              <span className="size-2.5 rounded-[3px] bg-[var(--vc)]" data-vkey={v.key} aria-hidden />
              {v.label ?? v.key}
            </h3>
            <History events={v.history ?? []} titleId={v.title_id ?? d.id} version={v.key} />
          </Card>
        ))}
      </div>
      {perms.admin ? <TitleJournal id={d.id ?? 0} /> : null}
    </div>
  );
}

/** The copies block: header (search now, add), the cards, and the releases panel for managers. */
function Copies({ d, id, versions, target }: { d: TitleDetail; id: string; versions: Version[]; target: Target }) {
  const perms = usePerms();
  return (
    <section className="space-y-3" data-testid="title-copies">
      <TitleAddActions titleId={Number(id)} name={target.name} year={target.year} kind={target.kind} poster={target.poster} anime={!!d.anime} add={false} cardNotes />
      <CopyCards versions={versions} />
      {perms.manage ? <ShowReleases titleId={Number(id)} /> : null}
    </section>
  );
}

export function TitlePage() {
  const { id = "" } = useParams();
  const q = useTitle(id);
  const perms = usePerms();
  const [params, setParams] = useSearchParams();
  const d = q.data;
  useEffect(() => {
    if (d?.name) document.title = `${d.name}${d.year ? ` (${d.year})` : ""} · ${PRODUCT_NAME}`;
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

  const versions = (d.versions ?? []) as Version[];
  const m = d.meta;
  const series = d.kind === "series";
  const franchises = d.franchises ?? [];
  const tabs = titleTabs({ series, franchise: franchises.length > 0, seeFiles: perms.seeFiles });
  const tab: TitleTab = resolveTab(params.get("tab"), tabs);
  const ready = readiness(d.kind, versions, !perms.seeFiles && !!d.acquiring);
  const runtime = fmtRuntime(d.runtime_minutes ?? m?.runtime_minutes);
  const lang = languageName(m?.original_language);
  const attrs = [
    d.certification ? <span key="c" className="rounded-[3px] px-1 py-px text-[11px] font-semibold leading-none shadow-[inset_0_0_0_1px_var(--line-2)]">{d.certification}</span> : null,
    runtime ? <span key="r">{runtime}{series ? " per episode" : ""}</span> : null,
    (d.genres ?? []).length ? <span key="g">{(d.genres ?? []).slice(0, 3).join(", ")}</span> : null,
    lang ? <span key="l">{lang}</span> : null,
  ].filter(Boolean);
  const audio = [...new Set(versions.flatMap((v) => (v.has_file ? (v.current?.languages ?? []) : [])))];
  const target: Target = { kind: series ? "series" : "movie", name: d.name ?? `Title ${id}`, year: d.year ?? undefined, poster: d.poster ?? undefined, title_id: Number(id), tmdb_id: d.tmdb_id ?? undefined, anime: !!d.anime };
  const has = versions.length > 0;
  const action = perms.add || perms.request;

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
              {d.name}
              {d.year ? <span className="ml-2 text-base font-normal text-muted-foreground sm:text-2xl">({d.year})</span> : null}
            </h1>
            <PrimaryAction target={target} has={has} className="max-md:hidden" />
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
            <Chip data-testid="title-readiness" tone={ready.tone === "positive" ? "ok" : ready.tone === "partial" ? "info" : "ghost"} data-tone={ready.tone}>
              {ready.label}
            </Chip>
            {audio.length > 0 ? <span className="text-xs text-muted-foreground">Audio: {audio.join(", ")}</span> : null}
            {perms.seeFiles && !d.monitored ? <Chip tone="ghost">Not monitored</Chip> : null}
          </div>
        </div>
        <div className="col-span-2 max-w-3xl sm:col-span-1 sm:col-start-2">
          {m?.overview ? <Synopsis text={m.overview} /> : null}
        </div>
      </div>

      {/* Requests (accounts that request rather than add): their status for this title. */}
      {!perms.add && perms.request ? (
        <div className="mt-5">
          <TitleRequest target={target} versions={versions} />
        </div>
      ) : null}

      {/* A movie's copies right under the hero (series: the Copies tab). */}
      {perms.seeFiles && !series ? (
        <div className="mt-6">
          <Copies d={d} id={id} versions={versions} target={target} />
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
      {tab === "copies" ? <Copies d={d} id={id} versions={versions} target={target} /> : null}
      {tab === "order" ? <AnimeOrder franchises={franchises} admin={perms.admin} /> : null}
      {tab === "about" ? <About d={d} /> : null}
      {tab === "history" ? <TitleHistory d={d} versions={versions} /> : null}

      {/* Phones: the primary action stays reachable above the bottom tabs. */}
      {action ? (
        <div className="fixed inset-x-0 bottom-[calc(3.75rem+env(safe-area-inset-bottom))] pointer-events-none z-30 flex justify-center px-4 pb-2 md:hidden" data-testid="title-sticky-action">
          <PrimaryAction target={target} has={has} className="pointer-events-auto w-full max-w-md shadow-lg" />
        </div>
      ) : null}
    </div>
  );
}
