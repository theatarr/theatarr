// The library's select mode (docs/design/ui.md "Library views"): with titles selected, a bar at the foot of the view
// offers, for managers, Monitor and Unmonitor (a review of POST /titles/monitor dry_run, then the apply with the
// preview's values as expect, in every library or one picked in the review), Search now (the selection's monitored
// libraries to POST /wanted/search, the Wanted page's queue under the hourly limit), Add to a library (with the add
// permission), Remove from a library and Remove (bulk-actions.tsx). The bar counts titles.
import { Loader2, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { useBulkMonitor, type BulkItem, type BulkMonitorResult } from "@/api/copies";
import { usePerms, type TitleRow } from "@/api/library";
import { useWanted, useWantedSearch, type WantedSearchBody } from "@/api/wanted";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Modal } from "@/components/ui/modal";
import { SELECT } from "@/components/library/filters";
import { bulkExpect, outcomeWords, type CopyColumn } from "@/lib/copies";
import { cn } from "@/lib/utils";
import { BulkAddDialog, BulkRemoveDialog } from "./bulk-actions";
import { CopyDot } from "./copy-bits";
import { plural } from "@/lib/format";

/** POST /wanted/search takes at most this many items (internal/web/wanted.go maxSearchNowItems). */
const SEARCH_CHUNK = 200;

type SearchItem = WantedSearchBody["items"] extends (infer I)[] | undefined ? I : never;

/** Search now for a selection: one item per title and library it is monitored in. */
export function searchItems(titles: readonly TitleRow[]): SearchItem[] {
  return titles.flatMap((t) => (t.versions ?? []).filter((v) => v.monitored && v.key).map((v) => ({ title_id: t.id ?? 0, version: v.key ?? "" })));
}

/** What the Search now requests of one selection answered, added up: queued (the searches a request added) and skipped
 * are per request and started is what each request let start; started and pending count the whole Search now queue,
 * not only this selection's part, and pending is the last answer's. */
type SearchSum = {
  /** The selection it was for; the bar shows it only while it is unchanged. */
  key: string;
  /** Searches in the requests that were answered. */
  sent: number;
  of: number;
  queued: number;
  started: number;
  pending: number;
  /** The titles skipped, by the server's reason. */
  skipped: Map<string, Set<number>>;
  error?: unknown;
};

function searchWords(r: SearchSum): string {
  const parts = [`Queued ${plural(r.queued, "search", "searches")}. ${r.started} started now, ${r.pending} waiting in the queue`];
  for (const [reason, ids] of r.skipped) parts.push(`Skipped ${plural(ids.size, "title")}: ${reason}`);
  return parts.join(". ") + ".";
}

type Open = "monitor" | "unmonitor" | "search" | "add" | "unlib" | "remove" | null;

