// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ContinueRow } from "./continue-row";
import { mockFetch, renderPage } from "@/test/page";

afterEach(() => vi.unstubAllGlobals());

const item = {
  id: 42, name: "Fennick's Last Engine", year: 2024, kind: "movie", poster: "", version: "movie-2160p", label: "4K",
  download: 7, state: "downloading", step: "downloading", words: "Downloading 40%", progress: 40, downloads: 1,
};

describe("Home's Getting ready row", () => {
  it("names the library by its label and opens the title's page", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["admin", "see_files"], csrf_token: "t" } },
      { method: "GET", path: "/activity/continue", reply: { items: [item], total: 1, more: "/activity" } },
    ]);
    renderPage(<ContinueRow />);
    const card = await screen.findByRole("link", { name: "Fennick's Last Engine: Downloading 40%" });
    expect(card.getAttribute("href")).toBe("/title/42");
    expect(await screen.findByText("2024 · 4K")).toBeTruthy();
    expect(card.textContent).not.toContain("movie-2160p");
  });
});
