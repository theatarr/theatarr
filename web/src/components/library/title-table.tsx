// The lists' table view (docs/design/ui.md "Library views"), on the shared DataTable: one line per title, a fixed Title
// column (thumbnail, name, an eye-off icon when not monitored) and the columns chosen for this list
// (lib/index-columns.ts), in order. Libraries are one pill each in the status colour (LibraryPill: "4K ·
// Remux-2160p", "WEB · Missing", "4K · 13/17"); a click on a pill opens the title's Libraries dialog
// (copy-dialog.tsx: file, stage, monitor switch). Status uses the poster badge's word, the same precedence as the
// posters (posterStatus), in the badge's tone (badgeTone), or a download's stage linked to Activity. Headers sort the
// grid where the server can. Below the phone width the rows are cards: title and Status, the pills, then each chosen
// column as a line. Without "See files" the file columns are not offered and Status reads today's watchability words,
// headed Availability. Managers get Search now and Interactive search at the end of each row and card (RowActions).
import { Download, EyeOff, ListVideo, Loader2, Search, UserSearch } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useSearchNow } from "@/api/add";
import type { Schemas } from "@/api/client";
import type { TitleRow } from "@/api/library";
import { TitleThumb } from "@/components/poster-card";
import { ProviderPills } from "@/components/provider-pills";
import { InteractiveSearch } from "@/components/title/interactive-search";
import { failed, searched } from "@/components/title/seasons";
import { Button } from "@/components/ui/button";
import { NO_LIVE_LIBRARY, searchOffWords } from "@/components/version-status";
import { DataTable, type DataColumn } from "@/components/ui/data-table";
import type { RowSelection } from "@/components/ui/row-select";
import { activityHref, copyBar, nextWords, stageTone, stageWords, titleAcquisition } from "@/lib/copies";
import { fmtAgo, fmtBytes, localDay } from "@/lib/format";
import {
  columnSort,
  languageWords,
  minAvailabilityWords,
  perLibrary,
  seriesTypeWords,
  sortColumn,
  type IndexColumnId,
} from "@/lib/index-columns";
import { badgeTone, copyTone, copyWords, type CopyTone } from "@/lib/library-status";
import { fmtDay, fmtRuntime, notAiredYet, POSTER_STATUS_LABEL, posterStatus, titleWatchWords, upcomingRelease, type PosterStatus } from "@/lib/library";
import { cn } from "@/lib/utils";
import { StageLink, useMinWidth } from "./copy-bits";
import { CopyDialog } from "./copy-dialog";
import { LibraryPill } from "./library-pill";
import { useEndReached } from "./poster-grid";
import { titleStatusWords } from "@/lib/tmdb-words";

type Version = NonNullable<TitleRow["versions"]>[number];
type Acquisition = Schemas["Acquisition"];

export type TableSort = { sort: string; sortVersion?: string; desc: boolean };

export type Selection = RowSelection;

type Props = {
  titles: TitleRow[];
  more: boolean;
  onMore: () => void;
  seeFiles: boolean;
  manage: boolean;
  /** The chosen columns, in order, with this list's labels. */
  columns: { id: IndexColumnId; label: string }[];
  sort: TableSort;
  /** A header asks for this grid sort. */
  onSort: (sort: string) => void;
  select?: Selection;
};

const TONE_INK: Record<CopyTone, string> = {
  ok: "text-[var(--ok-ink)]",
  bad: "text-[var(--bad-ink)]",
  warn: "text-[var(--warn-ink)]",
  progress: "text-[var(--accent-ink)]",
  idle: "text-muted-foreground",
};

const WATCH_INK = { positive: "text-[var(--ok-ink)]", partial: "text-[var(--warn-ink)]", neutral: "text-muted-foreground" } as const;

const dash = <span className="text-[var(--ink-3)]">—</span>;
const or = (s: string | number | undefined | null, cls?: string): ReactNode => (s || s === 0 ? <span className={cls}>{s}</span> : dash);
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** The badge words of a title that already has a file somewhere. */
const HELD: ReadonlySet<PosterStatus> = new Set(["available", "partly_available", "upgrade", "best_known", "some_episodes"]);

/** A download's stage beside the badge word, short so the Status column stays narrow: the percent while downloading,
 *  else a download icon; the stage's words are its tooltip and name. Linked to Activity, as StageLink. */
