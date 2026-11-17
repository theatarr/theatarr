// History (pages/activity.tsx, and the title page's History tab): what happened to the titles' files, worded like
// Radarr's (Grabbed, Imported, Adopted, Upgraded, Removed, Failed, Blocked, Added to / Not cached on a provider). The
// server builds the rows (journal history.go, from the stored rows; GET /activity?state=history, the title tab with
// title_id), so both places show the same rows. An upgrade is one row with the new release (its import folded in), an
// adoption that replaced a file one Adopted row (the removal folded in); "Replaced: …" under the release says what
// went, and the row opens to the replaced files, labelled. Activity filters by title, copy, event and since date in the URL
// (lib/activity-history.ts), newest first, 50 a page with Load more. Accounts that see files get a table (DataTable):
// Event (with its episodes on the title tab, and its reason) and Title, the columns they chose
// (lib/activity-history-columns.ts), and Retry on a failed download and the journal link. Household accounts and
// "View as a user" keep the simple rows grouped by day: no library key, no release. The episode window shows the
// same rows for one episode (EpisodeHistoryRows, from the episode endpoint) in the same table.
import { Columns3, History as HistoryIcon, NotebookPen } from "lucide-react";
import { createContext, useContext, useState } from "react";
import { Link } from "react-router";
import { useActivityHistory, type ActivityEvent } from "@/api/activity";
import { usePerms } from "@/api/library";
import { HistoryRetry } from "@/components/activity/download-actions";
import { HistoryFilters, useHistoryFilters } from "@/components/activity/history-filters";
import { Dash, episodesWords, FormatPills, Poster, StaleNotice, tight } from "@/components/activity/row-bits";
import { Empty, ErrorState, Loading, VersionTag } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ColumnPicker } from "@/components/ui/column-picker";
import { DataTable, type DataColumn } from "@/components/ui/data-table";
import { fmtBytes, fmtNumber, fmtTime, titleWithYear } from "@/lib/format";
import { cn } from "@/lib/utils";
import { adminLink, journalLink } from "@/lib/admin-links";
import { historyFilterCount, historyFiltersFor, historyParams } from "@/lib/activity-history";
import { eventMeaning, HISTORY_COLUMNS, HISTORY_DEFAULT, type HistoryColumnId } from "@/lib/activity-history-columns";
import { useColumnPrefs } from "@/lib/prefs";
import { ProviderTag } from "@/components/provider-pills";

const EVENT_TONE = {
  ok: "bg-[var(--ok-ink)]",
  info: "bg-[var(--accent)]",
  warn: "bg-[var(--warn)]",
  bad: "bg-[var(--bad-ink)]",
} as const;

function dayLabel(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  const y = new Date(today);
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: d.getFullYear() === today.getFullYear() ? undefined : "numeric" });
}

const titleName = (e: ActivityEvent) => titleWithYear(e.title.name, e.title.year);

/** Events whose file may be unknown on rows from before their facts were recorded. */
const FILE_EVENTS = new Set(["adopted", "deleted", "removed", "missing"]);

/** A row's episodes in short, the full list on hover; none when they are only the episode the window is about. */
function Episodes({ e, except }: { e: ActivityEvent; except?: string }) {
  if (!e.episodes?.length || (except && e.episodes.length === 1 && e.episodes[0] === except)) return null;
  return <p className="truncate text-xs text-muted-foreground" title={e.episodes.join(" ")} data-testid="history-episodes">{episodesWords(e.episodes)}</p>;
}

