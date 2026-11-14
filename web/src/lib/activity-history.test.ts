import { describe, expect, it } from "vitest";
import {
  clearHistoryFilters,
  historyFilterCount,
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
    // The words before Radarr's (grabs, upgrades, imports, failures) are no longer events: all.
    expect(parseHistoryFilters(new URLSearchParams("event=imports")).event).toBe("all");
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
    // Since is local midnight of that day.
    expect(new Date(p.since!).getTime()).toBe(new Date(2026, 9, 1).getTime());
    expect(historyFilterCount({ title: "A", copy: "", event: "all", since: "2026-10-01" })).toBe(2);
  });
});
