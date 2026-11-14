// The settings audit (GET /settings/audit?order=desc): saves, imports, deletes and source tests, newest first, in
// pages that continue before the last id (Older), with Newer back up the pages already read. Before/after are the server's redacted records (credentials as set/unset only). Each
// entry names its changed fields; expanded, it lists them with old → new (lib/audit-diff.ts) and the whole redacted
// records on demand. Entries stack on a phone instead of dropping columns.
import { ChevronDown, ChevronLeft, ChevronRight, ChevronUp } from "lucide-react";
import { useState } from "react";
import { useSettingsAudit, type SettingsAuditPage, type SettingsAuditRow } from "@/api/settings";
import { Empty, ErrorState, Loading, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { auditDiff, changeLine, redactAudit, type AuditChange } from "@/lib/audit-diff";
import { fmtTime } from "@/lib/format";
import { auditEntity, auditOperation } from "@/lib/settings";

/** How many changes the collapsed entry names. */
const BRIEF = 2;

function ChangeValue({ c }: { c: AuditChange }) {
  if (c.secret) {
    return (
      <span>
        <Badge tone="warning">credential {c.op}</Badge> <span className="text-muted-foreground">value never shown</span>
      </span>
    );
  }
  if (c.op === "added") return <span><Badge tone="positive">added</Badge> {c.after}</span>;
  if (c.op === "removed") return <span><Badge tone="negative">removed</Badge> <span className="text-muted-foreground">was</span> {c.before}</span>;
  return (
    <span>
      <span className="sr-only">from </span>
      <span className="text-muted-foreground">{c.before}</span>
      <span aria-hidden> → </span>
      <span className="sr-only"> to </span>
      <span className="font-medium text-foreground">{c.after}</span>
    </span>
  );
}

function FullRecord({ label, v }: { label: string; v: unknown }) {
  return (
    <div className="min-w-0">
      <div className="mb-1 text-xs text-muted-foreground">{label}</div>
      <pre className="max-h-80 overflow-y-auto whitespace-pre-wrap rounded-md border border-border bg-[var(--surface)] p-2 font-mono text-[11.5px] [overflow-wrap:anywhere]">
        {v == null ? "(none)" : JSON.stringify(redactAudit(v), null, 2)}
      </pre>
    </div>
  );
}

function AuditEntry({ r, open, onToggle }: { r: SettingsAuditRow; open: boolean; onToggle?: () => void }) {
  const changes = auditDiff(r.before, r.after);
  const hasRecords = r.before != null || r.after != null;
  const panel = `audit-${r.id}-changes`;
  return (
    <li className="px-4 py-3" data-audit={r.id} data-open={open ? "true" : undefined}>
      <div className="grid gap-x-4 gap-y-1.5 sm:grid-cols-[11rem_minmax(0,1fr)_auto] sm:items-start">
        <div className="text-[13px]">
          <span className="whitespace-nowrap">{fmtTime(r.created_at)}</span>
          <div className="text-xs text-muted-foreground">generation {r.generation} · {r.actor}</div>
        </div>
        <div className="min-w-0 text-[13px]">
          <Badge tone={r.operation === "delete" ? "negative" : r.operation.startsWith("test") ? "info" : "outline"}>{auditOperation(r.operation)}</Badge>{" "}
          {r.entity === "settings" ? <span className="text-muted-foreground">generation {r.generation}</span> : <><span className="text-muted-foreground">{auditEntity(r.entity)}</span> <Mono>{r.key}</Mono></>}
          <div className="mt-1 font-mono text-[11.5px] text-muted-foreground [overflow-wrap:anywhere]" data-testid="audit-brief">
            {changes.length === 0
              ? hasRecords ? "no field changed" : "whole settings saved"
              : changes.slice(0, BRIEF).map(changeLine).join(" · ") + (changes.length > BRIEF ? ` · +${changes.length - BRIEF} more` : "")}
          </div>
        </div>
        {hasRecords ? (
          <Button size="xs" variant="outline" className="justify-self-start sm:justify-self-end" onClick={onToggle} aria-expanded={open} aria-controls={panel} data-testid="audit-toggle">
            {open ? <ChevronUp /> : <ChevronDown />} {open ? "Hide changes" : `Show changes${changes.length ? ` (${changes.length})` : ""}`}
          </Button>
        ) : null}
      </div>
      {hasRecords && open ? (
        <div id={panel} className="mt-3 rounded-lg border border-border bg-muted/40 p-3" data-testid="audit-changes">
          {changes.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">No field changed.</p>
          ) : (
            <ul className="space-y-2 text-[13px]">
              {changes.map((c) => (
                <li key={c.path} className="grid gap-x-4 gap-y-0.5 sm:grid-cols-[minmax(8rem,max-content)_minmax(0,1fr)]" data-path={c.path} data-op={c.op}>
                  <Mono className="[overflow-wrap:anywhere]">{c.path}</Mono>
                  <span className="min-w-0 font-mono text-[12px] [overflow-wrap:anywhere]"><ChangeValue c={c} /></span>
                </li>
              ))}
            </ul>
          )}
          <details className="mt-3 text-[13px]" data-testid="audit-full">
            <summary className="cursor-pointer text-muted-foreground">Full records (redacted)</summary>
            <div className="mt-2 grid gap-3 md:grid-cols-2">
              <FullRecord label="before" v={r.before} />
              <FullRecord label="after" v={r.after} />
            </div>
          </details>
        </div>
      ) : null}
    </li>
  );
}

