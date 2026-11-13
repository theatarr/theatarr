// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { WantedPage, searchNowWords, searchResult, targetWords } from "./wanted";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";

type Row = Schemas["WantedRow"];

const page: Schemas["WantedPage"] = {
  items: [{ title_id: 1, version: "movie-web", targets: ["movie"], reason: "missing", waiting: "due", next_at: "2026-11-01T00:00:00Z",
    last_run: { id: 3, at: "2026-11-01T00:00:00Z", state: "nothing", results: 3, accepted: 0, message: "", downloaded: false }, decision_id: 9 }],
  titles: { "1": "Alpha" }, libraries: { "movie-web": "4K WEB" }, total: 51, counts: { due: 51, not_scheduled: 2 }, limit: 50, offset: 0,
  downloads_enabled: true, per_hour: 20, again_after: "24h0m0s", missing: true, cutoff_unmet: "quality", searched_last_hour: 4, searchable: 51, search_now_pending: 0,
};

const admin = { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage", "admin"] } };
const status = { method: "GET", path: "/status", reply: { versions: [] } };

it("shows read-only waits and handoffs, filters and pages on the server", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([admin, { method: "GET", path: "/wanted", reply: page }, status]);
  renderPage(<WantedPage />, "/wanted");
  expect(await screen.findByText("Alpha")).toBeTruthy();
  expect(screen.getByText("What is waiting for a search and when each item is searched next. Search now starts a search under the hourly limit.")).toBeTruthy();
  expect(screen.getByTestId("wanted-pace").textContent).toBe("Up to 20 searches an hour (4 in the last hour). New releases are tried again after 1, 2, 4, 8 and 16 hours, then daily for a week; older items every 24 h.");
  expect(screen.queryByTestId("wanted-switch-off")).toBeNull();
  expect(screen.getAllByRole("columnheader").map(h => h.textContent)).toEqual(["", "Title", "Library", "What", "Next search", "Last search", "Actions"]);
  expect(within(screen.getByTestId("wanted-row")).getByText("Now")).toBeTruthy();
  expect(screen.getByText(/3 results, none accepted/)).toBeTruthy();
  expect(await screen.findByRole("link", { name: "Decision" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Decision" }).getAttribute("href")).toBe("/activity/journal/9");
  expect(screen.queryByRole("button", { name: /Downloading/ })).toBeNull();
  expect(screen.getByTestId("pager-range").textContent).toBe("1–50 of 51");
  await user.click(screen.getByRole("button", { name: "Next" }));
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("offset")).toBe("50"));
  await user.click(screen.getByRole("button", { name: "Not scheduled (2)" }));
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("waiting")).toBe("not_scheduled"));
  expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("offset")).toBe("0");
  await user.selectOptions(screen.getByLabelText("Library"), "movie-web");
  await waitFor(() => expect(new URLSearchParams(fetch.calls("GET", "/wanted").at(-1)?.search).get("version")).toBe("movie-web"));
  expect(fetch.requests.every(r => r.method === "GET")).toBe(true);
});

const row = page.items[0];
const inHours = (h: number) => new Date(Date.now() + h * 3600_000).toISOString();

it("words each Next search state; no time is promised for a due row or while paused", async () => {
  const items: Row[] = [
    { ...row, title_id: 1 },
    { ...row, title_id: 2, why: "asked" },
    { ...row, title_id: 3, waiting: "scheduled", next_at: inHours(3) },
    { ...row, title_id: 4, waiting: "scheduled", next_at: inHours(2), why: "new", tries: 2 },
    { ...row, title_id: 5, waiting: "scheduled", next_at: inHours(5), why: "stalled" },
    { ...row, title_id: 6, waiting: "in_flight", next_at: null, search_blocked: "in_flight" },
    { ...row, title_id: 7, waiting: "not_scheduled", next_at: null },
  ];
  mockFetch([admin, { method: "GET", path: "/wanted", reply: { ...page, items, total: items.length, titles: {} } }, status]);
  renderPage(<WantedPage />, "/wanted");
  const rows = await screen.findAllByTestId("wanted-row");
  const next = rows.map(r => r.querySelector('[data-column="next"]')?.textContent);
  expect(next).toEqual(["Now", "Now · asked by Search now", "in 3 h", "in 2 h · New release, try 3", "in 5 h · After a failed download", "Downloading", "Not scheduled"]);
  // The exact time is the tooltip of a scheduled row.
  expect(within(rows[2]).getByText("in 3 h").getAttribute("title")).toMatch(/^\w{3} \d+ \d\d:\d\d$/);
  // A row asked for already cannot be asked again.
  expect((within(rows[1]).getByRole("button", { name: "Search now" }) as HTMLButtonElement).title).toBe("Already asked for");
});

