// Activity › Queue (pages/activity.tsx): every download in hand, filtered by Needs attention, Downloading, Waiting or
// All (?show=, lib/activity.ts). Accounts that see files get a table (DataTable): Status and Title, the columns they
// chose (lib/activity-queue-columns.ts), sorted in the browser, and Details/Inspect, Retry and Remove on each row;
// Details opens the steps and the file detail under the row. Household accounts and "View as a user" keep the simple
// rows: title, stage, progress or wait, and Details with the steps. Row layout after Scryer's activity queue
// (github.com/scryer-media/scryer apps/scryer-web/components/views/activity/queue-row-presentation.tsx @ 2ef7757c,
// GPL-3.0: the status pill, title and release lines, and the details panel's small uppercase labels).
import { ChevronDown, ChevronUp, Columns3, HelpCircle, Inbox, NotebookPen } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { removeDownload, useActivityAttempts, useActivityNow, type ActivityDownload } from "@/api/activity";
import { useAuth } from "@/api/auth";
import { usePerms } from "@/api/library";
import { BulkDialog, SelectionLine, useChosen } from "@/components/activity/bulk";
import { DownloadActions } from "@/components/activity/download-actions";
import { Dash, episodesWords, FormatPills, Poster, tight, TitleLine } from "@/components/activity/row-bits";
import { ProgressBar, StepTrack, toneOf } from "@/components/activity-bits";
import { Empty, ErrorState, Loading, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ColumnPicker, OptionsDialog } from "@/components/ui/column-picker";
import { Choice } from "@/components/ui/confirm-dialog";
import { DataTable, type DataColumn } from "@/components/ui/data-table";
import { fmtAgo, fmtBytes, fmtTime } from "@/lib/format";
import { bulkWords, filterDownloads, needsAttention, showCounts, SHOWS, STAGE_MEANINGS, stageLabel, stageMeaning, timing, type ActivityShow } from "@/lib/activity";
import {
  isQueueSortColumn,
  nextQueueSort,
  normalizeQueueSort,
  QUEUE_COLUMNS,
  QUEUE_DEFAULT,
  QUEUE_SORT_PREF,
  sortDownloads,
  type QueueColumnId,
  type QueueSort,
} from "@/lib/activity-queue-columns";
import { readPref, useColumnPrefs, writePref } from "@/lib/prefs";
import { SHARED_TORRENT } from "@/lib/ops";
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { adminLink, journalLink } from "@/lib/admin-links";
import { FAILED_WORDS } from "@/lib/request-timeline";
import { providerLabel } from "@/components/provider-pills";
import { InfoHash } from "@/components/title/info-hash";

const WORD_TONE = {
  active: "text-[var(--accent-ink)]",
  ok: "text-[var(--ok-ink)]",
  warn: "text-[var(--warn)]",
  bad: "text-[var(--bad-ink)]",
  idle: "text-muted-foreground",
} as const;

function Label({ children }: { children: React.ReactNode }) {
  return <p className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">{children}</p>;
}

