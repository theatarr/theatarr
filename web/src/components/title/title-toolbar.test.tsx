// @vitest-environment jsdom
// The action bar: why nothing is searched in the shared verdict words (lib/version-grab.ts), Interactive search for
// managers when a library searches live, and the rename preview. Invented keys.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { SearchProvider } from "@/components/search/search-context";
import { mockFetch, renderPage } from "@/test/page";
import { TitleToolbar } from "./title-toolbar";

const gates = { mode: "active", paused: false, pause_forced: false, will_grab: true };
const lib = (key: string, grab_state: object) => ({ key, label: key.toUpperCase(), kind: "movie", mode: "active", enable: "", fits: true, default: false, on: true, searchable: false, grab_state });
const options = (versions: object[]) => ({ method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 7, router_enabled: true, episodes_known: false, can_search: false, seasons: [], versions } });
const show = (versions: object[]) => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage", "add"] } }, options(versions)]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[]} page="releases" /></SearchProvider>);
};

it("says Downloads off for a Live library the server holds, and Watch only for a Watch-only one", async () => {
  show([lib("movie-web", { ...gates, will_grab: false }), lib("movie-1080", { ...gates, mode: "shadow", will_grab: false })]);
  const note = await screen.findByTestId("title-no-search");
  expect(note.textContent).toBe("Nothing is searched now: MOVIE-WEB: Downloads off · MOVIE-1080: Watch only");
});

it("says the global state once when Pause is forced", async () => {
  show([lib("movie-web", { ...gates, paused: true, pause_forced: true, will_grab: false })]);
  const note = await screen.findByTestId("title-no-search");
  expect(screen.getAllByTestId("downloads-off-notice")).toHaveLength(1);
  expect(note.textContent).toContain("Paused by the environment (THEATARR_FORCE_OFF)");
});

const control = (live: boolean) => ({ mode: live ? "active" : "shadow", can_edit: true, can_search: live, live_releases: live });
const bar = (permissions: string[], live: boolean, kind: "movie" | "series" = "movie") => {
  const api = mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions } }, options([])]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind={kind} versions={[{ id: 1, key: "movie-web", label: "WEB", control: control(live) }] as never} /></SearchProvider>);
  return api;
};

it("offers Interactive search of a movie to a manager when a library of it is Live", async () => {
  bar(["manage", "add"], true);
  expect(await screen.findByTestId("interactive-search-open")).toBeTruthy();
  expect(screen.queryByTestId("title-no-interactive")).toBeNull();
});

it("says why a movie whose libraries are all Watch only has no Interactive search", async () => {
  bar(["manage", "add"], false);
  expect((await screen.findByTestId("title-no-interactive")).textContent).toBe("Interactive search needs a Live library. This film's libraries are Watch only.");
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
});

it("offers no whole-title Interactive search for a series or a non-manager", async () => {
  const a = bar(["add"], true);
  await waitFor(() => expect(a.calls("GET", "/auth/me").length).toBeGreaterThan(0));
  await new Promise((r) => setTimeout(r, 20));
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
  // A series has Interactive search per season and per episode only (Sonarr's rule).
  bar(["manage", "add"], true, "series");
  expect(await screen.findAllByTestId("title-refresh")).toBeTruthy();
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
});

const renameBar = (preview: object) => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage", "see_files"] } },
    options([]),
    { method: "GET", path: "/titles/7/rename", reply: preview },
    { method: "POST", path: "/titles/7/rename", reply: preview },
  ]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[{ id: 1, key: "movie-web", label: "WEB", has_file: true, control: control(true) }] as never} /></SearchProvider>);
  return api;
};

it("offers Rename files only with a file to rename", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage", "see_files"] } }, options([])]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[{ id: 1, key: "movie-web", label: "WEB", has_file: false, control: control(true) }] as never} /></SearchProvider>);
  expect(await screen.findByTestId("title-refresh")).toBeTruthy();
  expect(screen.queryByTestId("title-rename")).toBeNull();
});

it("shows old and new names first and renames only on confirm", async () => {
  const api = renameBar({ files: [{ link_id: 4, version: "movie-web", existing_path: "/media/movies/Alpha/alpha.mkv", new_path: "/media/movies/Alpha (2020)/Alpha (2020) WEBDL-2160p.mkv" }], skipped: 1 });
  const user = userEvent.setup();
  await user.click(await screen.findByTestId("title-rename"));
  const dialog = await screen.findByTestId("rename-dialog");
  const file = await within(dialog).findByTestId("rename-file");
  expect(file.textContent).toBe("WEBalpha.mkv→Alpha (2020) WEBDL-2160p.mkv");
  expect(within(dialog).getByTestId("rename-skipped").textContent).toBe("1 file could not be checked.");
  expect(within(dialog).queryByTestId("rename-none")).toBeNull();
  expect(api.calls("POST", "/titles/7/rename")).toHaveLength(0);
  await user.click(within(dialog).getByTestId("rename-confirm"));
  expect((await within(dialog).findByTestId("rename-done")).textContent).toBe("Renamed 1 file.");
  expect(api.calls("POST", "/titles/7/rename")).toHaveLength(1);
});

