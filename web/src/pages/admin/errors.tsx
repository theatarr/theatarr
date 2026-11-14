// Admin › Errors (docs/design/errors.md): the error inbox. Unexpected failures (Error logs, panics, 5xx answers, failed
// jobs, failing health checks) grouped by fingerprint, newest sighting first, with how often and since when. A row
// opens to its normalized message, context and stack; Mark seen, Resolve, Ignore and Reopen change its status. A
// resolved row that happens again comes back as new (a regression). Nothing changes by looking.
import { useState, type ReactNode } from "react";
import { CheckCircle2, ChevronRight, Eye, EyeOff, RotateCcw } from "lucide-react";
import { useErrorActions, useErrorEvent, useErrors, type ErrorEvent, type ErrorFilter } from "@/api/errors";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Strip } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";
import { errorComponentWords, errorSourceWords, errorStatusWords } from "@/lib/error-words";

const STATUSES = [
  { id: "open", label: "Open" },
  { id: "new", label: "New" },
  { id: "seen", label: "Seen" },
  { id: "resolved", label: "Resolved" },
  { id: "ignored", label: "Ignored" },
  { id: "", label: "All" },
];
const SOURCES = ["log", "panic", "http", "job", "health"];
const PAGE = 100;

const STATUS_TONE: Record<string, "negative" | "warning" | "positive" | "neutral"> = {
  new: "negative",
  seen: "warning",
  resolved: "positive",
  ignored: "neutral",
};

export function ErrorsPage() {
  const [status, setStatus] = useState("open");
  const [source, setSource] = useState("");
  const [limit, setLimit] = useState(PAGE);
  const [open, setOpen] = useState<number | undefined>();
  const filter: ErrorFilter = { status, source, limit };
  const q = useErrors(filter);
  const { bulk } = useErrorActions();
  const counts = q.data?.counts ?? {};
  const items = q.data?.items ?? [];
  const header = (
    <PageHeader
      crumb={[{ label: "System", to: "/admin" }]}
      title="Errors"
      description="Unexpected failures, grouped: Error logs, panics, server errors, failed jobs and failing health checks. Expected outcomes (a provider refusing, an indexer down, a 4xx) are not here."
    />
  );
  const canBulk = items.length > 0 && (status === "open" || status === "new" || status === "seen");
  return (
    <div>
      {header}
      <Strip
        items={[
          { label: "new", value: fmtNumber(counts.new ?? 0), tone: (counts.new ?? 0) > 0 ? "bad" : undefined, testid: "errors-new" },
          { label: "seen", value: fmtNumber(counts.seen ?? 0), testid: "errors-seen" },
          { label: "resolved", value: fmtNumber(counts.resolved ?? 0), testid: "errors-resolved" },
          { label: "ignored", value: fmtNumber(counts.ignored ?? 0), testid: "errors-ignored" },
          ...((q.data?.drops ?? 0) > 0 ? [{ label: "dropped (queue full)", value: fmtNumber(q.data?.drops ?? 0), tone: "warn" as const, testid: "errors-drops" }] : []),
        ]}
      />
      <div className="my-4 flex flex-wrap items-center gap-3">
        <div className="flex gap-1" role="group" aria-label="Status">
          {STATUSES.map((s) => (
            <Button
              key={s.id || "all"}
              size="xs"
              variant={s.id === status ? "secondary" : "ghost"}
              aria-pressed={s.id === status}
              onClick={() => {
                setStatus(s.id);
                setLimit(PAGE);
              }}
            >
              {s.label}
            </Button>
          ))}
        </div>
        <select
          aria-label="Source"
          value={source}
          onChange={(e) => {
            setSource(e.target.value);
            setLimit(PAGE);
          }}
          className="h-8 rounded-md border border-input bg-field px-2 text-sm"
        >
          <option value="">All sources</option>
          {SOURCES.map((s) => <option key={s} value={s}>{errorSourceWords(s)}</option>)}
        </select>
        {canBulk ? (
          <span className="ml-auto flex gap-1.5">
            {status !== "seen" ? (
              <Button size="xs" variant="outline" data-testid="errors-seen-all" disabled={bulk.isPending}
                onClick={() => bulk.mutate({ where: { status: status === "open" ? "new" : status, source }, status: "seen" })}>
                <Eye /> Mark all seen
              </Button>
            ) : null}
            <Button size="xs" variant="outline" data-testid="errors-resolve-all" disabled={bulk.isPending}
              onClick={() => bulk.mutate({ where: { status, source }, status: "resolved" })}>
              <CheckCircle2 /> Resolve all
            </Button>
          </span>
        ) : null}
      </div>
      {q.isPending ? (
        <Loading />
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : items.length === 0 ? (
        <Card><Empty icon={CheckCircle2}>No errors here.</Empty></Card>
      ) : (
        <Card className="p-0">
          <ul className="divide-y divide-border" data-testid="errors-list">
            {items.map((r) => (
              <ErrorRow key={r.id} row={r} open={open === r.id} onToggle={() => setOpen(open === r.id ? undefined : r.id)} />
            ))}
          </ul>
        </Card>
      )}
      {q.data && q.data.total > items.length ? (
        <div className="mt-3 flex items-center gap-3 text-sm text-muted-foreground">
          {items.length} of {fmtNumber(q.data.total)}
          <Button size="xs" variant="outline" onClick={() => setLimit(limit + PAGE)} disabled={limit >= 500}>Show more</Button>
        </div>
      ) : null}
    </div>
  );
}

function ErrorRow({ row, open, onToggle }: { row: ErrorEvent; open: boolean; onToggle: () => void }) {
  return (
    <li data-error={row.id} className="px-4 py-3">
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2 sm:flex-nowrap">
        <button type="button" className="flex min-w-0 flex-1 basis-full items-start gap-2 text-left sm:basis-0" aria-expanded={open} data-testid={`error-open-${row.id}`} onClick={onToggle}>
          <ChevronRight className={`mt-0.5 size-4 shrink-0 transition-transform ${open ? "rotate-90" : ""}`} aria-hidden />
          <span className="min-w-0 flex-1">
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <Badge tone={STATUS_TONE[row.status] ?? "neutral"} data-testid="error-status" title={row.status}>{errorStatusWords(row.status)}</Badge>
              <Badge tone="outline" title={row.source}>{errorSourceWords(row.source)}</Badge>
              <span title={row.component || undefined}><Mono className="text-[11.5px] text-muted-foreground">{errorComponentWords(row.component)}</Mono></span>
              {row.regressions > 0 ? <Badge tone="warning">came back {row.regressions}×</Badge> : null}
            </span>
            <span className="mt-1 block break-words text-[13px] text-[var(--ink)]" data-testid="error-sample">{row.sample || row.message}</span>
            <span className="mt-0.5 flex flex-wrap gap-x-2 text-xs text-muted-foreground">
              <span data-testid="error-count">{fmtNumber(row.count)}×</span>
              <span>first {fmtAgo(row.first_seen)}</span>
              <span>last {fmtAgo(row.last_seen)}</span>
              <span>#{row.id}</span>
            </span>
          </span>
        </button>
        {/* Phones: the message takes the full width and the actions go below it, under the text. */}
        <div className="pl-6 sm:pl-0" data-testid={`error-actions-${row.id}`}><ErrorActions row={row} /></div>
      </div>
      {open ? <ErrorDetail id={row.id} /> : null}
    </li>
  );
}

/** Mark seen, Resolve and Ignore for an open row; Reopen for a resolved or ignored one. */
function ErrorActions({ row }: { row: ErrorEvent }) {
  const { update } = useErrorActions();
  const set = (status: string) => update.mutate({ id: row.id, status });
  const busy = update.isPending;
  if (row.status === "resolved" || row.status === "ignored") {
    return (
      <span className="inline-flex shrink-0 items-center gap-1.5">
        <Button size="xs" variant="ghost" data-testid={`error-reopen-${row.id}`} disabled={busy} onClick={() => set("new")}><RotateCcw /> Reopen</Button>
      </span>
    );
  }
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5">
      {row.status === "new" ? (
        <Button size="xs" variant="ghost" data-testid={`error-seen-${row.id}`} disabled={busy} onClick={() => set("seen")} title="Still open, no longer new"><Eye /> Seen</Button>
      ) : null}
      <Button size="xs" variant="outline" data-testid={`error-resolve-${row.id}`} disabled={busy} onClick={() => set("resolved")} title="Fixed; it comes back as new if it happens again"><CheckCircle2 /> Resolve</Button>
      <Button size="xs" variant="ghost" data-testid={`error-ignore-${row.id}`} disabled={busy} onClick={() => set("ignored")} title="Keep counting, never reopen"><EyeOff /> Ignore</Button>
    </span>
  );
}

