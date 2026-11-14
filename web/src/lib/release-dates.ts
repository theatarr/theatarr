// The About › Release dates summary (docs/design/ui.md "Release dates"): the stored rows behind the release-date
// choice the calendar, Home and the lists use, one per type; every other row is for "Show all".

type Row = { type?: string; date?: string; country?: string; source?: string };
type Chosen = { in_cinemas?: string; digital_release?: string; physical_release?: string };

const CHOSEN: [string, keyof Chosen][] = [["theatrical", "in_cinemas"], ["digital", "digital_release"], ["physical", "physical_release"]];

/** The server's precedence among rows of one date: the TMDb refresh (tmdb-art) first, then the other country-less
 *  rows (the arrs'), then TMDb's US row, then the rest. */
const rank = (r: Row) => (!r.country ? (r.source === "tmdb-art" ? 0 : 1) : r.country === "US" && r.source === "tmdb" ? 2 : 3);

/** Per release type, the row with the chosen date (in the server's precedence, rank), in cinema, digital, disc
 *  order; nothing for a type without a chosen date. */
export function chosenReleaseRows<R extends Row>(rows: R[], d: Chosen): R[] {
  const out: R[] = [];
  for (const [type, key] of CHOSEN) {
    const day = d[key];
    if (!day) continue;
    const same = rows.filter((r) => r.type === type && r.date?.slice(0, 10) === day);
    const row = [...same].sort((a, b) => rank(a) - rank(b))[0];
    if (row) out.push(row);
  }
  return out;
}
