// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { CollectionsPage } from "./collections";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";

const manager = { id: 2, username: "m", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const alien: Schemas["Collection"] = { tmdb_id: 8091, name: "Alien Collection", monitored: true, versions: ["movie-web"], search_on_add: true,
  minimum_availability: "released", poster: "/img/tmdb/w342/posterOne123.jpg", held: 2, total: 6 };
const page: Schemas["CollectionsPage"] = {
  collections: [alien, { tmdb_id: 9100, name: "", monitored: false, versions: [], search_on_add: false, minimum_availability: "released", held: 1, total: 1 }],
  libraries: { "movie-remux": "4K Remux", "movie-web": "4K WEB" },
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
  expect(row.getByText("2 of 6 movies in the library")).toBeTruthy();
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

it("is not for plain users and does not ask for collections", async () => {
  const fetch = mockFetch([{ method: "GET", path: "/auth/me", reply: { ...manager, permissions: ["browse", "request"] } }]);
  renderPage(<CollectionsPage />, "/collections");
  expect(await screen.findByTestId("forbidden")).toBeTruthy();
  expect(fetch.calls("GET", "/collections")).toHaveLength(0);
});
