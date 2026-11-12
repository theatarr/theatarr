// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { ListsPage } from "./lists";
import { listIDFrom } from "@/api/lists";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";

const manager = { id: 2, username: "m", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const libraries = { "movie-remux": "4K Remux", "movie-web": "4K WEB" };
const mine: Schemas["MovieList"] = { id: 1, name: "Favourites", list_type: "tmdb", kind: "movie", api_key_set: false, tmdb_id: 9, enabled: true, enable_auto: true, monitor: "movieOnly",
  search_on_add: true, versions: ["movie-remux"], minimum_availability: "released", libraries: ["movie-remux", "movie-web"] };
const arr: Schemas["MovieList"] = { ...mine, id: 2, name: "From Radarr", tmdb_id: 12, view: "radarr-4k", enable_auto: false, versions: ["movie-web"], libraries: ["movie-web"] };

it("reads the TMDb list id from digits or a pasted address", () => {
  expect(listIDFrom(" 8136 ")).toBe(8136);
  expect(listIDFrom("https://www.themoviedb.org/list/8136-best-of?language=en")).toBe(8136);
  expect(listIDFrom("themoviedb.org/list/77")).toBe(77);
  expect(listIDFrom("best of")).toBe(0);
  expect(listIDFrom("")).toBe(0);
});

it("adds a list from a pasted address after looking it up", async () => {
  const user = userEvent.setup();
  let lists: Schemas["MovieList"][] = [];
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/lists", reply: () => ({ lists, libraries, can_sync: true }) },
    { method: "GET", path: "/lists/lookup", reply: { tmdb_id: 9, name: "Favourites", description: "The good ones", movies: 12, added: false } },
    { method: "POST", path: "/lists", reply: () => { lists = [mine]; return { status: 201, body: mine }; } },
  ]);
  renderPage(<ListsPage />, "/collections/lists");
  expect(await screen.findByText("No lists yet. Add a TMDb list by its id or address, or a Radarr or Sonarr instance by its address and API key.")).toBeTruthy();
  expect(screen.getByText("A list is a TMDb movie list, or the titles of a Radarr or Sonarr instance.")).toBeTruthy();
  expect(screen.getByRole("tab", { name: "Lists" }).getAttribute("aria-selected")).toBe("true");
  expect(screen.getByRole("tab", { name: "Collections" }).getAttribute("href")).toBe("/collections");
  await user.click(screen.getByRole("button", { name: "Add list" }));
  const dialog = within(screen.getByTestId("list-dialog"));
  expect(dialog.getByRole("button", { name: "Add" }).hasAttribute("disabled")).toBe(true);
  await user.type(dialog.getByLabelText("TMDb list id or URL"), "https://www.themoviedb.org/list/9-favourites");
  expect((await dialog.findByTestId("list-found")).textContent).toBe("Favourites: 12 movies. The good ones");
  expect(fetch.calls("GET", "/lists/lookup").at(-1)?.search).toBe("?tmdb_id=9");
  // The name comes from TMDb unless one is typed; a library must be chosen.
  expect(dialog.getByRole("button", { name: "Add" }).hasAttribute("disabled")).toBe(true);
  await user.click(dialog.getByRole("button", { name: "4K Remux" }));
  await user.click(dialog.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(fetch.calls("POST", "/lists")).toHaveLength(1));
  expect(fetch.calls("POST", "/lists")[0].body).toEqual({ tmdb_id: 9, name: "Favourites", versions: ["movie-remux"], monitor: "movieOnly",
    search_on_add: true, minimum_availability: "released", enabled: true });
  const row = within(await screen.findByTestId("list-1"));
  expect(row.getByRole("link", { name: "TMDb list 9" }).getAttribute("href")).toBe("https://www.themoviedb.org/list/9");
  expect(row.getByText("Last sync: never", { exact: false })).toBeTruthy();
  expect(screen.queryByTestId("list-dialog")).toBeNull();
});

