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
 { method: "GET", path: "/settings/sync/reviews", reply: [] },
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
 for (const [text, href] of failure ? [["Could not load the gateway (VPN egress and back-offs)", "/admin/advanced/limits"]] : [["budget:test backed off", "/admin/advanced/limits"], ["indexer:test backed off", "/admin/settings/indexers"], ["provider:test backed off", "/admin/settings/downloads"]]) {
  const title = await screen.findByText(text);
  expect(title.closest("li")?.querySelector("a")?.getAttribute("href")).toBe(href);
 }
});

it("links readiness switches to their owning pages and shows the newest complete backup", async () => {
 const o = overview();
 mockFetch([...reviewRoutes,
  { method: "GET", path: "/settings", reply: { ...o, switches: { ...o.switches, router_enabled: false, library_enabled: true, notify_enabled: false } } },
  { method: "GET", path: "/system/backups", reply: { sets: [{ complete: true, created_at: "2026-11-01T03:00:00Z" }, { complete: true, created_at: "2026-11-02T03:00:00Z" }] } },
 ]);
 renderPage(<Readiness />);
 await screen.findByText("ON");
 for (const [label, state, route] of [["Downloads", "OFF", "downloads"], ["Library links", "ON", "libraries"], ["Notifications", "OFF", "notifications"]]) {
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
