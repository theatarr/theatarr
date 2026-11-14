// The shared table (docs/design/ui.md "Library views"): rows under a dense sticky header, the page's chosen columns in
// order, sortable headers (SortHead), an optional select column (RowSelection), row actions at the end and an optional
// panel under a row (Details). Below the phone width there is no table: each row is a card with the head the page
// draws, then each column as a "Label value" line, the actions and the panel. Built for the index, Activity and the
// title page's Libraries table, not as a general table framework: the page keeps the sort, the selection and which
// row is open.
// The table clips while it fits its frame, so the header can stick, and scrolls sideways inside its frame when it does
// not (useFitsFrame).
import { Fragment, useEffect, useState, type ReactNode } from "react";
import { useMinWidth } from "@/components/library/copy-bits";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import type { RowSelection } from "@/components/ui/row-select";
import { SortHead, type SortDir } from "@/components/ui/sort-head";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

export type DataColumn<T> = {
  id: string;
  /** The header's words; also the label of the column's line on a phone and the sort button's name. */
  label: string;
  /** Header content when it is more than the label (a colour dot, an icon). */
  header?: ReactNode;
  cell: (row: T) => ReactNode;
  /** The header sorts the table (onSort with this column's id). */
  sortable?: boolean;
  headClassName?: string;
  cellClassName?: string;
  /** On a phone: a "Label value" line (default), the value alone on its own line, or nothing (cardHead shows it). */
  card?: "line" | "full" | "none";
};

/** Attributes the page puts on each row (the table row, or the card on a phone): a test id, data-*, a class. */
export type RowAttrs = { className?: string } & { [k: `data-${string}`]: string | number | boolean | undefined };

type Props<T, K extends string | number> = {
  rows: readonly T[];
  rowKey: (row: T) => K;
  /** The columns shown, in order, fixed ones included. */
  columns: readonly DataColumn<T>[];
  /** The column the table is sorted by and its direction. */
  sort?: { column: string; dir: SortDir };
  onSort?: (column: string) => void;
  select?: RowSelection<K>;
  /** The select-all box's name; default "Select all shown". */
  selectAllLabel?: string;
  /** A row's name for its select box ("Select <name>"). */
  rowLabel?: (row: T) => string;
  /** The row's actions: the last column, and the foot of the card on a phone. */
  actions?: (row: T) => ReactNode;
  /** The panel under the row (Details), when the page has it open; null or undefined: closed. */
  expanded?: (row: T) => ReactNode;
  /** The top of a row's card on a phone (title, status). */
  cardHead?: (row: T) => ReactNode;
  rowProps?: (row: T) => RowAttrs;
  /** The width from which the table shows (below it, cards); default 640. */
  minWidth?: number;
  testid?: string;
};

/**
 * Whether the table fits its frame, measured (a ResizeObserver on both): the chosen columns decide its width, not the
 * viewport. A fitting table clips so its header can stick (a scrolling wrapper is its own scroll box); a wider one
 * scrolls sideways. Until measured it scrolls, which never hides a column.
 */
export function useFitsFrame(): [(el: HTMLElement | null) => void, boolean, boolean] {
  const [box, setBox] = useState<HTMLElement | null>(null);
  const [fits, setFits] = useState(false);
  const [more, setMore] = useState(false);
  useEffect(() => {
    const table = box?.querySelector("table");
    const frame = table?.parentElement;
    if (!table || !frame || typeof ResizeObserver === "undefined") return;
    const check = () => {
      const f = table.getBoundingClientRect().width <= frame.getBoundingClientRect().width + 0.5;
      setFits(f);
      // More to the right: the frame is scrolled short of the table's end (the cue on the frame's right edge).
      setMore(!f && frame.scrollLeft + frame.clientWidth < frame.scrollWidth - 1);
    };
    const ro = new ResizeObserver(check);
    ro.observe(table);
    ro.observe(frame);
    frame.addEventListener("scroll", check, { passive: true });
    check();
    return () => {
      ro.disconnect();
      frame.removeEventListener("scroll", check);
    };
  }, [box]);
  return [setBox, fits, more];
}

