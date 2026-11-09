// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { WantedPage, searchResult, targetWords } from "./wanted";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";

const page: Schemas["WantedPage"] = {
  items: [{ title_id: 1, version: "movie-web", targets: ["movie"], reason: "missing", waiting: "due", position: 1,
    last_run: { id: 3, at: "2026-11-01T00:00:00Z", state: "nothing", results: 3, accepted: 0, message: "", downloaded: false }, decision_id: 9, next_at: null }],
  titles: { "1": "Alpha" }, libraries: { "movie-web": "4K WEB" }, total: 51, counts: { due: 51, grab_off: 2 }, limit: 50, offset: 0,
  enabled: true, downloads_enabled: true, interval: "15m0s", per_hour: 20, again_after: "24h0m0s", missing: true, cutoff_unmet: "quality", searched_last_hour: 4, add_time_pending: 2,
};

it("shows read-only waits and handoffs, filters and pages on the server", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([{ method: "GET", path: "/wanted", reply: page }, { method: "GET", path: "/status", reply: { versions: [] } }]);
  renderPage(<WantedPage />, "/wanted");
  expect(await screen.findByText("Alpha")).toBeTruthy();
  expect(screen.getByText(/2 searches for newly added titles go first/)).toBeTruthy();
  expect(screen.getByText("Next in line (#1)")).toBeTruthy();
  expect(screen.getByText(/3 results, none accepted/)).toBeTruthy();
  expect(screen.getByRole("link", { name: "Releases" }).getAttribute("href")).toBe("/title/1/releases?version=movie-web");
  expect(screen.getByRole("link", { name: "Decision" }).getAttribute("href")).toBe("/admin/advanced/journal/9");
  expect(screen.queryByRole("button", { name: /Downloading/ })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Next" }));
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("offset")).toBe("50"));
  await user.click(screen.getByRole("button", { name: "Downloads off (2)" }));
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("waiting")).toBe("grab_off"));
  expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("offset")).toBe("0");
  await user.selectOptions(screen.getByLabelText("Library"), "movie-web");
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("version")).toBe("movie-web"));
  expect(fetch.requests.every(r => r.method === "GET")).toBe(true);
});

it("links closed gates to their settings without switches", async () => {
  mockFetch([{ method: "GET", path: "/wanted", reply: { ...page, enabled: false, downloads_enabled: false, items: [] } }, { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: { mode: "active", grab: true, router_enabled: false, router_forced_off: false, will_grab: false } }] } }]);
  renderPage(<WantedPage />, "/wanted/cutoff?version=movie-web");
  expect(await screen.findByText("Automatic searches are off.")).toBeTruthy();
  await waitFor(() => expect(screen.getByTestId("downloads-off-notice").textContent).toContain("New downloads are paused for every library"));
  expect(screen.getByRole("link", { name: "Automatic search settings" }).getAttribute("href")).toBe("/admin/settings/downloads/behaviour#searches");
  expect(screen.queryByRole("switch")).toBeNull();
  expect(screen.getByRole("tab", { name: "Missing" }).getAttribute("href")).toBe("/wanted?version=movie-web");
});

it("formats search outcomes and targets", () => {
  const run = page.items[0].last_run!;
  expect(searchResult(null)).toBe("Never searched");
  expect(searchResult({ ...run, results: 0 })).toBe("Nothing found");
  expect(searchResult({ ...run, state: "failed", message: "VPN unavailable" })).toBe("Failed: VPN unavailable");
  expect(searchResult({ ...run, state: "routed", accepted: 1 })).toBe("Sent to download");
  expect(searchResult({ ...run, state: "routed", downloaded: true })).toBe("Grabbed");
  expect(targetWords(["s01", "s02e03"])).toBe("S01, S02E03");
  expect(targetWords(["s01e04", "s01e02", "s01e03", "s01e06"])).toBe("S01E02-E04, S01E06");
});

const gates = { mode: "active", grab: true, router_enabled: true, router_forced_off: false, will_grab: true };
const row = page.items[0];

it("words rows and the notice from the server's grab state: locked off, not paused", async () => {
  const locked = { ...gates, router_forced_off: true, will_grab: false };
  mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, counts: { downloads_off: 1 }, items: [{ ...row, waiting: "downloads_off" }] } },
    { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: locked }] } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(await within(r).findByText("Locked off")).toBeTruthy();
  expect(within(r).queryByText("Paused globally")).toBeNull();
  expect(screen.getAllByTestId("downloads-off-notice")).toHaveLength(1);
  expect(screen.getByTestId("downloads-off-notice").textContent).toContain("locked off by the environment");
  expect(screen.getByRole("button", { name: "Locked off (1)" })).toBeTruthy();
});

it("keeps a library's closed download gate visible while the search loop is off", async () => {
  mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, enabled: false, items: [{ ...row, waiting: "loop_off" }] } },
    { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: { ...gates, grab: false, will_grab: false } }] } },
  ]);
  const user = userEvent.setup();
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByText("Automatic searches are off")).toBeTruthy();
  expect(await within(r).findByText("Downloads off")).toBeTruthy();
  await user.click(within(r).getByRole("button", { name: "Why?" }));
  expect(within(r).getByRole("link", { name: "Settings › Libraries" }).getAttribute("href")).toBe("/admin/settings/libraries?edit=movie-web");
});

it("names no cause until the lock state is known (status failed)", async () => {
  mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, counts: { downloads_off: 1 }, items: [{ ...row, waiting: "downloads_off" }] } },
    { method: "GET", path: "/status", reply: { status: 500, body: { error: "x" } } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByText("New downloads off")).toBeTruthy();
  expect(screen.queryByText(/Paused globally/)).toBeNull();
  expect(screen.getByTestId("downloads-off-notice").textContent).toContain("New downloads are off for every library.");
  expect(screen.getByRole("button", { name: "New downloads off (1)" })).toBeTruthy();
});
