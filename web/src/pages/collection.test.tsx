// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { Route, Routes } from "react-router";
import { CollectionPage } from "./collection";
import { json, mockFetch, renderPage } from "@/test/page";
import { SearchProvider } from "@/components/search/search-context";
import { useRemove } from "@/api/ops";
import type { Schemas } from "@/api/client";

const manager = { id: 2, username: "m", permissions: ["browse", "see_files", "manage", "add"], csrf_token: "t" };
const alien: Schemas["Collection"] = { tmdb_id: 8091, name: "Alien Collection", monitored: true, versions: ["movie-web"], search_on_add: true,
  minimum_availability: "released", poster: "/img/tmdb/w342/posterOne123.jpg", held: 2, available: 1, total: 3 };
const detail = (c: Schemas["Collection"]): Schemas["CollectionDetail"] => ({
  collection: c,
  parts: [
    { tmdb_id: 348, title: "Alien", year: 1979, title_id: 217, availability: "available", poster: "/img/tmdb/w342/alien123.jpg" },
    { tmdb_id: 679, title: "Aliens", year: 1986, title_id: 218, availability: "missing" },
    { tmdb_id: 8077, title: "Alien 3", year: 1992 },
  ],
  libraries: { "movie-remux": "4K Remux", "movie-web": "4K WEB" },
  can_add: true,
});
const lonely: Schemas["Collection"] = { tmdb_id: 9100, name: "", monitored: false, versions: [], search_on_add: false, minimum_availability: "released", held: 1, available: 1, total: 1 };

const show = (path: string, extra?: React.ReactNode) => renderPage(
  <SearchProvider><Routes><Route path="/collections/:id" element={<><CollectionPage />{extra}</>} /></Routes></SearchProvider>, path);

/** Removes title 217 as the title page's Remove does. */
function RemoveAlien() {
  const remove = useRemove(217);
  return <button type="button" onClick={() => remove.mutate({} as Parameters<typeof remove.mutate>[0])}>Remove Alien</button>;
}

it("shows a collection's films with their status and offers Add for one not in the library", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections/8091", reply: detail(alien) },
    { method: "GET", path: "/add/options", reply: { kind: "movie", versions: [] } },
    { method: "POST", path: "/collections/8091/refresh", reply: { collection: { ...alien, held: 3 }, added: 1, skipped: 2 } },
  ]);
  show("/collections/8091");
  expect(await screen.findByRole("heading", { name: "Alien Collection" })).toBeTruthy();
  expect(screen.getByText("2 of 3 in the library · 1 available")).toBeTruthy();
  expect(screen.getByTestId("collection-summary").textContent).toBe("MonitoredAdds missing movies to 4K WEB and searches for them; minimum availability Released.");
  const alienCard = within(screen.getByTestId("part-348"));
  expect(alienCard.getByRole("link").getAttribute("href")).toBe("/title/217");
  expect(alienCard.getByRole("link").getAttribute("aria-label")).toBe("Alien (1979), Available");
  expect(within(screen.getByTestId("part-679")).getByRole("link").getAttribute("aria-label")).toBe("Aliens (1986), Missing");
  const missing = within(screen.getByTestId("part-8077"));
  expect(missing.getByTestId("part-not-held").textContent).toBe("Not in the library · Add");
  await user.click(missing.getByRole("button", { name: "Alien 3 (1992), not in the library, add" }));
  await waitFor(() => expect(fetch.calls("GET", "/add/options")[0]?.search).toContain("?kind=movie&tmdb_id=8077"));
  // No TMDb call on open: only the stored collection was read.
  expect(fetch.calls("POST", "/collections/8091/refresh")).toHaveLength(0);
});

it("adds missing movies now from the collection's page", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections/8091", reply: detail(alien) },
    { method: "POST", path: "/collections/8091/refresh", reply: { collection: { ...alien, held: 3 }, added: 1, skipped: 2 } },
  ]);
  show("/collections/8091");
  await user.click(await screen.findByRole("button", { name: "Add missing movies now" }));
  await waitFor(() => expect(fetch.calls("POST", "/collections/8091/refresh")).toHaveLength(1));
  expect((await screen.findByTestId("collections-done")).textContent).toBe("Alien Collection: 1 movie added, 2 already in the library or excluded.");
});

it("keeps monitoring, libraries, search on add and minimum availability behind Edit, saving each at once", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections/8091", reply: detail(alien) },
    { method: "PUT", path: "/collections/8091", reply: { ...alien, versions: ["movie-remux", "movie-web"] } },
    { method: "PUT", path: "/collections/8091", reply: { ...alien, versions: ["movie-remux", "movie-web"], search_on_add: false } },
    { method: "PUT", path: "/collections/8091", reply: { ...alien, versions: ["movie-remux", "movie-web"], search_on_add: false, minimum_availability: "inCinemas" } },
  ]);
  show("/collections/8091");
  expect(screen.queryByTestId("collection-settings")).toBeNull();
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  const dialog = () => within(screen.getByTestId("collection-settings"));
  // A monitored collection keeps its last library.
  expect(dialog().getByRole("button", { name: "4K WEB" }).getAttribute("aria-pressed")).toBe("true");
  expect(dialog().getByRole("button", { name: "4K WEB" }).hasAttribute("disabled")).toBe(true);
  await user.click(dialog().getByRole("button", { name: "4K Remux" }));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/8091")).toHaveLength(1));
  expect(fetch.calls("PUT", "/collections/8091")[0].body).toEqual({ versions: ["movie-web", "movie-remux"] });
  await waitFor(() => expect(dialog().getByRole("button", { name: "4K Remux" }).getAttribute("aria-pressed")).toBe("true"));
  expect(dialog().getByRole("button", { name: "4K WEB" }).hasAttribute("disabled")).toBe(false);
  await user.click(dialog().getByLabelText("Search on add"));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/8091").at(-1)?.body).toEqual({ search_on_add: false }));
  await waitFor(() => expect(dialog().getByLabelText("Minimum availability").hasAttribute("disabled")).toBe(false));
  await user.selectOptions(dialog().getByLabelText("Minimum availability"), "inCinemas");
  await waitFor(() => expect(fetch.calls("PUT", "/collections/8091").at(-1)?.body).toEqual({ minimum_availability: "inCinemas" }));
  // The page behind follows what was saved.
  await user.click(dialog().getByRole("button", { name: "Done" }));
  expect(screen.getByTestId("collection-summary").textContent).toBe("MonitoredAdds missing movies to 4K Remux, 4K WEB; minimum availability In cinemas.");
});

