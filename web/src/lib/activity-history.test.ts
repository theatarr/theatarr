import { describe, expect, it } from "vitest";
import {
  clearHistoryFilters,
  historyFilterCount,
  historyFiltersFor,
  historyParams,
  NO_HISTORY_FILTERS,
  parseHistoryFilters,
  withHistoryFilter,
} from "./activity-history";

describe("activity history filters", () => {
  it("reads the filters from the URL and drops unknown values", () => {
    expect(parseHistoryFilters(new URLSearchParams(""))).toEqual(NO_HISTORY_FILTERS);
    expect(parseHistoryFilters(new URLSearchParams("title=%20Alpha%20&copy=movie-web&event=failed&since=2026-10-01"))).toEqual({
      title: "Alpha",
      copy: "movie-web",
      event: "failed",
      since: "2026-10-01",
    });
    expect(parseHistoryFilters(new URLSearchParams("event=shadow&since=yesterday"))).toEqual(NO_HISTORY_FILTERS);
    // Bookmarks from before Radarr's words open the matching filter.
    for (const [old, now] of [["grabs", "grabbed"], ["upgrades", "grabbed"], ["imports", "imported"], ["failures", "failed"]])
      expect(parseHistoryFilters(new URLSearchParams(`event=${old}`)).event).toBe(now);
  });
  it("changes one filter and keeps the rest of the URL", () => {
    const sp = new URLSearchParams("as=user&title=Alpha");
    expect(withHistoryFilter(sp, "event", "grabs").toString()).toBe("as=user&title=Alpha&event=grabs");
    expect(withHistoryFilter(sp, "title", "").toString()).toBe("as=user");
    expect(withHistoryFilter(new URLSearchParams("event=grabs"), "event", "all").toString()).toBe("");
    expect(clearHistoryFilters(new URLSearchParams("as=user&title=A&copy=b&event=grabs&since=2026-10-01")).toString()).toBe("as=user");
    // The URL handed in is not changed.
    expect(sp.toString()).toBe("as=user&title=Alpha");
  });
  it("turns the filters into API parameters", () => {
    expect(historyParams(NO_HISTORY_FILTERS)).toEqual({ title: undefined, version: undefined, events: undefined, since: undefined });
    const p = historyParams({ title: "Alpha", copy: "movie-web", event: "imported", since: "2026-10-01" });
    expect(p).toMatchObject({ title: "Alpha", version: "movie-web", events: "imported,adopted" });
    expect(historyParams({ ...NO_HISTORY_FILTERS, event: "deleted" }).events).toBe("deleted,removed,missing");
    // Other providers is for accounts that see files: the user view asks for every event.
    expect(historyFiltersFor({ ...NO_HISTORY_FILTERS, event: "copies" }, false).event).toBe("all");
    expect(historyFiltersFor({ ...NO_HISTORY_FILTERS, event: "copies" }, true).event).toBe("copies");
    // Since is local midnight of that day.
    expect(new Date(p.since!).getTime()).toBe(new Date(2026, 9, 1).getTime());
    expect(historyFilterCount({ title: "A", copy: "", event: "all", since: "2026-10-01" })).toBe(2);
  });
});
