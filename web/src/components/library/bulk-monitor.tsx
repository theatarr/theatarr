// The library's select mode (docs/design/ui.md "Library views"): with titles selected, a bar at the foot of the view
// picks the target copy and the monitoring, and Review asks the server for a preview (POST /titles/monitor dry_run)
// listing what changes, what is already set and what is excluded (a title without the copy has nothing to change). Apply sends the preview's values as expect, so a copy changed since is skipped,
// and lists each item's outcome. Monitoring only: nothing is searched or grabbed, no copy is added.
// Managers also get Search now: the selection goes to POST /wanted/search (the Wanted page's queue, under the hourly
// limit) for the one library picked, 200 titles a request, and the bar shows what the server answered.
import { Loader2, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { useBulkMonitor, type BulkItem, type BulkMonitorResult } from "@/api/copies";
import { usePerms, type TitleRow } from "@/api/library";
import { useWanted, useWantedSearch } from "@/api/wanted";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Modal } from "@/components/ui/modal";
import { SELECT } from "@/components/library/filters";
import { bulkExpect, outcomeWords, type CopyColumn } from "@/lib/copies";
import { cn } from "@/lib/utils";
import { CopyDot } from "./copy-bits";
import { plural } from "@/lib/format";

/** What the bar can say before asking the server: copies to change, titles without the copy. */
export function localPreview(titles: readonly TitleRow[], target: string): { editable: number; missing: number } {
  let editable = 0;
  let missing = 0;
  for (const t of titles) {
    const vs = (t.versions ?? []).filter((v) => target === "" || v.key === target);
    if (vs.length === 0) missing++;
    editable += vs.length;
  }
  return { editable, missing };
}


/** POST /wanted/search takes at most this many items (internal/web/wanted.go maxSearchNowItems). */
const SEARCH_CHUNK = 200;

/** What the Search now requests of one selection answered, added up: queued (the searches a request added) and skipped
 * are per request and started is what each request let start; started and pending count the whole Search now queue,
 * not only this selection's part, and pending is the last answer's. */
type SearchSum = {
  /** The selection and library it was for; the bar shows it only while they are unchanged. */
  key: string;
  /** Titles in the requests that were answered. */
  sent: number;
  of: number;
  queued: number;
  started: number;
  pending: number;
  skipped: Map<string, number>;
  error?: unknown;
};


function searchWords(r: SearchSum): string {
  const parts = [`Queued ${plural(r.queued, "search", "searches")}. ${r.started} started now, ${r.pending} waiting in the queue`];
  for (const [reason, n] of r.skipped) parts.push(`Skipped ${plural(n, "title")}: ${reason}`);
  return parts.join(". ") + ".";
}

