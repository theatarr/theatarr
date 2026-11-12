// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { WantedPage, searchNowWords, searchResult, targetWords } from "./wanted";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";

const page: Schemas["WantedPage"] = {
  items: [{ title_id: 1, version: "movie-web", targets: ["movie"], reason: "missing", waiting: "due", position: 1,
    last_run: { id: 3, at: "2026-11-01T00:00:00Z", state: "nothing", results: 3, accepted: 0, message: "", downloaded: false }, decision_id: 9, next_at: null }],
  titles: { "1": "Alpha" }, libraries: { "movie-web": "4K WEB" }, total: 51, counts: { due: 51, loop_off: 2 }, limit: 50, offset: 0,
  enabled: true, downloads_enabled: true, interval: "15m0s", per_hour: 20, again_after: "24h0m0s", missing: true, cutoff_unmet: "quality", searched_last_hour: 4, searchable: 51, search_now_pending: 0, search_now_next_at: null,
};

const admin = { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage", "admin"] } };

it("shows read-only waits and handoffs, filters and pages on the server", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([admin, { method: "GET", path: "/wanted", reply: page }, { method: "GET", path: "/status", reply: { versions: [] } }]);
  renderPage(<WantedPage />, "/wanted");
  expect(await screen.findByText("Alpha")).toBeTruthy();
  expect(screen.queryByText(/newly added titles/)).toBeNull();
  expect(screen.getByText("Next in line (#1)")).toBeTruthy();
  expect(screen.getByText(/3 results, none accepted/)).toBeTruthy();
  expect(await screen.findByRole("link", { name: "Decision" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Decision" }).getAttribute("href")).toBe("/activity/journal/9");
  expect(screen.queryByRole("link", { name: "Releases" })).toBeNull();
  expect(screen.queryByRole("button", { name: /Downloading/ })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Next" }));
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("offset")).toBe("50"));
  await user.click(screen.getByRole("button", { name: "Loop off (2)" }));
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("waiting")).toBe("loop_off"));
  expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("offset")).toBe("0");
  await user.selectOptions(screen.getByLabelText("Library"), "movie-web");
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("version")).toBe("movie-web"));
  expect(fetch.requests.every(r => r.method === "GET")).toBe(true);
});

it("links closed gates to their settings without switches", async () => {
  mockFetch([admin, { method: "GET", path: "/wanted", reply: { ...page, enabled: false, downloads_enabled: false, items: [] } }, { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: { mode: "active", paused: true, pause_forced: false, will_grab: false } }] } }]);
  renderPage(<WantedPage />, "/wanted/cutoff?version=movie-web");
  expect(await screen.findByText("Automatic searches are off.")).toBeTruthy();
  await waitFor(() => expect(screen.getByTestId("downloads-off-notice").textContent).toContain("Paused: nothing is searched, downloaded or linked for any library."));
  expect((await screen.findByRole("link", { name: "Automatic search settings" })).getAttribute("href")).toBe("/admin/settings/downloads/behaviour#searches");
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

const gates = { mode: "active", paused: false, pause_forced: false, will_grab: true };
const row = page.items[0];

it("words rows and the notice from the server's grab state: forced Pause", async () => {
  const locked = { ...gates, paused: true, pause_forced: true, will_grab: false };
  mockFetch([
    admin,
    { method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, counts: { downloads_off: 1 }, items: [{ ...row, waiting: "downloads_off" }] } },
    { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: locked }] } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(await within(r).findByText("Paused")).toBeTruthy();
  expect(screen.getAllByTestId("downloads-off-notice")).toHaveLength(1);
  expect(screen.getByTestId("downloads-off-notice").textContent).toContain("Paused by the environment (THEATARR_FORCE_OFF)");
  expect(screen.getByRole("button", { name: "Paused (locked) (1)" })).toBeTruthy();
});

