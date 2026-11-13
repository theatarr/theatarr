// "Add to other providers" (docs/design/router.md "Copies to other providers"): the copies of a file's torrent to
// the providers that did not hold it, in plain words. The "Other providers" line leaves out only an added copy whose
// provider the same surface shows under Cached on.
import type { Schemas } from "@/api/client";
import { providerLabel } from "@/components/provider-pills";
import { fmtAgo, fmtTime } from "@/lib/format";

export type ProviderCopy = Schemas["ProviderCopy"];

/** How often a page asks again while a copy is unfinished. */
export const COPY_POLL_MS = 5000;

/** A copy's label as Cached on shows its provider: by its cache code (rd → RD), else its key. */
const label = (c: ProviderCopy) => providerLabel(c.code || c.provider);

const UNFINISHED = new Set(["queued", "adding", "checking"]);

const WORDS: Record<string, string> = {
  queued: "adding…",
  adding: "adding…",
  checking: "checking…",
  not_cached: "not cached",
  refused: "refused",
  skipped: "skipped",
  failed: "failed",
};

/** True while any copy is still being worked: the page asks again until it ends. */
export const copiesUnfinished = (copies?: readonly ProviderCopy[] | null) => !!copies?.some((c) => UNFINISHED.has(c.state));

/** The copies the line shows: all but an added copy whose provider this surface shows under Cached on (shown: its
 *  cache codes; none on the Torrents row, which has no Cached on). */
export const otherCopies = (copies?: readonly ProviderCopy[] | null, shown?: readonly string[] | null) => {
  const held = new Set((shown ?? []).map(providerLabel));
  return (copies ?? []).filter((c) => c.state !== "added" || !held.has(label(c)));
};

/** An added copy's words: still downloading (with its progress) when the entry found was, else added. */
function addedWords(c: ProviderCopy): string {
  const m = /still downloading(?: \(\d+%\))?/.exec(c.message ?? "");
  return m ? m[0] : "added";
}

/** "TB adding… · RD not cached, 2 h ago"; "" when there is nothing to say. now is for tests. */
export function otherProvidersLine(copies?: readonly ProviderCopy[] | null, shown?: readonly string[] | null, now?: Date): string {
  const rest = otherCopies(copies, shown);
  if (!rest.length) return "";
  const parts = rest.map((c) => `${label(c)} ${c.state === "added" ? addedWords(c) : WORDS[c.state] ?? c.state}`);
  const newest = rest.reduce((a, c) => (c.updated_at > a ? c.updated_at : a), rest[0].updated_at);
  return `${parts.join(" · ")}, ${fmtAgo(newest, now)}`;
}

/** Each copy's reason, for the line's tooltip. */
export const copyReasons = (copies?: readonly ProviderCopy[] | null, shown?: readonly string[] | null) =>
  otherCopies(copies, shown).map((c) => `${label(c)}: ${c.message || WORDS[c.state] || c.state}${c.until ? ` until ${fmtTime(c.until)}` : ""}`).join("\n");

/** The action's words for its answer, following the current copies: the providers still being added to (one the
 *  current copies show finished is left out); "" once none is; "Every provider already holds it." for an empty
 *  answer. */
export function queuedWords(answer: readonly ProviderCopy[], current?: readonly ProviderCopy[] | null): string {
  if (!answer.length) return "Every provider already holds it.";
  const now = new Map((current ?? []).map((c) => [c.provider, c.state]));
  const adding = answer.filter((c) => UNFINISHED.has(c.state) && UNFINISHED.has(now.get(c.provider) ?? c.state)).map(label);
  return adding.length ? `Adding to ${adding.join(", ")}. The result shows here when it ends.` : "";
}
