// Activity › History (pages/activity.tsx): what happened to downloads (the grab journal in plain words), filtered by
// title, copy, event and since date in the URL (lib/activity-history.ts).
import { History as HistoryIcon, NotebookPen } from "lucide-react";
import { Link } from "react-router";
import { useActivityHistory, type ActivityEvent } from "@/api/activity";
import { usePerms } from "@/api/library";
import { HistoryRetry } from "@/components/activity/download-actions";
import { HistoryFilters, useHistoryFilters } from "@/components/activity/history-filters";
import { Poster } from "@/components/activity/row-bits";
import { Empty, ErrorState, Loading, VersionTag } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fmtTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { adminLink, journalLink } from "@/lib/admin-links";
import { historyFilterCount, historyParams } from "@/lib/activity-history";

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

function EventRow({ e, admin }: { e: ActivityEvent; admin: boolean }) {
  // The journal is an admin page; managers (see files) get the summary without the link.
  const journal = adminLink(usePerms().admin, journalLink(e.id));
  const titleName = `${e.title.name}${e.title.year ? ` (${e.title.year})` : ""}`;
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
            <Link to={`/title/${e.title.id}`} className="truncate text-foreground hover:text-primary">{titleName}</Link>
          ) : <span className="truncate text-foreground">{titleName}</span>}
          {admin && e.version ? <VersionTag k={e.version} className="text-[11px]" /> : null}
        </div>
        {admin && e.summary ? (
          <div className="mt-1 flex min-w-0 items-start gap-2 text-xs text-muted-foreground">
            <span className="min-w-0 break-words font-mono text-[11.5px]">{e.summary}</span>
            <HistoryRetry e={e} />
            {journal ? (
              <Link to={journal} className="ml-auto shrink-0 text-primary hover:underline" aria-label="Journal entry">
                <NotebookPen className="size-3.5" />
              </Link>
            ) : null}
          </div>
        ) : null}
      </div>
    </li>
  );
}

export function HistoryTab({ admin, as }: { admin: boolean; as?: "user" }) {
  const filters = useHistoryFilters();
  const q = useActivityHistory({ as }, historyParams(filters));
  return (
    <div>
      <HistoryFilters />
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : <HistoryList q={q} admin={admin} filtered={historyFilterCount(filters) > 0} />}
    </div>
  );
}

function HistoryList({ q, admin, filtered }: { q: ReturnType<typeof useActivityHistory>; admin: boolean; filtered: boolean }) {
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
                <EventRow key={e.id} e={e} admin={admin} />
              ))}
            </ul>
          </Card>
        </section>
      ))}
      {q.hasNextPage ? (
        <div className="flex justify-center">
          <Button variant="outline" onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>
            {q.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