export function BulkBar({ titles, columns, onClear, kinds = [], anime = false }: {
  titles: TitleRow[];
  columns: CopyColumn[];
  onClear: () => void;
  /** The kinds the list holds, for Add to a library's choice. */
  kinds?: ("movie" | "series")[];
  anime?: boolean;
}) {
  const perms = usePerms();
  const search = useWantedSearch();
  const [open, setOpen] = useState<Open>(null);
  const [busy, setBusy] = useState(false);
  const [sum, setSum] = useState<SearchSum>();
  const ids = titles.map((t) => t.id ?? 0).filter(Boolean);
  const key = ids.join();
  const result = sum?.key === key ? sum : undefined;
  const items = searchItems(titles);
  // One request after another: each answer's pending is the queue after it.
  const runSearch = async () => {
    const out: SearchSum = { key, sent: 0, of: items.length, queued: 0, started: 0, pending: 0, skipped: new Map() };
    setBusy(true);
    try {
      for (let i = 0; i < items.length; i += SEARCH_CHUNK) {
        const chunk = items.slice(i, i + SEARCH_CHUNK);
        const r = await search.mutateAsync({ only: "missing", items: chunk });
        out.sent += chunk.length;
        out.queued += r.queued;
        out.started += r.started;
        out.pending = r.pending;
        for (const s of r.skipped ?? []) out.skipped.set(s.reason, (out.skipped.get(s.reason) ?? new Set()).add(s.title_id));
      }
    } catch (e) {
      out.error = e;
    }
    setBusy(false);
    setOpen(null);
    setSum(out);
  };
  if (titles.length === 0 || !perms.manage) return null;
  const done = () => {
    setOpen(null);
    onClear();
  };
  return (
    <section
      aria-label="Bulk edit"
      data-testid="bulk-bar"
      className="sticky bottom-3 z-30 mt-4 rounded-xl border border-[var(--th-baccent)] bg-[var(--surface)] p-3 shadow-[var(--shadow-2)]"
    >
      <div className="flex flex-wrap items-center gap-2">
        <strong className="text-sm">{plural(titles.length, "title")} selected</strong>
        <Button variant="ghost" size="sm" onClick={onClear}>Clear</Button>
        <span className="grow" />
        <Button variant="outline" size="sm" onClick={() => setOpen("monitor")}>Monitor</Button>
        <Button variant="outline" size="sm" onClick={() => setOpen("unmonitor")}>Unmonitor</Button>
        <Button variant="outline" size="sm" disabled={busy} onClick={() => setOpen("search")}>Search now</Button>
        {perms.add ? <Button variant="outline" size="sm" onClick={() => setOpen("add")}>Add to a library</Button> : null}
        <Button variant="outline" size="sm" disabled={columns.length === 0} onClick={() => setOpen("unlib")}>Remove from a library</Button>
        <Button variant="destructive" size="sm" onClick={() => setOpen("remove")}>Remove</Button>
      </div>
      {result ? (
        <p className="mt-2 text-[13px] text-[var(--ink-2)]" data-testid="bulk-search-result">
          {result.sent > 0 ? <span role="status">{searchWords(result)} </span> : null}
          {result.error ? (
            <span role="alert" className="text-[var(--bad-ink)]">
              {result.sent > 0 ? `Stopped after ${result.sent} of ${plural(result.of, "search", "searches")}: ` : ""}{errText(result.error)}{" "}
            </span>
          ) : null}
          {result.sent > 0 ? <Link className="text-primary hover:underline" to="/wanted">Wanted</Link> : null}
        </p>
      ) : null}
      {open === "search" ? <SearchConfirm titles={titles} items={items.length} busy={busy} onClose={() => setOpen(null)} onConfirm={runSearch} /> : null}
      {open === "monitor" || open === "unmonitor" ? (
        <ReviewDialog titleIds={ids} columns={columns} monitored={open === "monitor"} onClose={() => setOpen(null)} onDone={done} />
      ) : null}
      {open === "add" ? <BulkAddDialog titles={titles} kinds={kinds} anime={anime} onClose={() => setOpen(null)} onDone={done} /> : null}
      {open === "unlib" ? <BulkRemoveDialog titles={titles} libraries={columns} onClose={() => setOpen(null)} onDone={done} /> : null}
      {open === "remove" ? <BulkRemoveDialog titles={titles} onClose={() => setOpen(null)} onDone={done} /> : null}
    </section>
  );
}

/** The confirmation before a selection is queued. The hourly limit is read here (GET /wanted), not by the bar. */
function SearchConfirm({ titles, items, busy, onClose, onConfirm }: { titles: TitleRow[]; items: number; busy: boolean; onClose: () => void; onConfirm: () => void }) {
  const wanted = useWanted({ only: "missing", limit: 1, offset: 0 });
  const perHour = wanted.data?.per_hour;
  const none = titles.filter((t) => !(t.versions ?? []).some((v) => v.monitored)).length;
  const n = titles.length - none;
  return (
    <ConfirmDialog
      danger={false}
      testid="bulk-search-confirm"
      busy={busy}
      onClose={onClose}
      onConfirm={onConfirm}
      scope={{
        title: `Search ${plural(titles.length, "title")}`,
        confirm: `Queue ${plural(items, "search", "searches")}`,
        does: [`Queue a search of what is missing for ${plural(n, "title")} in every library ${n === 1 ? "it is" : "each is"} monitored in (${plural(items, "search", "searches")}); a series is searched season by season`],
        keeps: [
          "The hourly limit: no search goes around it",
          "Monitoring and files",
          "Libraries with nothing missing: they are skipped",
          ...(none ? [`${plural(none, "title")} monitored in no library: not searched`] : []),
        ],
        blocked: items === 0 ? "None of these titles is monitored in a library" : undefined,
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
  columns,
  monitored,
  onClose,
  onDone,
}: {
  titleIds: number[];
  /** The copies' display labels in view (lib/copies distinctLabels), so the items read as the columns do. */
  columns: CopyColumn[];
  monitored: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const [target, setTarget] = useState("");
  const preview = useBulkMonitor();
  const apply = useBulkMonitor();
  const cancel = useRef<HTMLButtonElement>(null);
  const versions = target ? [target] : undefined;
  const labels = new Map(columns.map((c) => [c.key, c.label]));
  const targetLabel = target ? labels.get(target) ?? target : "every library";
  // A preview when the dialog opens and when the library changes (a dry run writes nothing).
  useEffect(() => {
    preview.mutate({ title_ids: titleIds, versions, monitored, dry_run: true });
  }, [target]); // eslint-disable-line react-hooks/exhaustive-deps
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
          {monitored ? "Monitor" : "Unmonitor"} · {targetLabel} · {plural(titleIds.length, "title")}
        </p>
        <button type="button" onClick={done ? onDone : onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4 text-sm">
        {done ? null : (
          <select aria-label="Libraries" className={cn(SELECT, "w-full")} value={target} onChange={(e) => setTarget(e.target.value)}>
            <option value="">Every library</option>
            {columns.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
          </select>
        )}
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
