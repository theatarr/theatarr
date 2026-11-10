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
