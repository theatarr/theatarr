// @vitest-environment jsdom
import { fireEvent, screen, within, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { overview } from "@/components/settings/fixtures";
import { fmtTime } from "@/lib/format";
import { Overview, Readiness } from "./overview";
import { JobsPage } from "./jobs";

const reviewRoutes = [
 { method: "GET", path: "/auth/me", reply: { permissions: ["admin"] } },
 { method: "GET", path: "/settings/sync/changes", reply: [] },
 { method: "GET", path: "/anime/review", reply: { items: [] } },
 { method: "GET", path: "/cardigann/holds", reply: [] },
 { method: "GET", path: "/journal", reply: { items: [] } },
];
const status = { schema: { current: 1, target: 1 } };

describe("admin overview", () => {
  it("names the subsystems it could not load instead of saying nothing needs attention", async () => {
    mockFetch([...reviewRoutes,
      { method: "GET", path: "/readyz", reply: { status: "ok" } },
      { method: "GET", path: "/status", reply: status },
      { method: "GET", path: "/jobs", reply: json({ error: "jobs down" }, 500) },
      { method: "GET", path: "/gateway", reply: { egress: { vpn_ok: true }, backoffs: [] } },
      { method: "GET", path: "/health", reply: json({ error: "health down" }, 500) },
      { method: "GET", path: "/activity", reply: { now: [] } },
    ]);
    renderPage(<Overview />);
    expect(await screen.findByText("Could not load failed jobs")).toBeTruthy();
    expect(await screen.findByText("Could not load health checks")).toBeTruthy();
    expect(screen.queryByText("Nothing needs attention.")).toBeNull();
  });

  it("says nothing needs attention only once every source loaded", async () => {
    mockFetch([...reviewRoutes,
      { method: "GET", path: "/readyz", reply: { status: "ok" } },
      { method: "GET", path: "/status", reply: status },
      { method: "GET", path: "/jobs", reply: { items: [], counts: {} } },
      { method: "GET", path: "/gateway", reply: { egress: { vpn_ok: true }, backoffs: [] } },
      { method: "GET", path: "/health", reply: { checks: [] } },
      { method: "GET", path: "/activity", reply: { now: [] } },
    ]);
    renderPage(<Overview />);
    expect(await screen.findByText("Nothing needs attention.")).toBeTruthy();
  });
});

describe("admin jobs", () => {
  it("shows the failed-jobs error instead of no failed job needs attention", async () => {
    mockFetch([...reviewRoutes,
      { method: "GET", path: "/jobs", reply: (r: { search: string }) => r.search.includes("state=failed") ? json({ error: "failed jobs down" }, 500) : { items: [], counts: {} } },
    ]);
    renderPage(<JobsPage />);
    expect(await screen.findByText("failed jobs down")).toBeTruthy();
    expect(screen.queryByText("No failed job needs attention.")).toBeNull();
  });
});

it.each([false, true])("routes back-offs and gateway failures to their settings (failure=%s)", async (failure) => {
 mockFetch([...reviewRoutes,
  { method: "GET", path: "/readyz", reply: { status: "ok" } },
  { method: "GET", path: "/status", reply: status },
  { method: "GET", path: "/jobs", reply: { items: [], counts: {} } },
  { method: "GET", path: "/gateway", reply: failure ? json({ error: "gateway down" }, 500) : { egress: { vpn_ok: true }, backoffs: ["budget:test", "indexer:test", "provider:test"].map(subject => ({ subject, active: true })) } },
  { method: "GET", path: "/health", reply: { checks: [] } },
  { method: "GET", path: "/activity", reply: { now: [] } },
 ]);
 renderPage(<Overview />);
 for (const [text, href] of failure ? [["Could not load the gateway (VPN egress and back-offs)", "/admin/advanced/limits"]] : [["Request budget test is backed off", "/admin/advanced/limits"], ["Indexer test is backed off", "/admin/settings/indexers"], ["Provider test is backed off", "/admin/settings/downloads"]]) {
  const title = await screen.findByText(text);
  expect(title.closest("li")?.querySelector("a")?.getAttribute("href")).toBe(href);
 }
});

it("links readiness switches to their owning pages and shows the newest complete backup", async () => {
 const o = overview();
 mockFetch([...reviewRoutes,
  { method: "GET", path: "/settings", reply: { ...o, switches: { ...o.switches, paused: true, pause_forced: false } } },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 1, sections: [{ key: "notify", live: true, config: { enabled: false }, yaml: "", secrets: [] }] } },
  { method: "GET", path: "/system/backups", reply: { sets: [{ complete: true, created_at: "2026-11-01T03:00:00Z" }, { complete: true, created_at: "2026-11-02T03:00:00Z" }] } },
 ]);
 renderPage(<Readiness />);
 await screen.findByText("ON");
 for (const [label, state, route] of [["Pause", "ON", "libraries"], ["Notifications", "OFF", "notifications"]]) {
  const link = screen.getByRole("link", { name: `${label} ${state}` });
  expect(link.getAttribute("href")).toBe(`/admin/settings/${route}`);
 }
 expect(screen.getByRole("link", { name: `Backup ${fmtTime("2026-11-02T03:00:00Z")}` }).getAttribute("href")).toBe("/admin/system/backup");
});
it("groups repeated failures by kind, keeps the newest error and retries only the newest", async () => {
 const fetch = mockFetch([...reviewRoutes,
  { method: "GET", path: "/readyz", reply: { status: "ok" } },
  { method: "GET", path: "/status", reply: status },
  { method: "GET", path: "/jobs", reply: { items: [{ id: 1, kind: "provider.sync", state: "failed", last_error: "new error", finished_at: "2026-11-02T12:00:00Z" }, { id: 2, kind: "provider.sync", state: "failed", last_error: "old error", finished_at: "2026-11-02T11:00:00Z" }], counts: {} } },
  { method: "GET", path: "/gateway", reply: { egress: { vpn_ok: true }, backoffs: [] } },
  { method: "GET", path: "/health", reply: { checks: [] } },
  { method: "GET", path: "/activity", reply: { now: [] } },
  { method: "POST", path: "/jobs/1/retry", reply: { job: { id: 3 }, retry_of: 1, created: true } },
 ]);
 renderPage(<Overview />);
 const attention = await screen.findByTestId("attention");
 expect(within(attention).getAllByRole("listitem")).toHaveLength(1);
 expect(within(attention).getByText(/Sync debrid holdings failed 2 times/)).toBeTruthy();
 expect(within(attention).getByText("new error")).toBeTruthy();
 expect(within(attention).queryByText("old error")).toBeNull();
 expect(screen.getByRole("link", { name: "See all" }).getAttribute("href")).toBe("/admin/system/tasks?kind=provider.sync");
 fireEvent.click(screen.getByRole("button", { name: "Retry newest" }));
 await waitFor(() => expect(fetch.calls("POST", "/jobs/1/retry")).toHaveLength(1));
});
it("filters Tasks by the kind query and can show all again", async () => {
 const fetch = mockFetch([{ method: "GET", path: "/jobs", reply: (r: { search: string }) => ({ counts: {}, items: [{ id: 1, kind: "provider.sync", state: "failed", last_error: "provider error" }, { id: 2, kind: "arr.import", state: "failed", last_error: "arr error" }].filter(j => !new URLSearchParams(r.search).has("kind") || j.kind === new URLSearchParams(r.search).get("kind")) }) }]);
 renderPage(<JobsPage />, "/admin/system/tasks?kind=provider.sync");
 expect(await screen.findByText("provider error")).toBeTruthy();
 expect(screen.queryByText("arr error")).toBeNull();
 expect(fetch.calls("GET", "/jobs").every(r => new URLSearchParams(r.search).get("kind") === "provider.sync")).toBe(true);
 fireEvent.click(screen.getByRole("button", { name: "Show all tasks" }));
 expect(await screen.findByText("arr error")).toBeTruthy();
});

