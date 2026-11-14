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
