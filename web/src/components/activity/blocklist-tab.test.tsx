// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { BlocklistTab } from "./blocklist-tab";

const me = { id: 9, username: "manager", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const blocked = (i: number) => ({
  version: "movie-web",
  info_hash: `h${i}`,
  title: { id: i, name: `Title ${i}`, kind: "movie" },
  release: `Release.${i}.1080p`,
  reason: "sample",
  source: "library",
  words: "Failed the library check (sample or runtime)",
  created_at: "2026-11-10T10:00:00Z",
  quality: "WEBDL-1080p",
  indexer: "torrentio",
});

it("sends the typed title, pages with the filtered total, and shows quality and indexer", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    {
      method: "GET",
      path: "/activity",
      reply: (r: { search: string }) => {
        const offset = Number(new URLSearchParams(r.search).get("offset") ?? 0);
        return { state: "blocked", admin: true, total: 312, blocked: [blocked(offset + 1), blocked(offset + 2)] };
      },
    },
  ]);
  renderPage(<BlocklistTab />, "/activity/blocklist");
  const rows = await screen.findAllByTestId("blocked-row");
  expect(rows[0].querySelector('[data-column="quality"]')?.textContent).toBe("WEBDL-1080p");
  expect(rows[0].querySelector('[data-column="indexer"]')?.textContent).toBe("torrentio");
  expect(screen.getByTestId("pager-range").textContent).toBe("1–50 of 312");
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Next" }));
  await waitFor(() => expect(screen.getByTestId("pager-range").textContent).toBe("51–100 of 312"));
  expect(fetch.calls("GET", "/activity").at(-1)!.search).toContain("offset=50");
  await user.type(screen.getByTestId("blocked-title"), "Alp");
  await waitFor(() => expect(fetch.calls("GET", "/activity").at(-1)!.search).toContain("title=Alp"));
  // A new filter starts at the first page.
  await waitFor(() => expect(fetch.calls("GET", "/activity").at(-1)!.search).not.toContain("offset="));
});

it("says nothing matches when a filter leaves no row", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    { method: "GET", path: "/activity", reply: (r: { search: string }) => ({ state: "blocked", admin: true, total: r.search.includes("title=") ? 0 : 1, blocked: r.search.includes("title=") ? [] : [blocked(1)] }) },
  ]);
  renderPage(<BlocklistTab />, "/activity/blocklist");
  await screen.findAllByTestId("blocked-row");
  await userEvent.setup().type(screen.getByTestId("blocked-title"), "zzz");
  expect(await screen.findByText("No blocked release matches.")).toBeTruthy();
});

it("unblocks the selected releases one by one", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    { method: "GET", path: "/activity", reply: { state: "blocked", admin: true, total: 2, blocked: [blocked(1), blocked(2)] } },
    { method: "POST", path: "/activity/blocked/unblock", reply: { version: "movie-web", info_hash: "h", release: "r", message: "Unblocked" } },
  ]);
  renderPage(<BlocklistTab />, "/activity/blocklist");
  const user = userEvent.setup();
  await user.click(await screen.findByRole("checkbox", { name: "Select Release.1.1080p" }));
  await user.click(screen.getByRole("checkbox", { name: "Select Release.2.1080p" }));
  await user.click(screen.getByRole("button", { name: "Unblock selected…" }));
  expect(screen.getByText("These 2 releases may be picked again for their libraries")).toBeTruthy();
  expect(screen.getByText("Nothing is searched now")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Unblock 2 releases" }));
  expect((await screen.findByTestId("confirm-done")).textContent).toBe("Unblocked 2 releases.");
  expect(fetch.calls("POST", "/activity/blocked/unblock").map((r) => r.body)).toEqual([
    { version: "movie-web", info_hash: "h1", search: false },
    { version: "movie-web", info_hash: "h2", search: false },
  ]);
});