export function BulkBar({ titles, columns, onClear }: { titles: TitleRow[]; columns: CopyColumn[]; onClear: () => void }) {
  const [target, setTarget] = useState("");
  const [on, setOn] = useState(true);
  const [review, setReview] = useState(false);
  const p = useMemo(() => localPreview(titles, target), [titles, target]);
  const perms = usePerms();
  const search = useWantedSearch();
  const [ask, setAsk] = useState(false);
  const [busy, setBusy] = useState(false);
  const [sum, setSum] = useState<SearchSum>();
  const ids = titles.map((t) => t.id ?? 0).filter(Boolean);
  const key = `${target}/${ids.join()}`;
  const targetLabel = columns.find((c) => c.key === target)?.label ?? target;
  const result = sum?.key === key ? sum : undefined;
  // One request after another: each answer's pending is the queue after it.
  const runSearch = async () => {
    const out: SearchSum = { key, sent: 0, of: ids.length, queued: 0, started: 0, pending: 0, skipped: new Map() };
    setBusy(true);
    try {
      for (let i = 0; i < ids.length; i += SEARCH_CHUNK) {
        const chunk = ids.slice(i, i + SEARCH_CHUNK);
        const r = await search.mutateAsync({ only: "missing", items: chunk.map((id) => ({ title_id: id, version: target })) });
        out.sent += chunk.length;
        out.queued += r.queued;
        out.started += r.started;
        out.pending = r.pending;
        for (const s of r.skipped ?? []) out.skipped.set(s.reason, (out.skipped.get(s.reason) ?? 0) + 1);
      }
    } catch (e) {
      out.error = e;
    }
    setBusy(false);
    setAsk(false);
    setSum(out);
  };
  if (titles.length === 0) return null;
  return (
    <section
      aria-label="Bulk edit"
      data-testid="bulk-bar"
      className="sticky bottom-3 z-30 mt-4 rounded-xl border border-[var(--th-baccent)] bg-[var(--surface)] p-3 shadow-[var(--shadow-2)]"
    >
      <div className="flex flex-wrap items-center gap-2">
        <strong className="text-sm">{plural(titles.length, "title")} selected</strong>
        <select aria-label="Target libraries" className={cn(SELECT, "w-auto min-w-0 max-w-full")} value={target} onChange={(e) => setTarget(e.target.value)}>
          <option value="">Every library</option>
          {columns.map((c) => (
            <option key={c.key} value={c.key}>{c.label}</option>
          ))}
        </select>
        <select aria-label="Monitoring" className={cn(SELECT, "w-auto")} value={on ? "on" : "off"} onChange={(e) => setOn(e.target.value === "on")}>
          <option value="on">Monitor: on</option>
          <option value="off">Monitor: off</option>
        </select>
        <span className="grow" />
        <Button variant="ghost" size="sm" onClick={onClear}>Clear</Button>
        {perms.manage ? (
          <Button variant="outline" size="sm" disabled={!target || ids.length === 0 || busy} title={target ? undefined : "Pick a library to search"} onClick={() => setAsk(true)}>
            Search now
          </Button>
        ) : null}
        <Button size="sm" onClick={() => setReview(true)}>Review changes</Button>
      </div>
      <p className="mt-2 text-xs text-[var(--ink-3)]" data-testid="bulk-local">
        {p.editable === 1 ? "1 editable library" : `${p.editable} editable libraries`}
        {p.missing ? ` · ${plural(p.missing, "title")} without this library` : ""}
        {" · "}Files stay in place. No search is started.
      </p>
      {perms.manage && !target ? <p className="mt-1 text-xs text-[var(--ink-3)]">Search now needs one library: pick it above.</p> : null}
      {result ? (
        <p className="mt-2 text-[13px] text-[var(--ink-2)]" data-testid="bulk-search-result">
          {result.sent > 0 ? <span role="status">{searchWords(result)} </span> : null}
          {result.error ? (
            <span role="alert" className="text-[var(--bad-ink)]">
              {result.sent > 0 ? `Stopped after ${result.sent} of ${plural(result.of, "title")}: ` : ""}{errText(result.error)}{" "}
            </span>
          ) : null}
          {result.sent > 0 ? <Link className="text-primary hover:underline" to={`/wanted?version=${encodeURIComponent(target)}`}>Wanted</Link> : null}
        </p>
      ) : null}
      {ask ? <SearchConfirm count={ids.length} target={target} label={targetLabel} busy={busy} onClose={() => setAsk(false)} onConfirm={runSearch} /> : null}
      {review ? (
        <ReviewDialog
          titleIds={titles.map((t) => t.id ?? 0).filter(Boolean)}
          target={target}
          targetLabel={target ? columns.find((c) => c.key === target)?.label ?? target : "every library"}
          labels={new Map(columns.map((c) => [c.key, c.label]))}
          monitored={on}
          onClose={() => setReview(false)}
          onDone={() => {
            setReview(false);
            onClear();
          }}
        />
      ) : null}
    </section>
  );
}

/** The confirmation before a selection is queued. The hourly limit is read here (GET /wanted), not by the bar. */
function SearchConfirm({ count, target, label, busy, onClose, onConfirm }: { count: number; target: string; label: string; busy: boolean; onClose: () => void; onConfirm: () => void }) {
  const wanted = useWanted({ only: "missing", version: target, limit: 1, offset: 0 });
  const perHour = wanted.data?.per_hour;
  return (
    <ConfirmDialog
      danger={false}
      testid="bulk-search-confirm"
      busy={busy}
      onClose={onClose}
      onConfirm={onConfirm}
      scope={{
        title: `Search ${plural(count, "title")} in ${label}`,
        confirm: `Queue ${plural(count, "title")}`,
        does: [`Queue ${plural(count, "title")} for a search of what is missing in ${label}; a series is searched season by season`],
        keeps: ["The hourly limit: no search goes around it", "Monitoring and files", "Titles with nothing missing in this library: they are skipped"],
      }}
    >
      <p data-testid="bulk-search-estimate">
        {perHour ? `Searches start as the hourly limit allows: up to ${perHour} an hour.` : "Searches start as the hourly limit allows."}
      </p>
    </ConfirmDialog>
  );
}

const GROUPS: { id: string; outcomes: BulkItem["outcome"][] }[] = [
  { id: "change", outcomes: ["changed"] },
  { id: "excluded", outcomes: ["missing", "stale"] },
  { id: "same", outcomes: ["unchanged"] },
];

