// @vitest-environment jsdom
// The releases header says why nothing is searched in the shared verdict words (lib/version-grab.ts), and offers
// Interactive search to managers when a library searches live. Invented keys.
import { cleanup, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { SearchProvider } from "@/components/search/search-context";
import { mockFetch, renderPage } from "@/test/page";
import { TitleAddActions } from "./title-add-actions";

const gates = { mode: "active", paused: false, pause_forced: false, will_grab: true };
const lib = (key: string, grab_state: object) => ({ key, label: key.toUpperCase(), kind: "movie", mode: "active", enable: "", fits: true, default: false, on: true, searchable: false, grab_state });
const show = (versions: object[], router_enabled = true) => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage", "add"] } },
    { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 7, router_enabled, episodes_known: false, can_search: false, seasons: [], versions } },
  ]);
  renderPage(<SearchProvider><TitleAddActions titleId={7} name="Alpha" kind="movie" /></SearchProvider>);
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
const headerFor = (permissions: string[], live: boolean, kind: "movie" | "series" = "movie") => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions } },
    { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 7, router_enabled: true, episodes_known: false, can_search: false, seasons: [], versions: [] } },
    { method: "GET", path: "/titles/7", reply: { id: 7, name: "Alpha", kind, versions: [{ id: 1, key: "movie-web", label: "WEB", control: control(live) }] } },
    { method: "GET", path: "/titles/7/removal", reply: { title_id: 7, name: "Alpha", title: true, leftover: [], copies: [{ key: "movie-web", label: "WEB", links: 0, downloads: 0 }] } },
  ]);
  renderPage(<SearchProvider><TitleAddActions titleId={7} name="Alpha" kind={kind} /></SearchProvider>);
  return api;
};

it("offers Interactive search of a movie to a manager when a library of it is Live", async () => {
  headerFor(["manage", "add"], true);
  expect(await screen.findByTestId("interactive-search-open")).toBeTruthy();
  expect(screen.queryByTestId("title-no-interactive")).toBeNull();
});

it("says why a movie whose libraries are all Watch only has no Interactive search", async () => {
  headerFor(["manage", "add"], false);
  expect((await screen.findByTestId("title-no-interactive")).textContent).toBe("Interactive search needs a Live library. This film's libraries are Watch only.");
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
});

it("offers no whole-title Interactive search for a series, a title only in Watch-only libraries, or a non-manager", async () => {
  const api = headerFor(["manage", "add"], false);
  await waitFor(() => expect(api.calls("GET", "/titles/7")).toHaveLength(1));
  await screen.findByTestId("title-add-versions");
  await new Promise((r) => setTimeout(r, 20));
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
  cleanup();
  vi.unstubAllGlobals();
  const other = headerFor(["add"], true);
  await screen.findByTestId("title-add-versions");
  await waitFor(() => expect(other.calls("GET", "/auth/me").length).toBeGreaterThan(0));
  await new Promise((r) => setTimeout(r, 20));
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
  expect(other.calls("GET", "/titles/7")).toHaveLength(0);
  cleanup();
  vi.unstubAllGlobals();
  // A series has Interactive search per season and per episode only (Sonarr's rule).
  const series = headerFor(["manage", "add"], true, "series");
  await screen.findByTestId("title-add-versions");
  await waitFor(() => expect(series.calls("GET", "/auth/me").length).toBeGreaterThan(0));
  await new Promise((r) => setTimeout(r, 20));
  expect(screen.queryByTestId("interactive-search-open")).toBeNull();
});
