// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SearchPage } from "./search";
import { SearchProvider } from "@/components/search/search-context";
import { mockFetch, renderPage } from "@/test/page";

const answer = (r: { search: string }) =>
  r.search.includes("scope=library")
    ? { library: [{ id: 4, kind: "movie", name: "Juniper Signal", year: 2021, versions: [] }], library_total: 1 }
    : { tmdb_state: "ok", tmdb: [{ tmdb_id: 99, media_type: "movie", kind: "movie", name: "Signal Fires", year: 2019 }] };

function show(permissions: string[]) {
  const me = { id: 9, username: "someone", permissions, csrf_token: "t" };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/search", reply: answer },
    { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
  ]);
  renderPage(<SearchProvider><SearchPage /></SearchProvider>, "/search?q=Signal");
}

describe("Search page wording by role", () => {
  it("offers Add to an account that adds", async () => {
    show(["browse", "add"]);
    await screen.findByText("Add from TMDb");
    expect(screen.getByText(/Add a movie or series/)).toBeTruthy();
    expect(await screen.findByRole("button", { name: "Add Signal Fires (2019)" })).toBeTruthy();
  });

  it("offers Request, never Add, to an account that requests", async () => {
    show(["browse", "request"]);
    await screen.findByText("Request from TMDb");
    expect(screen.getByText(/Request a movie or series/)).toBeTruthy();
    expect(screen.queryByText(/Add a movie or series/)).toBeNull();
    expect(await screen.findByRole("button", { name: "Request Signal Fires (2019)" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Request Juniper Signal" })).toBeTruthy();
  });

  it("promises nothing to an account that may neither add nor request", async () => {
    show(["browse"]);
    await screen.findByText("On TMDb");
    await screen.findAllByText("Signal Fires");
    expect(screen.queryByText(/Add a movie or series|Request a movie or series/)).toBeNull();
    expect(screen.queryByRole("button", { name: /^(Add|Request) / })).toBeNull();
    expect(screen.getByRole("link", { name: "Open Juniper Signal" })).toBeTruthy();
  });
});

describe("Search page without a TMDb key", () => {
  const off = (r: { search: string }) =>
    r.search.includes("scope=library") ? { library: [], library_total: 0 } : { tmdb_state: "off" };
  function showOff(permissions: string[]) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions, csrf_token: "t" } },
      { method: "GET", path: "/search", reply: off },
      { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
    ]);
    renderPage(<SearchProvider><SearchPage /></SearchProvider>, "/search?q=Signal");
  }

  it("links admins to where the key is set", async () => {
    showOff(["browse", "add", "admin"]);
    const link = await screen.findByRole("link", { name: "Set the TMDb key" });
    expect(link.getAttribute("href")).toBe("/admin/settings/metadata#sources");
    expect(screen.getByText(/no TMDb key on this server/)).toBeTruthy();
  });

  it("tells everyone else who can add one, with no settings link", async () => {
    showOff(["browse", "add"]);
    expect(await screen.findByText(/An administrator can add one\./)).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Set the TMDb key" })).toBeNull();
  });
});

describe("Search page poster badges", () => {
  it("keeps only the kind on a narrow poster and shows In library below it instead of clipping", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions: ["browse", "add"], csrf_token: "t" } },
      { method: "GET", path: "/search", reply: (r: { search: string }) => r.search.includes("scope=library")
        ? { library: [], library_total: 0 }
        : { tmdb_state: "ok", tmdb: [{ tmdb_id: 99, media_type: "movie", kind: "movie", name: "Signal Fires", year: 2019, in_library: true, title_id: 4 }] } },
      { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
    ]);
    renderPage(<SearchProvider><SearchPage /></SearchProvider>, "/search?q=Signal");
    const status = await screen.findByText("In library");
    // The status is in the caption under the poster, not on the art beside the kind chip, where it was cut off.
    expect(status.closest("[data-testid=poster-badges]")).toBeNull();
    const badges = document.querySelector("[data-testid=poster-badges]") as HTMLElement;
    expect(badges.textContent).toBe("Movie");
    expect(status.closest("[data-testid=status-badge]")).toBeTruthy();
  });
});

describe("Search library results by account", () => {
  const rows = (r: { search: string }) =>
    r.search.includes("scope=library")
      ? { library: [
          { id: 4, kind: "movie", name: "Juniper Signal", year: 2021, availability: "available",
            versions: [{ key: "movie-remux", label: "4K Remux", has_file: false }, { key: "movie-web", label: "4K WEB", has_file: true, quality: "2160p" }] },
          { id: 5, kind: "movie", name: "Signal Lost", year: 2022, availability: "missing", acquiring: true, versions: [{ key: "movie-web", label: "4K WEB", has_file: false }] },
        ], library_total: 2 }
      : { tmdb_state: "ok", tmdb: [] };
  function showRows(permissions: string[]) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions, csrf_token: "t" } },
      { method: "GET", path: "/search", reply: rows },
      { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
    ]);
    renderPage(<SearchProvider><SearchPage /></SearchProvider>, "/search?q=Signal");
  }

  it("says availability, never library keys, to an account without See files", async () => {
    showRows(["browse", "request"]);
    await screen.findByRole("link", { name: "Open Juniper Signal" });
    expect(screen.getAllByTestId("search-watch").map((e) => e.textContent)).toEqual(["Available in 4K", "Getting ready"]);
    expect(document.querySelector("[data-search-result] [data-vkey]")).toBeNull();
    expect(document.body.textContent).not.toMatch(/movie-remux|movie-web/);
  });

  it("keeps the library keys for an account that sees files", async () => {
    showRows(["browse", "add", "see_files"]);
    await screen.findByRole("link", { name: "Open Juniper Signal" });
    expect(screen.queryByTestId("search-watch")).toBeNull();
    expect([...document.querySelectorAll("[data-search-result] [data-vkey]")].map((e) => e.getAttribute("data-vkey"))).toEqual(["movie-remux", "movie-web", "movie-web"]);
  });
});

describe("Search page when TMDb does not answer", () => {
  const failed = (r: { search: string }) =>
    r.search.includes("scope=library") ? { library: [], library_total: 0 } : { tmdb_state: "error", tmdb_error: "TMDb search failed; try again in a moment" };
  function showFailed(permissions: string[]) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions, csrf_token: "t" } },
      { method: "GET", path: "/search", reply: failed },
      { method: "GET", path: "/requests", reply: { items: [], total: 0 } },
    ]);
    renderPage(<SearchProvider><SearchPage /></SearchProvider>, "/search?q=Signal");
  }

  it("says what to check and links admins to the TMDb key", async () => {
    showFailed(["browse", "add", "admin"]);
    const next = await screen.findByTestId("tmdb-next");
    expect(next.textContent).toContain("the TMDb key may be wrong or Theatarr cannot reach TMDb");
    expect(screen.getByRole("link", { name: "Check the TMDb key and address" }).getAttribute("href")).toBe("/admin/settings/metadata#sources");
    expect(screen.getByText("TMDb did not answer")).toBeTruthy();
  });

  it("gives everyone else the next step without a settings link", async () => {
    showFailed(["browse", "add"]);
    expect((await screen.findByTestId("tmdb-next")).textContent).toContain("An administrator can check the key.");
    expect(screen.queryByRole("link", { name: "Check the TMDb key and address" })).toBeNull();
  });
});