function ItemList({ items, dryRun, labels }: { items: BulkItem[]; dryRun: boolean; labels: ReadonlyMap<string, string> }) {
  return (
    <div className="space-y-3">
      {GROUPS.map((g) => {
        const list = items.filter((i) => g.outcomes.includes(i.outcome));
        if (list.length === 0) return null;
        const head = g.id === "change" ? (dryRun ? "Will change" : "Changed") : g.id === "excluded" ? (dryRun ? "Excluded" : "Skipped") : "Already set";
        return (
          <div key={g.id} data-testid={`bulk-${g.id}`}>
            <div className="mb-1 text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">{head} · {list.length}</div>
            <ul className="space-y-1">
              {list.map((i) => (
                <li key={`${i.title_id}/${i.version}`} className="flex min-w-0 items-start gap-2 text-sm" data-outcome={i.outcome}>
                  <CopyDot vkey={i.version} className="mt-1.5" />
                  <span className="min-w-0 [overflow-wrap:anywhere]">
                    <span className="font-medium">{i.title}</span> · {labels.get(i.version) || i.label || i.version}
                    <span className="block text-xs text-[var(--ink-3)]">{i.reason || outcomeWords(i.outcome, dryRun)}</span>
                  </span>
                </li>
              ))}
            </ul>
          </div>
        );
      })}
    </div>
  );
}

function ReviewDialog({
  titleIds,
  target,
  targetLabel,
  labels,
  monitored,
  onClose,
  onDone,
}: {
  titleIds: number[];
  target: string;
  targetLabel: string;
  /** The copies' display labels in view (lib/copies distinctLabels), so the items read as the columns do. */
  labels: ReadonlyMap<string, string>;
  monitored: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const preview = useBulkMonitor();
  const apply = useBulkMonitor();
  const cancel = useRef<HTMLButtonElement>(null);
  const versions = target ? [target] : undefined;
  // One preview when the dialog opens (a dry run writes nothing); the dialog is opened afresh for each review.
  useEffect(() => {
    preview.mutate({ title_ids: titleIds, versions, monitored, dry_run: true });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const pv: BulkMonitorResult | undefined = preview.data;
  const done = apply.data;
  const shown = done ?? pv;
  const changes = pv?.changed ?? 0;
  return (
    <Modal
      label="Review bulk edit"
      onClose={done ? onDone : onClose}
      testid="bulk-review"
      initialFocus={cancel}
      className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] sm:max-w-lg sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <h2 className="text-lg font-semibold text-[var(--ink)]">Review bulk edit</h2>
        <p className="text-sm text-muted-foreground">
          Monitoring {monitored ? "on" : "off"} · {targetLabel} · {titleIds.length} title{titleIds.length === 1 ? "" : "s"}
        </p>
        <button type="button" onClick={done ? onDone : onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4 text-sm">
        {preview.isPending ? (
          <p className="flex items-center gap-2 text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Checking each library…</p>
        ) : null}
        {preview.error ? <p className="text-[var(--bad-ink)]" data-testid="bulk-error">{errText(preview.error)}</p> : null}
        {shown ? (
          <>
            <p
              className={cn(
                "rounded-md border px-3 py-2",
                done
                  ? "border-[var(--th-success-border)] bg-[var(--ok-soft)] text-[var(--ok-ink)]"
                  : "border-[var(--line)] bg-[var(--bg)]",
              )}
              data-testid="bulk-message"
            >
              {shown.message}
            </p>
            <ItemList items={shown.items} dryRun={!done} labels={labels} />
          </>
        ) : null}
        {apply.error ? <p className="text-[var(--bad-ink)]" data-testid="bulk-error">{errText(apply.error)}</p> : null}
        <p className="text-xs text-[var(--ink-3)]">Only monitoring changes. Files stay in place and no search starts.</p>
      </div>
      <div className="flex justify-end gap-2 border-t border-[var(--line)] p-3">
        <Button ref={cancel} variant="ghost" size="sm" onClick={done ? onDone : onClose}>
          {done ? "Close" : "Cancel"}
        </Button>
        {done ? null : (
          <Button
            size="sm"
            disabled={!pv || changes === 0 || apply.isPending}
            onClick={() => pv && apply.mutate({ title_ids: titleIds, versions, monitored, expect: bulkExpect(pv.items) })}
          >
            {apply.isPending ? <Loader2 className="animate-spin" /> : null}
            {changes ? `Apply to ${changes === 1 ? "1 library" : `${changes} libraries`}` : "Nothing to change"}
          </Button>
        )}
      </div>
    </Modal>
  );
}