it("keeps a library's closed download gate visible while the search loop is off", async () => {
  mockFetch([
    admin,
    { method: "GET", path: "/wanted", reply: { ...page, enabled: false, items: [{ ...row, waiting: "loop_off" }] } },
    { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: { ...gates, will_grab: false } }] } },
  ]);
  const user = userEvent.setup();
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByText("Automatic searches are off")).toBeTruthy();
  expect(await within(r).findByText("Downloads off")).toBeTruthy();
  await user.click(within(r).getByRole("button", { name: "Why?" }));
  expect((await within(r).findByRole("link", { name: "Settings › Libraries" })).getAttribute("href")).toBe("/admin/settings/libraries?edit=movie-web");
});

it("names no cause until the lock state is known (status failed)", async () => {
  mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, counts: { downloads_off: 1 }, items: [{ ...row, waiting: "downloads_off" }] } },
    { method: "GET", path: "/status", reply: { status: 500, body: { error: "x" } } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByText("Paused")).toBeTruthy();
  expect(screen.queryByText(/THEATARR_FORCE_OFF/)).toBeNull();
  expect(screen.getByTestId("downloads-off-notice").textContent).toBe("Paused: nothing is searched, downloaded or linked for any library.");
  expect(screen.getByRole("button", { name: "Paused (1)" })).toBeTruthy();
});

const status = { method: "GET", path: "/status", reply: { versions: [] } };
const answer = { queued: 1, started: 1, pending: 0, skipped: [], next_at: null };
const beta = { ...row, title_id: 2, targets: ["s02"], season: 2, version: "series-web", waiting: "searched_recently" as const, position: undefined };