function CompactStage({ a }: { a: Acquisition }) {
  const words = stageWords(a);
  return (
    <Link to={activityHref(a.stage)} data-stage={a.stage} title={words} aria-label={words}
      className={cn("inline-flex items-center text-[13px] font-medium tabular-nums hover:underline", stageTone(a.stage) === "warning" ? "text-[var(--warn-ink)]" : "text-[var(--accent-ink)]")}>
      {a.stage === "downloading" && a.progress !== undefined ? `${Math.round(Math.max(0, Math.min(a.progress, 100)))}%` : <Download aria-hidden className="size-3.5" />}
    </Link>
  );
}

/** Status for an account that sees files: the poster badge's word (the day for Upcoming), and a download in hand's
 *  short stage after it ("Partly available · 64%", CompactStage); a title with no file yet shows the download's stage
 *  alone, in words. words: a phone card's head (no hover), which shows the stage in words after the badge word and
 *  wraps the status under the title when both do not fit. */
function StatusCell({ t, words = false }: { t: TitleRow; words?: boolean }) {
  const a = titleAcquisition(t);
  const s = posterStatus(t.availability, t.upgrade_seen, false, t.upgrade_state, t);
  if (a && !(s && HELD.has(s))) return <StageLink a={a.acquisition} className="text-[13px]" />;
  if (!s) return dash;
  const day = s === "upcoming" ? (upcomingRelease(t) ?? (notAiredYet(t) ? t.next_up?.date : undefined)) : undefined;
  const word = (
    <span data-testid="status-cell" className={cn("text-[13px] font-medium", TONE_INK[badgeTone(s)])}>
      {POSTER_STATUS_LABEL[s]}
      {day ? ` · ${fmtDay(day)}` : ""}
    </span>
  );
  if (!a) return word;
  return (
    <span className="inline-flex min-w-0 items-baseline gap-1 whitespace-nowrap">
      {word}
      <span aria-hidden className="text-[var(--ink-3)]">·</span>
      {words ? <StageLink a={a.acquisition} className="text-[13px]" /> : <CompactStage a={a.acquisition} />}
    </span>
  );
}

/** Without "See files": the title's one watchability line, today's words. */
function WatchCell({ t }: { t: TitleRow }) {
  const w = titleWatchWords(t);
  if (!w) return dash;
  return <span data-testid="watch-cell" className={cn("text-[13px] font-medium", WATCH_INK[w.tone])}>{w.label}</span>;
}

/** A library's words on its pill and in its name (copyWords), saying for a movie library without a file that is not
 *  monitored that it has no file, so a title listed under Partly explains itself. */
function pillWords(v: Version, t: TitleRow): string {
  if (t.kind !== "series" && !v.has_file && !v.acquisition && v.monitored === false) return "no file, not monitored";
  return copyWords(v, t);
}

/** A library pill's detail: a movie's quality or what it lacks ("Missing", "Downloading 37%", "No file · Not
 *  monitored"); a series' count, none before anything has aired (the grey dot, the Status column's Upcoming and the
 *  pill's tooltip say so). */
function pillDetail(v: Version, t: TitleRow): string | undefined {
  if (t.kind === "series") return v.coverage && v.coverage.total > 0 ? copyBar(v, "series").count : undefined;
  if (v.has_file) return v.quality || "Has a file";
  return pillWords(v, t).split(", ").map(cap).join(" · ");
}

function Pills({ t, onOpen }: { t: TitleRow; onOpen: () => void }) {
  const vs = t.versions ?? [];
  if (!vs.length) return dash;
  return (
    <span className="flex min-w-0 flex-wrap gap-x-1.5 gap-y-1">
      {vs.map((v) => {
        const label = v.label ?? v.key ?? "";
        const words = pillWords(v, t);
        return (
          <button
            key={v.key}
            type="button"
            onClick={onOpen}
            aria-label={`${t.name}, ${label}: ${words}`}
            className="inline-flex min-w-0 max-w-full items-center rounded-[4px] border border-[var(--line)] px-1.5 py-0.5 hover:border-[var(--line-2)] hover:bg-[var(--hover)]"
          >
            <LibraryPill label={label} tone={copyTone(v, t)} words={words} detail={pillDetail(v, t)} className="text-[11.5px]" />
          </button>
        );
      })}
    </span>
  );
}

const dayOf = (d: string | undefined | null) => (d ? fmtDay(d.slice(0, 10)) : "");

