// The About › Release dates summary (docs/design/ui.md "Release dates"): the stored rows behind the release-date
// choice the calendar, Home and the lists use, one per type; every other row is for "Show all".

type Row = { type?: string; date?: string; country?: string };
type Chosen = { in_cinemas?: string; digital_release?: string; physical_release?: string };

const CHOSEN: [string, keyof Chosen][] = [["theatrical", "in_cinemas"], ["digital", "digital_release"], ["physical", "physical_release"]];

/** Per release type, the row with the chosen date (a country-less row first, as the choice prefers), in cinema,
 *  digital, disc order; nothing for a type without a chosen date. */
export function chosenReleaseRows<R extends Row>(rows: R[], d: Chosen): R[] {
  const out: R[] = [];
  for (const [type, key] of CHOSEN) {
    const day = d[key];
    if (!day) continue;
    const same = rows.filter((r) => r.type === type && r.date?.slice(0, 10) === day);
    const row = same.find((r) => !r.country) ?? same[0];
    if (row) out.push(row);
  }
  return out;
}
