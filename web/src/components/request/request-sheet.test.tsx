// @vitest-environment jsdom
// The request sheet's add options follow the title's stored anime flag, as the request it creates does: it never
// sends anime=0 (only the Add sheet's own Anime choice does). A new series' own seasons show without episode counts.
// Invented keys and names.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { RequestSheet } from "./request-sheet";

const options = { kind: "movie", anime: true, title_id: 5, in_library: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions: [] };

it("asks for a removed title's options without overriding its stored anime flag", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: options },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
  ]);
  renderPage(<RequestSheet target={{ kind: "movie", name: "Alpha", title_id: 5, anime: false }} onClose={() => {}} />);
  await waitFor(() => expect(fetch.calls("GET", "/add/options")).toHaveLength(1));
  expect(fetch.calls("GET", "/add/options")[0].search).not.toContain("anime=");
});

it("shows In library only while the title has a current copy, not for one whose every copy was removed", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: options },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
  ]);
  const { unmount } = renderPage(<RequestSheet target={{ kind: "movie", name: "Alpha", title_id: 5 }} onClose={() => {}} />);
  await screen.findByTestId("request-sheet");
  await waitFor(() => expect(screen.queryByTestId("request-submit") ?? screen.queryByText(/Available|View/)).not.toBeNull());
  expect(screen.queryByText("In library")).toBeNull();
  unmount();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: { ...options, in_library: true } },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
  ]);
  renderPage(<RequestSheet target={{ kind: "movie", name: "Alpha", title_id: 5 }} onClose={() => {}} />);
  expect(await screen.findByText("In library")).toBeTruthy();
});

it("asks for a whole removed series whose seasons could not be read, by its title id", async () => {
  const series = { ...options, kind: "series", anime: false, seasons: [],
    versions: [{ key: "tv-web", label: "TV", kind: "series", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: { paused: false, pause_forced: false, library_writes: true, will_grab: true } }] };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: series },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
  ]);
  renderPage(<RequestSheet target={{ kind: "series", name: "Kappa", title_id: 5 }} onClose={() => {}} />);
  const submit = await screen.findByTestId("request-submit");
  await waitFor(() => expect(submit.textContent).toBe("Request all seasons"));
  expect(screen.queryByText("In library")).toBeNull();
});

it("shows a new series' own seasons without episode counts and asks for the picked ones", async () => {
  const series = { ...options, kind: "series", anime: false, title_id: undefined, seasons: [{ season: 0 }, { season: 1 }, { season: 2 }],
    versions: [{ key: "tv-web", label: "TV", kind: "series", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: { paused: false, pause_forced: false, library_writes: true, will_grab: true } }] };
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: series },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
    { method: "POST", path: "/requests", reply: json({ approved: false, merged: false, request: { id: 4, name: "Kappa", status: "pending", versions: [{ key: "tv-web", label: "TV" }], seasons: [1] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<RequestSheet target={{ kind: "series", name: "Kappa", tmdb_id: 8 }} onClose={() => {}} />);
  const list = await screen.findByTestId("request-seasons");
  expect(within(list).getAllByRole("checkbox")).toHaveLength(2);
  expect(list.textContent).not.toMatch(/ep\./);
  expect(screen.getByTestId("request-submit").textContent).toBe("Request");
  await user.click(within(list).getAllByRole("checkbox")[1]);
  await user.click(screen.getByTestId("request-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/requests")).toHaveLength(1));
  expect(fetch.calls("POST", "/requests")[0].body).toMatchObject({ kind: "series", tmdb_id: 8, seasons: [1] });
});

it("says Request all seasons for a new series whose seasons could not be read, and asks for the whole series", async () => {
  const series = { ...options, kind: "series", anime: false, title_id: undefined, seasons: [],
    versions: [{ key: "tv-web", label: "TV", kind: "series", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: { paused: false, pause_forced: false, library_writes: true, will_grab: true } }] };
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: series },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
    { method: "POST", path: "/requests", reply: json({ approved: false, merged: false, request: { id: 4, name: "Kappa", status: "pending", versions: [{ key: "tv-web", label: "TV" }], seasons: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<RequestSheet target={{ kind: "series", name: "Kappa", tmdb_id: 8 }} onClose={() => {}} />);
  const submit = await screen.findByTestId("request-submit");
  await waitFor(() => expect(submit.textContent).toBe("Request all seasons"));
  expect(screen.queryByTestId("request-seasons")).toBeNull();
  await user.click(submit);
  await waitFor(() => expect(fetch.calls("POST", "/requests")).toHaveLength(1));
  expect("seasons" in (fetch.calls("POST", "/requests")[0].body as object)).toBe(false);
});

it("shows the libraries in sync with the asked-for one, which the request stores too", async () => {
  const gs = { paused: false, pause_forced: false, library_writes: true, will_grab: true };
  const movie = { ...options, anime: false, title_id: undefined, versions: [
    { key: "movie-remux", label: "4K Remux", kind: "movie", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: gs, synced: ["movie-web"] },
    { key: "movie-web", label: "4K WEB", kind: "movie", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: gs },
  ] };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"], request_versions: ["movie-remux"] } },
    { method: "GET", path: "/add/options", reply: movie },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
  ]);
  renderPage(<RequestSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 8 }} onClose={() => {}} />);
  const list = await screen.findByTestId("request-versions");
  await waitFor(() => expect(list.querySelector('[data-vkey="movie-web"]')).not.toBeNull());
  expect(list.querySelector('[data-vkey="movie-remux"]')?.textContent).toBe("4K Remux");
  expect(list.querySelector('[data-vkey="movie-web"]')?.textContent).toBe("4K WEBin sync with 4K Remux");
});