it("monitors a collection only once it has a library", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections/9100", reply: { ...detail(lonely), parts: [] } },
    { method: "PUT", path: "/collections/9100", reply: (r: { body: unknown }) =>
      ({ ...lonely, versions: ["movie-remux"], monitored: (r.body as { monitored?: boolean }).monitored ?? false }) },
  ]);
  show("/collections/9100");
  expect(await screen.findByRole("heading", { name: "Collection 9100" })).toBeTruthy();
  expect(screen.getByText("No movies known yet. They arrive with the next list refresh.")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Add missing movies now" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const dialog = () => within(screen.getByTestId("collection-settings"));
  expect(dialog().getByText("Choose a library to monitor this collection")).toBeTruthy();
  expect(dialog().getByRole("switch", { name: "Monitor Collection 9100" }).getAttribute("aria-disabled")).toBe("true");
  await user.click(dialog().getByRole("button", { name: "4K Remux" }));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/9100")[0]?.body).toEqual({ versions: ["movie-remux"] }));
  await waitFor(() => expect(dialog().getByRole("switch", { name: "Monitor Collection 9100" }).getAttribute("aria-disabled")).toBeNull());
  await user.click(dialog().getByRole("switch", { name: "Monitor Collection 9100" }));
  await waitFor(() => expect(fetch.calls("PUT", "/collections/9100")[1]?.body).toEqual({ monitored: true }));
  await waitFor(() => expect(dialog().getByRole("switch", { name: "Monitor Collection 9100" }).getAttribute("aria-checked")).toBe("true"));
});

it("marks a film removed from every library, offers Add for it, and refreshes the films after an add", async () => {
  const user = userEvent.setup();
  const gates = { paused: false, pause_forced: false, library_writes: true, mode: "active", will_grab: true };
  const options = { kind: "movie", anime: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [],
    versions: [{ key: "movie-web", label: "4K WEB", mode: "active", grab_state: gates, kind: "movie", enable: "", fits: true, default: true, on: false, searchable: true }] };
  const removedFirst = { ...detail({ ...alien, held: 1, removed: 1 }), parts: [detail(alien).parts[0], { tmdb_id: 679, title: "Aliens", year: 1986, removed: true }] };
  let added = false;
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections/8091", reply: () => added
      ? { ...removedFirst, collection: { ...alien, held: 2, removed: 0 }, parts: [removedFirst.parts[0], { tmdb_id: 679, title: "Aliens", year: 1986, title_id: 218, availability: "missing" }] }
      : removedFirst },
    { method: "GET", path: "/add/options", reply: options },
    { method: "POST", path: "/titles", reply: () => { added = true; return json({ created: true, new: ["movie-web"], title: { id: 218, name: "Aliens", versions: [] } }); } },
  ]);
  show("/collections/8091");
  expect(await screen.findByText("1 of 3 in the library (1 removed) · 1 available")).toBeTruthy();
  const aliens = within(screen.getByTestId("part-679"));
  expect(aliens.getByTestId("part-not-held").textContent).toBe("Removed from the library · Add");
  await user.click(aliens.getByRole("button", { name: "Aliens (1986), removed from the library, add" }));
  await user.click(await screen.findByTestId("add-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  // The page reads the collection again: the film now links to its title.
  await waitFor(() => expect(within(screen.getByTestId("part-679")).getByRole("link").getAttribute("href")).toBe("/title/218"));
  expect(screen.getByText("2 of 3 in the library · 1 available")).toBeTruthy();
});

it("follows a removal: the film no longer links to its title and the counts change", async () => {
  const user = userEvent.setup();
  let removed = false;
  mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/collections/8091", reply: () => removed
      ? { ...detail({ ...alien, held: 1, available: 0, removed: 1 }), parts: [{ tmdb_id: 348, title: "Alien", year: 1979, removed: true }, ...detail(alien).parts.slice(1)] }
      : detail(alien) },
    { method: "POST", path: "/titles/217/remove", reply: () => { removed = true; return {}; } },
  ]);
  show("/collections/8091", <RemoveAlien />);
  expect(within(await screen.findByTestId("part-348")).getByRole("link").getAttribute("href")).toBe("/title/217");
  await user.click(screen.getByRole("button", { name: "Remove Alien" }));
  await waitFor(() => expect(within(screen.getByTestId("part-348")).queryByRole("link")).toBeNull());
  expect(within(screen.getByTestId("part-348")).getByTestId("part-not-held").textContent).toBe("Removed from the library · Add");
  expect(screen.getByText("1 of 3 in the library (1 removed) · 0 available")).toBeTruthy();
});
