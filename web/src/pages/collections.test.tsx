// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { CollectionsPage } from "./collections";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";

const manager = { id: 2, username: "m", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const alien: Schemas["Collection"] = { tmdb_id: 8091, name: "Alien Collection", monitored: true, versions: ["movie-web"], search_on_add: true,
  minimum_availability: "released", poster: "/img/tmdb/w342/posterOne123.jpg", held: 2, available: 1, total: 6 };
const page: Schemas["CollectionsPage"] = {
  collections: [alien, { tmdb_id: 9100, name: "", monitored: false, versions: [], search_on_add: false, minimum_availability: "released", held: 1, available: 1, total: 1 }],
  libraries: { "movie-remux": "4K Remux", "movie-web": "4K WEB" },
  can_add: true,
};

it("lists collections and saves monitoring, libraries and search on add", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections", reply: page },
    { method: "PUT", path: "/collections/8091", reply: { ...alien, versions: ["movie-remux", "movie-web"] } },
    { method: "PUT", path: "/collections/9100", reply: (r: { body: unknown }) =>
      ({ ...page.collections[1], versions: ["movie-remux"], monitored: (r.body as { monitored?: boolean }).monitored ?? false }) },
  ]);
  renderPage(<CollectionsPage />, "/collections");
  const row = within(await screen.findByTestId("collection-8091"));
  // In the library and available (a file held) are counted apart.
  expect(row.getByText("2 of 6 in the library · 1 available")).toBeTruthy();
  expect(screen.getByText("2 collections, 1 monitored.", { exact: false })).toBeTruthy();
  const other = () => within(screen.getByTestId("collection-9100"));
  expect(other().getByText("Collection 9100")).toBeTruthy();
  // No library yet: it cannot be monitored. A monitored collection keeps its last library.
  expect(other().getByText("Choose a library to monitor this collection")).toBeTruthy();
  expect(other().getByRole("switch", { name: "Monitor Collection 9100" }).getAttribute("aria-disabled")).toBe("true");
  expect(row.getByRole("button", { name: "4K WEB" }).getAttribute("aria-pressed")).toBe("true");
  expect(row.getByRole("button", { name: "4K WEB" }).hasAttribute("disabled")).toBe(true);

  await user.click(screen.getByLabelText("Monitored only"));
  expect(screen.queryByTestId("collection-9100")).toBeNull();
  await user.click(screen.getByLabelText("Monitored only"));
  await user.type(screen.getByLabelText("Filter collections"), "alien");
  expect(screen.queryByTestId("collection-9100")).toBeNull();
  await user.clear(screen.getByLabelText("Filter collections"));

  await user.click(row.getByRole("button", { name: "4K Remux" }));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/8091")).toHaveLength(1));
  expect(fetch.calls("PUT", "/collections/8091")[0].body).toEqual({ versions: ["movie-web", "movie-remux"] });
  await waitFor(() => expect(row.getByRole("button", { name: "4K Remux" }).getAttribute("aria-pressed")).toBe("true"));
  expect(row.getByRole("button", { name: "4K WEB" }).hasAttribute("disabled")).toBe(false);

  await user.click(other().getByRole("button", { name: "4K Remux" }));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/9100")[0]?.body).toEqual({ versions: ["movie-remux"] }));
  await waitFor(() => expect(other().getByRole("switch", { name: "Monitor Collection 9100" }).getAttribute("aria-disabled")).toBeNull());
  await user.click(other().getByRole("switch", { name: "Monitor Collection 9100" }));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/9100")[1]?.body).toEqual({ monitored: true }));
  await waitFor(() => expect(other().getByRole("switch", { name: "Monitor Collection 9100" }).getAttribute("aria-checked")).toBe("true"));

  await user.click(row.getByLabelText("Search on add"));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/8091").at(-1)?.body).toEqual({ search_on_add: false }));

});

const predator: Schemas["Collection"] = { tmdb_id: 399, name: "Predator Collection", monitored: true, versions: ["movie-remux"], search_on_add: true,
  minimum_availability: "released", held: 5, available: 5, total: 5 };

