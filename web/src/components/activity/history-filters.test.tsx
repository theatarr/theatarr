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
  renderPage(<HistoryFilters admin />);
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

it("Today sets Since to today and clears it again; adoption is its own event", async () => {
  mockFetch([{ method: "GET", path: "/titles/facets", reply: { versions: [] } }]);
  renderPage(<HistoryFilters />, "/activity/history");
  const todayButton = await screen.findByTestId("history-today");
  const since = screen.getByTestId("history-since") as HTMLInputElement;
  expect(todayButton.getAttribute("aria-pressed")).toBe("false");
  const user = userEvent.setup();
  await user.click(todayButton);
  await waitFor(() => expect(since.value).toBe(since.max));
  expect(todayButton.getAttribute("aria-pressed")).toBe("true");
  await user.click(todayButton);
  await waitFor(() => expect(since.value).toBe(""));
  const events = [...(screen.getByTestId("history-event") as HTMLSelectElement).options].map((o) => o.textContent);
  expect(events).toContain("Imported");
  expect(events).toContain("Adopted (files found in the library)");
});

it("has no library selector in the household view, asks for no libraries and counts no library filter", async () => {
  const api = mockFetch([{ method: "GET", path: "/titles/facets", reply: { versions: [{ key: "movie-web", label: "WEB" }] } }]);
  renderPage(<HistoryFilters />, "/activity/history?copy=movie-web");
  await screen.findByTestId("history-event");
  expect(screen.queryByTestId("history-copy")).toBeNull();
  expect(screen.queryByTestId("history-clear")).toBeNull();
  expect(api.calls("GET", "/titles/facets")).toHaveLength(0);
});
