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
  renderPage(<SearchProvider><TitleToolbar titleId={7} name="Alpha" kind="movie" versions={[{ id: 1, key: "movie-web", label: "WEB", control: control(true) }] as never} /></SearchProvider>);
  return api;
};

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
