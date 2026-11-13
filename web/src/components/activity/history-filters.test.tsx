// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { HistoryFilters } from "./history-filters";

const zone = process.env.TZ;
afterEach(() => {
  vi.useRealTimers();
  if (zone === undefined) delete process.env.TZ;
  else process.env.TZ = zone;
});

it("lets Since reach today in the browser's time zone, not the UTC day", async () => {
  // 09:00 on Nov 10 in Auckland is still Nov 9 in UTC; "since" is sent as local midnight (historyParams).
  process.env.TZ = "Pacific/Auckland";
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-11-09T20:00:00Z"));
  mockFetch([{ method: "GET", path: "/titles/facets", reply: { versions: [] } }]);
  renderPage(<HistoryFilters />);
  const since = (await screen.findByTestId("history-since")) as HTMLInputElement;
  expect(since.max).toBe("2026-11-10");
});

it("names libraries without their keys for a household view, telling equal names apart by number", async () => {
  mockFetch([{ method: "GET", path: "/titles/facets", reply: { versions: [{ key: "movie-4k", label: "4K" }, { key: "tv-4k", label: "4K" }, { key: "movie-web", label: "WEB" }] } }]);
  renderPage(<HistoryFilters plain />);
  const select = (await screen.findByTestId("history-copy")) as HTMLSelectElement;
  await screen.findByRole("option", { name: "4K (2)" });
  expect([...select.options].map((o) => [o.textContent, o.value])).toEqual([["All libraries", ""], ["4K", "movie-4k"], ["4K (2)", "tv-4k"], ["WEB", "movie-web"]]);
  expect(select.textContent).not.toContain("movie-4k");
});