/** A chosen column's value for a title. */
function cell(id: IndexColumnId, t: TitleRow, open: () => void): ReactNode {
  const vs = t.versions ?? [];
  switch (id) {
    case "status":
      return <StatusCell t={t} />;
    case "libraries":
      return <Pills t={t} onOpen={open} />;
    case "network":
      return or(t.network, "text-muted-foreground");
    case "next":
      return or(nextWords(t.next_up, t.kind));
    case "seasons":
      return or(t.season_count, "tabular-nums");
    case "size":
      return or(t.size_bytes ? fmtBytes(t.size_bytes) : "", "tabular-nums");
    case "added":
      return or(t.title_added_at ? fmtDay(localDay(t.title_added_at)) : "", "text-muted-foreground");
    case "latest_file":
      return or(t.added_at ? fmtAgo(t.added_at) : "", "text-muted-foreground");
    case "previous":
      return or(dayOf(t.previous_airing));
    case "quality":
      return or(vs.length === 1 ? vs[0].quality : vs.map((v) => `${v.label ?? v.key}: ${v.quality || "no file"}`).join(" · "));
    case "profile":
      return or(perLibrary(vs, (i) => vs[i].profile));
    case "year":
      return or(t.year, "tabular-nums");
    case "release":
      return or(dayOf(t.release_date));
    case "in_cinemas":
      return or(dayOf(t.in_cinemas));
    case "digital":
      return or(dayOf(t.digital_release));
    case "physical":
      return or(dayOf(t.physical_release));
    case "title_status":
      return or(titleStatusWords(t.status));
    case "series_type":
      return or(seriesTypeWords(t.series_type));
    case "runtime":
      return or(fmtRuntime(t.runtime_minutes));
    case "certification":
      return or(t.certification);
    case "rating":
      return or(t.rating ? t.rating.toFixed(1) : "", "tabular-nums");
    case "genres":
      return or((t.genres ?? []).slice(0, 3).join(", "));
    case "language":
      return or(languageWords(t.original_language));
    case "min_availability":
      return or(perLibrary(vs, (i) => minAvailabilityWords(vs[i].minimum_availability)));
    case "provider":
      return <ProviderPills codes={[...new Set(vs.flatMap((v) => v.providers ?? []))]} />;
    case "folder": {
      const f = perLibrary(vs, (i) => vs[i].folder);
      return f ? <span className="block max-w-[22rem] truncate font-mono text-xs" title={f}>{f}</span> : dash;
    }
  }
}

function TitleCell({ t, seeFiles }: { t: TitleRow; seeFiles: boolean }) {
  return (
    <span className="flex min-w-0 items-center gap-2.5">
      <TitleThumb id={t.id} className="w-6" />
      <Link to={`/title/${t.id}`} className="min-w-0 truncate font-medium hover:text-primary" title={t.name}>
        {t.name}
      </Link>
      {seeFiles && t.monitored === false ? (
        <span className="shrink-0 text-[var(--ink-3)]" title="Not monitored">
          <EyeOff aria-hidden className="size-3.5" />
          <span className="sr-only">Not monitored</span>
        </span>
      ) : null}
    </span>
  );
}

/** Why the title's Search now would search nothing, from the row's libraries (each one's control and monitoring), in
 *  the title page's words (searchOffWords); "" when a monitored library can search. */
export function searchOff(vs: readonly Version[]): string {
  if (vs.some((v) => v.monitored && v.control?.can_search)) return "";
  const on = vs.filter((v) => v.monitored).map((v) => ({ label: v.label, key: v.key, grab_state: v.control?.grab_state }));
  return searchOffWords(vs.map((v) => v.control?.grab_state), !vs.some((v) => v.control?.mode === "active"), on);
}

/** A row's actions for managers: Search now (the title page's: every monitored Live library, missing and upgrades;
 *  the answer is a toast), then for a movie Interactive search in its first Live library, for a series a link to its
 *  Episodes tab (interactive search is per season or episode). Disabled with the reason as tooltip from the row's own
 *  data; a disabled button still shows its tooltip. On a phone the buttons carry their words. */
