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

it("shows a title in the library once: its TMDb hit leaves the TMDb group while its card is shown", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["browse", "add", "admin"], csrf_token: "t" } },
    { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library")
      ? { library: [{ id: 4, kind: "movie", name: "Signal", year: 2026, versions: [] }], library_total: 1 }
      : { tmdb_state: "ok", tmdb: [{ tmdb_id: 99, media_type: "movie", kind: "movie", name: "Signal", year: 2026, in_library: true, title_id: 4 },
        { tmdb_id: 7, media_type: "movie", kind: "movie", name: "Other Signal", year: 2020, in_library: true, title_id: 8 }] }) },
    { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
  ]);
  renderPage(<SearchProvider><Opener /></SearchProvider>, "/");
  // The held hit whose card is not shown above (title 8) stays, marked In library.
  await waitFor(() => expect(screen.getByTestId("search-group-tmdb").textContent).toContain("Other Signal"));
  const tm = screen.getByTestId("search-group-tmdb");
  expect(tm.querySelectorAll("[data-search-result=tmdb]").length).toBe(1);
  expect(tm.textContent).toContain("1 result");
  expect(tm.textContent).toContain("In library");
});

it("keeps a household member's View request on the library card when its TMDb hit is left out", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions: ["browse", "request"], csrf_token: "t" } },
    { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library")
      ? { library: [{ id: 4, kind: "series", name: "Lanterns", year: 2026, versions: [] }], library_total: 1 }
      : { tmdb_state: "ok", tmdb: [{ tmdb_id: 555, media_type: "tv", kind: "series", name: "Lanterns", year: 2026, in_library: true, title_id: 4 }] }) },
    { method: "GET", path: "/requests", reply: { items: [{ id: 7, kind: "series", tmdb_id: 555, status: "pending", name: "Lanterns" }], total: 1 } },
  ]);
  renderPage(<SearchProvider><Opener /></SearchProvider>, "/");
  await waitFor(() => expect(screen.getByTestId("search-group-tmdb").textContent).toContain("No additional matches."));
  const lib = screen.getByTestId("search-group-library");
  await waitFor(() => expect(lib.querySelector("[data-testid=view-request]")?.getAttribute("aria-label")).toBe("View your request for Lanterns"));
  expect(screen.queryByRole("button", { name: "Request Lanterns" })).toBeNull();
});

// After an add from Search the new title's page is not covered by Search (F1); Cancel and a failed add come back to
// the same search, its text and results kept. Invented keys and names.
const addGates = { paused: false, pause_forced: false, library_writes: true, will_grab: true };
const addOptions = { kind: "movie", anime: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [],
  versions: [{ key: "movie-web", label: "Web", kind: "movie", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: addGates }] };
const addRoutes = (post: unknown) => [
  { method: "GET", path: "/auth/me", reply: { id: 1, username: "admin", permissions: ["browse", "add", "admin"], csrf_token: "t" } },
  { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library") ? { library: [], library_total: 0 }
    : { tmdb_state: "ok", tmdb: [{ tmdb_id: 99, media_type: "movie", kind: "movie", name: "Signal", year: 2026, in_library: false }] }) },
  { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
  { method: "GET", path: "/add/options", reply: addOptions },
  { method: "POST", path: "/titles", reply: post },
];

it("closes Search when an add succeeds and goes to the new title's page", async () => {
  const user = userEvent.setup();
  mockFetch(addRoutes({ created: true, new: ["movie-web"], title: { id: 9, name: "Signal", versions: [] } }));
  renderPage(<SearchProvider><Opener /><Where /></SearchProvider>, "/");
  await user.click(await screen.findByRole("button", { name: "Add Signal (2026)" }));
  await user.click(await screen.findByTestId("add-submit"));
  await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/title/9"));
  expect(screen.queryByTestId("add-sheet")).toBeNull();
  expect(screen.queryByTestId("search-overlay")).toBeNull();
});

it("comes back to the same search after Cancel and after a failed add", async () => {
  const user = userEvent.setup();
  mockFetch(addRoutes({ status: 500, body: { error: "boom" } }));
  renderPage(<SearchProvider><Opener /><Where /></SearchProvider>, "/");
  await user.click(await screen.findByRole("button", { name: "Add Signal (2026)" }));
  await screen.findByTestId("add-submit");
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByTestId("add-sheet")).toBeNull());
  expect(screen.getByTestId("search-overlay")).toBeTruthy();
  expect((screen.getByTestId("search-input") as HTMLInputElement).value).toBe("Signal");
  // The failed add keeps the sheet, its error shown; closing it shows the same search.
  await user.click(screen.getByRole("button", { name: "Add Signal (2026)" }));
  await user.click(await screen.findByTestId("add-submit"));
  await waitFor(() => expect(screen.getByTestId("add-sheet").textContent).toMatch(/boom|Could not add/));
  expect(screen.getByTestId("search-overlay")).toBeTruthy();
  expect(screen.getByTestId("where").textContent).toBe("/");
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByTestId("add-sheet")).toBeNull());
  expect((screen.getByTestId("search-input") as HTMLInputElement).value).toBe("Signal");
  expect(screen.getByRole("button", { name: "Add Signal (2026)" })).toBeTruthy();
});

it("keeps Search under a member's sent request until the member follows its link", async () => {
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, username: "member", permissions: ["browse", "request"], csrf_token: "t" } },
    { method: "GET", path: "/search", reply: (r: { search: string }) => (r.search.includes("scope=library") ? { library: [], library_total: 0 }
      : { tmdb_state: "ok", tmdb: [{ tmdb_id: 99, media_type: "movie", kind: "movie", name: "Signal", year: 2026, in_library: false }] }) },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
    { method: "GET", path: "/add/options", reply: addOptions },
    { method: "POST", path: "/requests", reply: { approved: false, merged: false, request: { id: 4, name: "Signal", status: "pending", versions: [{ key: "movie-web", label: "Web" }] } } },
  ]);
  renderPage(<SearchProvider><Opener /><Where /></SearchProvider>, "/");
  await user.click(await screen.findByRole("button", { name: "Request Signal (2026)" }));
  await user.click(await screen.findByTestId("request-submit"));
  await screen.findByTestId("request-done");
  expect(screen.getByTestId("search-overlay")).toBeTruthy();
  await user.click(screen.getByRole("link", { name: "View request" }));
  await waitFor(() => expect(screen.queryByTestId("search-overlay")).toBeNull());
  expect(screen.queryByTestId("request-sheet")).toBeNull();
  expect(screen.getByTestId("where").textContent).not.toBe("/");
});
