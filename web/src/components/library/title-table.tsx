// The grid's table view (docs/design/ui.md "Library views"): a row per title and a column per library (version key), each
// header naming the copy and its ownership (followed: read-only here, or Theatarr-managed). A copy cell has its state,
// current quality, bar (linked solid, in progress striped), acquisition stage and, for managers, its monitor switch
// (locked on a followed copy). The extra columns (next / activity, quality, size, added, network) are picked and
// ordered in the column picker; the headers sort the grid. In select mode a checkbox column feeds the bulk bar.
// Dense rows under a sticky header while the table fits its frame; a table wider than its frame (many libraries or
// columns) scrolls sideways instead, so every column stays reachable (useFitsFrame). Below the sm
// breakpoint the rows stack as cards, the extra columns as lines under the title. Libraries under a title opens its
// copy dialog (copy-dialog.tsx). Without "See files" one Availability column replaces the per-library ones.
import { ArrowDown, ArrowUp } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { usePerms, type TitleRow } from "@/api/library";
import { VersionStatus } from "@/components/version-status";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TitleThumb } from "@/components/poster-card";
import {
  TABLE_COLUMNS,
  columnOwnerWords,
  copyBar,
  copyStatus,
  headerSort,
  nextWords,
  titleAcquisition,
  type ColumnId,
  type CopyColumn,
} from "@/lib/copies";
import { fmtAgo, fmtBytes } from "@/lib/format";
import { titleWatchWords, upcomingRelease } from "@/lib/library";
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { CopyBarLine, CopyDot, OwnerLine, StageLink, useMinWidth } from "./copy-bits";
import { CopyDialog, CopyMonitor } from "./copy-dialog";
import { useEndReached } from "./poster-grid";

type Version = NonNullable<TitleRow["versions"]>[number];

export type TableSort = { sort: string; sortVersion?: string; desc: boolean };

export type Selection = { chosen: ReadonlySet<number>; toggle: (id: number, on: boolean) => void; all: (on: boolean) => void };

type Props = {
  titles: TitleRow[];
  more: boolean;
  onMore: () => void;
  seeFiles: boolean;
  manage: boolean;
  columns: CopyColumn[];
  extra: ColumnId[];
  sort: TableSort;
  onSort: (s: { sort: string; sort_version?: string }) => void;
  select?: Selection;
};

const TONE = {
  positive: "text-[var(--ok-ink)]",
  warning: "text-[var(--warn-ink)]",
  negative: "text-[var(--bad-ink)]",
  neutral: "text-muted-foreground",
} as const;

function SortButton({ children, s, sort, onSort, label }: { children: React.ReactNode; s?: { sort: string; sort_version?: string }; sort: TableSort; onSort: Props["onSort"]; label: string }) {
  if (!s) return <>{children}</>;
  const active = sort.sort === s.sort && (sort.sortVersion ?? "") === (s.sort_version ?? "");
  return (
    <button type="button" onClick={() => onSort(s)} aria-label={`Sort by ${label}`} className="inline-flex min-w-0 items-center gap-1 text-left hover:text-foreground">
      {children}
      {active ? (sort.desc ? <ArrowDown className="size-3 shrink-0" aria-hidden /> : <ArrowUp className="size-3 shrink-0" aria-hidden />) : null}
    </button>
  );
}

const ariaSort = (sort: TableSort, s?: { sort: string; sort_version?: string }) =>
  s && sort.sort === s.sort && (sort.sortVersion ?? "") === (s.sort_version ?? "") ? (sort.desc ? "descending" : "ascending") : undefined;

/** One copy of one title: state, quality, bar, stage and the monitor switch. */
function CopyCell({ t, v, seeFiles, manage }: { t: TitleRow; v: Version | undefined; seeFiles: boolean; manage: boolean }) {
  const admin = usePerms().admin;
  if (!v) return <span className="text-xs text-[var(--ink-3)]">Not in this library</span>;
  const st = copyStatus(v, t.kind, !seeFiles, upcomingRelease(t));
  const q = seeFiles ? v.quality : resolutionWords(v.quality);
  const bar = copyBar(v, t.kind);
  return (
    <div className="space-y-1" data-copy={v.key}>
      <div className={cn("text-[13px] font-semibold", TONE[st.tone])}>{st.label}</div>
      {q ? <div className="text-xs text-muted-foreground">{q}</div> : null}
      {v.control?.grab_state ? <VersionStatus state={v.control.grab_state} why={false} canManage={admin} settingsKey={v.key} /> : null}
      <StageLink a={v.acquisition} />
      {t.kind === "series" || bar.flight ? <CopyBarLine vkey={v.key} label={v.label ?? v.key ?? ""} bar={bar} /> : null}
      {manage ? <CopyMonitor t={t} v={v} /> : null}
    </div>
  );
}

/** Without "See files" (a household account): one watchability line for the title (titleWatchWords) instead of a cell
 * per library. */