function DownloadDetails({ d }: { d: ActivityDownload }) {
  const history = useActivityAttempts(d.id);
  const attempts = history.data?.attempts ?? d.attempts ?? [];
  const admin = usePerms().admin;
  const journal = d.journal ? adminLink(admin, journalLink(d.journal)) : undefined;
  return (
    <div className="mt-3 grid gap-4 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] p-3 md:grid-cols-2">
      <div className="min-w-0 space-y-3">
        <div>
          <Label>Release</Label>
          <p className="mt-1 break-all font-mono text-[12.5px] text-foreground">{d.release}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            {[d.quality, d.score !== undefined ? `score ${d.score}` : "", d.size ? fmtBytes(d.size) : "", d.indexer].filter(Boolean).join(" · ")}
          </p>
        </div>
        {d.info_hash ? (
          <div>
            <Label>Hash</Label>
            <div className="mt-1"><InfoHash hash={d.info_hash} admin={admin} /></div>
          </div>
        ) : null}
        {d.error ? (
          <div>
            <Label>Message</Label>
            <p className="mt-1 whitespace-pre-wrap break-words text-sm text-[var(--warn-ink)]">{d.error}</p>
          </div>
        ) : null}
      </div>
      <div className="min-w-0 space-y-3">
        <div>
          <Label>Provider attempts</Label>
          {history.isPending ? <p role="status" className="mt-1 text-sm text-muted-foreground">Loading full attempt history…</p> : null}
          {history.error ? <div className="mt-1 text-sm"><p role="alert">Could not load full attempt history.</p><Button size="xs" variant="outline" onClick={() => void history.refetch()}>Retry attempt history</Button></div> : null}
          {!history.data && d.attempts_more ? <p className="mt-1 text-xs text-muted-foreground">Showing the latest {attempts.length} {attempts.length === 1 ? "attempt" : "attempts"}.</p> : null}
          {attempts.length === 0 && !history.isPending && !history.error ? (
            <p className="mt-1 text-sm text-muted-foreground">None recorded.</p>
          ) : (
            <ul className="mt-1 space-y-1 text-sm">
              {attempts.map((a, i) => (
                <li key={i} className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-medium" title={a.provider}>{providerLabel(a.provider)}</span>
                  <Badge tone={a.outcome === "accepted" ? "positive" : a.outcome === "deferred" ? "warning" : a.outcome === "skipped" ? "neutral" : "negative"}>
                    {a.outcome}
                    {a.code ? ` · ${a.code}` : ""}
                  </Badge>
                  <span className="text-xs text-muted-foreground">{fmtTime(a.at)}</span>
                  {a.message ? <span className="w-full break-words text-xs text-muted-foreground">{a.message}</span> : null}
                </li>
              ))}
            </ul>
          )}
        </div>
        <div>
          <Label>Cache evidence</Label>
          {(d.cache ?? []).length === 0 ? (
            <p className="mt-1 text-sm text-muted-foreground">None for this hash.</p>
          ) : (
            <div className="mt-1 flex flex-wrap gap-1.5">
              {(d.cache ?? []).map((c, i) => (
                <Badge key={i} tone={c.state === "cached" ? "positive" : c.state === "miss" || c.state === "uncached" ? "negative" : "neutral"}>
                  {providerLabel(c.provider)} {c.state} <span className="opacity-70">({c.source})</span>
                </Badge>
              ))}
            </div>
          )}
        </div>
        {journal ? (
          <Link to={journal} className="inline-flex items-center gap-1.5 text-sm text-primary hover:underline">
            <NotebookPen className="size-4" /> Journal entry
          </Link>
        ) : null}
      </div>
    </div>
  );
}

function Stage({ d, admin, attention }: { d: ActivityDownload; admin: boolean; attention: boolean }) {
  const label = stageLabel(d);
  const tone = attention && toneOf(d) !== "bad" ? "warn" : toneOf(d);
  const words = admin && d.detail ? d.detail : d.words;
  return (
    <div className="min-w-0">
      <p className={cn("text-sm font-medium", WORD_TONE[tone])} data-testid="stage" title={stageMeaning(label) || undefined}>{label}</p>
      {words && words !== label ? <p className="truncate text-xs text-muted-foreground" data-testid="words" title={words}>{words}</p> : null}
    </div>
  );
}

/** Whether the row shows a progress bar: while it is with the provider or being linked. */
const hasBar = (d: ActivityDownload) => d.state === "downloading" || d.state === "added" || d.state === "ready" || d.state === "importing";

function Progress({ d, now }: { d: ActivityDownload; now: number }) {
  const tone = toneOf(d);
  const showBar = hasBar(d);
  const t = timing(d, now);
  const parts = [
    t.waited ? `waiting ${t.waited}` : null,
    t.updated ? `updated ${t.updated}` : null,
    t.retry ? `next try ${t.retry}` : null,
  ].filter(Boolean);
  return (
    <div className="min-w-0">
      {showBar ? (
        <div className="flex items-center gap-2">
          <ProgressBar pct={d.progress} tone={tone} />
          <span className="w-9 text-right text-xs font-semibold tabular-nums">{Math.round(d.progress)}%</span>
        </div>
      ) : null}
      {parts.length > 0 ? <p className={cn("text-xs text-muted-foreground", showBar && "mt-1")} data-testid="timing">{parts.join(" · ")}</p> : null}
    </div>
  );
}

/** The table's Progress cell: the bar while it moves, and how long it has waited and the next try (Last update is its
 * own column). */
