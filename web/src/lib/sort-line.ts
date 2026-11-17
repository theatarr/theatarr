// The value of a list's active sort, as the table's cells say it (title-table.tsx cell) and as the posters' sort line
// shows it under each poster (docs/design/ui.md "Library views"): rating, size, runtime, added, latest file and
// release date. Title and Year have none: the poster already shows them.
import type { TitleRow } from "@/api/library";
import { fmtAgo, fmtBytes, fmtDay, localDay } from "@/lib/format";
import { fmtRuntime, kindLabel, SORTS, type CatalogId } from "@/lib/library";
import { releasedText } from "@/lib/released";
import { releasedLabel } from "@/lib/title-layout";

export const ratingText = (t: Pick<TitleRow, "rating">) => (t.rating ? t.rating.toFixed(1) : "");
export const sizeText = (t: Pick<TitleRow, "size_bytes">) => (t.size_bytes ? fmtBytes(t.size_bytes) : "");
export const addedText = (t: Pick<TitleRow, "title_added_at">) => (t.title_added_at ? fmtDay(localDay(t.title_added_at)) : "");
export const latestFileText = (t: Pick<TitleRow, "added_at">) => (t.added_at ? fmtAgo(t.added_at) : "");

/** The poster detail (Poster options) that already shows a sort's value: no second line for it. */
const SAME_DETAIL: Partial<Record<string, string>> = { rating: "rating", size: "size", created: "added" };

/** The posters' line for the active sort: "Rating 7.5", "Size 52.1 GB", "Runtime 2 h 4 min", "Added Oct 1, 2025",
 *  "Latest file 3 days ago", "Released Jul 16, 1988" ("First aired" for a series); "" for any
 *  other sort, a title without the value, a detail already shown, and file values (size, latest file) without "see
 *  files". */
export function sortLine(sort: string, t: TitleRow, o: { seeFiles: boolean; catalog?: CatalogId; details?: readonly string[] }): string {
  const shown = SAME_DETAIL[sort];
  if (shown && o.details?.includes(shown)) return "";
  const value =
    sort === "rating" ? ratingText(t)
    : sort === "size" ? (o.seeFiles ? sizeText(t) : "")
    : sort === "runtime" ? (fmtRuntime(t.runtime_minutes) ?? "")
    : sort === "created" ? addedText(t)
    : sort === "added" ? (o.seeFiles ? latestFileText(t) : "")
    : sort === "release" ? releasedText(t)
    : "";
  if (!value) return "";
  // The release day in the title's own words (Released, Releases, First aired, First airs), as About says it.
  if (sort === "release") return `${releasedLabel(t.kind === "series", t.released ?? "")} ${value}`;
  const label = SORTS.find((s) => s.id === sort)?.label ?? "";
  return `${kindLabel(o.catalog, sort, label)} ${value}`;
}
