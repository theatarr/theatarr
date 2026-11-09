// Activity › History filters (docs/design/ui.md, Activity): title, copy, event type and a since date, kept in the URL
// (?title=, ?copy=, ?event=, ?since=) so a filtered history can be bookmarked and shared. The server filters the
// grab journal (GET /activity?state=history&title=&version=&kinds=&since=), so paging stays right.

export type HistoryEventId = "all" | "grabs" | "upgrades" | "imports" | "failures";

/** The event types, in the words the history rows use, and the journal kinds behind each. */
export const HISTORY_EVENTS: { id: HistoryEventId; label: string; kinds: string[] }[] = [
  { id: "all", label: "All events", kinds: [] },
  { id: "grabs", label: "Grabs", kinds: ["grab"] },
  { id: "upgrades", label: "Upgrades", kinds: ["upgrade"] },
  { id: "imports", label: "Imports and links", kinds: ["import", "link"] },
  { id: "failures", label: "Failures and blocks", kinds: ["failure", "stall", "blocklist", "research"] },
];

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
    event: HISTORY_EVENTS.some((e) => e.id === event) ? (event as HistoryEventId) : "all",
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
export function historyParams(f: HistoryFilters): { title?: string; version?: string; kinds?: string; since?: string } {
  const kinds = HISTORY_EVENTS.find((e) => e.id === f.event)?.kinds ?? [];
  return {
    title: f.title || undefined,
    version: f.copy || undefined,
    kinds: kinds.length ? kinds.join(",") : undefined,
    since: f.since ? new Date(`${f.since}T00:00:00`).toISOString() : undefined,
  };
}