function ErrorDetail({ id }: { id: number }) {
  const q = useErrorEvent(id);
  const { update } = useErrorActions();
  const [note, setNote] = useState<string | undefined>();
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const r = q.data;
  const ctx = Object.entries((r.context ?? {}) as Record<string, string>);
  const noteValue = note ?? r.note;
  return (
    <div className="mt-3 space-y-3 pl-6" data-testid="error-detail">
      <KV
        items={[
          ["Grouped as", <Mono key="m">{r.message}</Mono>],
          ["First seen", `${fmtTime(r.first_seen)}${r.first_version ? ` (${r.first_version})` : ""}`],
          ["Last seen", `${fmtTime(r.last_seen)}${r.last_version ? ` (${r.last_version})` : ""}`],
          ...(r.resolved_at ? [["Resolved", fmtTime(r.resolved_at)] as [string, string]] : []),
          ["Fingerprint", <Mono key="f" className="text-[11.5px]">{r.fingerprint}</Mono>],
          ...ctx.map(([k, v]) => [k, <Mono key={k}>{v}</Mono>] as [string, ReactNode]),
        ]}
      />
      {r.stack ? (
        <pre className="max-h-96 overflow-auto rounded-md border border-border bg-muted p-3 font-mono text-[11.5px] leading-snug" data-testid="error-stack">{r.stack}</pre>
      ) : null}
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate({ id: r.id, note: noteValue });
        }}
      >
        <input
          aria-label="Note"
          placeholder="Note: the cause, or what fixed it"
          value={noteValue}
          maxLength={4096}
          onChange={(e) => setNote(e.target.value)}
          className="h-8 min-w-64 flex-1 rounded-md border border-input bg-field px-2 text-sm"
        />
        <Button size="xs" variant="outline" type="submit" disabled={update.isPending || noteValue === r.note}>Save note</Button>
      </form>
    </div>
  );
}