it("edits, enables, syncs and deletes lists, and labels one an arr client made", async () => {
  const user = userEvent.setup();
  let lists = [mine, arr];
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/lists", reply: () => ({ lists, libraries, can_sync: true }) },
    { method: "PUT", path: "/lists/1", reply: (r: { body: unknown }) => { lists = [{ ...mine, ...(r.body as object) }, arr]; return lists[0]; } },
    { method: "PUT", path: "/lists/2", reply: arr },
    { method: "POST", path: "/lists/1/sync", reply: { list: mine, added: 3, skipped: 9 } },
    { method: "DELETE", path: "/lists/1", reply: () => { lists = [arr]; return { status: 204 }; } },
  ]);
  renderPage(<ListsPage />, "/collections/lists");
  const row = () => within(screen.getByTestId("list-1"));
  const other = within(await screen.findByTestId("list-2"));
  expect(other.getByText("Created through the radarr-4k view")).toBeTruthy();

  await user.click(row().getByRole("switch", { name: "Enable Favourites" }));
  await waitFor(() => expect(fetch.calls("PUT", "/lists/1")[0]?.body).toEqual({ enabled: false }));
  await waitFor(() => expect(row().getByRole("switch", { name: "Enable Favourites" }).getAttribute("aria-checked")).toBe("false"));

  await user.click(row().getByRole("button", { name: "Edit" }));
  let dialog = within(screen.getByTestId("list-dialog"));
  expect(dialog.queryByLabelText("TMDb list id or URL")).toBeNull();
  await user.clear(dialog.getByLabelText("List name"));
  await user.type(dialog.getByLabelText("List name"), "Best");
  await user.click(dialog.getByRole("button", { name: "4K WEB" }));
  await user.selectOptions(dialog.getByLabelText("Monitor"), "none");
  await user.click(dialog.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fetch.calls("PUT", "/lists/1")[1]?.body).toEqual({ name: "Best", versions: ["movie-remux", "movie-web"], monitor: "none",
    search_on_add: true, minimum_availability: "released", enabled: false }));
  await waitFor(() => expect(row().getByText("Best")).toBeTruthy());

  // A list an arr client made: only its view's libraries, and it says whose it is.
  await user.click(other.getByRole("button", { name: "Edit" }));
  dialog = within(screen.getByTestId("list-dialog"));
  expect(dialog.getByTestId("list-view").textContent).toContain("Created through the radarr-4k view");
  expect(dialog.queryByRole("button", { name: "4K Remux" })).toBeNull();
  expect(dialog.getByRole("button", { name: "4K WEB" }).getAttribute("aria-pressed")).toBe("true");
  await user.click(dialog.getByRole("button", { name: "Cancel" }));

  await user.click(row().getByRole("button", { name: "Sync now" }));
  await waitFor(() => expect(fetch.calls("POST", "/lists/1/sync")).toHaveLength(1));
  expect((await screen.findByTestId("lists-done")).textContent).toBe("Favourites: 3 titles added, 9 skipped (already in the library or excluded).");

  await user.click(row().getByRole("button", { name: "Delete" }));
  expect(within(screen.getByTestId("list-delete")).getByText("The titles it already added, and their files")).toBeTruthy();
  expect(fetch.calls("DELETE", "/lists/1")).toHaveLength(0);
  await user.click(screen.getByTestId("confirm-ok"));
  await waitFor(() => expect(fetch.calls("DELETE", "/lists/1")).toHaveLength(1));
  await waitFor(() => expect(screen.queryByTestId("list-1")).toBeNull());
  expect(screen.queryByTestId("list-delete")).toBeNull();
});

it("is not for plain users and does not ask for lists", async () => {
  const fetch = mockFetch([{ method: "GET", path: "/auth/me", reply: { ...manager, permissions: ["browse", "request"] } }]);
  renderPage(<ListsPage />, "/collections/lists");
  expect(await screen.findByTestId("forbidden")).toBeTruthy();
  expect(fetch.calls("GET", "/lists")).toHaveLength(0);
});

