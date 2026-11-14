// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { json, mockFetch, renderPage, type Route } from "@/test/page";
import { useReviewCounts, suspectCount } from "@/api/reviews";
import { ReviewsPage } from "./reviews";
import { SettingsPage } from "./settings/pages";
import { overview } from "@/components/settings/fixtures";
import schema from "@/components/settings/testdata/schema.json";

function Counts() { const c = useReviewCounts(); return <output>{JSON.stringify(c)}</output>; }
const routes = (empty = false, catalogOff = false, admin = true): Route[] => [
 { method: "GET", path: "/auth/me", reply: { kind: "session", permissions: admin ? ["admin"] : ["browse"] } },
 { method: "GET", path: "/anime/review", reply: { items: empty ? [] : [{ state: "open" }, { state: "accepted" }] } },
 { method: "GET", path: "/cardigann/holds", reply: catalogOff ? json({ error: "off" }, 503) : empty ? [] : [{ id: "site" }] },
 { method: "GET", path: "/journal", reply: { items: empty ? [] : Array.from({ length: 50 }, (_, id) => ({ id: `dl:${id}` })) } },
 { method: "GET", path: "/journal/resolved", reply: { count: 0, items: [] } },
];
it("counts the three queues, treats catalog 503 as zero, and marks a full journal page as a lower bound", async () => {
 const fetch = mockFetch(routes(false, true));
 renderPage(<Counts />);
 await waitFor(() => expect(JSON.parse(screen.getByRole("status").textContent!)).toMatchObject({ anime: 1, indexers: 0, decisions: 50, total: 51, decisionsMore: true, isPending: false }));
 expect(fetch.calls("GET", "/anime/review")[0].search).toBe("?state=open");
 expect(fetch.calls("GET", "/journal")[0].search).toBe("?suspect=1&limit=50");
 expect(suspectCount({ items: [], total: 81 })).toEqual({ count: 81, more: false });
});
it("does not request review queues for non-admins", async () => {
 const fetch = mockFetch(routes(false, false, false));
 renderPage(<Counts />);
 await waitFor(() => expect(fetch.calls("GET", "/auth/me")).toHaveLength(1));
 expect(fetch.calls("GET", "/anime/review")).toHaveLength(0);
 expect(fetch.calls("GET", "/cardigann/holds")).toHaveLength(0);
 expect(fetch.calls("GET", "/journal")).toHaveLength(0);
});
it("shows Waiting cards with counts and links to each review tab", async () => {
 mockFetch(routes()); renderPage(<ReviewsPage />, "/admin/reviews");
 for (const title of ["Anime mappings (1)", "Indexer definitions (1)", "Flagged decisions (50+)"]) expect(await screen.findByRole("heading", { name: title })).toBeTruthy();
 expect(screen.getByText(/Wrong blocks the release.*decision was for an active library.*otherwise it records the verdict/)).toBeTruthy();
 expect(screen.queryByRole("heading", { name: /Quality updates/ })).toBeNull();
 for (const [label, tab] of [["anime mappings", "anime"], ["indexer definitions", "indexers"], ["flagged decisions", "decisions"]]) expect(screen.getByRole("link", { name: `Review ${label}` }).getAttribute("href")).toBe(`/admin/reviews/${tab}`);
});
it("shows the empty inbox only after queues load", async () => {
 mockFetch(routes(true)); renderPage(<ReviewsPage />, "/admin/reviews");
 expect(await screen.findByText("Nothing is waiting for you")).toBeTruthy();
 expect(screen.getByText("Anime mappings, indexer definitions and flagged decisions.")).toBeTruthy();
});
it("has no Quality tab: its old address shows Waiting, and settings render sources, help and the update log", async () => {
 mockFetch([
  ...routes(true),
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/policy", reply: {} },
  { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [{ id: 1, key: "source", config: {} }] } },
  { method: "GET", path: "/settings/formats/schema", reply: [] },
  { method: "GET", path: "/settings/sync/changes", reply: [] },
  { method: "GET", path: "/settings/sync/runs", reply: [] },
 ]);
 const view = renderPage(<ReviewsPage />, "/admin/reviews/quality");
 expect(await screen.findByText("Nothing is waiting for you")).toBeTruthy();
 expect(screen.queryByRole("link", { name: /Quality updates/ })).toBeNull();
 view.unmount();
 renderPage(<SettingsPage />, "/admin/settings/quality-sync");
 expect(await screen.findByRole("button", { name: "Run sync now" })).toBeTruthy();
 expect(await screen.findByText("No updates applied yet.")).toBeTruthy();
 expect(screen.queryByRole("link", { name: "Review" })).toBeNull();
 expect(screen.getByText("How sync works").closest("details")?.open).toBe(false);
});