export function DataTable<T, K extends string | number>({
  rows,
  rowKey,
  columns,
  sort,
  onSort,
  select,
  selectAllLabel = "Select all shown",
  rowLabel,
  actions,
  expanded,
  cardHead,
  rowProps,
  minWidth = 640,
  testid,
}: Props<T, K>) {
  const wide = useMinWidth(minWidth);
  const [box, fits, more] = useFitsFrame();
  const can = (r: T) => !select?.selectable || select.selectable(rowKey(r));
  const pickable = select?.selectable ? rows.filter(can) : rows;
  const chosenCount = select ? pickable.filter((r) => select.chosen.has(rowKey(r))).length : 0;
  const allChosen = pickable.length > 0 && chosenCount === pickable.length;
  const selectAll = select ? (
    <Checkbox aria-label={selectAllLabel} checked={allChosen} indeterminate={chosenCount > 0 && !allChosen} disabled={!!select.selectable && !pickable.length} onChange={(e) => select.all(e.target.checked)} />
  ) : null;
  const rowBox = (r: T) =>
    select ? <Checkbox aria-label={`Select ${rowLabel?.(r) ?? rowKey(r)}`} checked={can(r) && select.chosen.has(rowKey(r))} disabled={!can(r)} onChange={(e) => select.toggle(rowKey(r), e.target.checked)} /> : null;

  if (!wide) {
    return (
      <div data-testid={testid}>
        {select ? (
          <label className="mb-2 flex items-center gap-2 text-sm">
            {selectAll}
            <span aria-hidden>{selectAllLabel}</span>
          </label>
        ) : null}
        <ul className="space-y-2">
          {rows.map((r) => {
            const k = rowKey(r);
            const { className, ...attrs } = rowProps?.(r) ?? {};
            const lines = columns.filter((c) => (c.card ?? "line") !== "none");
            const panel = expanded?.(r);
            return (
              <li key={k} {...attrs} className={className}>
                <Card className={cn("space-y-2 p-3", select?.chosen.has(k) && "ring-2 ring-[var(--accent)]")}>
                  <div className="flex min-w-0 items-start gap-2">
                    {select ? <span className="mt-0.5 flex">{rowBox(r)}</span> : null}
                    <div className="min-w-0 flex-1 space-y-1.5">
                      {cardHead?.(r)}
                      {lines.length ? (
                        <dl className="space-y-0.5 text-xs">
                          {lines.map((c) => (
                            <div key={c.id} className="flex min-w-0 gap-2" data-column={c.id}>
                              {c.card === "full" ? null : <dt className="shrink-0 text-[var(--ink-3)]">{c.label}</dt>}
                              <dd className="min-w-0 [overflow-wrap:anywhere]">{c.cell(r)}</dd>
                            </div>
                          ))}
                        </dl>
                      ) : null}
                    </div>
                  </div>
                  {actions ? <div className="flex flex-wrap items-center gap-2">{actions(r)}</div> : null}
                  {panel ? <div className="border-t border-[var(--line)] pt-2">{panel}</div> : null}
                </Card>
              </li>
            );
          })}
        </ul>
      </div>
    );
  }

  const span = columns.length + (select ? 1 : 0) + (actions ? 1 : 0);
  return (
    <div ref={box} data-fits={fits || undefined} data-more={more || undefined}>
      {/* A table wider than its frame scrolls inside it; while columns are hidden on the right, the edge fades. */}
      <Card className={cn("p-0", more && "relative after:pointer-events-none after:absolute after:inset-y-0 after:right-0 after:w-8 after:rounded-r-[8px] after:bg-linear-to-l after:from-[var(--card)] after:to-transparent")}>
        <Table density="dense" overflow={fits ? "clip" : "auto"} data-testid={testid}>
          <TableHeader>
            <TableRow>
              {select ? <TableHead className="w-8">{selectAll}</TableHead> : null}
              {columns.map((c) => (
                <SortHead
                  key={c.id}
                  className={c.headClassName}
                  data-column={c.id}
                  dir={sort?.column === c.id ? sort.dir : undefined}
                  onSort={c.sortable && onSort ? () => onSort(c.id) : undefined}
                  sortLabel={c.label.toLowerCase()}
                >
                  {c.header ?? c.label}
                </SortHead>
              ))}
              {actions ? (
                <TableHead className="w-px">
                  <span className="sr-only">Actions</span>
                </TableHead>
              ) : null}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => {
              const k = rowKey(r);
              const panel = expanded?.(r);
              return (
                <Fragment key={k}>
                  <TableRow {...rowProps?.(r)} data-selected={select?.chosen.has(k) || undefined}>
                    {select ? <TableCell>{rowBox(r)}</TableCell> : null}
                    {columns.map((c) => (
                      <TableCell key={c.id} className={c.cellClassName} data-column={c.id}>
                        {c.cell(r)}
                      </TableCell>
                    ))}
                    {actions ? <TableCell className="whitespace-nowrap text-right">{actions(r)}</TableCell> : null}
                  </TableRow>
                  {panel ? (
                    <TableRow data-expanded>
                      <TableCell colSpan={span} className="bg-[var(--sunken)] px-4 py-3">
                        {panel}
                      </TableCell>
                    </TableRow>
                  ) : null}
                </Fragment>
              );
            })}
          </TableBody>
        </Table>
      </Card>
    </div>
  );
}
