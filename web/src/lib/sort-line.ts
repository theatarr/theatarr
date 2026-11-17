// The value of a list's active sort, as the table's cells say it (title-table.tsx cell) and as the posters' sort line
// shows it under each poster (docs/design/ui.md "Library views"): rating, size, runtime, added, latest file and
// release date. Title and Year have none: the poster already shows them.
import type { TitleRow } from "@/api/library";
import { fmtAgo, fmtBytes, fmtDay, localDay } from "@/lib/format";
import { fmtRuntime, SORTS } from "@/lib/library";
import { releasedText } from "@/lib/released";
import { releasedLabel } from "@/lib/title-layout";

export const ratingText = (t: Pick<TitleRow, "rating">) => (t.rating ? t.rating.toFixed(1) : "");
export const sizeText = (t: Pick<TitleRow, "size_bytes">) => (t.size_bytes ? fmtBytes(t.size_bytes) : "");
export const addedText = (t: Pick<TitleRow, "title_added_at">) => (t.title_added_at ? fmtDay(localDay(t.title_added_at)) : "");
export const latestFileText = (t: Pick<TitleRow, "added_at">) => (t.added_at ? fmtAgo(t.added_at) : "");

/** The poster detail (Poster options) a sort's line replaces while that sort is active: one line, in the table's words
 *  (Added shows the day, as the table, not the detail's age). */
export const SORT_DETAIL: Partial<Record<string, string>> = { rating: "rating", size: "size", created: "added" };

const VALUE: Partial<Record<string, (t: TitleRow) => string>> = {
  rating: ratingText,
  size: sizeText,
  runtime: (t) => fmtRuntime(t.runtime_minutes) ?? "",
  created: addedText,
  added: latestFileText,
  release: releasedText,
};

/** File values: none without "see files". */
const FILE_SORTS = new Set(["size", "added"]);

/** The posters' line for the active sort, resolved once per grid: "Rating 7.5", "Size 48.4 GiB", "Runtime 2 h 4 min",
 *  "Added Oct 1, 2025", "Latest file 3 d ago", "Released Jul 16, 1988" ("First aired" for a series). undefined for
 *  any other sort (Title and Year are on the poster already) and for file values (size, latest file) without "see
 *  files"; the function gives "" for a title without the value. */
export function sortLineFor(sort: string, seeFiles: boolean): ((t: TitleRow) => string) | undefined {
  const value = VALUE[sort];
  if (!value || (FILE_SORTS.has(sort) && !seeFiles)) return undefined;
  if (sort === "release") return (t) => (t.released ? `${releasedLabel(t.kind === "series", t.released)} ${value(t)}` : "");
  const label = SORTS.find((s) => s.id === sort)?.label ?? "";
  return (t) => {
    const v = value(t);
    return v ? `${label} ${v}` : "";
  };
}
