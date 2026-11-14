// Activity › History (pages/activity.tsx): what happened to downloads (the grab journal in plain words), filtered by
// title, copy, event and since date in the URL (lib/activity-history.ts), newest first, 50 a page with Load more.
// Accounts that see files get a table (DataTable): Event and Title, the columns they chose
// (lib/activity-history-columns.ts), and Retry on a failed download and the journal link. Household accounts and
// "View as a user" keep the simple rows grouped by day: no library key, no release.
import { Columns3, History as HistoryIcon, NotebookPen } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { useActivityHistory, type ActivityEvent } from "@/api/activity";
import { usePerms } from "@/api/library";
import { HistoryRetry } from "@/components/activity/download-actions";
import { HistoryFilters, useHistoryFilters } from "@/components/activity/history-filters";
import { Dash, episodesWords, FormatPills, Poster, tight } from "@/components/activity/row-bits";
import { Empty, ErrorState, Loading, VersionTag } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ColumnPicker } from "@/components/ui/column-picker";
import { DataTable, type DataColumn } from "@/components/ui/data-table";
import { fmtBytes, fmtTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { adminLink, journalLink } from "@/lib/admin-links";
import { historyFilterCount, historyParams } from "@/lib/activity-history";
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

const titleName = (e: ActivityEvent) => `${e.title.name}${e.title.year ? ` (${e.title.year})` : ""}`;

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
      <span className="text-[13px] font-medium leading-snug">{e.words}</span>
    </span>
  );
}

function TitleCell({ e }: { e: ActivityEvent }) {
  return (
    <div className="min-w-0">
      {e.title.id ? (
        <Link to={`/title/${e.title.id}`} className="block truncate font-medium text-[var(--ink)] hover:text-primary">{titleName(e)}</Link>
      ) : <span className="block truncate font-medium text-[var(--ink)]">{titleName(e)}</span>}
      {e.episodes?.length ? <p className="truncate text-xs text-muted-foreground">{episodesWords(e.episodes)}</p> : null}
    </div>
  );
}

const HISTORY_FIXED: DataColumn<ActivityEvent>[] = [
  { id: "event", label: "Event", card: "none", cellClassName: "min-w-[8rem] max-w-[10rem]", cell: (e) => <EventWords e={e} /> },
  { id: "title", label: "Title", card: "none", cellClassName: "max-w-[12rem]", cell: (e) => <TitleCell e={e} /> },
];

const HISTORY_OPTIONAL: Record<HistoryColumnId, DataColumn<ActivityEvent>> = {
  library: { id: "library", label: "Library", cell: (e) => (e.version ? <VersionTag k={e.version} className="text-[11px]" /> : <Dash />) },
  quality: { id: "quality", label: "Quality", cellClassName: "whitespace-nowrap", cell: (e) => e.quality || <Dash /> },
  score: { id: "score", label: "Score", cellClassName: "tabular-nums", cell: (e) => (e.score !== undefined ? e.score : <Dash />) },
  date: { id: "date", label: "Date", card: "none", cellClassName: "whitespace-nowrap tabular-nums", cell: (e) => fmtTime(e.at) },
  release: {
    id: "release",
    label: "Release",
    cellClassName: "max-w-[13rem]",
    cell: (e) => (e.release ? <span className="block font-mono text-[12px] max-sm:break-all sm:truncate" title={e.release}>{e.release}</span> : <Dash />),
  },
  provider: { id: "provider", label: "Provider", cellClassName: "whitespace-nowrap", cell: (e) => (e.provider ? <ProviderTag account={e.provider} /> : <Dash />) },
  indexer: { id: "indexer", label: "Indexer", cellClassName: "whitespace-nowrap", cell: (e) => e.indexer || <Dash /> },
  formats: { id: "formats", label: "Formats", cell: (e) => <FormatPills formats={e.formats} /> },
  size: { id: "size", label: "Size", cellClassName: "whitespace-nowrap tabular-nums", cell: (e) => (e.size ? fmtBytes(e.size) : <Dash />) },
};

function HistoryActions({ e }: { e: ActivityEvent }) {
  // The journal is an admin page; managers (see files) get the row without the link.
  const journal = adminLink(usePerms().admin, journalLink(e.id));
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

export function HistoryTab({ admin, as }: { admin: boolean; as?: "user" }) {
  const filters = useHistoryFilters();
  const q = useActivityHistory({ as }, historyParams(filters));
  const [prefs, savePrefs] = useColumnPrefs("activity-history", HISTORY_COLUMNS, HISTORY_DEFAULT);
  const [picking, setPicking] = useState(false);
  return (
    <div>
      <HistoryFilters
        tools={
          admin ? (
            <Button variant="outline" size="sm" aria-label="Columns" onClick={() => setPicking(true)}>
              <Columns3 />
              <span className="hidden sm:inline">Columns</span>
            </Button>
          ) : undefined
        }
      />
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : <HistoryList q={q} admin={admin} prefs={prefs} filtered={historyFilterCount(filters) > 0} />}
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

function HistoryList({ q, admin, prefs, filtered }: { q: ReturnType<typeof useActivityHistory>; admin: boolean; prefs: { order: HistoryColumnId[]; shown: HistoryColumnId[] }; filtered: boolean }) {
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
    const columns = tight([...HISTORY_FIXED, ...prefs.order.filter((id) => prefs.shown.includes(id)).map((id) => HISTORY_OPTIONAL[id])]);
    return (
      <div className="space-y-4">
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
              <TitleCell e={e} />
            </>
          )}
          actions={(e) => <HistoryActions e={e} />}
        />
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