/** A simple row, for household accounts and "View as a user". */
function EventRow({ e }: { e: ActivityEvent }) {
  return (
    <li className="flex gap-3 px-3 py-2.5 sm:px-4" data-testid="history-row">
      {e.title.id ? (
        <Link to={`/title/${e.title.id}`} className="shrink-0" tabIndex={-1} aria-hidden>
          <Poster src={e.title.poster} className="w-9" />
        </Link>
      ) : <Poster src={e.title.poster} className="w-9" />}
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <div className="flex min-w-0 items-center gap-2">
            <span className={cn("size-2 shrink-0 rounded-full", EVENT_TONE[e.tone])} aria-hidden />
            <span className="text-sm font-medium">{e.words}</span>
          </div>
          <span className="text-xs tabular-nums text-muted-foreground" title={fmtTime(e.at)}>
            {new Date(e.at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
          </span>
        </div>
        <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-muted-foreground">
          {e.title.id ? (
            <Link to={`/title/${e.title.id}`} className="truncate text-foreground hover:text-primary">{titleName(e)}</Link>
          ) : <span className="truncate text-foreground">{titleName(e)}</span>}
        </div>
        <Episodes e={e} />
      </div>
    </li>
  );
}

/** The event's dot and words; the tooltip says what it means, then the journal's summary. */
function EventWords({ e }: { e: ActivityEvent }) {
  const tip = [eventMeaning(e.words), e.summary].filter(Boolean).join("\n");
  return (
    <span className="flex min-w-0 items-baseline gap-2" title={tip || undefined}>
      <span className={cn("size-2 shrink-0 translate-y-[-1px] rounded-full", EVENT_TONE[e.tone])} aria-hidden />
      <span className="text-[13px] font-medium leading-snug" data-testid="history-words">{e.words}</span>
    </span>
  );
}

/** The rows whose replaced files are open, and the toggle (HistoryList keeps them). */
const FilesOpen = createContext<{ open: ReadonlySet<string>; toggle: (id: string) => void }>({ open: new Set(), toggle: () => {} });

/** The files an upgrade or an adoption replaced: the toggle in the Event cell; the list opens under the row
 *  (ReplacedList). */
function ReplacedToggle({ e }: { e: ActivityEvent }) {
  const { open, toggle } = useContext(FilesOpen);
  const n = e.replaced?.length ?? 0;
  if (n === 0) return null;
  const shown = open.has(e.id);
  return (
    <button type="button" className="mt-0.5 block text-xs text-primary hover:underline" aria-expanded={shown} onClick={() => toggle(e.id)} data-testid="history-replaced-toggle">
      {shown ? (n === 1 ? "Hide the replaced file" : "Hide the replaced files") : n === 1 ? "Show the replaced file" : `Show the ${n} replaced files`}
    </button>
  );
}

/** The first episode a file held as [season, episode]; files without one go last. */
function episodeKey(eps: readonly string[] | undefined): [number, number] {
  const m = eps?.length ? /^S(\d+)E(\d+)/i.exec(eps[0]) : null;
  return m ? [Number(m[1]), Number(m[2])] : [Infinity, Infinity];
}

const fileName = (path: string) => path.split("/").pop() ?? "";
const scoreWords = (score: number | null | undefined) => (score !== undefined && score !== null ? fmtNumber(score) : "");

/** The files an upgrade or an adoption replaced, across the row, in episode order (then path): each file on its own
 *  lines, its parts labelled (file name, the path as tooltip; release; quality; score; episodes). When no file has a
 *  release name it is said once, not on every file. */
function ReplacedList({ e }: { e: ActivityEvent }) {
  const files = [...(e.replaced ?? [])].sort((a, b) => {
    const [as, ae] = episodeKey(a.episodes), [bs, be] = episodeKey(b.episodes);
    return as - bs || ae - be || a.path.localeCompare(b.path);
  });
  const named = files.some((f) => f.release);
  return (
    <div className="text-xs text-muted-foreground" data-testid="history-replaced">
      <p className="mb-1 font-semibold text-[var(--ink-2)]" data-testid="history-replaced-head">{files.length === 1 ? "Replaced file" : "Replaced files"}</p>
      {named ? null : <p className="mb-1">Release names not recorded.</p>}
      <ul className="space-y-2">
        {files.map((f, i) => (
          <li key={f.path || i} className="min-w-0">
            <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5">
              {f.path ? <><dt>File</dt><dd className="font-mono text-[11px] text-foreground [overflow-wrap:anywhere]" title={f.path}>{fileName(f.path)}</dd></> : null}
              {named ? <><dt>Release</dt><dd className="font-mono text-[11px] [overflow-wrap:anywhere]">{f.release || "Name not recorded"}</dd></> : null}
              {f.quality ? <><dt>Quality</dt><dd>{f.quality}</dd></> : null}
              {scoreWords(f.score) ? <><dt>Score</dt><dd className="tabular-nums">{scoreWords(f.score)}</dd></> : null}
              {f.episodes?.length ? <><dt>Episodes</dt><dd>{episodesWords(f.episodes)}</dd></> : null}
            </dl>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The line under an upgrade's or an adoption's release: what it replaced ("Replaced: <old release or file name> ·
 *  <quality> · <score>", or "Replaced N files"). */
function ReplacedLine({ e }: { e: ActivityEvent }) {
  const files = e.replaced ?? [];
  if (files.length === 0) return null;
  const f = files[0];
  const facts = files.length === 1 ? [f.quality, scoreWords(f.score)].filter(Boolean) : [];
  const name = files.length === 1 ? f.release || fileName(f.path) : "";
  const words = files.length === 1 ? ["Replaced: " + (name || "a file not recorded"), ...facts].join(" · ") : `Replaced ${files.length} files`;
  return (
    <span className="flex min-w-0 flex-wrap text-xs text-muted-foreground" title={words} data-testid="history-replaced-line">
      {name ? (
        <>
          <span className="shrink-0">Replaced:&nbsp;</span>
          <span className="min-w-0 flex-[1_1_4rem] font-mono max-sm:flex-auto max-sm:break-all sm:truncate">{name}</span>
          {facts.length ? <span className="shrink-0 whitespace-pre">{" · " + facts.join(" · ")}</span> : null}
        </>
      ) : words}
    </span>
  );
}

/** A row's reason; an adoption's ("From Sonarr") names its instance on hover. */
function Reason({ e, className }: { e: ActivityEvent; className?: string }) {
  if (!e.reason) return null;
  const tip = e.event === "adopted" && e.source ? e.source : e.reason;
  return <p className={cn("text-xs text-muted-foreground", className)} title={tip} data-testid="history-reason">{e.reason}</p>;
}

/** The Event cell: the words, the episodes when no Title column shows them, the reason and an upgrade's files toggle. */
function EventCell({ e, episodes, except }: { e: ActivityEvent; episodes: boolean; except?: string }) {
  return (
    <div className="min-w-0">
      <EventWords e={e} />
      {episodes ? <Episodes e={e} except={except} /> : null}
      <Reason e={e} className="mt-0.5 max-sm:break-words sm:line-clamp-2" />
      <ReplacedToggle e={e} />
    </div>
  );
}

function TitleCell({ e }: { e: ActivityEvent }) {
  return (
    <div className="min-w-0">
      {e.title.id ? (
        <Link to={`/title/${e.title.id}`} className="block truncate font-medium text-[var(--ink)] hover:text-primary">{titleName(e)}</Link>
      ) : <span className="block truncate font-medium text-[var(--ink)]">{titleName(e)}</span>}
      <Episodes e={e} />
    </div>
  );
}

/** The release ("File name not recorded" for a file event from before its facts were recorded) and, for an upgrade or an
 *  adoption, what it replaced. */
function ReleaseCell({ e }: { e: ActivityEvent }) {
  return (
    <div className="min-w-0">
      {e.release ? (
        <span className="block font-mono text-[12px] max-sm:break-all sm:truncate" title={e.release}>{e.release}</span>
      ) : FILE_EVENTS.has(e.event) ? <span className="text-[var(--ink-3)]">File name not recorded</span> : <Dash />}
      <ReplacedLine e={e} />
    </div>
  );
}

/** The score; an adopted file Theatarr never scored has none (a recorded 0 stays 0). */
function ScoreCell({ e }: { e: ActivityEvent }) {
  if (e.score !== undefined && e.score !== null) return <>{fmtNumber(e.score)}</>;
  return e.event === "adopted" ? <span className="text-[var(--ink-3)]" title="Adopted file; not scored" data-testid="history-unscored">—</span> : <Dash />;
}

function historyFixed(withTitle: boolean, except?: string): DataColumn<ActivityEvent>[] {
  const event: DataColumn<ActivityEvent> = { id: "event", label: "Event", card: "none", cellClassName: withTitle ? "min-w-[8rem] max-w-[10rem]" : "min-w-[9rem] max-w-[14rem]", cell: (e) => <EventCell e={e} episodes={!withTitle} except={except} /> };
  if (!withTitle) return [event];
  return [event, { id: "title", label: "Title", card: "none", cellClassName: "max-w-[12rem]", cell: (e) => <TitleCell e={e} /> }];
}

const HISTORY_OPTIONAL: Record<HistoryColumnId, DataColumn<ActivityEvent>> = {
  library: { id: "library", label: "Library", cell: (e) => (e.version ? <VersionTag k={e.version} className="text-[11px]" /> : <Dash />) },
  quality: { id: "quality", label: "Quality", cellClassName: "whitespace-nowrap", cell: (e) => e.quality || <Dash /> },
  score: { id: "score", label: "Score", cellClassName: "tabular-nums", cell: (e) => <ScoreCell e={e} /> },
  date: { id: "date", label: "Date", card: "none", cellClassName: "whitespace-nowrap tabular-nums", cell: (e) => fmtTime(e.at) },
  release: { id: "release", label: "Release", cellClassName: "max-w-[13rem]", cell: (e) => <ReleaseCell e={e} /> },
  provider: { id: "provider", label: "Provider", cellClassName: "whitespace-nowrap", cell: (e) => (e.provider ? <ProviderTag account={e.provider} /> : <Dash />) },
  indexer: { id: "indexer", label: "Indexer", cellClassName: "whitespace-nowrap", cell: (e) => e.indexer || <Dash /> },
  formats: { id: "formats", label: "Formats", cell: (e) => <FormatPills formats={e.formats} /> },
  size: { id: "size", label: "Size", cellClassName: "whitespace-nowrap tabular-nums", cell: (e) => (e.size ? fmtBytes(e.size) : <Dash />) },
};

function HistoryActions({ e }: { e: ActivityEvent }) {
  // The journal is an admin page; managers (see files) get the row without the link. A row without an entry (a copy
  // to another provider, a release unblocked since) has none.
  const admin = usePerms().admin;
  const journal = e.entry ? adminLink(admin, journalLink(e.entry)) : undefined;
  return (
    <span className="inline-flex items-center gap-1.5">
      <HistoryRetry e={e} />
      {journal ? (
        <Link to={journal} className="inline-flex size-6 items-center justify-center text-primary hover:underline" aria-label="Journal entry">
          <NotebookPen className="size-3.5" />
        </Link>
      ) : null}
    </span>
  );
}

/** History. titleId: one title's (the title page's History tab): no filters and no Title column, the same rows. */
export function HistoryTab({ admin, as, titleId }: { admin: boolean; as?: "user"; titleId?: number }) {
  const asked = useHistoryFilters();
  const filters = historyFiltersFor(asked, admin);
  const q = useActivityHistory({ as }, titleId ? { title_id: titleId } : historyParams(filters));
  const [prefs, savePrefs] = useColumnPrefs("activity-history", HISTORY_COLUMNS, HISTORY_DEFAULT);
  const [picking, setPicking] = useState(false);
  // A failed background refresh keeps the last rows of these filters (StaleNotice); the full error only when there
  // are none (a first load, or new filters still showing the old ones' rows).
  const failed = q.error && (!q.data || q.isPlaceholderData);
  const columns = admin ? (
    <Button variant="outline" size="sm" aria-label="Columns" onClick={() => setPicking(true)}>
      <Columns3 />
      <span className="hidden sm:inline">Columns</span>
    </Button>
  ) : undefined;
  return (
    <div>
      {titleId ? (columns ? <div className="mb-3 flex justify-end">{columns}</div> : null) : <HistoryFilters admin={admin} tools={columns} />}
      {q.isPending ? <Loading /> : failed ? <ErrorState error={q.error} /> : (
        <>
          {q.error ? <StaleNotice at={q.dataUpdatedAt} onRetry={() => void q.refetch()} retrying={q.isFetching} /> : null}
          <HistoryList q={q} admin={admin} prefs={prefs} filtered={!titleId && historyFilterCount(filters) > 0} withTitle={!titleId} />
        </>
      )}
      {picking ? (
        <ColumnPicker
          label="History columns"
          note="Event, Title and the row actions always stay. On a phone the extra columns show under each event."
          columns={HISTORY_COLUMNS}
          prefs={prefs}
          onChange={savePrefs}
          onClose={() => setPicking(false)}
        />
      ) : null}
    </div>
  );
}

/** The table of History rows for those who see files (Activity, the title tab, the episode window), each row's
 *  replaced files opening under it. except: the episode the episode window is about. */
function HistoryTable({ items, columns, withTitle, except }: { items: ActivityEvent[]; columns: DataColumn<ActivityEvent>[]; withTitle: boolean; except?: string }) {
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const toggle = (id: string) => setOpen((o) => { const n = new Set(o); if (!n.delete(id)) n.add(id); return n; });
  return (
    <FilesOpen.Provider value={{ open, toggle }}>
      <DataTable
        rows={items}
        rowKey={(e) => e.id}
        columns={columns}
        testid="history-table"
        rowProps={() => ({ "data-testid": "history-row" })}
        cardHead={(e) => (
          <>
            <div className="flex flex-wrap items-baseline justify-between gap-x-3">
              <EventWords e={e} />
              <span className="text-xs tabular-nums text-muted-foreground">{fmtTime(e.at)}</span>
            </div>
            {withTitle ? <TitleCell e={e} /> : <Episodes e={e} except={except} />}
            <Reason e={e} />
            <ReplacedToggle e={e} />
          </>
        )}
        actions={(e) => <HistoryActions e={e} />}
        expanded={(e) => (open.has(e.id) && e.replaced?.length ? <ReplacedList e={e} /> : null)}
      />
    </FilesOpen.Provider>
  );
}

/** The episode window's columns: the window heads each library, and has no column choice. */
const EPISODE_COLUMNS: HistoryColumnId[] = ["quality", "score", "date", "release"];

/** One episode's History in one library (the episode window): the rows the title's History shows for it (the episode
 *  endpoint reads them with the same reader and words), in the same table. */
export function EpisodeHistoryRows({ events, episode }: { events: ActivityEvent[]; episode: string }) {
  if (events.length === 0) return <p className="text-sm text-muted-foreground">No history yet.</p>;
  // Narrower than the page's, so the table fits the window without scrolling sideways.
  const columns = tight([...historyFixed(false, episode), ...EPISODE_COLUMNS.map((id) => HISTORY_OPTIONAL[id])]).map((c) =>
    c.id === "release" ? { ...c, cellClassName: "px-2 max-w-[9rem]" } : c.id === "event" ? { ...c, cellClassName: "px-2 min-w-[7rem] max-w-[10rem]" } : c,
  );
  // The window is its own scroll box: the header sticks at its top, not under the page's top bar (--top).
  return (
    <div className="[--top:0px]">
      <HistoryTable items={events} columns={columns} withTitle={false} except={episode} />
    </div>
  );
}

function HistoryList({ q, admin, prefs, filtered, withTitle }: { q: ReturnType<typeof useActivityHistory>; admin: boolean; prefs: { order: HistoryColumnId[]; shown: HistoryColumnId[] }; filtered: boolean; withTitle: boolean }) {
  const items = q.data?.pages.flatMap((p) => p.history ?? []) ?? [];
  if (items.length === 0) {
    return (
      <Card>
        <Empty icon={HistoryIcon}>
          {filtered ? (q.hasNextPage ? "Nothing matches on this page." : "Nothing matches these filters.") : "Nothing has happened yet."}
        </Empty>
        {q.hasNextPage ? (
          <div className="flex justify-center pb-4">
            <Button variant="outline" onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>
              {q.isFetchingNextPage ? "Loading…" : "Look further back"}
            </Button>
          </div>
        ) : null}
      </Card>
    );
  }
  const more = q.hasNextPage ? (
    <div className="flex justify-center">
      <Button variant="outline" onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>
        {q.isFetchingNextPage ? "Loading…" : "Load more"}
      </Button>
    </div>
  ) : null;
  if (admin) {
    const columns = tight([...historyFixed(withTitle), ...prefs.order.filter((id) => prefs.shown.includes(id)).map((id) => HISTORY_OPTIONAL[id])]);
    return (
      <div className="space-y-4">
        <HistoryTable items={items} columns={columns} withTitle={withTitle} />
        {more}
      </div>
    );
  }
  const groups: { day: string; items: ActivityEvent[] }[] = [];
  for (const e of items) {
    const day = dayLabel(e.at);
    if (groups.length === 0 || groups[groups.length - 1].day !== day) groups.push({ day, items: [] });
    groups[groups.length - 1].items.push(e);
  }
  return (
    <div className="space-y-5">
      {groups.map((g) => (
        <section key={g.day}>
          <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-[0.14em] text-[var(--ink-3)]">{g.day}</h3>
          <Card className="p-0">
            <ul className="divide-y divide-border">
              {g.items.map((e) => (
                <EventRow key={e.id} e={e} />
              ))}
            </ul>
          </Card>
        </section>
      ))}
      {more}
    </div>
  );
}