function QueueProgress({ d, now, empty = <Dash /> }: { d: ActivityDownload; now: number; empty?: ReactNode }) {
  const showBar = hasBar(d);
  const t = timing(d, now);
  const parts = [t.waited ? `waiting ${t.waited}` : null, t.retry ? `next try ${t.retry}` : null].filter(Boolean);
  if (!showBar && parts.length === 0) return <>{empty}</>;
  return (
    <div className="w-[5.5rem] min-w-0">
      {showBar ? (
        <div className="flex items-center gap-2">
          <ProgressBar pct={d.progress} tone={toneOf(d)} />
          <span className="w-9 text-right text-xs font-semibold tabular-nums">{Math.round(d.progress)}%</span>
        </div>
      ) : null}
      {parts.length > 0 ? <p className={cn("text-xs text-muted-foreground", showBar && "mt-0.5")} data-testid="timing">{parts.join(" · ")}</p> : null}
    </div>
  );
}

/** A simple row, for household accounts and "View as a user": no library key, no release, no provider. */
function DownloadRow({ d, now }: { d: ActivityDownload; now: number }) {
  const [open, setOpen] = useState(false);
  const attention = needsAttention(d, now);
  return (
    <li className={cn("px-3 py-2 sm:px-4", open && "bg-[var(--sunken)]")} data-testid="download" data-state={d.state} data-attention={attention || undefined}>
      <div className="flex gap-3 md:grid md:grid-cols-[1.75rem_minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1fr)_5.5rem] md:items-center md:gap-4">
        {d.title.id ? (
          <Link to={`/title/${d.title.id}`} className="shrink-0 self-start md:self-center" tabIndex={-1} aria-hidden>
            <Poster src={d.title.poster} className="w-7" />
          </Link>
        ) : <Poster src={d.title.poster} className="w-7 self-start md:self-center" />}
        <div className="min-w-0 flex-1">
          <TitleLine t={d.title} />
          <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            {d.quality ? <Badge tone="outline">{resolutionWords(d.quality) || d.quality}</Badge> : null}
            {d.episodes && d.episodes.length > 0 ? <span>{episodesWords(d.episodes)}</span> : null}
          </div>
          <div className="mt-1.5 space-y-1 md:hidden">
            <Stage d={d} admin={false} attention={attention} />
            <Progress d={d} now={now} />
          </div>
        </div>
        <div className="hidden md:block"><Stage d={d} admin={false} attention={attention} /></div>
        <div className="hidden md:block"><Progress d={d} now={now} /></div>
        <div className="shrink-0 self-start md:self-center md:text-right">
          <DetailsButton attention={attention} open={open} onClick={() => setOpen((o) => !o)} />
        </div>
      </div>
      {open ? (
        <div className="mt-2 border-t border-[var(--line)] pt-3 md:pl-[2.75rem]" data-testid="download-open">
          <div className="max-w-xl">
            <StepTrack steps={d.steps} />
          </div>
          {d.state === "failed" && !d.not_started ? <p className="mt-2 text-sm" data-testid="failed-words">{FAILED_WORDS}</p> : null}
          <p className="mt-2 text-xs text-muted-foreground">Added {fmtAgo(d.created_at)}</p>
        </div>
      ) : null}
    </li>
  );
}

/** Details, or Inspect when the row needs attention; the chevron is left out in the table, where width is short. */
function DetailsButton({ attention, open, onClick, chevron = true }: { attention: boolean; open: boolean; onClick: () => void; chevron?: boolean }) {
  return (
    <Button size="xs" variant={attention ? "outline" : "ghost"} onClick={onClick} aria-expanded={open} data-testid="download-details">
      {attention ? "Inspect" : "Details"} {chevron ? open ? <ChevronUp /> : <ChevronDown /> : null}
    </Button>
  );
}

/** The title's name and year on one line (the library tag says the kind). */
function TableTitle({ t }: { t: ActivityDownload["title"] }) {
  const name = "min-w-0 truncate font-semibold text-[var(--ink)]";
  return (
    <div className="flex min-w-0 items-baseline gap-1.5">
      {t.id ? <Link to={`/title/${t.id}`} className={`${name} hover:text-primary`} title={t.name}>{t.name}</Link> : <span className={name} title={t.name}>{t.name}</span>}
      {t.year ? <span className="shrink-0 text-xs text-muted-foreground">{t.year}</span> : null}
    </div>
  );
}

