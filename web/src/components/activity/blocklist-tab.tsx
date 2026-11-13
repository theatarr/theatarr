// Activity › Blocklist (pages/activity.tsx): the releases a version never routes again (accounts that see files), as a
// table (DataTable) newest first, 50 a page (Pager), filtered by part of the title's name and the library (sent as
// title and version; the total is the filtered count). Each row has Unblock, which can also search the library again.
import { CircleSlash, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { BLOCKED_PAGE, unblockRelease, useActivityBlocked, type ActivityBlocked } from "@/api/activity";
import { useDebounced } from "@/api/add";
import { useFacets, usePerms } from "@/api/library";
import { BulkDialog, SelectionLine, useChosen } from "@/components/activity/bulk";
import { UnblockAction } from "@/components/activity/download-actions";
import { Dash, tight } from "@/components/activity/row-bits";
import { Empty, ErrorState, Loading, VersionTag } from "@/components/page";
import { Card } from "@/components/ui/card";
import { DataTable, type DataColumn } from "@/components/ui/data-table";
import { Input } from "@/components/ui/input";
import { Pager } from "@/components/ui/pager";
import { Select } from "@/components/ui/select";
import { bulkWords } from "@/lib/activity";
import { distinctLabels, relabel } from "@/lib/copies";
import { fmtTime } from "@/lib/format";

const rowKey = (b: ActivityBlocked) => `${b.version}\n${b.info_hash}`;
const releases = (n: number) => `${n} ${n === 1 ? "release" : "releases"}`;

const titleName = (b: ActivityBlocked) => `${b.title.name}${b.title.year ? ` (${b.title.year})` : ""}`;

const COLUMNS: DataColumn<ActivityBlocked>[] = tight([
  { id: "title", label: "Title", card: "none", cellClassName: "max-w-[10.5rem]", cell: (b) => <span className="block truncate font-medium text-[var(--ink)]">{titleName(b)}</span> },
  { id: "library", label: "Library", cell: (b) => <VersionTag k={b.version} className="text-[11px]" /> },
  {
    id: "release",
    label: "Release",
    card: "full",
    cellClassName: "max-w-[12rem]",
    cell: (b) => <span className="block font-mono text-[12px] max-sm:break-all sm:truncate" title={b.release}>{b.release}</span>,
  },
  { id: "quality", label: "Quality", card: "none", cellClassName: "whitespace-nowrap", cell: (b) => b.quality || <Dash /> },
  { id: "indexer", label: "Indexer", card: "none", cellClassName: "whitespace-nowrap", cell: (b) => b.indexer || <Dash /> },
  {
    id: "reason",
    label: "Reason",
    card: "full",
    cellClassName: "min-w-[9rem] max-w-[12rem]",
    cell: (b) => (
      <span className="block text-[12.5px] leading-snug" title={b.reason || undefined}>
        <span className="text-[var(--warn-ink)]">{b.words}</span>
        {b.researched_at ? <span className="block text-[11px] text-muted-foreground">searched again</span> : null}
      </span>
    ),
  },
  { id: "blocked", label: "Blocked", cellClassName: "whitespace-nowrap tabular-nums", cell: (b) => fmtTime(b.created_at) },
]);

export function BlocklistTab() {
  const [text, setText] = useState("");
  const title = useDebounced(text.trim(), 350);
  const [version, setVersion] = useState("");
  const [offset, setOffset] = useState(0);
  // A new filter starts at the first page.
  useEffect(() => setOffset(0), [title, version]);
  const q = useActivityBlocked({ title, version, offset });
  const facets = useFacets();
  const all = facets.data?.versions ?? [];
  const copies = relabel(all, distinctLabels(all));
  const total = q.data?.total ?? 0;
  // The last row of the last page unblocked: show the page before it.
  useEffect(() => {
    if (q.data && offset > 0 && offset >= total) setOffset(Math.max(0, offset - BLOCKED_PAGE));
  }, [q.data, offset, total]);
  const filtered = title !== "" || version !== "";
  const items = q.data?.blocked ?? [];
  const manage = usePerms().manage;
  const select = useChosen(items.map(rowKey));
  const [unblocking, setUnblocking] = useState(false);
  return (
    <div>
      <div className="mb-4 grid grid-cols-1 gap-2 sm:flex sm:flex-wrap sm:items-center" data-testid="blocked-filters">
        <div className="relative min-w-0 sm:max-w-64 sm:flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input aria-label="Title" placeholder="Title" value={text} onChange={(e) => setText(e.target.value)} className="h-8 pl-8" data-testid="blocked-title" />
        </div>
        <Select aria-label="Library" value={version} onChange={(e) => setVersion(e.target.value)} className="sm:w-auto" data-testid="blocked-copy">
          <option value="">All libraries</option>
          {copies.map((v) => (
            <option key={v.key} value={v.key}>{v.label || v.key}</option>
          ))}
        </Select>
      </div>
      {q.isPending ? (
        <Loading />
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : items.length === 0 ? (
        <Card>
          <Empty icon={CircleSlash}>
            {filtered ? (
              <span>No blocked release matches.</span>
            ) : (
              <>
                <span>No release is blocked.</span>
                <span className="text-xs">A release lands here when it fails the library check, stalls, holds dangerous files, is removed with blocklist, or is judged wrong in the journal.</span>
              </>
            )}
          </Empty>
        </Card>
      ) : (
        <div className="space-y-4">
          <SelectionLine count={select.count} action="Unblock selected…" onAct={() => setUnblocking(true)} onClear={select.clear} />
          <DataTable
            rows={items}
            rowKey={rowKey}
            columns={COLUMNS}
            select={manage ? select : undefined}
            selectAllLabel={`Select all ${items.length} shown`}
            rowLabel={(b) => b.release}
            testid="blocked-table"
            rowProps={() => ({ "data-testid": "blocked-row" })}
            cardHead={(b) => <span className="block font-medium text-[var(--ink)]">{titleName(b)}</span>}
            actions={(b) => <UnblockAction b={b} />}
          />
          <Pager offset={offset} limit={BLOCKED_PAGE} total={total} onPage={setOffset} />
        </div>
      )}
      {unblocking ? (
        <BulkDialog
          items={items.filter((b) => select.chosen.has(rowKey(b)))}
          scope={(n) => ({
            title: `Unblock ${releases(n)}`,
            does: [`${n === 1 ? "This release" : `These ${n} releases`} may be picked again for ${n === 1 ? "its library" : "their libraries"}`],
            keeps: ["Nothing is searched now"],
            confirm: `Unblock ${releases(n)}`,
          })}
          run={unblockRelease}
          words={(n, failed) => bulkWords("Unblocked", "Not unblocked", ["release", "releases"], n, failed)}
          done="Releases unblocked"
          onDone={select.clear}
          onClose={() => setUnblocking(false)}
          danger={false}
          testid="unblock-releases-dialog"
        />
      ) : null}
    </div>
  );
}