it.each(["pending", "error", "scan"])("Status does not give an all-clear for %s reviews", async state => {
 mockFetch([
  ...reviewRoutes.map(r => r.path === "/journal" ? { ...r, reply: state === "pending" ? () => new Promise(() => {}) : state === "error" ? json({ error: "journal down" }, 500) : { items: [], next_cursor: "older" } } : r),
  { method: "GET", path: "/readyz", reply: { status: "ok" } },
  { method: "GET", path: "/status", reply: status },
  { method: "GET", path: "/jobs", reply: { items: [], counts: {} } },
  { method: "GET", path: "/gateway", reply: { egress: { vpn_ok: true } } },
  { method: "GET", path: "/health", reply: { checks: [] } },
  { method: "GET", path: "/activity", reply: { now: [] } },
 ]);
 renderPage(<Overview />);
 await screen.findByText("Needs attention" + (state === "pending" ? "" : " (1)"));
 if (state === "error") {
  const row = await screen.findByText("Reviews: couldn't load flagged decisions");
  expect(row.closest("li")?.querySelector("a")?.getAttribute("href")).toBe("/admin/reviews");
 }
 if (state === "scan") expect(await screen.findByText("? decisions waiting in Reviews")).toBeTruthy();
 expect(screen.queryByText("Nothing needs attention.")).toBeNull();
});

