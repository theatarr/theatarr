// @vitest-environment jsdom
// The releases header says why nothing is searched in the shared verdict words (lib/version-grab.ts). Invented keys.
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
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
