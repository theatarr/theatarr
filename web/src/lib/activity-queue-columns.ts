// Activity › Queue's table (docs/design/ui.md "Activity"): the optional columns, the default ones, and the sort. The
// queue is loaded whole, so it sorts in the browser; columns are kept under theatarr.library.activity-queue.<user>
// (useColumnPrefs) and the sort under theatarr.library.activity-queue-sort.<user>.
import type { ActivityDownload } from "@/api/activity";
import type { ColumnDef } from "@/lib/prefs";

export type QueueColumnId = "library" | "quality" | "provider" | "size" | "progress" | "score" | "formats" | "indexer" | "release" | "added" | "updated";

export const QUEUE_COLUMNS: ColumnDef<QueueColumnId>[] = [
  { id: "library", label: "Library", hint: "The library it is for" },
  { id: "quality", label: "Quality", hint: "The release's quality" },
  { id: "provider", label: "Provider", hint: "The debrid provider or download client, and whether it was cached" },
  { id: "size", label: "Size", hint: "The release's size" },
  { id: "progress", label: "Progress", hint: "The bar while it moves; else how long it has waited and the next try" },
  { id: "score", label: "Score", hint: "The release's custom format score" },
  { id: "formats", label: "Formats", hint: "The custom formats it matched" },
  { id: "indexer", label: "Indexer", hint: "Where the release was found" },
  { id: "release", label: "Release", hint: "The release name" },
  { id: "added", label: "Added", hint: "When it was grabbed" },
  { id: "updated", label: "Last update", hint: "When it last changed" },
];

export const QUEUE_DEFAULT: QueueColumnId[] = ["library", "quality", "provider", "size", "progress"];

export const QUEUE_SORT_PREF = "activity-queue-sort";

export type QueueSortColumn = "library" | "provider" | "size" | "progress" | "score" | "indexer" | "added" | "updated";
export type QueueSort = { column: QueueSortColumn; dir: "asc" | "desc" };

const SORTABLE: QueueSortColumn[] = ["library", "provider", "size", "progress", "score", "indexer", "added", "updated"];
const TEXT: QueueSortColumn[] = ["library", "provider", "indexer"];

/** Newest first. */
export const QUEUE_SORT_DEFAULT: QueueSort = { column: "added", dir: "desc" };

export function isQueueSortColumn(id: string): id is QueueSortColumn {
  return (SORTABLE as string[]).includes(id);
}

/** The stored sort, or newest first. */
export function normalizeQueueSort(v: unknown): QueueSort {
  const o = (v && typeof v === "object" ? v : {}) as { column?: unknown; dir?: unknown };
  if (typeof o.column !== "string" || !isQueueSortColumn(o.column) || (o.dir !== "asc" && o.dir !== "desc")) return QUEUE_SORT_DEFAULT;
  return { column: o.column, dir: o.dir };
}

/** A header click: the same column turns around; another starts A–Z for words and largest/newest first otherwise. */
export function nextQueueSort(cur: QueueSort, column: QueueSortColumn): QueueSort {
  if (cur.column === column) return { column, dir: cur.dir === "asc" ? "desc" : "asc" };
  return { column, dir: TEXT.includes(column) ? "asc" : "desc" };
}

type Sortable = Pick<ActivityDownload, "id" | "version" | "provider_name" | "client" | "size" | "progress" | "score" | "indexer" | "created_at" | "updated_at">;

function key(d: Sortable, c: QueueSortColumn): string | number | undefined {
  switch (c) {
    case "library":
      return d.version;
    case "provider":
      return d.provider_name || (d.client ? `client ${d.client}` : undefined);
    case "size":
      return d.size || undefined;
    case "progress":
      return d.progress;
    case "score":
      return d.score;
    case "indexer":
      return d.indexer || undefined;
    case "added":
      return Date.parse(d.created_at);
    case "updated":
      return Date.parse(d.updated_at);
  }
}

/** The queue in the chosen order; rows without the value go last either way, ties newest first. */
export function sortDownloads<T extends Sortable>(items: readonly T[], s: QueueSort): T[] {
  const sign = s.dir === "asc" ? 1 : -1;
  return [...items].sort((a, b) => {
    const x = key(a, s.column);
    const y = key(b, s.column);
    const xu = x === undefined || (typeof x === "number" && Number.isNaN(x));
    const yu = y === undefined || (typeof y === "number" && Number.isNaN(y));
    if (xu !== yu) return xu ? 1 : -1;
    if (!xu && x !== y) {
      const c = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y));
      if (c !== 0) return sign * c;
    }
    return b.id - a.id;
  });
}