it("searches one row, and the selected rows of the page, through the queue", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, total: 2, searchable: 2, items: [row, beta], titles: { "1": "Alpha", "2": "Beta" }, libraries: { "movie-web": "4K WEB", "series-web": "Series" } } },
    status, { method: "POST", path: "/wanted/search", reply: answer },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const rows = await screen.findAllByTestId("wanted-row");
  expect(screen.getByText(/Search now queues one under the hourly limit/)).toBeTruthy();
  expect((screen.getByRole("button", { name: "Search selected (0)" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(within(rows[1]).getByRole("button", { name: "Search now" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")).toHaveLength(1));
  expect(fetch.calls("POST", "/wanted/search")[0].body).toEqual({ only: "missing", items: [{ title_id: 2, version: "series-web", season: 2 }] });
  expect((await screen.findByTestId("search-now-result")).textContent).toBe("Started 1, queued 0.");
  await waitFor(() => expect(fetch.calls("GET", "/wanted").length).toBeGreaterThan(1));
  // The header checkbox takes every row of the page; a movie row carries no season.
  await user.click(screen.getByRole("checkbox", { name: "Select all on this page" }));
  await user.click(screen.getByRole("button", { name: "Search selected (2)" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")).toHaveLength(2));
  expect(fetch.calls("POST", "/wanted/search")[1].body).toEqual({ only: "missing", items: [{ title_id: 1, version: "movie-web" }, { title_id: 2, version: "series-web", season: 2 }] });
  expect(screen.queryByTestId("search-now-confirm")).toBeNull();
  await waitFor(() => expect(screen.getByRole("button", { name: "Search selected (0)" })).toBeTruthy());
});

it("confirms Search all with the count, the library and the time the hourly limit takes", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    admin,
    { method: "GET", path: "/wanted", reply: { ...page, total: 320, searchable: 312, per_hour: 10, counts: { due: 320 } } }, status,
    { method: "POST", path: "/wanted/search", reply: { queued: 312, started: 6, pending: 306, skipped: [{ title_id: 5, version: "movie-web", reason: "downloads are off for this library" }], next_at: "2026-11-01T14:05:00Z" } },
  ]);
  renderPage(<WantedPage />, "/wanted?version=movie-web&waiting=due");
  await user.click(await screen.findByRole("button", { name: "Search all 312 missing in 4K WEB" }));
  const dialog = await screen.findByTestId("search-now-confirm");
  expect(within(dialog).getByRole("heading", { name: "Search all 312 missing in 4K WEB" })).toBeTruthy();
  const estimate = within(dialog).getByTestId("search-now-estimate");
  expect(estimate.textContent).toContain("312 searches. Up to 10 start each hour (4 used this hour), so this takes about 31 hours.");
  expect(estimate.textContent).toContain("While they wait, each automatic search round starts them before its own searches.");
  expect(within(estimate).getByRole("link", { name: "Settings › Download clients" }).getAttribute("href")).toBe("/admin/settings/downloads/behaviour#searches");
  expect(fetch.calls("POST", "/wanted/search")).toHaveLength(0);
  await user.click(within(dialog).getByRole("button", { name: "Queue 312 searches" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")).toHaveLength(1));
  expect(fetch.calls("POST", "/wanted/search")[0].body).toEqual({ only: "missing", all: { version: "movie-web", waiting: "due" } });
  await waitFor(() => expect(screen.queryByTestId("search-now-confirm")).toBeNull());
  expect(screen.getByTestId("search-now-result").textContent).toMatch(/^Started 6, queued 306; next at \d\d:\d\d( [AP]M)?\. Skipped 1: downloads are off for this library\.$/);
});

it("confirms a selection larger than the budget left; the cutoff tab asks for cutoff rows", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, per_hour: 4, searched_last_hour: 4, total: 1, searchable: 1 } }, status,
    { method: "POST", path: "/wanted/search", reply: { ...answer, started: 0, pending: 1 } },
  ]);
  renderPage(<WantedPage />, "/wanted/cutoff");
  expect(await screen.findByRole("button", { name: "Search all 1 cutoff unmet" })).toBeTruthy();
  await user.click(screen.getByRole("checkbox", { name: "Select Alpha, Movie, 4K WEB" }));
  await user.click(screen.getByRole("button", { name: "Search selected (1)" }));
  const dialog = await screen.findByTestId("search-now-confirm");
  expect(within(dialog).getByTestId("search-now-estimate").textContent).toContain("1 search. Up to 4 start each hour (4 used this hour), so this takes about 1 hour.");
  await user.click(within(dialog).getByRole("button", { name: "Queue 1 search" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")[0]?.body).toEqual({ only: "cutoff", items: [{ title_id: 1, version: "movie-web" }] }));
});

it("disables Search now with the reason where a search could not start", async () => {
  // The loop's passes are off, so every row waits as loop_off; the search blocker is reported apart from it.
  const items = (["downloads_off", "in_flight", undefined] as const).map((search_blocked, i) => ({ ...row, title_id: i + 1, waiting: "loop_off" as const, search_blocked }));
  mockFetch([{ method: "GET", path: "/wanted", reply: { ...page, enabled: false, total: 3, searchable: 1, items, titles: {} } }, status]);
  renderPage(<WantedPage />, "/wanted");
  const rows = await screen.findAllByTestId("wanted-row");
  const button = (i: number) => within(rows[i]).getByRole("button", { name: "Search now" }) as HTMLButtonElement;
  expect([0, 1, 2].map(i => button(i).disabled)).toEqual([true, true, false]);
  expect([0, 1].map(i => button(i).title)).toEqual(["Paused", "A download for it is in progress"]);
  expect((within(rows[0]).getByRole("checkbox") as HTMLInputElement).disabled).toBe(true);
  // The header checkbox takes only the rows that can be searched.
  await userEvent.setup().click(screen.getByRole("checkbox", { name: "Select all on this page" }));
  expect(screen.getByRole("button", { name: "Search selected (1)" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Search all 1 missing" })).toBeTruthy();
});

it("shows the queue, a queued row's place, and cancels the queue", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, search_now_pending: 306, search_now_next_at: "2026-11-01T14:05:00Z", items: [{ ...row, search_queued: 12 }] } },
    status, { method: "DELETE", path: "/wanted/search", reply: { cleared: 306 } },
    { method: "GET", path: "/wanted", reply: page },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const line = await screen.findByTestId("search-now-queue");
  expect(line.textContent).toMatch(/^306 searches queued by Search now · next at \d\d:\d\d/);
  const r = screen.getByTestId("wanted-row");
  expect(within(r).getByText("Queued for search (#12)")).toBeTruthy();
  expect((within(r).getByRole("button", { name: "Search now" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(within(line).getByRole("button", { name: "Cancel queued searches" }));
  await waitFor(() => expect(fetch.calls("DELETE", "/wanted/search")).toHaveLength(1));
  await waitFor(() => expect(screen.queryByTestId("search-now-queue")).toBeNull());
});

it("says the queued searches wait while paused", async () => {
  mockFetch([{ method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, search_now_pending: 3, search_now_next_at: null } }, status]);
  renderPage(<WantedPage />, "/wanted");
  const line = await screen.findByTestId("search-now-queue");
  expect(line.textContent).toMatch(/^3 searches queued by Search now · waiting until Pause is off/);
});

it("says why a search was refused", async () => {
  mockFetch([{ method: "GET", path: "/wanted", reply: page }, status, { method: "POST", path: "/wanted/search", reply: { status: 409, body: { error: "New downloads are off, so nothing can be searched." } } }]);
  renderPage(<WantedPage />, "/wanted");
  await userEvent.setup().click(await screen.findByRole("button", { name: "Search now" }));
  expect((await screen.findByRole("alert")).textContent).toBe("New downloads are off, so nothing can be searched.");
  expect(searchNowWords({ queued: 0, started: 0, pending: 0, skipped: [], next_at: null })).toBe("Started 0, queued 0.");
});

it("opens Interactive search for a series row's season from the row", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage"] } },
    { method: "GET", path: "/wanted", reply: { ...page, items: [{ ...row, version: "tv-web", targets: ["s02e03"], season: 2 }] } },
    { method: "GET", path: "/status", reply: { versions: [] } },
    { method: "GET", path: "/titles/1", reply: { id: 1, name: "Alpha", kind: "series", versions: [{ id: 1, key: "tv-web", label: "TV WEB", control: { mode: "active", can_edit: true, can_search: true, live_releases: true } }] } },
    { method: "GET", path: "/titles/1/removal", reply: { title_id: 1, name: "Alpha", title: true, leftover: [], copies: [{ key: "tv-web", label: "TV WEB", links: 0, downloads: 0 }] } },
    { method: "GET", path: "/titles/1/releases", reply: { title_id: 1, version: "tv-web", versions: ["tv-web"], mode: "active", live_allowed: true, source: "live", results: 0, accepted: 0, candidates: [], grab: true } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).queryByRole("link", { name: "Releases" })).toBeNull();
  await user.click(await within(r).findByRole("button", { name: "Interactive search" }));
  expect(await screen.findByText("No releases found.")).toBeTruthy();
  expect(fetch.calls("GET", "/titles/1/releases").map((c) => c.search)).toEqual(["?version=tv-web&live=1&season=2"]);
});

it("opens for a manager without the admin links, and says Search all missing when nothing can be searched", async () => {
  // Automatic searches off and Pause on; /status is admin-only and not asked.
  const items = [{ ...row, waiting: "loop_off" as const, search_blocked: "downloads_off" as const }];
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage"] } },
    { method: "GET", path: "/wanted", reply: { ...page, enabled: false, downloads_enabled: false, searchable: 0, total: 1, items } },
    { method: "GET", path: "/status", reply: { status: 403, body: { error: "admin required" } } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByRole("button", { name: "Interactive search" })).toBeTruthy();
  expect(screen.getByTestId("downloads-off-notice")).toBeTruthy();
  expect((within(r).getByRole("button", { name: "Search now" }) as HTMLButtonElement).disabled).toBe(true);
  expect(within(r).getByTestId("search-blocked").textContent).toBe("Paused");
  expect(fetch.calls("GET", "/status")).toHaveLength(0);
  expect(within(r).queryByRole("link", { name: "Decision" })).toBeNull();
  expect(screen.queryByRole("tab", { name: "Manual import" })).toBeNull();
  expect(screen.getByRole("tab", { name: "Missing" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Automatic search settings" })).toBeNull();
  expect(document.querySelector('a[href^="/admin"]')).toBeNull();
  const all = screen.getByRole("button", { name: "Search all missing" }) as HTMLButtonElement;
  expect(all.disabled).toBe(true);
});
