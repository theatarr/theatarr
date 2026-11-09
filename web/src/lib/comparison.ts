import { fmtNumber } from "./format";

/** The comparison's Search results heading: how many the search found, how many of the best the comparison kept (the
 * rows shown; shadow.KeepResults), and how many of all found were accepted. */
export function resultsSummary(found: number | undefined, kept: number, accepted: number | undefined, note?: string): string {
  found ??= 0;
  const shown = kept >= found ? `${fmtNumber(found)} found, all shown` : `${fmtNumber(found)} found · best ${fmtNumber(kept)} kept and shown`;
  return `${shown} · ${fmtNumber(accepted ?? 0)} accepted${note ? ` · ${note}` : ""}`;
}
