// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link } from "react-router";
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

it("names libraries without their keys, telling equal names apart by number", async () => {
  mockFetch([{ method: "GET", path: "/titles/facets", reply: { versions: [{ key: "movie-4k", label: "4K" }, { key: "tv-4k", label: "4K" }, { key: "movie-web", label: "WEB" }] } }]);
  renderPage(<HistoryFilters />);
  const select = (await screen.findByTestId("history-copy")) as HTMLSelectElement;
  await screen.findByRole("option", { name: "4K (2)" });
  expect([...select.options].map((o) => [o.textContent, o.value])).toEqual([["All libraries", ""], ["4K", "movie-4k"], ["4K (2)", "tv-4k"], ["WEB", "movie-web"]]);
  expect(select.textContent).not.toContain("movie-4k");
});

it("empties the title box when a link drops ?title=, and keeps what is typed", async () => {
  mockFetch([{ method: "GET", path: "/titles/facets", reply: { versions: [] } }]);
  renderPage(<><Link to="/activity/history">History</Link><HistoryFilters /></>, "/activity/history?title=Anora");
  const box = (await screen.findByTestId("history-title")) as HTMLInputElement;
  expect(box.value).toBe("Anora");
  const user = userEvent.setup();
  await user.click(screen.getByRole("link", { name: "History" }));
  await waitFor(() => expect(box.value).toBe(""));
  await user.type(box, "Foundation");
  await new Promise((r) => setTimeout(r, 450));
  expect(box.value).toBe("Foundation");
});
