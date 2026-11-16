// "Add to other providers" (docs/design/torrents.md): the result line of a copy, one part per provider in the provider
// tags the page already uses ("Added to TB · Not cached on RD").
import type { Schemas } from "@/api/client";
import { providerLabel } from "@/components/provider-pills";
import { fmtClock } from "@/lib/format";

export type CopyResult = Schemas["TorrentCopyResult"];
export type CopyState = Schemas["TorrentCopyState"];

// The detail prefixes of a failed account read and a failed add (internal/torrents copy.go).
const READ_FAILED = "could not read the account";
const ADD_FAILED = "the add failed";

/** How often the button asks again while the copy runs. */
export const COPY_POLL_MS = 3000;

/** The button's tooltip; pack: the torrent holds more than this file, and the whole pack is added. */
export const addToTooltip = (pack = false) =>
  `Adds this file's torrent${pack ? " (the whole pack)" : ""} to the other providers that have it cached. An add that is not cached is removed again.`;

/** One provider's part of the line. */
export function copyPart(r: CopyResult): string {
  const x = providerLabel(r.code || r.provider);
  switch (r.outcome) {
    case "added":
      return `Added to ${x}`;
    case "present":
      if (r.detail === "failed there") return `Already on ${x}, but failed there`;
      return r.detail ? `Already on ${x}, ${r.detail}` : `Already on ${x}`;
    case "not_cached":
      return `Not cached on ${x}`;
    case "refused":
      return `${x} refuses this release name`;
    case "skipped":
      if (r.until) return `${x} is busy until ${fmtClock(new Date(r.until))}, try again then`;
      return `${x} skipped${r.detail ? `: ${r.detail}` : ""}`;
    case "sending":
      return `Adding to ${x}…`;
    default:
      // The provider's own words stay in the torrent journal: a failed read or add is said plainly.
      if (r.detail?.startsWith(READ_FAILED)) return `Could not read ${x}'s account just now`;
      if (r.detail?.startsWith(ADD_FAILED)) return `Could not add to ${x} just now`;
      return `Could not add to ${x}${r.detail ? `: ${r.detail}` : ""}`;
  }
}

/** This copy's results (at or after since, the server's time of the click; the journal keeps milliseconds), in the
 *  order they ran. */
export function copyResults(st: CopyState | undefined, since: string | null): CopyResult[] {
  if (!since) return [];
  const from = Date.parse(since) - 1;
  // The server lists newest first; within one millisecond, its order reversed is the order they ran.
  return (st?.results ?? []).filter((r) => Date.parse(r.at) >= from).reverse().sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}

/** The line under the button: "Added to TB · Not cached on RD"; "" for none. */
export const copyLine = (results: readonly CopyResult[]) => results.map(copyPart).join(" · ");
