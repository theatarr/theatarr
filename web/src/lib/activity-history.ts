// Activity › History filters (docs/design/ui.md, Activity): title, copy, event type and a since date, kept in the URL
// (?title=, ?copy=, ?event=, ?since=) so a filtered history can be bookmarked and shared. The server filters History's
// rows (GET /activity?state=history&title=&version=&events=&since=), so paging stays right.

export type HistoryEventId = "all" | "grabbed" | "imported" | "deleted" | "failed" | "copies";

/** The event types, in the words the history rows use, and the History events behind each. admin: only for accounts
 *  that see files (the copies to other providers are theirs only). */
export const HISTORY_EVENTS: { id: HistoryEventId; label: string; events: string[]; admin?: boolean }[] = [
  { id: "all", label: "All events", events: [] },
  { id: "grabbed", label: "Grabbed", events: ["grabbed"] },
  { id: "imported", label: "Imported and adopted", events: ["imported", "adopted"] },
  { id: "deleted", label: "Deleted and removed", events: ["deleted", "removed", "missing"] },
  { id: "failed", label: "Failed and blocked", events: ["failed", "blocked"] },
  { id: "copies", label: "Other providers", events: ["copy_added", "copy_already", "copy_not_cached"], admin: true },
];

/** Event filters of bookmarks from before Radarr's words, and the filter each now is. */
const OLD_EVENTS: Record<string, HistoryEventId> = { grabs: "grabbed", upgrades: "grabbed", imports: "imported", failures: "failed" };

export type HistoryFilters = { title: string; copy: string; event: HistoryEventId; since: string };

export const NO_HISTORY_FILTERS: HistoryFilters = { title: "", copy: "", event: "all", since: "" };

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** The filters a URL names; anything unknown is left out. */
export function parseHistoryFilters(sp: URLSearchParams): HistoryFilters {
  const event = sp.get("event") ?? "";
  const since = sp.get("since") ?? "";
  return {
    title: (sp.get("title") ?? "").trim(),
    copy: (sp.get("copy") ?? "").trim(),
    event: HISTORY_EVENTS.some((e) => e.id === event) ? (event as HistoryEventId) : (OLD_EVENTS[event] ?? "all"),
    since: DATE.test(since) && !Number.isNaN(new Date(`${since}T00:00:00`).getTime()) ? since : "",
  };
}

/** The URL with one filter changed (an empty or default value removes it); other parameters stay. */
export function withHistoryFilter(sp: URLSearchParams, key: keyof HistoryFilters, value: string): URLSearchParams {
  const next = new URLSearchParams(sp);
  if (!value || (key === "event" && value === "all")) next.delete(key);
  else next.set(key, value);
  return next;
}

/** The URL without any history filter. */
export function clearHistoryFilters(sp: URLSearchParams): URLSearchParams {
  const next = new URLSearchParams(sp);
  for (const k of ["title", "copy", "event", "since"]) next.delete(k);
  return next;
}

/** How many filters are set. */
export function historyFilterCount(f: HistoryFilters): number {
  return [f.title, f.copy, f.event !== "all" ? f.event : "", f.since].filter(Boolean).length;
}

/** The API parameters for the filters: since is the start of that day in the browser's time zone. */
export function historyParams(f: HistoryFilters): { title?: string; version?: string; events?: string; since?: string } {
  const events = HISTORY_EVENTS.find((e) => e.id === f.event)?.events ?? [];
  return {
    title: f.title || undefined,
    version: f.copy || undefined,
    events: events.length ? events.join(",") : undefined,
    since: f.since ? new Date(`${f.since}T00:00:00`).toISOString() : undefined,
  };
}

/** The filters as a view can use them: the admin-only event types (Other providers) are All events without "see files". */
export function historyFiltersFor(f: HistoryFilters, admin: boolean): HistoryFilters {
  return !admin && HISTORY_EVENTS.find((e) => e.id === f.event)?.admin ? { ...f, event: "all" } : f;
}
