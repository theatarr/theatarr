// "Add to other providers" (docs/design/torrents.md): the result line of a copy, one part per provider in the provider
// tags the page already uses ("Added to TB · Not cached on RD").
import type { Schemas } from "@/api/client";
import { providerLabel } from "@/components/provider-pills";
import { fmtClock } from "@/lib/format";

export type CopyResult = Schemas["TorrentCopyResult"];
export type CopyState = Schemas["TorrentCopyState"];

/** How often the button asks again while the copy runs. */
export const COPY_POLL_MS = 3000;

/** The button's tooltip. pack: the torrent holds more than this file (the whole pack is added); "maybe" where the page
 *  cannot tell (an episode's file). */
export const addToTooltip = (pack: boolean | "maybe" = false) =>
  `Adds this file's torrent${pack === "maybe" ? " (the whole pack, when it came in one)" : pack ? " (the whole pack)" : ""} to the other providers that have it cached. An add that is not cached is removed again.`;

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
      return `Could not add to ${x}${r.detail ? `: ${r.detail}` : ""}`;
  }
}

/** This copy's results (at or after since, the server's time of the click; the journal keeps milliseconds), in the
 *  order they ran. */
export function copyResults(st: CopyState | undefined, since: string | null): CopyResult[] {
  if (!since) return [];
  const from = Date.parse(since) - 1;
  return (st?.results ?? []).filter((r) => Date.parse(r.at) >= from).sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}

/** The line under the button: "Added to TB · Not cached on RD"; "" for none. */
export const copyLine = (results: readonly CopyResult[]) => results.map(copyPart).join(" · ");
