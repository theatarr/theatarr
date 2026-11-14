// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { useLocation } from "react-router";
import { expect, it } from "vitest";
import { SearchProvider, useSearchUI } from "./search-context";
import { mockFetch, renderPage } from "@/test/page";

function Opener() {
  const { openOverlay } = useSearchUI();
  useEffect(() => openOverlay("Signal"), [openOverlay]);
  return null;
}

function Where() {
  return <p data-testid="where">{useLocation().pathname + useLocation().hash}</p>;
}

it("closes the overlay when an admin follows the TMDb key link", async () => {
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["browse", "add", "admin"], csrf_token: "t" } },
    { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library") ? { library: [], library_total: 0 } : { tmdb_state: "off" }) },
    { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
  ]);
  renderPage(<SearchProvider><Opener /><Where /></SearchProvider>, "/");
  const link = await screen.findByRole("link", { name: "Set the TMDb key" });
  await user.click(link);
  await waitFor(() => expect(screen.queryByTestId("search-overlay")).toBeNull());
  expect(screen.getByTestId("where").textContent).toBe("/admin/settings/metadata#sources");
});

it("puts an In library TMDb hit the name search missed into the library group", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["browse", "add", "admin"], csrf_token: "t" } },
    { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library") ? { library: [], library_total: 0 }
      : { tmdb_state: "ok", tmdb: [{ tmdb_id: 99, media_type: "movie", kind: "movie", name: "Signal", year: 2026, in_library: true, title_id: 4 }],
        library: [{ id: 4, kind: "movie", name: "Juniper Signal", year: 2026, versions: [] }], library_total: 1 }) },
    { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
  ]);
  renderPage(<SearchProvider><Opener /></SearchProvider>, "/");
  await waitFor(() => expect(screen.getByTestId("search-group-library").textContent).toContain("Juniper Signal"));
});

it("starts with the previous text selected, and shows an in-library title's status", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["browse", "add", "admin", "see_files"], csrf_token: "t" } },
    { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library")
      ? { library: [{ id: 4, kind: "series", name: "Lanterns", year: 2026, availability: "unmonitored", monitored: false, versions: [{ key: "tv", has_file: false }] }], library_total: 1 }
      : { tmdb_state: "off" }) },
    { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
  ]);
  renderPage(<SearchProvider><Opener /></SearchProvider>, "/");
  const input = (await screen.findByTestId("search-input")) as HTMLInputElement;
  await waitFor(() => expect([input.selectionStart, input.selectionEnd]).toEqual([0, "Signal".length]));
  expect(input.maxLength).toBe(200);
  expect((await screen.findByTestId("search-status")).textContent).toBe("Not monitored");
});