function WatchCell({ t }: { t: TitleRow }) {
  const w = titleWatchWords(t);
  if (!w) return <span className="text-[var(--ink-3)]">—</span>;
  return <span data-testid="watch-cell" className={cn("text-[13px] font-semibold", TONE[w.tone === "partial" ? "warning" : w.tone])}>{w.label}</span>;
}

/** An extra column's value for a title. */
function Extra({ id, t, seeFiles }: { id: ColumnId; t: TitleRow; seeFiles: boolean }) {
  switch (id) {
    case "next": {
      const a = titleAcquisition(t);
      if (a) {
        return (
          <span className="block min-w-0">
            <StageLink a={a.acquisition} />
            <span className="flex items-center gap-1 text-[11px] text-[var(--ink-3)]"><CopyDot vkey={a.version.key} className="size-1.5" />{a.version.label}</span>
          </span>
        );
      }
      const n = nextWords(t.next_up, t.kind);
      return <span className={n ? "" : "text-[var(--ink-3)]"}>{n || "—"}</span>;
    }
    case "quality":
      return (
        <span className="block space-y-0.5">
          {(t.versions ?? []).map((v) => (
            <span key={v.key} className="flex items-center gap-1.5 text-xs">
              <CopyDot vkey={v.key} className="size-1.5" />
              {(seeFiles ? v.quality : resolutionWords(v.quality)) || "—"}
            </span>
          ))}
        </span>
      );
    case "size":
      return <span className="tabular-nums">{t.size_bytes ? fmtBytes(t.size_bytes) : "—"}</span>;
    case "added":
      return <span className="text-muted-foreground">{t.added_at ? fmtAgo(t.added_at) : "—"}</span>;
    case "network":
      return <span className="text-muted-foreground">{t.network || "—"}</span>;
  }
}

function TitleCell({ t, onCopies }: { t: TitleRow; onCopies?: () => void }) {
  return (
    <span className="flex min-w-0 items-center gap-3">
      <TitleThumb id={t.id} />
      <span className="min-w-0">
        <Link to={`/title/${t.id}`} className="font-medium hover:text-primary [overflow-wrap:anywhere]">{t.name}</Link>
        <span className="block text-xs text-muted-foreground">
          {[t.year, t.anime ? "anime" : t.kind, t.monitored ? null : "not monitored"].filter(Boolean).join(" · ")}
        </span>
        {onCopies && (t.versions ?? []).length ? (
          <button type="button" onClick={onCopies} className="text-xs text-[var(--accent-ink)] hover:underline" aria-label={`${t.name}: libraries`}>
            Libraries
          </button>
        ) : null}
      </span>
    </span>
  );
}

/**
 * Whether the table fits its frame, measured (a ResizeObserver on both): the libraries and the chosen columns decide
 * its width, not the viewport. A fitting table clips so its header can stick (a scrolling wrapper is its own scroll
 * box); a wider one scrolls sideways. The table's width does not depend on which, so the answer is stable. Until
 * measured it scrolls, which never hides a column.
 */
function useFitsFrame(): [(el: HTMLElement | null) => void, boolean] {
  const [box, setBox] = useState<HTMLElement | null>(null);
  const [fits, setFits] = useState(false);
  useEffect(() => {
    const table = box?.querySelector("table");
    const frame = table?.parentElement;
    if (!table || !frame || typeof ResizeObserver === "undefined") return;
    const check = () => setFits(table.getBoundingClientRect().width <= frame.getBoundingClientRect().width + 0.5);
    const ro = new ResizeObserver(check);
    ro.observe(table);
    ro.observe(frame);
    check();
    return () => ro.disconnect();
  }, [box]);
  return [setBox, fits];
}

const label = (id: ColumnId) => TABLE_COLUMNS.find((c) => c.id === id)?.label ?? id;

