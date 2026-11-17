// A title's general release day as the lists and the title page show it (docs/design/ui.md "Library views"): the
// server's `released` (internal/web/library.go releasedSQL), a movie's earliest stored cinema date of any kind or
// TMDb's date, whichever is earlier, so a later wide re-release never makes an old film look new; a series' first air
// date. The Released column, its sort, About and the posters' sort line read it through releasedText; the In cinemas
// column, Upcoming, the calendar and home availability keep their own dates.
import { fmtDay } from "@/lib/format";

/** The general release day, formatted as the list's dates ("Jul 16, 1988"); "" when the title has none. */
export function releasedText(t: { released?: string | null }): string {
  return t.released ? fmtDay(t.released.slice(0, 10)) : "";
}
