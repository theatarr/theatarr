// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
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
 { method: "GET", path: "/settings/sync/reviews", reply: empty ? [] : [{ id: 1 }, { id: 2 }] },
 { method: "GET", path: "/anime/review", reply: { items: empty ? [] : [{ state: "open" }, { state: "accepted" }] } },
 { method: "GET", path: "/cardigann/holds", reply: catalogOff ? json({ error: "off" }, 503) : empty ? [] : [{ id: "site" }] },
 { method: "GET", path: "/journal", reply: { items: empty ? [] : Array.from({ length: 50 }, (_, id) => ({ id: `dl:${id}` })) } },
];
it("counts the four queues, treats catalog 503 as zero, and marks a full journal page as a lower bound", async () => {
 const fetch = mockFetch(routes(false, true));
 renderPage(<Counts />);
 await waitFor(() => expect(JSON.parse(screen.getByRole("status").textContent!)).toMatchObject({ quality: 2, anime: 1, indexers: 0, decisions: 50, total: 53, decisionsMore: true, isPending: false }));
 expect(fetch.calls("GET", "/anime/review")[0].search).toBe("?state=open");
 expect(fetch.calls("GET", "/journal")[0].search).toBe("?suspect=1&limit=50");
 expect(suspectCount({ items: [], total: 81 })).toEqual({ count: 81, more: false });
});
it("does not request review queues for non-admins", async () => {
 const fetch = mockFetch(routes(false, false, false));
 renderPage(<Counts />);
 await waitFor(() => expect(fetch.calls("GET", "/auth/me")).toHaveLength(1));
 expect(fetch.calls("GET", "/settings/sync/reviews")).toHaveLength(0);
 expect(fetch.calls("GET", "/anime/review")).toHaveLength(0);
 expect(fetch.calls("GET", "/cardigann/holds")).toHaveLength(0);
 expect(fetch.calls("GET", "/journal")).toHaveLength(0);
});
it("shows Waiting cards with counts and links to each review tab", async () => {
 mockFetch(routes()); renderPage(<ReviewsPage />, "/admin/reviews");
 for (const title of ["Quality updates (2)", "Anime mappings (1)", "Indexer definitions (1)", "Flagged decisions (50+)"]) expect(await screen.findByRole("heading", { name: title })).toBeTruthy();
 expect(screen.getByText(/Wrong blocks the release.*decision was for an active library.*otherwise it records the verdict/)).toBeTruthy();
 for (const [label, tab] of [["quality updates", "quality"], ["anime mappings", "anime"], ["indexer definitions", "indexers"], ["flagged decisions", "decisions"]]) expect(screen.getByRole("link", { name: `Review ${label}` }).getAttribute("href")).toBe(`/admin/reviews/${tab}`);
});
it("shows the empty inbox only after queues load", async () => {
 mockFetch(routes(true)); renderPage(<ReviewsPage />, "/admin/reviews");
 expect(await screen.findByText("Nothing is waiting for you")).toBeTruthy();
 expect(screen.getByText("Quality updates, anime mappings, indexer definitions and flagged decisions.")).toBeTruthy();
});
it("quality tab renders pending reviews only, while settings renders sources, help and a waiting link", async () => {
 mockFetch([
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/policy", reply: {} },
  { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [{ id: 1, key: "source", config: {} }] } },
  { method: "GET", path: "/settings/formats/schema", reply: [] },
  { method: "GET", path: "/settings/sync/reviews", reply: [{ id: 1, source: "source" }] },
  { method: "GET", path: "/settings/sync/runs", reply: [] },
 ]);
 const view = renderPage(<ReviewsPage />, "/admin/reviews/quality");
 expect(await screen.findByText("Pending reviews (1)")).toBeTruthy();
 expect(screen.queryByRole("button", { name: "Run sync now" })).toBeNull();
 expect(screen.queryByText("Recent runs")).toBeNull();
 view.unmount();
 renderPage(<SettingsPage />, "/admin/settings/quality-sync");
 expect(await screen.findByRole("button", { name: "Run sync now" })).toBeTruthy();
 expect(screen.getByRole("link", { name: "Review" }).getAttribute("href")).toBe("/admin/reviews/quality");
 expect(screen.queryByText(/Pending reviews/)).toBeNull();
 expect(screen.getByText("How sync works").closest("details")?.open).toBe(false);
});

it.each([409, 503])("quality sync %s is unavailable without a Reviews error", async (status) => {
 mockFetch(routes(true).map(r => r.path === "/settings/sync/reviews" ? { ...r, reply: json({ error: "off" }, status) } : r).concat([{ method: "GET", path: "/settings", reply: overview() }]));
 const view = renderPage(<Counts />);
 await waitFor(() => expect(JSON.parse(screen.getByRole("status").textContent!)).toMatchObject({ quality: 0, isPending: false }));
 expect(JSON.parse(screen.getByRole("status").textContent!).error).toBeUndefined();
 view.unmount();
 renderPage(<ReviewsPage />, "/admin/reviews/quality");
 expect(await screen.findByText(status === 409 ? "Quality sync is not available while settings come from YAML" : "Quality sync is not running")).toBeTruthy();
 expect(screen.queryByText("Pending reviews (0)")).toBeNull();
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
