// Activity › History filters (lib/activity-history.ts): a title search, the copy, the event type and a since date,
// in one wrapping row; at phone width they stack, one per line, the date input full width. Every change goes to the URL, so a filtered history survives a reload
// and can be shared; the title search waits for typing to pause.
import { Search, X } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { useDebounced } from "@/api/add";
import { useFacets } from "@/api/library";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import {
  clearHistoryFilters,
  HISTORY_EVENTS,
  historyFilterCount,
  parseHistoryFilters,
  withHistoryFilter,
  type HistoryFilters as Filters,
} from "@/lib/activity-history";
import { distinctLabels, relabel } from "@/lib/copies";
import { useLibraryKnown, useLibraryName } from "@/lib/library-names";
import { today } from "@/lib/library";

const FIELD = "h-8 rounded-[5px] border border-input bg-field px-2 text-[13.5px]";

/** The filters the URL names, and the filter row. */
export function useHistoryFilters(): Filters {
  const [sp] = useSearchParams();
  return parseHistoryFilters(sp);
}

/** tools: buttons at the row's end (the table's Columns). Library names never hold keys, for every account. */
export function HistoryFilters({ tools }: { tools?: ReactNode } = {}) {
  const nameOf = useLibraryName();
  const known = useLibraryKnown();
  const [sp, setSp] = useSearchParams();
  const f = parseHistoryFilters(sp);
  const facets = useFacets();
  const [text, setText] = useState(f.title);
  const typed = useDebounced(text.trim(), 350);
  useEffect(() => {
    if (typed !== f.title) setSp((cur) => withHistoryFilter(cur, "title", typed), { replace: true });
    // Only the typed text drives this; the URL's value is compared, not followed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [typed]);
  const set = (key: keyof Filters, value: string) => setSp(withHistoryFilter(sp, key, value), { replace: true });
  const all = facets.data?.versions ?? [];
  const copies = relabel(all, distinctLabels(all));
  const count = historyFilterCount(f);
  return (
    <div className="mb-4 grid grid-cols-1 gap-2 sm:flex sm:flex-wrap sm:items-center" data-testid="history-filters">
      <div className="relative min-w-0 sm:max-w-64 sm:flex-1">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input aria-label="Title" placeholder="Title" value={text} onChange={(e) => setText(e.target.value)} className="h-8 pl-8" data-testid="history-title" />
      </div>
      <Select aria-label="Library" value={f.copy} onChange={(e) => set("copy", e.target.value)} className="sm:w-auto" data-testid="history-copy">
        <option value="">All libraries</option>
        {copies.map((v) => (
          <option key={v.key} value={v.key}>{v.label || v.key}</option>
        ))}
        {f.copy && !copies.some((v) => v.key === f.copy) ? <option value={f.copy}>{known(f.copy) ? nameOf(f.copy) : "Removed library"}</option> : null}
      </Select>
      <Select aria-label="Event" value={f.event} onChange={(e) => set("event", e.target.value)} className="sm:w-auto" data-testid="history-event">
        {HISTORY_EVENTS.map((e) => (
          <option key={e.id} value={e.id}>{e.label}</option>
        ))}
      </Select>
      <label className="flex min-w-0 items-center gap-1.5 text-sm text-[var(--ink-3)]">
        Since
        <input type="date" aria-label="Since" value={f.since} max={today()} onChange={(e) => set("since", e.target.value)} className={`${FIELD} w-full min-w-0 flex-1 text-foreground sm:w-auto`} data-testid="history-since" />
      </label>
      {count > 0 ? (
        <Button
          variant="ghost"
          size="xs"
          className="justify-self-start"
          onClick={() => {
            setText("");
            setSp(clearHistoryFilters(sp), { replace: true });
          }}
          data-testid="history-clear"
        >
          <X /> Clear {count > 1 ? `${count} filters` : "filter"}
        </Button>
      ) : null}
      {tools ? <div className="flex justify-end sm:ml-auto">{tools}</div> : null}
    </div>
  );
}