export function AuditView({
  page,
  pageNo,
  onPrev,
  onNext,
  open = {},
  onToggle,
}: {
  page: SettingsAuditPage;
  pageNo: number;
  onPrev?: () => void;
  onNext?: () => void;
  /** Expanded entries, by audit id. */
  open?: Record<number, boolean>;
  onToggle?: (id: number) => void;
}) {
  return (
    <div data-testid="audit">
      <Card className="p-0">
        {page.records.length === 0 ? (
          <Empty>No audit records.</Empty>
        ) : (
          <ol className="divide-y divide-border" aria-label="Settings audit">
            {page.records.map((r) => (
              <AuditEntry key={r.id} r={r} open={!!open[r.id]} onToggle={onToggle ? () => onToggle(r.id) : undefined} />
            ))}
          </ol>
        )}
      </Card>
      <div className="mt-3 flex items-center justify-between gap-2 text-sm">
        <Button size="sm" variant="outline" onClick={onPrev} disabled={!onPrev} data-testid="audit-prev">
          <ChevronLeft /> Newer
        </Button>
        <span className="text-muted-foreground" data-testid="audit-page">page {pageNo}</span>
        <Button size="sm" variant="outline" onClick={onNext} disabled={!onNext} data-testid="audit-next">
          Older <ChevronRight />
        </Button>
      </div>
    </div>
  );
}

const LIMIT = 50;

/** The audit with its cursor: a stack of the before values that led to each page (the first is the newest), and the
 * expanded entries. */
export function AuditPanel({ readOnly }: { readOnly?: string }) {
  const [stack, setStack] = useState<number[]>([0]);
  const [open, setOpen] = useState<Record<number, boolean>>({});
  const before = stack[stack.length - 1];
  const q = useSettingsAudit(before, LIMIT, !readOnly);
  if (readOnly) {
    return <p className="text-[13px] text-muted-foreground" data-testid="audit-readonly">The settings audit is unavailable.</p>;
  }
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const page = q.data;
  return (
    <AuditView
      page={page}
      pageNo={stack.length}
      onPrev={stack.length > 1 ? () => setStack(stack.slice(0, -1)) : undefined}
      onNext={page.next ? () => setStack([...stack, page.next]) : undefined}
      open={open}
      onToggle={(id) => setOpen((o) => ({ ...o, [id]: !o[id] }))}
    />
  );
}