it("adds a Radarr instance as a list after a check, previews it, and never shows its key on edit", async () => {
  const user = userEvent.setup();
  const radarr: Schemas["MovieList"] = { ...mine, id: 3, name: "Radarr", list_type: "radarr", tmdb_id: 0, url: "http://10.0.0.5:7878", api_key_set: true };
  let lists: Schemas["MovieList"][] = [];
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/lists", reply: () => ({ lists, libraries, series_libraries: { "tv-2160p": "TV 4K" }, can_sync: true }) },
    { method: "POST", path: "/lists/check", reply: { app: "Radarr", version: "5.2.0", titles: 1234 } },
    { method: "POST", path: "/lists", reply: () => { lists = [radarr]; return { status: 201, body: radarr }; } },
    { method: "POST", path: "/lists/3/preview", reply: { add_title: 12, add_library: 0, present: 5, excluded: 1, skipped: 1,
      titles: [...Array.from({ length: 12 }, (_, i) => ({ name: `Movie ${i}`, tmdb_id: 100 + i, change: "add_title" })),
        { name: "Gamma", year: 2021, tmdb_id: 13, change: "skipped" }] } },
  ]);
  renderPage(<ListsPage />, "/collections/lists");
  await user.click(await screen.findByRole("button", { name: "Add list" }));
  let dialog = within(screen.getByTestId("list-dialog"));
  await user.selectOptions(dialog.getByLabelText("Source"), "radarr");
  expect(dialog.queryByLabelText("TMDb list id or URL")).toBeNull();
  expect(dialog.getByTestId("list-arr-note").textContent).toContain("Theatarr only reads this instance.");
  expect(dialog.getByRole("button", { name: "Check" }).hasAttribute("disabled")).toBe(true);
  await user.type(dialog.getByLabelText("Address"), "http://10.0.0.5:7878");
  await user.type(dialog.getByLabelText("API key"), "secret");
  await user.click(dialog.getByRole("button", { name: "Check" }));
  expect((await dialog.findByTestId("list-checked")).textContent).toBe("Radarr 5.2.0: 1,234 movies.");
  expect(fetch.calls("POST", "/lists/check")[0].body).toEqual({ list_type: "radarr", url: "http://10.0.0.5:7878", api_key: "secret" });
  await user.click(dialog.getByRole("button", { name: "4K Remux" }));
  await user.click(dialog.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(fetch.calls("POST", "/lists")).toHaveLength(1));
  expect(fetch.calls("POST", "/lists")[0].body).toEqual({ list_type: "radarr", name: "Radarr", versions: ["movie-remux"], monitor: "movieOnly",
    search_on_add: true, minimum_availability: "released", enabled: true, url: "http://10.0.0.5:7878", api_key: "secret" });

  const row = within(await screen.findByTestId("list-3"));
  expect(row.getByText("Radarr at http://10.0.0.5:7878")).toBeTruthy();
  await user.click(row.getByRole("button", { name: "Preview" }));
  expect((await row.findByTestId("list-preview-3")).textContent).toContain(
    "Sync now would add 12 titles (12 new, 0 to libraries they are not in yet); 5 are already there, 1 excluded, 1 skipped (they fit none of the list's libraries).");
  expect(row.getByText("Movie 0")).toBeTruthy();
  expect(row.queryByText("Movie 11")).toBeNull();
  // A skipped title is named even when it comes after the first titles to add.
  expect(within(row.getByTestId("list-skipped-3")).getByText("Gamma (2021)")).toBeTruthy();

  // On edit the key field is empty; the saved key is kept unless a new one is typed.
  await user.click(row.getByRole("button", { name: "Edit" }));
  dialog = within(screen.getByTestId("list-dialog"));
  const key = dialog.getByLabelText("API key") as HTMLInputElement;
  expect(key.value).toBe("");
  expect(key.placeholder).toBe("Saved. Type a new key to replace it.");
  expect((dialog.getByLabelText("Address") as HTMLInputElement).value).toBe("http://10.0.0.5:7878");
  // The same address spelled differently is no new address: Save and Check need no key.
  await user.type(dialog.getByLabelText("Address"), "/");
  expect(dialog.getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(false);
  expect(dialog.getByRole("button", { name: "Check" }).hasAttribute("disabled")).toBe(false);
  await user.clear(dialog.getByLabelText("Address"));
  await user.type(dialog.getByLabelText("Address"), "http://10.0.0.9:7878");
  expect(dialog.getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(true);
});

it("labels a Sonarr list's series libraries by their names", async () => {
  const user = userEvent.setup();
  const sonarr: Schemas["MovieList"] = { ...mine, id: 4, name: "Sonarr", list_type: "sonarr", kind: "series", tmdb_id: 0, monitor: "all",
    url: "http://10.0.0.7:8989", api_key_set: true, versions: ["tv-2160p"], libraries: ["tv-2160p", "anime-tv"] };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/lists", reply: { lists: [sonarr], libraries, series_libraries: { "tv-2160p": "TV 4K", "anime-tv": "Anime TV" }, can_sync: true } },
  ]);
  renderPage(<ListsPage />, "/collections/lists");
  const row = within(await screen.findByTestId("list-4"));
  expect(row.getByText("TV 4K")).toBeTruthy();
  expect(row.getByText("Sonarr at http://10.0.0.7:8989")).toBeTruthy();
  await user.click(row.getByRole("button", { name: "Edit" }));
  const dialog = within(screen.getByTestId("list-dialog"));
  expect(dialog.getByRole("button", { name: "TV 4K" }).getAttribute("aria-pressed")).toBe("true");
  expect(dialog.getByRole("button", { name: "Anime TV" }).getAttribute("aria-pressed")).toBe("false");
});

it("says next to Check why it is off when TMDb is not set up", async () => {
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: manager },
    { method: "GET", path: "/lists", reply: { lists: [], libraries, can_sync: false } },
  ]);
  renderPage(<ListsPage />, "/collections/lists");
  await user.click(await screen.findByRole("button", { name: "Add list" }));
  const dialog = within(screen.getByTestId("list-dialog"));
  await user.selectOptions(dialog.getByRole("combobox", { name: "Source" }), "radarr");
  expect(dialog.getByRole("button", { name: "Check" }).hasAttribute("disabled")).toBe(true);
  expect(dialog.getByTestId("list-check-off").textContent).toBe("TMDb is not set up, so the instance cannot be checked or synced yet.");
});