function RowActions({ t, wide }: { t: TitleRow; wide: boolean }) {
  const id = t.id ?? 0;
  const name = t.name ?? `Title ${id}`;
  const vs = t.versions ?? [];
  const search = useSearchNow(id);
  const [interactive, setInteractive] = useState(false);
  const off = searchOff(vs);
  const series = t.kind === "series";
  const live = vs.find((v) => v.control?.live_releases && v.key);
  const size = wide ? "icon-sm" : "sm";
  const tip = cn("disabled:pointer-events-auto disabled:cursor-not-allowed", !wide && "h-9");
  return (
    <>
      {/* A phone has no hover: a disabled Search now says why in a line above the buttons. */}
      {!wide && off ? <p className="w-full text-xs text-[var(--ink-3)]" data-testid="row-search-off">{off}</p> : null}
      <span className="inline-flex items-center gap-0.5" data-testid="row-actions">
        <Button size={size} variant="ghost" className={tip} disabled={!!off || search.isPending} aria-label={`Search now, ${name}`}
          title={off || (series ? "Search every monitored episode now, in every Live library (missing episodes and upgrades)" : "Search every monitored Live library now")}
          onClick={() => search.mutate(undefined, { onSuccess: (o) => searched(o, `/img/t/${id}/poster/w92`), onError: failed("Not searched") })}
          data-testid="row-search">
          {search.isPending ? <Loader2 className="animate-spin" /> : <Search />}
          {wide ? null : "Search now"}
        </Button>
        {series ? (
          <Button asChild size={size} variant="ghost" className={wide ? undefined : "h-9"}>
            <Link to={`/title/${id}?tab=episodes`} aria-label={`Choose season or episode, ${name}`} title="Choose season or episode" data-testid="row-episodes">
              <ListVideo />
              {wide ? null : "Choose season or episode"}
            </Link>
          </Button>
        ) : (
          <Button size={size} variant="ghost" className={tip} disabled={!live} aria-label={`Interactive search, ${name}`}
            title={live ? "Search now and choose the release yourself" : NO_LIVE_LIBRARY} onClick={() => setInteractive(true)} data-testid="row-interactive">
            <UserSearch />
            {wide ? null : "Interactive search"}
          </Button>
        )}
        {interactive && live ? <InteractiveSearch titleId={id} version={live.key} onClose={() => setInteractive(false)} /> : null}
      </span>
    </>
  );
}

export function TitleTable({ titles, more, onMore, seeFiles, manage, columns, sort, onSort, select }: Props) {
  const end = useEndReached(onMore, more);
  const [copiesOf, setCopiesOf] = useState<number | null>(null);
  const open = titles.find((x) => x.id === copiesOf);
  const sorted = sortColumn(sort.sort);
  const wide = useMinWidth(640);
  const cols: DataColumn<TitleRow>[] = [
    {
      id: "title",
      label: "Title",
      sortable: true,
      card: "none",
      headClassName: "min-w-[11rem]",
      cellClassName: "w-full max-w-0",
      cell: (t) => <TitleCell t={t} seeFiles={seeFiles} />,
    },
    ...columns.map(({ id, label }): DataColumn<TitleRow> => ({
      id,
      label,
      sortable: !!columnSort(id),
      card: id === "status" ? "none" : id === "libraries" ? "full" : "line",
      headClassName: cn("whitespace-nowrap", id === "size" && "text-right"),
      cellClassName: cn("whitespace-nowrap text-sm", id === "size" && "text-right", (id === "libraries" || id === "provider") && "[&>span]:flex-nowrap"),
      cell: (t) => (id === "status" && !seeFiles ? <WatchCell t={t} /> : cell(id, t, () => setCopiesOf(t.id ?? null))),
    })),
  ];
  const showStatus = columns.some((c) => c.id === "status");
  return (
    <>
      <DataTable
        testid="title-table"
        rows={titles}
        rowKey={(t) => t.id ?? 0}
        columns={cols}
        sort={sorted && !sort.sortVersion ? { column: sorted, dir: sort.desc ? "desc" : "asc" } : undefined}
        onSort={(c) => {
          const s = columnSort(c as IndexColumnId | "title");
          if (s) onSort(s);
        }}
        select={select}
        selectAllLabel={`Select all ${titles.length} shown`}
        rowLabel={(t) => t.name ?? String(t.id)}
        rowProps={() => ({ className: "cv-auto-row-sm" })}
        actions={manage ? (t) => <RowActions t={t} wide={wide} /> : undefined}
        cardHead={(t) => (
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-2 gap-y-0.5">
            <TitleCell t={t} seeFiles={seeFiles} />
            {showStatus ? <span className="shrink-0">{seeFiles ? <StatusCell t={t} words /> : <WatchCell t={t} />}</span> : null}
          </div>
        )}
      />
      {more ? <div ref={end} aria-hidden className="h-12" /> : null}
      {open ? <CopyDialog t={open} seeFiles={seeFiles} manage={manage} onClose={() => setCopiesOf(null)} /> : null}
    </>
  );
}