it("never names the environment to a household account requesting under a forced Pause", async () => {
  const locked = { paused: true, pause_forced: true, will_grab: false };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["browse", "request"] } },
    { method: "GET", path: "/add/options", reply: { ...options, router_enabled: false, can_search: false,
      versions: [{ key: "movie-web", label: "4K", kind: "movie", enable: "", fits: true, default: true, on: false, searchable: false, grab_state: locked }] } },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
  ]);
  renderPage(<RequestSheet target={{ kind: "movie", name: "Alpha", title_id: 5 }} onClose={() => {}} />);
  await screen.findByTestId("request-sheet");
  await waitFor(() => expect(screen.getByTestId("request-sheet").textContent).toContain("4K"));
  expect(document.body.textContent).not.toContain("THEATARR_FORCE_OFF");
});

// T6: the household words name no role, a season already in the library reads "Already added", and a Pause (ordinary
// or locked) is said before sending.
it("says what happens in plain words: approval with no role, Already added, and Pause when downloads are paused", async () => {
  const gs = (paused: boolean, forced = false) => ({ paused, pause_forced: forced, library_writes: true, will_grab: !paused });
  const series = (grab: object) => ({ ...options, kind: "series", anime: false, in_library: true, seasons: [{ season: 1, episodes: 8 }, { season: 2, episodes: 8 }],
    versions: [{ key: "tv-web", label: "TV", kind: "series", enable: "", fits: true, default: true, on: true, open_seasons: [2], searchable: true, grab_state: grab }] });
  for (const [grab, paused] of [[gs(false), false], [gs(true), true], [gs(true, true), true]] as const) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["browse", "request"] } },
      { method: "GET", path: "/add/options", reply: series(grab) },
      { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
      { method: "POST", path: "/requests", reply: json({ approved: false, merged: false, request: { id: 4, name: "Kappa", status: "pending", versions: [{ key: "tv-web", label: "TV" }], seasons: [2] } }) },
    ]);
    const user = userEvent.setup();
    const { unmount } = renderPage(<RequestSheet target={{ kind: "series", name: "Kappa", title_id: 5 }} onClose={() => {}} />);
    expect((await screen.findByTestId("request-approval-notice")).textContent).toBe("Your request needs approval.");
    const list = screen.getByTestId("request-seasons");
    expect(list.querySelector('[data-season="1"]')?.textContent).toBe("Season 1Already added");
    const pause = screen.queryByTestId("request-paused");
    expect(pause?.textContent ?? null).toBe(paused ? "Downloads are paused right now. Your request is saved and starts when they are turned back on." : null);
    await user.click(screen.getByTestId("request-submit"));
    expect((await screen.findByText(/^Sent\./)).textContent).toBe("Sent. It is waiting for approval. You will see it change here and on your requests page.");
    unmount();
  }
});

it("counts the seasons to request in the footer with the noun in the number it needs: 1 of 1 season", async () => {
  const gs = { paused: false, pause_forced: false, library_writes: true, will_grab: true };
  const series = (open: number[]) => ({ ...options, kind: "series", anime: false, in_library: true, seasons: [{ season: 1, episodes: 8 }, { season: 2, episodes: 8 }, { season: 3, episodes: 8 }],
    versions: [{ key: "tv-web", label: "TV", kind: "series", enable: "", fits: true, default: true, on: true, open_seasons: open, searchable: true, grab_state: gs }] });
  for (const [open, words] of [[[3], "1 of 1 season"], [[2, 3], "2 of 2 seasons"]] as const) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["browse", "request"] } },
      { method: "GET", path: "/add/options", reply: series([...open]) },
      { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
    ]);
    const { unmount } = renderPage(<RequestSheet target={{ kind: "series", name: "Kappa", title_id: 5 }} onClose={() => {}} />);
    await screen.findByTestId("request-seasons");
    await waitFor(() => expect(screen.getByTestId("request-season-count").textContent).toBe(words));
    unmount();
  }
});