/** The provider (or download client) and whether it was cached or reused. */
function ProviderCell({ d }: { d: ActivityDownload }) {
  if (!d.provider_name && !d.client) return <Dash />;
  return (
    <span className="block whitespace-nowrap">
      {d.provider_name ? <span className="block">{d.provider_name}</span> : <span className="block">client {d.client}</span>}
      {d.cached || d.reused ? (
        <span className="block text-[11px] leading-tight">
          {d.cached ? <span className="text-[var(--ok-ink)]">cached</span> : null}
          {d.cached && d.reused ? " · " : null}
          {d.reused ? <span className="text-muted-foreground">reused</span> : null}
        </span>
      ) : null}
    </span>
  );
}

function Ago({ iso }: { iso: string }) {
  return <span className="whitespace-nowrap" title={fmtTime(iso)}>{fmtAgo(iso)}</span>;
}

/** Every Queue column; the page shows Status, Title and the chosen ones. */
function queueColumns(now: number): { fixed: DataColumn<ActivityDownload>[]; optional: Record<QueueColumnId, DataColumn<ActivityDownload>> } {
  return {
    fixed: [
      { id: "status", label: "Status", card: "none", cellClassName: "max-w-[8rem]", cell: (d) => <Stage d={d} admin attention={needsAttention(d, now)} /> },
      {
        id: "title",
        label: "Title",
        card: "none",
        cellClassName: "max-w-[10rem]",
        cell: (d) => (
          <div className="min-w-0">
            <TableTitle t={d.title} />
            {d.episodes?.length ? <p className="truncate text-xs text-muted-foreground">{episodesWords(d.episodes)}</p> : null}
          </div>
        ),
      },
    ],
    optional: {
      library: { id: "library", label: "Library", sortable: true, card: "none", cell: (d) => <VersionTag k={d.version} className="text-[11px]" /> },
      quality: { id: "quality", label: "Quality", card: "none", cellClassName: "whitespace-nowrap", cell: (d) => d.quality || <Dash /> },
      provider: { id: "provider", label: "Provider", sortable: true, cell: (d) => <ProviderCell d={d} /> },
      size: { id: "size", label: "Size", sortable: true, cellClassName: "whitespace-nowrap tabular-nums", cell: (d) => (d.size ? fmtBytes(d.size) : <Dash />) },
      progress: { id: "progress", label: "Progress", sortable: true, card: "none", cell: (d) => <QueueProgress d={d} now={now} /> },
      score: { id: "score", label: "Score", sortable: true, cellClassName: "tabular-nums", cell: (d) => (d.score !== undefined ? d.score : <Dash />) },
      formats: { id: "formats", label: "Formats", cell: (d) => <FormatPills formats={d.formats} /> },
      indexer: { id: "indexer", label: "Indexer", sortable: true, cellClassName: "whitespace-nowrap", cell: (d) => d.indexer || <Dash /> },
      release: {
        id: "release",
        label: "Release",
        cellClassName: "max-w-[24rem]",
        cell: (d) => (d.release ? <span className="block truncate font-mono text-[12px]" title={d.release}>{d.release}</span> : <Dash />),
      },
      added: { id: "added", label: "Added", sortable: true, cell: (d) => <Ago iso={d.created_at} /> },
      updated: { id: "updated", label: "Last update", sortable: true, cell: (d) => <Ago iso={d.updated_at} /> },
    },
  };
}

/** The open row's panel: the steps, then the release, hash, message, provider attempts, cache evidence and journal. */
function OpenPanel({ d }: { d: ActivityDownload }) {
  return (
    <div data-testid="download-open">
      <div className="max-w-xl">
        <StepTrack steps={d.steps} />
      </div>
      <DownloadDetails d={d} />
    </div>
  );
}