it("adds a collection found on TMDb, and shows one that is already added instead", async () => {
  const user = userEvent.setup();
  let added = false;
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections", reply: () => added ? { ...page, collections: [...page.collections, predator] } : page },
    { method: "GET", path: "/collections/search", reply: { results: [
      { tmdb_id: 399, name: "Predator Collection", overview: "Hunters from the stars.", added: false },
      { tmdb_id: 8091, name: "Alien Collection", added: true }] } },
    { method: "POST", path: "/collections", reply: () => { added = true; return { collection: predator, added: 4, skipped: 1 }; } },
    { method: "POST", path: "/collections/8091/refresh", reply: { collection: { ...alien, held: 3 }, added: 1, skipped: 5 } },
  ]);
  renderPage(<CollectionsPage />, "/collections");
  await user.click(await screen.findByRole("button", { name: "Add collection" }));
  const dialog = within(await screen.findByTestId("add-collection"));
  expect(dialog.getByRole("button", { name: "Add" }).hasAttribute("disabled")).toBe(true);
  await user.type(dialog.getByLabelText("Search TMDb collections"), "predator");
  expect(within(await dialog.findByTestId("collection-hit-8091")).getByText("Already added")).toBeTruthy();
  expect(fetch.calls("GET", "/collections/search").at(-1)?.search).toBe("?q=predator");
  await user.click(dialog.getByTestId("collection-hit-399"));
  // Two libraries: none is preselected, and a monitored collection needs one.
  expect(dialog.getByText("Choose a library to monitor this collection.")).toBeTruthy();
  expect(dialog.getByRole("button", { name: "Add" }).hasAttribute("disabled")).toBe(true);
  await user.click(dialog.getByRole("button", { name: "4K Remux" }));
  await user.click(dialog.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(fetch.calls("POST", "/collections")).toHaveLength(1));
  expect(fetch.calls("POST", "/collections")[0].body).toEqual({ tmdb_id: 399, versions: ["movie-remux"], monitored: true, search_on_add: true, minimum_availability: "released" });
  expect((await screen.findByTestId("collections-done")).textContent).toBe("Added Predator Collection: 4 movies added, 1 already in the library or excluded.");
  expect(await screen.findByTestId("collection-399")).toBeTruthy();
  expect(screen.queryByTestId("add-collection")).toBeNull();

  // An already added collection is not added again: the dialog closes on its row.
  await user.click(screen.getByRole("button", { name: "Add collection" }));
  await user.type(within(screen.getByTestId("add-collection")).getByLabelText("Search TMDb collections"), "alien");
  await user.click(await screen.findByTestId("collection-hit-8091"));
  expect(screen.queryByTestId("add-collection")).toBeNull();
  expect(fetch.calls("POST", "/collections")).toHaveLength(1);

  // A monitored collection's missing movies on demand.
  await user.click(within(screen.getByTestId("collection-8091")).getByRole("button", { name: "Add missing movies now" }));
  await waitFor(() => expect(fetch.calls("POST", "/collections/8091/refresh")).toHaveLength(1));
  await waitFor(() => expect(screen.getByTestId("collections-done").textContent).toBe("Alien Collection: 1 movie added, 5 already in the library or excluded."));
  expect(within(screen.getByTestId("collection-9100")).queryByRole("button", { name: "Add missing movies now" })).toBeNull();
});

it("adds a collection unmonitored when no library can take its movies, and offers nothing without TMDb", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections", reply: { collections: [], libraries: {}, can_add: true } },
    { method: "GET", path: "/collections/search", reply: { results: [{ tmdb_id: 399, name: "Predator Collection", added: false }] } },
    { method: "POST", path: "/collections", reply: { collection: { ...predator, monitored: false, versions: [] }, added: 0, skipped: 0 } },
  ]);
  const first = renderPage(<CollectionsPage />, "/collections");
  expect(await screen.findByText("No collections yet. Add one, or they appear when a movie in the library belongs to one.")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Add collection" }));
  const dialog = within(screen.getByTestId("add-collection"));
  await user.type(dialog.getByLabelText("Search TMDb collections"), "predator");
  await user.click(await dialog.findByTestId("collection-hit-399"));
  expect(dialog.getByText("only a Live movie library can take", { exact: false })).toBeTruthy();
  await user.click(dialog.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(fetch.calls("POST", "/collections")[0]?.body).toMatchObject({ tmdb_id: 399, versions: [], monitored: false }));
  first.unmount();

  mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections", reply: { ...page, can_add: false } },
  ]);
  renderPage(<CollectionsPage />, "/collections");
  const row = within(await screen.findByTestId("collection-8091"));
  expect(screen.getByRole("button", { name: "Add collection" }).hasAttribute("disabled")).toBe(true);
  expect(row.queryByRole("button", { name: "Add missing movies now" })).toBeNull();
});

it("is not for plain users and does not ask for collections", async () => {
  const fetch = mockFetch([{ method: "GET", path: "/auth/me", reply: { ...manager, permissions: ["browse", "request"] } }]);
  renderPage(<CollectionsPage />, "/collections");
  expect(await screen.findByTestId("forbidden")).toBeTruthy();
  expect(fetch.calls("GET", "/collections")).toHaveLength(0);
});

it("keeps nameless empty collections off the list until named, and offers Add missing only when movies are missing", async () => {
  const nameless = { tmdb_id: 1802110, name: "", monitored: false, versions: [], search_on_add: false, minimum_availability: "released" as const, held: 0, available: 0, total: 0 };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections", reply: { ...page, collections: [{ ...alien, held: 6, available: 6 }, nameless] } },
  ]);
  renderPage(<CollectionsPage />, "/collections");
  const row = within(await screen.findByTestId("collection-8091"));
  expect(row.queryByRole("button", { name: "Add missing movies now" })).toBeNull();
  expect(screen.queryByTestId("collection-1802110")).toBeNull();
  expect(screen.queryByText(/0 of 0/)).toBeNull();
  expect(screen.getByText("1 collection, 1 monitored. 1 more collection gets its name with the next list refresh.", { exact: false })).toBeTruthy();
});
