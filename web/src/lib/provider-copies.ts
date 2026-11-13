// "Add to other providers" (docs/design/router.md "Copies to other providers"): the copies of a file's torrent to
// the providers that did not hold it, in plain words. "Cached on" already shows a copy that was added; the "Other
// providers" line lists only the rest: in progress, not cached, refused, skipped, failed.
import type { Schemas } from "@/api/client";
import { providerLabel } from "@/components/provider-pills";
import { fmtAgo, fmtTime } from "@/lib/format";

export type ProviderCopy = Schemas["ProviderCopy"];

/** How often a page asks again while a copy is unfinished. */
export const COPY_POLL_MS = 5000;

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

/** The copies the line shows: everything "Cached on" does not (an added copy is in Cached on). */
export const otherCopies = (copies?: readonly ProviderCopy[] | null) => (copies ?? []).filter((c) => c.state !== "added");

/** "TB adding… · RD not cached, 2 h ago"; "" when there is nothing to say. now is for tests. */
export function otherProvidersLine(copies?: readonly ProviderCopy[] | null, now?: Date): string {
  const rest = otherCopies(copies);
  if (!rest.length) return "";
  const parts = rest.map((c) => `${providerLabel(c.provider)} ${WORDS[c.state] ?? c.state}`);
  const newest = rest.reduce((a, c) => (c.updated_at > a ? c.updated_at : a), rest[0].updated_at);
  return `${parts.join(" · ")}, ${fmtAgo(newest, now)}`;
}

/** Each copy's reason, for the line's tooltip. */
export const copyReasons = (copies?: readonly ProviderCopy[] | null) =>
  otherCopies(copies).map((c) => `${providerLabel(c.provider)}: ${c.message || WORDS[c.state] || c.state}${c.until ? ` until ${fmtTime(c.until)}` : ""}`).join("\n");

/** What the action answered, in one line: the providers it is adding to, or that none was left. */
export function queuedWords(copies: readonly ProviderCopy[]): string {
  if (!copies.length) return "Every provider already holds it.";
  const adding = copies.filter((c) => UNFINISHED.has(c.state)).map((c) => providerLabel(c.provider));
  return adding.length ? `Adding to ${adding.join(", ")}. The result shows here when it ends.` : "Nothing left to add.";
}