export function TitleTable({ titles, more, onMore, seeFiles, manage, columns, extra, sort, onSort, select }: Props) {
  const end = useEndReached(onMore, more);
  const wide = useMinWidth(640);
  const [box, fits] = useFitsFrame();
  const [copiesOf, setCopiesOf] = useState<TitleRow | null>(null);
  const dialog = copiesOf ? <CopyDialog t={titles.find((x) => x.id === copiesOf.id) ?? copiesOf} seeFiles={seeFiles} manage={manage} onClose={() => setCopiesOf(null)} /> : null;
  const allChosen = !!select && titles.length > 0 && titles.every((t) => select.chosen.has(t.id ?? 0));
  const byKey = (t: TitleRow, key: string) => t.versions?.find((v) => v.key === key);
  const copyCols = seeFiles ? columns : [];
  if (!wide) {
    return (
      <>
        {select ? (
          <label className="mb-2 flex items-center gap-2 text-sm">
            <Checkbox checked={allChosen} onChange={(e) => select.all(e.target.checked)} />
            Select all shown titles
          </label>
        ) : null}
        <ul className="space-y-2" data-testid="title-list">
          {titles.map((t) => (
            <li key={t.id} className="cv-auto-row">
              <Card className={cn("space-y-2 p-3", select?.chosen.has(t.id ?? 0) && "ring-2 ring-[var(--accent)]")}>
                <div className="flex min-w-0 items-start gap-2">
                  {select ? (
                    <Checkbox aria-label={`Select ${t.name}`} className="mt-2" checked={select.chosen.has(t.id ?? 0)} onChange={(e) => select.toggle(t.id ?? 0, e.target.checked)} />
                  ) : null}
                  <div className="min-w-0 flex-1"><TitleCell t={t} onCopies={seeFiles ? () => setCopiesOf(t) : undefined} /></div>
                </div>
                {!seeFiles ? <WatchCell t={t} /> : (
                <div className="grid grid-cols-1 gap-2 min-[360px]:grid-cols-2">
                  {(t.versions ?? []).map((v) => (
                    <div key={v.key} className="min-w-0 rounded-lg border border-[var(--line)] p-2">
                      <div className="flex min-w-0 items-center gap-1.5 text-xs font-medium"><CopyDot vkey={v.key} />{v.label}</div>
                      <OwnerLine c={v.control} className="mb-1" />
                      <CopyCell t={t} v={v} seeFiles={seeFiles} manage={manage} />
                    </div>
                  ))}
                </div>
                )}
                {extra.length ? (
                  <dl className="space-y-0.5 text-xs">
                    {extra.map((id) => (
                      <div key={id} className="flex min-w-0 gap-2">
                        <dt className="shrink-0 text-[var(--ink-3)]">{label(id)}</dt>
                        <dd className="min-w-0 [overflow-wrap:anywhere]"><Extra id={id} t={t} seeFiles={seeFiles} /></dd>
                      </div>
                    ))}
                  </dl>
                ) : null}
              </Card>
            </li>
          ))}
        </ul>
        {more ? <div ref={end} aria-hidden className="h-12" /> : null}
        {dialog}
      </>
    );
  }
  return (
    <>
      <div ref={box} data-fits={fits || undefined}>
        <Card className="p-0">
          <Table density="dense" overflow={fits ? "clip" : "auto"}>
            <TableHeader>
              <TableRow>
                {select ? (
                  <TableHead className="w-8">
                    <Checkbox aria-label="Select all shown titles" checked={allChosen} onChange={(e) => select.all(e.target.checked)} />
                  </TableHead>
                ) : null}
                <TableHead aria-sort={ariaSort(sort, headerSort("title"))}>
                  <SortButton s={headerSort("title")} sort={sort} onSort={onSort} label="title">Title</SortButton>
                </TableHead>
                {!seeFiles ? <TableHead>Availability</TableHead> : null}
                {copyCols.map((c) => {
                  const s = headerSort("copy", c.key);
                  return (
                    <TableHead key={c.key} aria-sort={ariaSort(sort, s)} data-copy-col={c.key} className="align-bottom">
                      {c.owner ? <span className="block text-[10px] font-normal normal-case text-[var(--ink-3)]" data-testid="column-owner">{columnOwnerWords(c)}</span> : null}
                      <SortButton s={s} sort={sort} onSort={onSort} label={`${c.label} completion`}>
                        <CopyDot vkey={c.key} />
                        {c.label}
                      </SortButton>
                    </TableHead>
                  );
                })}
                {extra.map((id) => {
                  const s = headerSort(id);
                  return (
                    <TableHead key={id} aria-sort={ariaSort(sort, s)} className={cn(id === "size" && "text-right")}>
                      <SortButton s={s} sort={sort} onSort={onSort} label={label(id).toLowerCase()}>{label(id)}</SortButton>
                    </TableHead>
                  );
                })}
              </TableRow>
            </TableHeader>
            <TableBody>
              {titles.map((t) => (
                <TableRow key={t.id} className="cv-auto-row" data-selected={select?.chosen.has(t.id ?? 0) || undefined}>
                  {select ? (
                    <TableCell>
                      <Checkbox aria-label={`Select ${t.name}`} checked={select.chosen.has(t.id ?? 0)} onChange={(e) => select.toggle(t.id ?? 0, e.target.checked)} />
                    </TableCell>
                  ) : null}
                  <TableCell className="min-w-[150px]"><TitleCell t={t} onCopies={seeFiles ? () => setCopiesOf(t) : undefined} /></TableCell>
                  {!seeFiles ? <TableCell className="align-top"><WatchCell t={t} /></TableCell> : null}
                  {copyCols.map((c) => (
                    <TableCell key={c.key} className="align-top">
                      <CopyCell t={t} v={byKey(t, c.key)} seeFiles={seeFiles} manage={manage} />
                    </TableCell>
                  ))}
                  {extra.map((id) => (
                    <TableCell key={id} className={cn("align-top text-sm", id === "size" && "whitespace-nowrap text-right")}>
                      <Extra id={id} t={t} seeFiles={seeFiles} />
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </div>
      {seeFiles ? <p className="mt-2 text-xs text-[var(--ink-3)]">Bars count linked episodes; striped parts are in progress. A library's state is separate from its current quality.</p> : null}
      {more ? <div ref={end} aria-hidden className="h-12" /> : null}
      {dialog}
    </>
  );
}