it("says the tab's switch is off, every row reads Not scheduled, and Search now still works per row", async () => {
  // The owner's install after the upgrade: both switches off, not paused.
  const user = userEvent.setup();
  const items = [{ ...row, waiting: "not_scheduled" as const, next_at: null }];
  const fetch = mockFetch([admin, { method: "GET", path: "/wanted", reply: { ...page, missing: false, cutoff_unmet: "off", items, total: 1, counts: { not_scheduled: 1 } } }, status,
    { method: "POST", path: "/wanted/search", reply: { queued: 1, started: 1, pending: 0, skipped: [] } }]);
  renderPage(<WantedPage />, "/wanted");
  const banner = await screen.findByTestId("wanted-switch-off");
  expect(banner.textContent).toBe("Theatarr is not searching for missing items by itself. Items here are searched when you ask, or after a failed download. Automatic search settings");
  expect(within(banner).getByRole("link", { name: "Automatic search settings" }).getAttribute("href")).toBe("/admin/settings/downloads/behaviour#searches");
  const r = screen.getByTestId("wanted-row");
  expect(r.querySelector('[data-column="next"]')?.textContent).toBe("Not scheduled");
  await user.click(within(r).getByRole("button", { name: "Search now" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")).toHaveLength(1));
  expect(fetch.calls("POST", "/wanted/search")[0].body).toEqual({ only: "missing", items: [{ title_id: 1, version: "movie-web" }] });
});

it("names upgrades on the cutoff tab's banner and links its switch", async () => {
  mockFetch([admin, { method: "GET", path: "/wanted", reply: { ...page, cutoff_unmet: "off", items: [] } }, status]);
  renderPage(<WantedPage />, "/wanted/cutoff?version=movie-web");
  const banner = await screen.findByTestId("wanted-switch-off");
  expect(banner.textContent).toContain("Theatarr is not searching for upgrades by itself.");
  expect(within(banner).getByRole("link", { name: "Automatic search settings" }).getAttribute("href")).toBe("/admin/settings/downloads/behaviour#searches");
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

it("words rows and the notice from the server's grab state: forced Pause", async () => {
  const locked = { ...gates, paused: true, pause_forced: true, will_grab: false };
  mockFetch([
    admin,
    { method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, counts: { downloads_off: 1 }, items: [{ ...row, waiting: "downloads_off", next_at: null, search_blocked: "downloads_off" }] } },
    { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: locked }] } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(await within(r).findByText("Paused")).toBeTruthy();
  expect(screen.getAllByTestId("downloads-off-notice")).toHaveLength(1);
  expect(screen.getByTestId("downloads-off-notice").textContent).toContain("Paused by the environment (THEATARR_FORCE_OFF)");
  expect(screen.getByRole("button", { name: "Paused (locked) (1)" })).toBeTruthy();
  // Nothing can be searched while paused, so nothing is offered for selection.
  expect(within(r).queryByRole("checkbox")).toBeNull();
});

it("keeps a library's closed download gate visible on a row that is not scheduled", async () => {
  mockFetch([
    admin,
    { method: "GET", path: "/wanted", reply: { ...page, missing: false, items: [{ ...row, waiting: "not_scheduled", next_at: null }] } },
    { method: "GET", path: "/status", reply: { versions: [{ key: "movie-web", grab_state: { ...gates, will_grab: false } }] } },
  ]);
  const user = userEvent.setup();
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByText(/Not scheduled/)).toBeTruthy();
  expect(await within(r).findByText("Downloads off")).toBeTruthy();
  await user.click(within(r).getByRole("button", { name: "Why?" }));
  expect((await within(r).findByRole("link", { name: "Settings › Libraries" })).getAttribute("href")).toBe("/admin/settings/libraries?edit=movie-web");
});

it("names no cause until the lock state is known (status failed)", async () => {
  mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, counts: { downloads_off: 1 }, items: [{ ...row, waiting: "downloads_off", next_at: null }] } },
    { method: "GET", path: "/status", reply: { status: 500, body: { error: "x" } } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByText("Paused")).toBeTruthy();
  expect(screen.queryByText(/THEATARR_FORCE_OFF/)).toBeNull();
  expect(screen.getByTestId("downloads-off-notice").textContent).toBe("Paused: nothing is searched, downloaded or linked for any library.");
  expect(screen.getByRole("button", { name: "Paused (1)" })).toBeTruthy();
});

const answer = { queued: 1, started: 1, pending: 0, skipped: [] };
const beta: Row = { ...row, title_id: 2, targets: ["s02"], season: 2, version: "series-web", waiting: "scheduled", next_at: inHours(4) };

it("searches one row, and the selected rows of the page", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, total: 2, searchable: 2, items: [row, beta], titles: { "1": "Alpha", "2": "Beta" }, libraries: { "movie-web": "4K WEB", "series-web": "Series" } } },
    status, { method: "POST", path: "/wanted/search", reply: answer },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const rows = await screen.findAllByTestId("wanted-row");
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
    { method: "POST", path: "/wanted/search", reply: { queued: 312, started: 6, pending: 306, skipped: [{ title_id: 5, version: "movie-web", reason: "a download for it is in progress" }] } },
  ]);
  renderPage(<WantedPage />, "/wanted?version=movie-web&waiting=due");
  await user.click(await screen.findByRole("button", { name: "Search all 312 missing in 4K WEB" }));
  const dialog = await screen.findByTestId("search-now-confirm");
  expect(within(dialog).getByRole("heading", { name: "Search all 312 missing in 4K WEB" })).toBeTruthy();
  const estimate = within(dialog).getByTestId("search-now-estimate");
  expect(estimate.textContent).toContain("312 searches. They start as the hourly limit allows: up to 10 an hour (4 used this hour).");
  expect(estimate.textContent).not.toMatch(/round|takes about|start now|oldest/);
  expect(within(estimate).getByRole("link", { name: "Settings › Download clients" }).getAttribute("href")).toBe("/admin/settings/downloads/behaviour#searches");
  expect(fetch.calls("POST", "/wanted/search")).toHaveLength(0);
  await user.click(within(dialog).getByRole("button", { name: "Queue 312 searches" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")).toHaveLength(1));
  expect(fetch.calls("POST", "/wanted/search")[0].body).toEqual({ only: "missing", all: { version: "movie-web", waiting: "due" } });
  await waitFor(() => expect(screen.queryByTestId("search-now-confirm")).toBeNull());
  expect(screen.getByTestId("search-now-result").textContent).toBe("Started 6, queued 306. Skipped 1: a download for it is in progress.");
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
  expect(within(dialog).getByTestId("search-now-estimate").textContent).toContain("1 search. They start as the hourly limit allows: up to 4 an hour (4 used this hour).");
  await user.click(within(dialog).getByRole("button", { name: "Queue 1 search" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")[0]?.body).toEqual({ only: "cutoff", items: [{ title_id: 1, version: "movie-web" }] }));
});

it("disables a row's Search now with the reason where a search could not start", async () => {
  const items = (["in_flight", undefined] as const).map((search_blocked, i) => ({ ...row, title_id: i + 1, waiting: "not_scheduled" as const, next_at: null, search_blocked }));
  mockFetch([{ method: "GET", path: "/wanted", reply: { ...page, missing: false, total: 2, searchable: 1, items, titles: {} } }, status]);
  renderPage(<WantedPage />, "/wanted");
  const rows = await screen.findAllByTestId("wanted-row");
  const button = (i: number) => within(rows[i]).getByRole("button", { name: "Search now" }) as HTMLButtonElement;
  expect([0, 1].map(i => button(i).disabled)).toEqual([true, false]);
  expect(button(0).title).toBe("A download for it is in progress");
  expect(within(rows[0]).getByTestId("search-blocked").textContent).toBe("A download for it is in progress");
  expect(screen.getByRole("button", { name: "Search all 1 missing" })).toBeTruthy();
});

it("leaves rows with a download in flight out of the selection", async () => {
  const user = userEvent.setup();
  const items = (["in_flight", undefined, undefined] as const).map((search_blocked, i) => ({ ...row, title_id: i + 1, waiting: search_blocked ? "in_flight" as const : "due" as const, search_blocked }));
  const fetch = mockFetch([{ method: "GET", path: "/wanted", reply: { ...page, total: 3, searchable: 2, items, titles: { "1": "Busy", "2": "Alpha", "3": "Beta" } } }, status,
    { method: "POST", path: "/wanted/search", reply: answer }]);
  renderPage(<WantedPage />, "/wanted");
  await screen.findAllByTestId("wanted-row");
  const box = (name: string) => screen.getByRole("checkbox", { name }) as HTMLInputElement;
  expect(box("Select Busy, Movie, 4K WEB").disabled).toBe(true);
  // Select all takes the two that can be searched; clicking it again clears them.
  await user.click(box("Select all on this page"));
  expect(box("Select all on this page").checked).toBe(true);
  expect(screen.getByRole("button", { name: "Search selected (2)" })).toBeTruthy();
  await user.click(box("Select all on this page"));
  expect(screen.getByRole("button", { name: "Search selected (0)" })).toBeTruthy();
  // Select all, then untick Alpha: exactly Beta is sent.
  await user.click(box("Select all on this page"));
  await user.click(box("Select Alpha, Movie, 4K WEB"));
  await user.click(screen.getByRole("button", { name: "Search selected (1)" }));
  await waitFor(() => expect(fetch.calls("POST", "/wanted/search")).toHaveLength(1));
  expect(fetch.calls("POST", "/wanted/search")[0].body).toEqual({ only: "missing", items: [{ title_id: 3, version: "movie-web" }] });
});

it("shows the asked rows not started yet and cancels them", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/wanted", reply: { ...page, search_now_pending: 306, items: [{ ...row, why: "asked" }] } },
    status, { method: "DELETE", path: "/wanted/search", reply: { cleared: 306 } },
    { method: "GET", path: "/wanted", reply: page },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const line = await screen.findByTestId("search-now-queue");
  expect(line.textContent).toMatch(/^306 searches queued by Search now/);
  expect(line.textContent).not.toMatch(/next at/);
  const r = screen.getByTestId("wanted-row");
  expect(within(r).getByText("Now · asked by Search now")).toBeTruthy();
  expect((within(r).getByRole("button", { name: "Search now" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(within(line).getByRole("button", { name: "Cancel queued searches" }));
  await waitFor(() => expect(fetch.calls("DELETE", "/wanted/search")).toHaveLength(1));
  await waitFor(() => expect(screen.queryByTestId("search-now-queue")).toBeNull());
});

it("says the asked searches wait while paused", async () => {
  mockFetch([{ method: "GET", path: "/wanted", reply: { ...page, downloads_enabled: false, search_now_pending: 3 } }, status]);
  renderPage(<WantedPage />, "/wanted");
  const line = await screen.findByTestId("search-now-queue");
  expect(line.textContent).toMatch(/^3 searches queued by Search now · waiting until Pause is off/);
});

it("says why a search was refused", async () => {
  mockFetch([{ method: "GET", path: "/wanted", reply: page }, status, { method: "POST", path: "/wanted/search", reply: { status: 409, body: { error: "Theatarr is paused, so nothing can be searched." } } }]);
  renderPage(<WantedPage />, "/wanted");
  await userEvent.setup().click(await screen.findByRole("button", { name: "Search now" }));
  expect((await screen.findByRole("alert")).textContent).toBe("Theatarr is paused, so nothing can be searched.");
  expect(searchNowWords({ queued: 0, started: 0, pending: 0, skipped: [] })).toBe("Started 0, queued 0.");
});

it("hides and restores a column from the column picker", async () => {
  const user = userEvent.setup();
  mockFetch([admin, { method: "GET", path: "/wanted", reply: page }, status]);
  renderPage(<WantedPage />, "/wanted");
  await screen.findByTestId("wanted-row");
  await user.click(screen.getByRole("button", { name: "Columns" }));
  const list = await screen.findByTestId("column-list");
  await user.click(within(list).getByRole("checkbox", { name: /Last search/ }));
  expect(screen.getAllByRole("columnheader").map(h => h.textContent)).not.toContain("Last search");
  await user.click(within(list).getByRole("checkbox", { name: /Last search/ }));
  expect(screen.getAllByRole("columnheader").map(h => h.textContent)).toContain("Last search");
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
  // The missing switch off and Pause on; /status is admin-only and not asked.
  const items = [{ ...row, waiting: "downloads_off" as const, next_at: null, search_blocked: "downloads_off" as const }];
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage"] } },
    { method: "GET", path: "/wanted", reply: { ...page, missing: false, downloads_enabled: false, searchable: 0, total: 1, items } },
    { method: "GET", path: "/status", reply: { status: 403, body: { error: "admin required" } } },
  ]);
  renderPage(<WantedPage />, "/wanted");
  const r = await screen.findByTestId("wanted-row");
  expect(within(r).getByRole("button", { name: "Interactive search" })).toBeTruthy();
  expect(screen.getByTestId("downloads-off-notice")).toBeTruthy();
  expect(screen.getByTestId("wanted-switch-off")).toBeTruthy();
  expect((within(r).getByRole("button", { name: "Search now" }) as HTMLButtonElement).disabled).toBe(true);
  expect(fetch.calls("GET", "/status")).toHaveLength(0);
  expect(within(r).queryByRole("link", { name: "Decision" })).toBeNull();
  expect(screen.queryByRole("tab", { name: "Manual import" })).toBeNull();
  expect(screen.getByRole("tab", { name: "Missing" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Automatic search settings" })).toBeNull();
  expect(document.querySelector('a[href^="/admin"]')).toBeNull();
  const all = screen.getByRole("button", { name: "Search all missing" }) as HTMLButtonElement;
  expect(all.disabled).toBe(true);
});