/** The Queue as a table, for accounts that see files. */
function QueueTable({ items, now, prefs, sort, onSort }: { items: ActivityDownload[]; now: number; prefs: { order: QueueColumnId[]; shown: QueueColumnId[] }; sort: QueueSort; onSort: (c: string) => void }) {
  const [open, setOpen] = useState<ReadonlySet<number>>(new Set());
  const rows = useMemo(() => sortDownloads(items, sort), [items, sort]);
  const manage = usePerms().manage;
  const select = useChosen(rows.map((d) => d.id));
  const [removing, setRemoving] = useState(false);
  const [block, setBlock] = useState(false);
  const { fixed, optional } = queueColumns(now);
  const columns = tight([...fixed, ...prefs.order.filter((id) => prefs.shown.includes(id)).map((id) => optional[id])]);
  const toggle = (id: number) =>
    setOpen((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  return (
    <>
      <SelectionLine count={select.count} action="Remove selected…" onAct={() => setRemoving(true)} onClear={select.clear} />
      <DataTable
        rows={rows}
        rowKey={(d) => d.id}
        columns={columns}
        sort={sort}
        onSort={onSort}
        select={manage ? select : undefined}
        selectAllLabel={`Select all ${rows.length} shown`}
        rowLabel={(d) => d.title.name}
        testid="queue-table"
        rowProps={(d) => ({
          "data-testid": "download",
          "data-state": d.state,
          "data-attention": needsAttention(d, now) || undefined,
          className: open.has(d.id) ? "bg-[var(--sunken)]" : undefined,
        })}
        cardHead={(d) => (
          <>
            <TitleLine t={d.title} />
            <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
              <VersionTag k={d.version} className="text-[11px]" />
              {d.quality ? <Badge tone="outline">{d.quality}</Badge> : null}
              {d.episodes?.length ? <span>{episodesWords(d.episodes)}</span> : null}
            </div>
            <Stage d={d} admin attention={needsAttention(d, now)} />
            <QueueProgress d={d} now={now} empty={null} />
          </>
        )}
        actions={(d) => (
          <span className="inline-flex items-center gap-1">
            <DetailsButton attention={needsAttention(d, now)} open={open.has(d.id)} onClick={() => toggle(d.id)} chevron={false} />
            <DownloadActions d={d} />
          </span>
        )}
        expanded={(d) => (open.has(d.id) ? <OpenPanel d={d} /> : null)}
      />
      {removing ? (
        <BulkDialog
          items={rows.filter((d) => select.chosen.has(d.id))}
          scope={(n) => removeManyScope(n, block)}
          run={(d) => removeDownload(d.id, block)}
          words={(n, failed) => bulkWords("Removed", "Not removed", ["download", "downloads"], n, failed)}
          note={(answers) => answers.find((a) => a.search && !a.search.started)?.search?.message ?? ""}
          done="Downloads removed"
          onDone={select.clear}
          onClose={() => {
            setRemoving(false);
            setBlock(false);
          }}
          testid="remove-downloads-dialog"
        >
          <Choice checked={block} onChange={setBlock} testid="downloads-block">
            Also blocklist these releases and search again
          </Choice>
        </BulkDialog>
      ) : null}
    </>
  );
}

const downloads = (n: number) => `${n} ${n === 1 ? "download" : "downloads"}`;

/** What removing n chosen downloads does and leaves alone. */
function removeManyScope(n: number, block: boolean) {
  return {
    title: `Remove ${downloads(n)}`,
    does: [
      `Removes ${downloads(n)} from Theatarr's queue`,
      ...(block ? ["Each release is blocklisted for its library, and a search for that library starts now (when it is monitored and active)"] : []),
    ],
    keeps: [SHARED_TORRENT, "Files already linked into the library stay", ...(block ? [] : ["The releases are not blocklisted: a later search may pick them again"])],
    confirm: `Remove ${downloads(n)}`,
  };
}

/** The clock the waiting times count from, ticking every 30 s. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(t);
  }, []);
  return now;
}

const EMPTY: Record<ActivityShow, string> = {
  attention: "Nothing needs attention.",
  downloading: "Nothing is downloading right now.",
  waiting: "Nothing is waiting.",
  all: "Nothing is downloading right now.",
};

/** The Queue's sort for the signed-in user, kept under theatarr.library.activity-queue-sort.<user>. */
function useQueueSort(): [QueueSort, (column: string) => void] {
  const uid = useAuth().data?.id;
  const [saved, setSaved] = useState<{ uid?: number; sort: QueueSort } | null>(null);
  const sort = saved && saved.uid === uid ? saved.sort : readPref(QUEUE_SORT_PREF, uid, normalizeQueueSort);
  const onSort = (column: string) => {
    if (!isQueueSortColumn(column)) return;
    const next = nextQueueSort(sort, column);
    setSaved({ uid, sort: next });
    writePref(QUEUE_SORT_PREF, uid, next);
  };
  return [sort, onSort];
}

export function QueueTab({ admin, as, show, onShow }: { admin: boolean; as?: "user"; show: ActivityShow; onShow: (s: ActivityShow) => void }) {
  const q = useActivityNow({ as });
  const now = useNow();
  const [prefs, savePrefs] = useColumnPrefs("activity-queue", QUEUE_COLUMNS, QUEUE_DEFAULT);
  const [sort, onSort] = useQueueSort();
  const [dialog, setDialog] = useState<"columns" | "meanings" | null>(null);
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const all = q.data.now ?? [];
  const counts = showCounts(all, now);
  const items = filterDownloads(all, show, now);
  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <div className="flex flex-wrap gap-2" data-testid="counts" role="group" aria-label="Show">
          {SHOWS.map((s) => (
            <button
              key={s.id}
              type="button"
              onClick={() => onShow(s.id)}
              aria-pressed={show === s.id}
              data-show={s.id}
              data-count={counts[s.id]}
              className={cn(
                "rounded-[4px] border px-2.5 py-1 text-[13px] font-medium transition",
                show === s.id
                  ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]"
                  : "border-[var(--line)] text-muted-foreground hover:bg-[var(--hover)] hover:text-foreground",
                s.id === "attention" && counts.attention > 0 && show !== s.id && "border-[var(--warn)] text-[var(--warn)]",
              )}
            >
              {admin || !s.userLabel ? s.label : s.userLabel} <span className="tabular-nums opacity-80">{counts[s.id]}</span>
            </button>
          ))}
        </div>
        {admin ? (
          <>
            <Button variant="ghost" size="sm" onClick={() => setDialog("meanings")}>
              <HelpCircle /> What do these mean?
            </Button>
            <span className="grow" />
            <Button variant="outline" size="sm" aria-label="Columns" onClick={() => setDialog("columns")}>
              <Columns3 />
              <span className="hidden sm:inline">Columns</span>
            </Button>
          </>
        ) : null}
      </div>
      {items.length === 0 ? (
        <Card>
          <Empty icon={Inbox}>
            <span>{EMPTY[show]}</span>
            {show === "all" ? <span className="text-xs">New downloads show up here as they start, without a reload.</span> : null}
          </Empty>
        </Card>
      ) : admin ? (
        <QueueTable items={items} now={now} prefs={prefs} sort={sort} onSort={onSort} />
      ) : (
        <Card className="p-0">
          <div className="hidden border-b border-[var(--line)] bg-[var(--sunken)] px-4 py-1.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)] md:grid md:grid-cols-[1.75rem_minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1fr)_5.5rem] md:gap-4">
            <span />
            <span>Title and library</span>
            <span>Stage</span>
            <span>Progress</span>
            <span className="sr-only">Action</span>
          </div>
          <ul className="divide-y divide-border">
            {items.map((d) => (
              <DownloadRow key={d.id} d={d} now={now} />
            ))}
          </ul>
        </Card>
      )}
      {dialog === "columns" ? (
        <ColumnPicker
          label="Queue columns"
          note="Status, Title and the row actions always stay. On a phone the extra columns show under each download."
          columns={QUEUE_COLUMNS}
          prefs={prefs}
          onChange={savePrefs}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {dialog === "meanings" ? (
        <OptionsDialog label="What the statuses mean" note="The words in the Status column, in the order a download meets them." onClose={() => setDialog(null)}>
          <dl className="space-y-2" data-testid="status-meanings">
            {STAGE_MEANINGS.map((m) => (
              <div key={m.label}>
                <dt className="font-medium text-[var(--ink)]">{m.label}</dt>
                <dd className="text-muted-foreground">{m.meaning}</dd>
              </div>
            ))}
          </dl>
        </OptionsDialog>
      ) : null}
    </div>
  );
}
