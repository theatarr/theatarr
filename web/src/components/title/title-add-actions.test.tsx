// @vitest-environment jsdom
// The releases header says why nothing is searched in the shared verdict words (lib/version-grab.ts), and offers
// Interactive search to managers when a library searches live. Invented keys.
import { cleanup, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { SearchProvider } from "@/components/search/search-context";
import { mockFetch, renderPage } from "@/test/page";
import { TitleAddActions } from "./title-add-actions";

const gates = { mode: "active", grab: true, router_enabled: true, router_forced_off: false, will_grab: true };
const lib = (key: string, grab_state: object) => ({ key, label: key.toUpperCase(), kind: "movie", mode: "active", enable: "", fits: true, default: false, on: true, searchable: false, grab_state });
const show = (versions: object[], router_enabled = true) => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "manage", "add"] } },
    { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 7, router_enabled, episodes_known: false, can_search: false, seasons: [], versions } },
  ]);
  renderPage(<SearchProvider><TitleAddActions titleId={7} name="Alpha" kind="movie" /></SearchProvider>);
};

it("says Downloads off, not Preview only, for an active library with its own switch off", async () => {
  show([lib("movie-web", { ...gates, grab: false, will_grab: false }), lib("movie-1080", { ...gates, mode: "shadow", will_grab: false })]);
  const note = await screen.findByTestId("title-no-search");
  expect(note.textContent).toBe("Nothing is searched now: MOVIE-WEB: Downloads off · MOVIE-1080: Preview only");
});

it("says the global state once when new downloads are locked off", async () => {
  show([lib("movie-web", { ...gates, router_forced_off: true, will_grab: false })]);
  const note = await screen.findByTestId("title-no-search");
  expect(screen.getAllByTestId("downloads-off-notice")).toHaveLength(1);
  expect(note.textContent).toContain("locked off by the environment");
});

const control = (live: boolean) => ({ mode: live ? "active" : "shadow", can_edit: true, can_search: live, live_releases: live });
const headerFor = (permissions: string[], live: boolean) => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions } },
    { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 7, router_enabled: true, episodes_known: false, can_search: false, seasons: [], versions: [] } },
    { method: "GET", path: "/titles/7", reply: { id: 7, name: "Alpha", kind: "movie", versions: [{ id: 1, key: "movie-web", label: "WEB", control: control(live) }] } },
  ]);
  renderPage(<SearchProvider><TitleAddActions titleId={7} name="Alpha" kind="movie" /></SearchProvider>);
  return api;
};

it("offers Interactive search to a manager when a library of the title is Live", async () => {
  headerFor(["manage", "add"], true);
  expect(await screen.findByTestId("interactive-search-open")).toBeTruthy();
});

it("offers no Interactive search for a title only in Watch-only libraries, nor to a non-manager", async () => {
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
});
