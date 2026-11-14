// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import { createElement } from "react";
import { expect, it } from "vitest";
import { Home, librarySummary } from "./home";
import { mockFetch, renderPage } from "@/test/page";

it("counts titles across libraries, not each title's place in each library", () => {
  expect(librarySummary(1204, 6)).toBe("1,204 titles across 6 libraries.");
  expect(librarySummary(1, 1)).toBe("1 title across 1 library.");
  expect(librarySummary(0, undefined)).toBe("0 titles across 0 libraries.");
});

it("links Home's Missing titles to Wanted's items for managers", async () => {
  const missing = { items: [{ id: 1, kind: "movie", name: "Alpha", availability: "missing", note: "4K WEB" }], total: 25, more: "/discover/missing" };
  const empty = { items: [], total: 0, more: "" };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage", "see_files"] } },
    { method: "GET", path: "/discover", reply: { recent: empty, upcoming: empty, missing, upgrades: empty } },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
    { method: "GET", path: "/continue", reply: { items: [] } },
  ]);
  renderPage(createElement(Home), "/");
  const link = await screen.findByTestId("missing-in-wanted");
  expect(link.getAttribute("href")).toBe("/wanted");
  const row = link.closest("section") as HTMLElement;
  expect(within(row).getByRole("link", { name: "See all 25 titles" }).getAttribute("href")).toBe("/discover/missing");
});
