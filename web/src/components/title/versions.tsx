// The link to a library's releases view (docs/design/ui.md, Title page). The library rows are in library-rows.tsx;
// History (the title tab and the episode window) is components/activity/history-tab.tsx.

/** The releases view of a title's library, with the row of one release (by hash) picked out when given. */
export function releasesHref(titleId: number, version: string, hash?: string): string {
  const q = new URLSearchParams({ version });
  if (hash) q.set("hash", hash.toLowerCase());
  return `/title/${titleId}/releases?${q.toString()}`;
}
