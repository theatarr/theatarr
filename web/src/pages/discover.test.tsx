// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DiscoverPage } from "./discover";
import { mockFetch, renderPage } from "@/test/page";

afterEach(() => vi.unstubAllGlobals());

describe("Discover › Upcoming", () => {
  it("says Upcoming only for a movie not out yet; one already released reads Missing", async () => {
    const items = [
      { id: 1, kind: "movie", name: "Not Out Yet", availability: "upcoming", release_date: "2999-01-01", date: "2999-01-01", note: "In cinemas" },
      { id: 2, kind: "movie", name: "Out In Cinemas", availability: "missing", release_date: "2001-01-01", next_up: { type: "physical", date: "2999-02-01" }, date: "2999-02-01", note: "Physical release" },
    ];
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["admin", "see_files"], csrf_token: "t" } },
      { method: "GET", path: "/discover/upcoming", reply: { items, total: 2, more: "/discover/upcoming" } },
    ]);
    renderPage(<Routes><Route path="/discover/:row" element={<DiscoverPage />} /></Routes>, "/discover/upcoming");
    const soon = await screen.findByRole("link", { name: /Not Out Yet/ });
    expect(soon.textContent).toContain("Upcoming");
    const out = screen.getByRole("link", { name: /Out In Cinemas/ });
    expect(out.textContent).toContain("Missing");
    expect(out.textContent).not.toContain("Upcoming");
  });
});

describe("Discover › Latest files and Upgrade found", () => {
  const me = { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["admin", "see_files"], csrf_token: "t" } };
  it("calls the newest-file row Latest files, not Added (the day a title was added)", async () => {
    const at = new Date().toISOString();
    mockFetch([me, { method: "GET", path: "/discover/recent", reply: { items: [{ id: 1, kind: "movie", name: "Anora", availability: "available", date: at.slice(0, 10), at }], total: 1, more: "/discover/recent" } }]);
    renderPage(<Routes><Route path="/discover/:row" element={<DiscoverPage />} /></Routes>, "/discover/recent");
    const card = await screen.findByRole("link", { name: /Anora/ });
    expect(card.textContent).toContain("File added today");
    expect(screen.getByRole("heading", { name: "Latest files" })).toBeTruthy();
  });
  it("keeps the upgrade row's note: which libraries have an upgrade found", async () => {
    mockFetch([me, { method: "GET", path: "/discover/upgrades", reply: { items: [{ id: 2, kind: "series", name: "The Kardashians", availability: "partial", note: "4K series: upgrade found" }], total: 1, more: "/discover/upgrades" } }]);
    renderPage(<Routes><Route path="/discover/:row" element={<DiscoverPage />} /></Routes>, "/discover/upgrades");
    expect((await screen.findByRole("link", { name: /The Kardashians/ })).textContent).toContain("4K series: upgrade found");
  });
});