it("says the names match only when nothing would change and nothing was skipped", async () => {
  renameBar({ files: [], skipped: 0 });
  const user = userEvent.setup();
  await user.click(await screen.findByTestId("title-rename"));
  expect((await screen.findByTestId("rename-none")).textContent).toBe("Every file already has the name the naming settings give it.");
  expect(screen.queryByTestId("rename-confirm")).toBeNull();
});

it("does not claim the names match when files could not be checked", async () => {
  renameBar({ files: [], skipped: 2 });
  const user = userEvent.setup();
  await user.click(await screen.findByTestId("title-rename"));
  expect((await screen.findByTestId("rename-skipped")).textContent).toBe("2 files could not be checked.");
  expect(screen.queryByTestId("rename-none")).toBeNull();
});

it("shows the changed folders when a rename keeps the file name", async () => {
  renameBar({ files: [{ link_id: 5, version: "movie-web", existing_path: "/media/movies/Alpha/Alpha.mkv", new_path: "/media/movies/Alpha (2020)/Alpha.mkv" }], skipped: 0 });
  const user = userEvent.setup();
  await user.click(await screen.findByTestId("title-rename"));
  expect((await screen.findByTestId("rename-file")).textContent).toBe("WEBAlpha/Alpha.mkv→Alpha (2020)/Alpha.mkv");
});

it("says a series' Search now also looks for upgrades", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage"] } }, options([{ ...lib("tv-web", gates), searchable: true }])]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="series" versions={[]} /></SearchProvider>);
  expect((await screen.findByTestId("title-search-now")).getAttribute("title")).toBe("Search every monitored episode now, in every Live library (missing episodes and upgrades)");
});


it("links an admin from the Watch-only notice to the library's settings; others get the sentence only", async () => {
  bar(["admin", "manage", "add"], false);
  const note = await screen.findByTestId("title-no-interactive");
  expect(note.textContent).toBe("Interactive search needs a Live library. This film's libraries are Watch only. Library settings");
  expect(within(note).getByRole("link", { name: "Library settings" }).getAttribute("href")).toBe("/admin/settings/libraries?edit=movie-web");
  document.body.innerHTML = "";
  bar(["manage", "add"], false);
  expect((await screen.findByTestId("title-no-interactive")).textContent).toBe("Interactive search needs a Live library. This film's libraries are Watch only.");
  expect(screen.queryByRole("link", { name: "Library settings" })).toBeNull();
});

it("shows Search now disabled with the reason when nothing would search, and none to a non-manager", async () => {
  const cases: [object[], boolean, string][] = [
    [[lib("movie-web", { ...gates, paused: true, will_grab: false })], true, "Paused: nothing is searched, downloaded or linked for any library."],
    [[lib("movie-web", { ...gates, mode: "shadow", will_grab: false })], false, "No library of this title is Live."],
    [[{ ...lib("movie-web", gates), on: false }], true, "No library is monitored."],
    [[lib("movie-web", { ...gates, will_grab: false })], true, "Nothing is searched now: MOVIE-WEB: Downloads off"],
  ];
  for (const [versions, live, reason] of cases) {
    document.body.innerHTML = "";
    mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage"] } }, options(versions)]);
    renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[{ id: 1, key: "movie-web", label: "WEB", control: control(live) }] as never} /></SearchProvider>);
    const button = await screen.findByTestId("title-search-now");
    expect(button.hasAttribute("disabled")).toBe(true);
    expect(button.getAttribute("title")).toBe(reason);
  }
  document.body.innerHTML = "";
  const a = mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["add"] } }, options([lib("movie-web", gates)])]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[]} /></SearchProvider>);
  await waitFor(() => expect(a.calls("GET", "/add/options").length).toBeGreaterThan(0));
  await new Promise((r) => setTimeout(r, 20));
  expect(screen.queryByTestId("title-search-now")).toBeNull();
});

it("offers no Add to another library when every library that fits is on the title; one that does not fit does not count", async () => {
  const a = mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage", "add"] } }, options([lib("movie-web", gates), lib("movie-remux", gates), { ...lib("anime-movie", gates), fits: false, on: false }])]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[]} page="releases" /></SearchProvider>);
  await screen.findByTestId("title-search-now");
  await waitFor(() => expect(a.calls("GET", "/add/options").length).toBeGreaterThan(0));
  await new Promise((r) => setTimeout(r, 20));
  expect(screen.queryByTestId("title-add-versions")).toBeNull();
  document.body.innerHTML = "";
  show([lib("movie-web", gates), { ...lib("movie-2160", gates), on: false }]);
  const open = await screen.findByTestId("title-add-versions");
  expect(open.textContent?.trim()).toBe("Add to another library");
  expect(open.hasAttribute("disabled")).toBe(false);
});

it("shows Search now disabled while the add options load, and no Interactive search on the Last search page", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage", "add"] } },
    { method: "GET", path: "/add/options", reply: () => new Promise(() => {}) },
  ]);
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[{ id: 1, key: "movie-web", label: "WEB", control: control(true) }] as never} page="releases" /></SearchProvider>);
  const search = await screen.findByTestId("title-search-now");
  expect(search.hasAttribute("disabled")).toBe(true);
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
});