it.each([0, 1])("keeps the scan-window count unknown with %s matches", async (n) => {
 const page = { items: Array.from({ length: n }, (_, id) => ({ id: String(id) })), next_cursor: "older" };
 expect(suspectCount(page)).toEqual({ count: n, more: true });
 mockFetch(routes(true).map(r => r.path === "/journal" ? { ...r, reply: page } : r));
 const view = renderPage(<ReviewsPage />, "/admin/reviews");
 expect(await screen.findByRole("heading", { name: `Flagged decisions (${n ? "1+" : "?"})` })).toBeTruthy();
 expect(screen.queryByText("Nothing is waiting for you")).toBeNull();
 view.unmount();
 renderPage(<ReviewsPage />, "/admin/reviews/decisions");
 expect(await screen.findByRole("link", { name: "See all flagged decisions" })).toBeTruthy();
 if (!n) expect(screen.getByText("None in the newest entries.")).toBeTruthy();
});

it.each(["pending", "error", "empty"])("definition reviews show %s", async state => {
 mockFetch([{ method: "GET", path: "/cardigann/holds", reply: state === "pending" ? () => new Promise(() => {}) : state === "error" ? json({ error: "holds unavailable" }, 500) : [] }]);
 renderPage(<ReviewsPage />, "/admin/reviews/indexers");
 if (state === "pending") expect(screen.getByText(/Loading/)).toBeTruthy();
 else expect(await screen.findByText(state === "error" ? "holds unavailable" : "No definition updates waiting.")).toBeTruthy();
});

it("shows a question mark badge when the scan found no matches but has older entries", async () => {
 const { ReviewsBadge } = await import("@/components/shell/app-shell");
 mockFetch(routes(true).map(r => r.path === "/journal" ? { ...r, reply: { items: [], next_cursor: "older" } } : r));
 renderPage(<ReviewsBadge />);
 expect((await screen.findByTestId("reviews-badge")).textContent).toBe("?");
});

it("names each flagged item and its reason, and acknowledges settled dead-link flags in bulk", async () => {
 let settled = 2;
 const fetch = mockFetch([
  ...routes(true).filter(r => r.path !== "/journal" && r.path !== "/journal/resolved"),
  { method: "GET", path: "/journal", reply: { items: [
   { id: "lib:4", at: "2026-11-01T12:00:00Z", kind: "link", title: { id: 3, name: "Gamma" }, version: "tv-2160p", summary: "dead (dry_run) /lib/Gamma/Season 01/Gamma S01E02.mkv", import: { state: "dry_run", links: [{ path: "/lib/Gamma/Season 01/Gamma S01E02.mkv" }] }, flags: [{ code: "dead_link", severity: "warn", reason: "the link's target is gone and no provider folder holds the file" }], suspect: true },
   { id: "dl:9:fail", at: "2026-11-01T11:00:00Z", kind: "failure", title: { id: 3, name: "Gamma" }, version: "tv-2160p", summary: "", release: { title: "Gamma.S01E03.2160p.WEB-DL", episodes: ["S01E03"] }, flags: [{ code: "repeated_failures", severity: "warn", reason: "4 failures for this title and library in 30 days" }], suspect: true },
  ] } },
  { method: "GET", path: "/journal/resolved", reply: () => ({ count: settled, items: [] }) },
  { method: "POST", path: "/journal/resolved/acknowledge", reply: () => { const n = settled; settled = 0; return { acknowledged: n }; } },
 ]);
 renderPage(<ReviewsPage />, "/admin/reviews/decisions");
 const rows = await screen.findAllByTestId("flagged");
 expect(within(rows[0]).getByText("/lib/Gamma/Season 01/Gamma S01E02.mkv")).toBeTruthy();
 expect(within(rows[0]).getByText(/target is gone/)).toBeTruthy();
 expect(within(rows[1]).getByRole("link", { name: "Gamma S01E03" })).toBeTruthy();
 expect(within(rows[1]).getByText("Gamma.S01E03.2160p.WEB-DL")).toBeTruthy();
 expect(within(rows[1]).getByText("4 failures for this title and library in 30 days")).toBeTruthy();
 const bar = await screen.findByTestId("resolved-flags");
 expect(bar.textContent).toContain("2 dead-link flags are settled");
 fireEvent.click(within(bar).getByRole("button", { name: "Acknowledge 2 settled flags" }));
 await waitFor(() => expect(fetch.calls("POST", "/journal/resolved/acknowledge")).toHaveLength(1));
 await waitFor(() => expect(screen.queryByTestId("resolved-flags")).toBeNull());
});
