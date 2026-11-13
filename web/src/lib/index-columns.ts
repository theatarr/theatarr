// The library table's extra columns (docs/design/ui.md "Library views"): which ones exist, the default choice, the
// stored choice read back, and the grid sort a header asks for. Moved unchanged from lib/copies.ts, which re-exports it.

// ---- Table columns ----

export type ColumnId = "next" | "quality" | "size" | "added" | "network";

export const TABLE_COLUMNS: { id: ColumnId; label: string; hint: string; seeFiles?: boolean }[] = [
  { id: "next", label: "Next / activity", hint: "What is in progress, else the next episode or release" },
  { id: "quality", label: "Quality per library", hint: "Each library's current quality", seeFiles: true },
  { id: "size", label: "Size", hint: "The title's library size", seeFiles: true },
  { id: "added", label: "Added", hint: "When the title was added" },
  { id: "network", label: "Network / studio", hint: "From the title's metadata" },
];

export type ColumnPrefs = { order: ColumnId[]; shown: ColumnId[] };

export const DEFAULT_COLUMNS: ColumnPrefs = { order: TABLE_COLUMNS.map((c) => c.id), shown: ["next", "size"] };

const isColumn = (x: unknown): x is ColumnId => TABLE_COLUMNS.some((c) => c.id === x);

/** Column prefs read back: unknown ids dropped, columns added since appended (hidden). */
export function normalizeColumns(p: unknown): ColumnPrefs {
  if (!p || typeof p !== "object") return DEFAULT_COLUMNS;
  const o = p as { order?: unknown; shown?: unknown };
  const order = Array.isArray(o.order) ? [...new Set(o.order.filter(isColumn))] : [];
  for (const c of TABLE_COLUMNS) if (!order.includes(c.id)) order.push(c.id);
  const shown = Array.isArray(o.shown) ? [...new Set(o.shown.filter(isColumn))] : DEFAULT_COLUMNS.shown;
  return { order, shown };
}

/** The extra columns shown, in order, for this account. */
export function visibleColumns(p: ColumnPrefs, seeFiles: boolean): ColumnId[] {
  return p.order.filter((id) => p.shown.includes(id) && (seeFiles || !TABLE_COLUMNS.find((c) => c.id === id)?.seeFiles));
}

// ---- Header sorting ----

/** The grid sort a table header asks for: a copy column sorts by that library's completion. */
export function headerSort(col: "title" | "copy" | ColumnId, key?: string): { sort: string; sort_version?: string } | undefined {
  switch (col) {
    case "title":
      return { sort: "name" };
    case "copy":
      return key ? { sort: "completion", sort_version: key } : undefined;
    case "next":
      return { sort: "next" };
    case "quality":
      return { sort: "quality" };
    case "size":
      return { sort: "size" };
    case "added":
      return { sort: "added" };
  }
  return undefined;
}

/** The order a sort starts in: names and next dates ascending, the rest descending (as the server defaults). */
export function defaultDesc(sort: string): boolean {
  return sort !== "name" && sort !== "next";
}