it("lists the broken links behind the dangling-links check when opened, once, and only shows them", async () => {
 const check = { source: "DanglingLinksCheck", name: "Dangling links", type: "notice", message: "3 of 40 links into the mount are dangling", detail: { dangling: 3, links: 40 }, checked_at: "2026-11-02T12:00:00Z" };
 const fetch = mockFetch([...reviewRoutes,
  { method: "GET", path: "/readyz", reply: { status: "ok" } },
  { method: "GET", path: "/status", reply: status },
  { method: "GET", path: "/jobs", reply: { items: [], counts: {} } },
  { method: "GET", path: "/gateway", reply: { egress: { vpn_ok: true }, backoffs: [] } },
  { method: "GET", path: "/health", reply: { checks: [check, { source: "MountCheck", name: "Mount", type: "ok", message: "fine" }] } },
  { method: "GET", path: "/activity", reply: { now: [] } },
  { method: "GET", path: "/health/dangling", reply: { at: "2026-11-02T12:00:00Z", links: 40, dangling: 3, truncated: true, items: [
   { path: "/media/remux/Alpha (2020)/Alpha.mkv", target: "/mnt/zurg/Alpha.2020/Alpha.mkv", folder: "Alpha.2020", library: "movie-remux", label: "4K Remux", cause: "no_video", first_seen: "2026-10-30T12:00:00Z", title_id: 1, title: "Alpha", link_state: "dead" },
   { path: "/media/web/Beta (2021)/Beta.mkv", target: "/mnt/zurg/Beta.2021/Beta.mkv", folder: "Beta.2021", library: "movie-web", label: "4K WEB", first_seen: "2026-10-31T12:00:00Z", title_id: 2, title: "Beta", link_state: "active" },
   { path: "/media/other/Stray.mkv", target: "/mnt/zurg/Stray/Stray.mkv", folder: "Stray", first_seen: "2026-11-01T12:00:00Z" },
  ] } },
 ]);
 renderPage(<Overview />);
 const row = await screen.findByTestId("check-DanglingLinksCheck");
 const toggle = within(row).getByRole("button", { name: "Show the 3 broken links" });
 expect(within(screen.getByTestId("check-MountCheck")).queryByRole("button")).toBeNull();
 expect(fetch.calls("GET", "/health/dangling")).toHaveLength(0);
 fireEvent.click(toggle);
 const items = await within(row).findAllByTestId("broken-link");
 expect(items).toHaveLength(3);
 expect(within(items[0]).getByRole("link", { name: "Alpha" }).getAttribute("href")).toBe("/title/1");
 expect(within(items[0]).getByText("4K Remux")).toBeTruthy();
 expect(within(items[0]).getByText("torrent has no video upstream")).toBeTruthy();
 expect(within(items[0]).getByText("Link marked missing")).toBeTruthy();
 expect(within(items[0]).getByText("/media/remux/Alpha (2020)/Alpha.mkv")).toBeTruthy();
 expect(within(items[1]).getByText("Waiting for the next repair sweep")).toBeTruthy();
 expect(within(items[2]).getByText("Stray")).toBeTruthy();
 expect(within(items[2]).queryByRole("link")).toBeNull();
 expect(within(items[2]).getByText("Not a link Theatarr made")).toBeTruthy();
 expect(within(row).getByText(/Showing the 3 oldest/)).toBeTruthy();
 // Only a list: the one button in the row folds it again, and reopening does not load it a second time.
 expect(within(row).getAllByRole("button")).toHaveLength(1);
 fireEvent.click(within(row).getByRole("button", { name: "Hide the broken links" }));
 expect(within(row).queryByTestId("broken-link")).toBeNull();
 fireEvent.click(within(row).getByRole("button", { name: "Show the 3 broken links" }));
 expect(await within(row).findAllByTestId("broken-link")).toHaveLength(3);
 expect(fetch.calls("GET", "/health/dangling")).toHaveLength(1);
 expect(fetch.calls("POST", "/health/dangling")).toHaveLength(0);
});

it("offers no broken-links list while the check counts none", async () => {
 mockFetch([...reviewRoutes,
  { method: "GET", path: "/readyz", reply: { status: "ok" } },
  { method: "GET", path: "/status", reply: status },
  { method: "GET", path: "/jobs", reply: { items: [], counts: {} } },
  { method: "GET", path: "/gateway", reply: { egress: { vpn_ok: true }, backoffs: [] } },
  { method: "GET", path: "/health", reply: { checks: [{ source: "DanglingLinksCheck", name: "Dangling links", type: "ok", message: "No dangling links (40 links into the mount)", detail: { dangling: 0, links: 40 } }] } },
  { method: "GET", path: "/activity", reply: { now: [] } },
 ]);
 renderPage(<Overview />);
 const row = await screen.findByTestId("check-DanglingLinksCheck");
 expect(within(row).queryByRole("button")).toBeNull();
});
