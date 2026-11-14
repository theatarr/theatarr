// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { HistoryTab } from "./history-tab";

const me = { id: 9, username: "manager", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const title = { id: 1, name: "Alpha", year: 2026, kind: "movie" };
const grab = { id: "dl:7", at: "2026-11-12T14:03:00Z", kind: "grab", title, version: "movie-web", words: "Found a release", tone: "info", release: "Alpha.2026.2160p.WEB-DL", quality: "WEBDL-2160p", score: 1500 };
const link = { id: "lib:3", at: "2026-11-11T09:00:00Z", kind: "link", title, version: "movie-web", words: "Linked into the library", tone: "ok" };

it("shows quality and score in cells, a dash where the event has none, and no day headings; Load more asks for the next cursor", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    {
      method: "GET",
      path: "/activity",
      reply: (r: { search: string }) =>
        r.search.includes("cursor=c2") ? { state: "history", admin: true, history: [{ ...link, id: "lib:2", at: "2026-11-01T09:00:00Z" }] } : { state: "history", admin: true, history: [grab, link], next_cursor: "c2" },
    },
  ]);
  renderPage(<HistoryTab admin />, "/activity/history");
  const rows = await screen.findAllByTestId("history-row");
  expect(rows).toHaveLength(2);
  expect(rows[0].querySelector('[data-column="quality"]')?.textContent).toBe("WEBDL-2160p");
  expect(rows[0].querySelector('[data-column="score"]')?.textContent).toBe("1,500");
  expect(rows[1].querySelector('[data-column="quality"]')?.textContent).toBe("—");
  expect(rows[1].querySelector('[data-column="score"]')?.textContent).toBe("—");
  expect(screen.queryByText("Yesterday")).toBeNull();
  expect(document.querySelector("h3")).toBeNull();
  await userEvent.setup().click(screen.getByRole("button", { name: "Load more" }));
  await waitFor(() => expect(screen.getAllByTestId("history-row")).toHaveLength(3));
  expect(fetch.calls("GET", "/activity").some((r) => r.search.includes("cursor=c2"))).toBe(true);
});
