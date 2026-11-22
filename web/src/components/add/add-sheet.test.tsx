// @vitest-environment jsdom
// The add drawer's library rows (audit 2): one checkbox per library, identity and state on their own rows, the
// library state from grab_state, the global notice said once, and "N libraries" from the ticked rows.
// Invented keys and names.
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { Toaster } from "@/components/toaster";
import { AddSheet } from "./add-sheet";

afterEach(() => vi.restoreAllMocks());

const gates = { paused: false, pause_forced: false, library_writes: true };
const long = "A library name that is long enough to wrap at four hundred p";
const versions = [
  { key: "movie-web", label: long, grab_state: { ...gates, will_grab: true } },
  { key: "movie-remux", label: "Remux", grab_state: { ...gates, will_grab: false } },
  { key: "movie-1080", label: "1080p", grab_state: { ...gates, will_grab: false, library_writes: false } },
  { key: "movie-4k", label: "4K", grab_state: { ...gates, will_grab: true } },
  { key: "movie-hdr", label: "HDR", grab_state: { ...gates, will_grab: true, library_writes: false } },
  { key: "movie-dv", label: "Dolby Vision", grab_state: { ...gates, will_grab: false, library_writes: false } },
  { key: "movie-sd", label: "SD", grab_state: { ...gates, will_grab: true } },
].map((v) => ({ ...v, kind: "movie", enable: "", fits: true, default: v.key !== "movie-sd", on: false, searchable: v.grab_state.will_grab }));
const options = { kind: "movie", anime: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions };

it("lists every library with its state, counts the ticked ones and sends the same add", async () => {
  expect(long).toHaveLength(60);
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: options },
    { method: "POST", path: "/titles", reply: json({ created: true, new: ["movie-web"], title: { id: 9, name: "Alpha", versions: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  const list = await screen.findByTestId("add-versions");
  expect(within(list).getAllByRole("checkbox")).toHaveLength(7);
  const web = screen.getByTestId("add-version-movie-web");
  expect(within(web).getByText(long)).toBeTruthy();
  // The key is no text of its own: an admin finds it as the name's tooltip.
  expect(within(web).queryByText("movie-web")).toBeNull();
  expect(within(web).getByText(long).getAttribute("title")).toBe("movie-web");
  // Downloads on is not said on every row: the state shows only when it says something.
  expect(within(web).queryByText("Downloads on")).toBeNull();
  expect(within(screen.getByTestId("add-version-movie-remux")).getByText("Downloads off")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-1080")).getByText("Downloads off")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-hdr")).getByTestId("links-off")).toBeTruthy();
  // The checkbox is named by the library and described by its state.
  const box = within(web).getByRole("checkbox", { name: new RegExp(long) });
  expect(document.getElementById(box.getAttribute("aria-describedby") ?? "")?.textContent).toBe("");
  expect(document.getElementById(within(screen.getByTestId("add-version-movie-remux")).getByRole("checkbox").getAttribute("aria-describedby") ?? "")?.textContent).toContain("Downloads off");
  // Six ticked by default.
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries");
  await user.click(within(screen.getByTestId("add-version-movie-sd")).getByRole("checkbox"));
  expect(screen.getByTestId("add-count").textContent).toBe("7 libraries");
  await user.click(box);
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries");
  expect(screen.queryByTestId("downloads-off-notice")).toBeNull();
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  expect(fetch.calls("POST", "/titles")[0].body).toEqual({ kind: "movie", tmdb_id: 7, title: "Alpha", year: 2024, anime: false, versions: ["movie-remux", "movie-1080", "movie-4k", "movie-hdr", "movie-dv", "movie-sd"], search: false });
});

it("offers Add and Add and search, which send search off and on, and no Search now switch", async () => {
  for (const [name, search] of [["Add", false], ["Add and search", true]] as const) {
    const fetch = mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
      { method: "GET", path: "/add/options", reply: options },
      { method: "POST", path: "/titles", reply: json({ created: true, new: ["movie-web"], title: { id: 9, name: "Alpha", versions: [] } }) },
    ]);
    const user = userEvent.setup();
    renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
    await screen.findByTestId("add-versions");
    expect(screen.getByTestId("add-submit").textContent).toBe("Add");
    expect(screen.getByTestId("add-submit-search").textContent).toBe("Add and search");
    expect(screen.queryByRole("switch", { name: /Search now/ })).toBeNull();
    await user.click(screen.getByRole("button", { name }));
    await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
    expect((fetch.calls("POST", "/titles")[0].body as { search: boolean }).search).toBe(search);
    cleanup();
  }
});

it("says Pause once, not on every row", async () => {
  const off = versions.map((v) => ({ ...v, searchable: false, grab_state: { ...v.grab_state, paused: true, will_grab: false } }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, router_enabled: false, versions: off } },
  ]);
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  expect((await screen.findAllByTestId("downloads-off-notice"))).toHaveLength(1);
  expect(screen.queryByTestId("add-no-search")).toBeNull();
  expect(within(screen.getByTestId("add-version-movie-web")).getByText("Paused")).toBeTruthy();
  // Nothing can search, so one Add.
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries");
  expect(screen.queryByTestId("add-submit-search")).toBeNull();
  // Not an admin: no settings link.
  expect(within(screen.getByTestId("downloads-off-notice")).queryByRole("link")).toBeNull();
});

const tv = { key: "tv-web", label: "TV", kind: "series", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: { ...gates, will_grab: true } };
// A new series whose own seasons could not be read: no seasons (the usual offline case).
const seriesOptions = { kind: "series", anime: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions: [tv] };
const posted = { created: true, new: ["tv-web"], title: { id: 9, name: "Kappa", versions: [] } };
const routes = (opts: unknown) => [
  { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
  { method: "GET", path: "/add/options", reply: opts },
  { method: "POST", path: "/titles", reply: json(posted) },
];
const bodyOf = async (fetch: ReturnType<typeof mockFetch>) => {
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  return fetch.calls("POST", "/titles")[0].body;
};

it("offers one Add and the explanation when no ticked library can search, ", async () => {
  const watch = versions.map((v) => ({ ...v, searchable: false, grab_state: { ...v.grab_state, will_grab: false } }));
  const fetch = mockFetch(routes({ ...options, versions: watch }));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  expect(await screen.findByTestId("add-no-search")).toBeTruthy();
  expect(screen.queryByTestId("add-submit-search")).toBeNull();
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries");
  await user.click(screen.getByTestId("add-submit"));
  expect(((await bodyOf(fetch)) as { search: boolean }).search).toBe(false);
});

it("offers only Add for a film ticked in a library whose anime rule it does not fit, and says why", async () => {
  const animeLib = { key: "anime-movie", label: "Anime movies", kind: "movie", enable: "", fits: false, default: false, on: false, anime: true, searchable: true, grab_state: { ...gates, will_grab: true } };
  const fetch = mockFetch(routes({ ...options, versions: [{ ...versions[0], default: false }, animeLib] }));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  await user.click(await screen.findByRole("button", { name: /Show other libraries/ }));
  await user.click(within(screen.getByTestId("add-version-anime-movie")).getByRole("checkbox"));
  expect(screen.queryByTestId("add-submit-search")).toBeNull();
  expect(screen.getByTestId("add-no-search").textContent).toBe("Adding searches nothing: the ticked library does not search this title (see the note under it).");
  await user.click(screen.getByTestId("add-submit"));
  expect(((await bodyOf(fetch)) as { search: boolean }).search).toBe(false);
});

it("adds a new movie unmonitored with the Monitored switch off: only Add, and the switch's line says why", async () => {
  const fetch = mockFetch(routes(options));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  const sw = await screen.findByRole("switch", { name: "Monitored" });
  expect(sw.getAttribute("aria-checked")).toBe("true");
  expect(screen.getByTestId("add-submit-search")).toBeTruthy();
  expect(screen.queryByText(/won't search for or download/)).toBeNull();
  await user.click(sw);
  expect(screen.getByText("Theatarr won't search for or download this movie automatically until you monitor it.")).toBeTruthy();
  expect(screen.queryByTestId("add-submit-search")).toBeNull();
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ kind: "movie", tmdb_id: 7, title: "Alpha", year: 2024, anime: false, versions: ["movie-web", "movie-remux", "movie-1080", "movie-4k", "movie-hdr", "movie-dv"], search: false, monitored: false });
  cleanup();

  // No library can search: off, the switch's line replaces the no-search paragraph.
  const watch = versions.map((v) => ({ ...v, searchable: false, grab_state: { ...v.grab_state, will_grab: false } }));
  mockFetch(routes({ ...options, versions: watch }));
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  expect(await screen.findByTestId("add-no-search")).toBeTruthy();
  await user.click(screen.getByRole("switch", { name: "Monitored" }));
  expect(screen.queryByTestId("add-no-search")).toBeNull();
});

it("offers no Monitored switch for a series or for a movie already in the library", async () => {
  mockFetch(routes(seriesOptions));
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", tmdb_id: 8 }} onClose={() => {}} />);
  await screen.findByTestId("add-versions");
  expect(screen.queryByRole("switch", { name: "Monitored" })).toBeNull();
  cleanup();
  const two = versions.map((v) => ({ ...v, on: v.key === "movie-web", default: v.key === "movie-4k" }));
  mockFetch(routes({ ...options, title_id: 5, versions: two }));
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", title_id: 5 }} onClose={() => {}} />);
  await screen.findByTestId("add-minimum-availability");
  expect(screen.queryByRole("switch", { name: "Monitored" })).toBeNull();
});

it("keeps one Save seasons when only the seasons of a series in the library change", async () => {
  const inLibrary = { ...seriesOptions, title_id: 10, episodes_known: true, seasons: [{ season: 1, episodes: 8, monitored: true }], versions: [{ ...tv, key: "tv-old", on: true, default: false }] };
  const fetch = mockFetch(routes(inLibrary));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", title_id: 10 }} onClose={() => {}} />);
  await user.click(within(await screen.findByTestId("add-seasons")).getByRole("checkbox"));
  expect(screen.getByTestId("add-submit").textContent).toBe("Save seasons");
  expect(screen.queryByTestId("add-submit-search")).toBeNull();
  expect(screen.getByTestId("add-count").textContent).toBe("1 library");
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ title_id: 10, kind: "series", versions: ["tv-old"], seasons: [{ season: 1, monitored: false }], search: false });
});

it("saves only the season changed for a series in the library, leaving its other seasons as they are", async () => {
  const seasons = [{ season: 0, episodes: 2, monitored: false }, { season: 1, episodes: 8, monitored: true }, { season: 2, episodes: 8, monitored: false }];
  const inLibrary = { ...seriesOptions, title_id: 10, episodes_known: true, seasons, versions: [{ ...tv, key: "tv-old", on: true, default: false }] };
  const fetch = mockFetch(routes(inLibrary));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", title_id: 10 }} onClose={() => {}} />);
  await user.click(within(await screen.findByTestId("add-seasons")).getAllByRole("checkbox")[0]);
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ title_id: 10, kind: "series", versions: ["tv-old"], seasons: [{ season: 0, monitored: true }], search: false });
});

it("sends today's request for a new series when every new choice is left alone", async () => {
  const fetch = mockFetch(routes(seriesOptions));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  expect(((await screen.findByTestId("add-monitor")) as HTMLSelectElement).value).toBe("");
  expect(screen.getByTestId("add-monitor-new").getAttribute("aria-checked")).toBe("true");
  expect((screen.getByTestId("add-series-type") as HTMLSelectElement).value).toBe("standard");
  expect(screen.queryByTestId("add-minimum-availability")).toBeNull();
  expect(screen.getByTestId("add-submit-search")).toBeTruthy();
  await user.click(screen.getByTestId("add-submit"));
  const body = await bodyOf(fetch);
  expect(body).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: false, versions: ["tv-web"], search: false });
  expect(Object.keys(body as object).sort()).toEqual(["anime", "kind", "search", "title", "tmdb_id", "versions", "year"]);
});

it("sends the monitor preset and the new-seasons choice of a new series", async () => {
  const fetch = mockFetch(routes(seriesOptions));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  await user.selectOptions(await screen.findByTestId("add-monitor"), "pilot");
  await user.click(screen.getByTestId("add-monitor-new"));
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: false, versions: ["tv-web"], search: false, monitor: "pilot", monitor_new: "none" });
});

it("sends monitor all with later seasons off, so the seasons the first refresh finds stay monitored", async () => {
  const fetch = mockFetch(routes(seriesOptions));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  await user.click(await screen.findByTestId("add-monitor-new"));
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: false, versions: ["tv-web"], search: false, monitor: "all", monitor_new: "none" });
});

it("hides the season ticks when a preset is chosen and sends no seasons", async () => {
  const known = { ...seriesOptions, episodes_known: true, seasons: [{ season: 1, episodes: 8, monitored: true }, { season: 2, episodes: 8, monitored: true }] };
  const fetch = mockFetch(routes(known));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  const select = await screen.findByTestId("add-monitor");
  expect(within(select).getByRole<HTMLOptionElement>("option", { name: "Seasons I pick below" }).selected).toBe(true);
  expect(screen.getByTestId("add-seasons")).toBeTruthy();
  await user.selectOptions(select, "firstSeason");
  expect(screen.queryByTestId("add-seasons")).toBeNull();
  expect(screen.getByText(/This choice decides which episodes are monitored/)).toBeTruthy();
  await user.click(screen.getByTestId("add-submit"));
  const body = await bodyOf(fetch);
  expect(body).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: false, versions: ["tv-web"], search: false, monitor: "firstSeason" });
  expect("seasons" in (body as object)).toBe(false);
});

it("offers Choose seasons for a new series with its own seasons: specials off, every shown season sent, no preset", async () => {
  const own = { ...seriesOptions, seasons: [{ season: 0 }, { season: 1 }, { season: 2 }] };
  const fetch = mockFetch(routes(own));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  const select = await screen.findByTestId("add-monitor");
  expect(screen.queryByTestId("add-seasons")).toBeNull();
  expect(screen.getByTestId("add-seasons-note").textContent).toMatch(/^2 seasons\./);
  await user.selectOptions(select, "choose");
  const ticks = within(screen.getByTestId("add-seasons")).getAllByRole("checkbox");
  expect(ticks.map((c) => (c as HTMLInputElement).checked)).toEqual([false, true, true]);
  expect(screen.getByTestId("add-seasons").textContent).not.toMatch(/ep\./);
  expect(screen.queryByText(/This choice decides/)).toBeNull();
  await user.click(ticks[2]);
  await user.click(screen.getByTestId("add-monitor-new"));
  await user.click(screen.getByTestId("add-submit"));
  const body = await bodyOf(fetch);
  expect(JSON.stringify(body)).toBe(
    '{"kind":"series","tmdb_id":8,"title":"Kappa","year":2025,"anime":false,"versions":["tv-web"],"seasons":[{"season":0,"monitored":false},{"season":1,"monitored":true},{"season":2,"monitored":false}],"search":false,"monitor_new":"none"}',
  );
});

it("sends a preset and no seasons for a new series with its own seasons, and offers no Choose seasons without them", async () => {
  const own = { ...seriesOptions, seasons: [{ season: 1 }, { season: 2 }] };
  const fetch = mockFetch(routes(own));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  const select = await screen.findByTestId("add-monitor");
  await user.selectOptions(select, "choose");
  await user.selectOptions(select, "lastSeason");
  expect(screen.queryByTestId("add-seasons")).toBeNull();
  await user.click(screen.getByTestId("add-submit"));
  expect(JSON.stringify(await bodyOf(fetch))).toBe(
    '{"kind":"series","tmdb_id":8,"title":"Kappa","year":2025,"anime":false,"versions":["tv-web"],"search":false,"monitor":"lastSeason"}',
  );
  cleanup();
  mockFetch(routes(seriesOptions));
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  expect(within(await screen.findByTestId("add-monitor")).queryByRole("option", { name: "Choose seasons" })).toBeNull();
});

it("offers neither series choice for a series already in the library", async () => {
  const inLibrary = { ...seriesOptions, title_id: 10, episodes_known: true, seasons: [{ season: 1, episodes: 8, monitored: true }], versions: [{ ...tv, key: "tv-old", on: true, default: false }, tv] };
  const fetch = mockFetch(routes(inLibrary));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", title_id: 10 }} onClose={() => {}} />);
  expect(await screen.findByTestId("add-seasons")).toBeTruthy();
  for (const id of ["add-monitor", "add-monitor-new", "add-series-type", "add-anime", "add-minimum-availability"]) expect(screen.queryByTestId(id)).toBeNull();
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ title_id: 10, kind: "series", versions: ["tv-web", "tv-old"], seasons: [{ season: 1, monitored: true }], search: false });
});

it("counts only the libraries this click adds for a film already in two", async () => {
  const two = versions.map((v) => ({ ...v, on: v.key === "movie-web" || v.key === "movie-remux", default: v.key === "movie-4k" }));
  mockFetch(routes({ ...options, title_id: 5, versions: two }));
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", title_id: 5 }} onClose={() => {}} />);
  await screen.findByTestId("add-versions");
  expect(screen.getByTestId("add-count").textContent).toBe("1 library");
  expect(screen.getByTestId("add-submit").textContent).toBe("Add libraries");
});

it("reads a film every copy of which was removed as a new add, restored by its title id", async () => {
  const fetch = mockFetch(routes({ ...options, title_id: 5, in_library: false }));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", title_id: 5 }} onClose={() => {}} />);
  await screen.findByTestId("add-versions");
  expect(screen.queryByText("In library")).toBeNull();
  expect(screen.getByTestId("add-submit").textContent).toBe("Add");
  // The new title's choices are offered and sent along.
  expect(screen.getByTestId("add-anime")).toBeTruthy();
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toMatchObject({ title_id: 5, kind: "movie", anime: false });
});

it("offers a removed series the new series' choices and sends them with its title id", async () => {
  const fetch = mockFetch(routes({ ...seriesOptions, title_id: 10, in_library: false }));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", title_id: 10 }} onClose={() => {}} />);
  await user.selectOptions(await screen.findByTestId("add-series-type"), "daily");
  for (const id of ["add-monitor", "add-monitor-new", "add-anime"]) expect(screen.getByTestId(id)).toBeTruthy();
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toMatchObject({ title_id: 10, kind: "series", series_type: "daily", monitor_new: "all" });
});

it("sends a removed series the defaults it shows, so its old type and new-season rule do not stay", async () => {
  const fetch = mockFetch(routes({ ...seriesOptions, title_id: 10, in_library: false }));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", title_id: 10 }} onClose={() => {}} />);
  await screen.findByTestId("add-series-type");
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toMatchObject({ title_id: 10, anime: false, series_type: "standard", monitor_new: "all" });
});

it("sends a new series' type only when it differs from the one the series would get", async () => {
  const fetch = mockFetch(routes(seriesOptions));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  const select = () => screen.getByTestId("add-series-type") as HTMLSelectElement;
  await user.selectOptions(await screen.findByTestId("add-series-type"), "daily");
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: false, versions: ["tv-web"], search: false, series_type: "daily" });
  cleanup();

  // Picked and put back to standard: nothing is sent. An anime title shows anime and sends nothing either.
  const again = mockFetch(routes(seriesOptions));
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  await user.selectOptions(await screen.findByTestId("add-series-type"), "daily");
  await user.selectOptions(select(), "standard");
  await user.click(screen.getByTestId("add-submit"));
  expect("series_type" in ((await bodyOf(again)) as object)).toBe(false);
  cleanup();

  const anime = mockFetch(routes(seriesOptions));
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8, anime: true }} onClose={() => {}} />);
  expect(((await screen.findByTestId("add-series-type")) as HTMLSelectElement).value).toBe("anime");
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(anime)).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: true, versions: ["tv-web"], search: false });
});

it("sends no minimum availability at Library default, and the picked one otherwise, Released included", async () => {
  const one = { ...options, versions: versions.slice(0, 1) };
  const fetch = mockFetch(routes(one));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  const select = (await screen.findByTestId("add-minimum-availability")) as HTMLSelectElement;
  expect(select.value).toBe("");
  expect([...select.options].map((o) => o.textContent)).toEqual(["Library default", "Announced", "In cinemas", "Released"]);
  expect(screen.queryByTestId("add-series-type")).toBeNull();
  // Picked and put back: the body is the one of a plain add.
  await user.selectOptions(select, "inCinemas");
  await user.selectOptions(select, "");
  await user.click(screen.getByTestId("add-submit"));
  const plain = await bodyOf(fetch);
  expect(plain).toEqual({ kind: "movie", tmdb_id: 7, title: "Alpha", year: 2024, anime: false, versions: ["movie-web"], search: false });
  expect("minimum_availability" in (plain as object)).toBe(false);
  cleanup();

  const again = mockFetch(routes(one));
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  await user.selectOptions(await screen.findByTestId("add-minimum-availability"), "released");
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(again)).toEqual({ kind: "movie", tmdb_id: 7, title: "Alpha", year: 2024, anime: false, versions: ["movie-web"], search: false, minimum_availability: "released" });
});

it("ticks the libraries of an anime series when the type picked is Anime, with the Anime box left off", async () => {
  // The server answers by the anime the sheet asks for: the box, or the type picked.
  const plain = { ...tv, anime: false };
  const animeTv = { ...tv, key: "anime-tv", label: "Anime TV", anime: true };
  const reply = (request: { search: string }) => {
    const anime = new URLSearchParams(request.search).get("anime") === "1";
    return { ...seriesOptions, anime, versions: [{ ...plain, fits: !anime, default: !anime }, { ...animeTv, fits: anime, default: anime }] };
  };
  const fetch = mockFetch(routes(reply));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  await user.selectOptions(await screen.findByTestId("add-series-type"), "anime");
  await waitFor(() => expect(fetch.calls("GET", "/add/options").at(-1)?.search).toContain("anime=1"));
  await waitFor(() => expect((screen.getByTestId("add-series-type") as HTMLSelectElement).value).toBe("anime"));
  await user.click(screen.getByTestId("add-submit"));
  expect(await bodyOf(fetch)).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: false, versions: ["anime-tv"], search: false, series_type: "anime" });
});

it("says what each library fetches, offers Monitor for one held unmonitored, and goes to the title page after the add", async () => {
  const held = [
    { ...versions[0], key: "movie-web", label: "4K WEB", held: true, on: false, cutoff: "WEBDL-2160p", upgrades: true, profile: "web-2160p" },
    { ...versions[3], key: "movie-4k", label: "4K Remux", on: false, default: false, fits: true, cutoff: "Remux-2160p", upgrades: false },
  ];
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, title_id: 9, in_library: true, versions: held } },
    { method: "POST", path: "/titles", reply: json({ created: false, new: ["movie-web"], title: { id: 9, name: "Alpha", versions: [] }, search: { started: true, message: "Searching 4K WEB", versions: ["movie-web"], skipped: [], queued: 1, found: 1 } }) },
  ]);
  const user = userEvent.setup();
  const closed = vi.fn();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", title_id: 9 }} onClose={closed} />, "/search");
  const web = await screen.findByTestId("add-version-movie-web");
  expect(within(web).getByTestId("add-fetches").textContent).toBe("Profile Web 2160p: prefers WEBDL-2160p, upgrades on");
  expect(within(web).getByText("Already here, not monitored: ticked, it is monitored again")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-4k")).getByTestId("add-fetches").textContent).toBe("Prefers Remux-2160p, no upgrades");
  expect(screen.getByTestId("add-submit").textContent).toContain("Monitor");
  await user.click(screen.getByTestId("add-submit-search"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  // The request is the add it always was: the held library, searched.
  expect(fetch.calls("POST", "/titles")[0].body).toMatchObject({ title_id: 9, versions: ["movie-web"], search: true });
  await waitFor(() => expect(closed).toHaveBeenCalled());
});

it("shows a library kept in sync with a ticked one ticked and disabled, and sends only what was ticked", async () => {
  const synced = [
    { key: "movie-a", label: "A", grab_state: { ...gates, will_grab: true }, synced: ["movie-b"] },
    { key: "movie-b", label: "B", grab_state: { ...gates, will_grab: true }, synced: ["movie-a"] },
    { key: "movie-c", label: "C", grab_state: { ...gates, will_grab: true } },
  ].map((v) => ({ ...v, kind: "movie", enable: "", fits: true, default: true, on: false, searchable: true }));
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, versions: synced } },
    { method: "POST", path: "/titles", reply: json({ created: true, new: ["movie-a", "movie-b"], synced: ["movie-b"], title: { id: 9, name: "Alpha", versions: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  await screen.findByTestId("add-versions");
  const row = (k: string) => screen.getByTestId(`add-version-${k}`);
  const box = (k: string) => within(row(k)).getByRole("checkbox") as HTMLInputElement;
  // Both pre-ticked: two ordinary boxes.
  expect(screen.queryByTestId("add-synced-with")).toBeNull();
  await user.click(box("movie-b"));
  expect(box("movie-b").checked && box("movie-b").disabled).toBe(true);
  expect(within(row("movie-b")).getByTestId("add-synced-with").textContent).toBe("in sync with A");
  await user.click(box("movie-c"));
  expect(screen.getByTestId("add-count").textContent).toBe("2 libraries");
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  expect((fetch.calls("POST", "/titles")[0].body as { versions: string[] }).versions).toEqual(["movie-a"]);
  // Unticking the named library frees the partner.
  cleanup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, versions: synced } },
  ]);
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  await screen.findByTestId("add-versions");
  await user.click(box("movie-b"));
  await user.click(box("movie-a"));
  expect(box("movie-a").checked || box("movie-b").checked || box("movie-b").disabled).toBe(false);
  expect(screen.queryByTestId("add-synced-with")).toBeNull();
});

it("says a removed copy in a library kept in sync comes back", async () => {
  const versions = [
    { key: "movie-a", label: "A", grab_state: { ...gates, will_grab: true }, synced: ["movie-b"] },
    { key: "movie-b", label: "B", grab_state: { ...gates, will_grab: true }, synced: ["movie-a"], removed: true },
  ].map((v) => ({ ...v, kind: "movie", enable: "", fits: true, default: true, on: false, searchable: true }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, versions } },
  ]);
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  await screen.findByTestId("add-versions");
  await user.click(within(screen.getByTestId("add-version-movie-b")).getByRole("checkbox"));
  expect(within(screen.getByTestId("add-version-movie-b")).getByTestId("add-synced-with").textContent)
    .toBe("in sync with A; it was removed and comes back");
});

it("names the library the person chose when the add brings back its removed copy, and the partner as in sync", async () => {
  const versions = [
    { key: "movie-a", label: "A", grab_state: { ...gates, will_grab: true }, synced: ["movie-b"], removed: true },
    { key: "movie-b", label: "B", grab_state: { ...gates, will_grab: true }, synced: ["movie-a"], removed: true },
  ].map((v) => ({ ...v, kind: "movie", enable: "", fits: true, default: false, on: false, searchable: true }));
  // The removed copy of A comes back (not a new row); its partner B is restored in sync. (No library names are
  // served here, so the toast names them by key.)
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, versions } },
    { method: "POST", path: "/titles", reply: json({ created: false, versions: ["movie-a", "movie-b"], new: ["movie-b"], synced: ["movie-b"], title: { id: 9, name: "Alpha", versions: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<><AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} /><Toaster /></>);
  await screen.findByTestId("add-versions");
  await user.click(within(screen.getByTestId("add-version-movie-a")).getByRole("checkbox"));
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(screen.getAllByTestId("toast").at(-1)?.textContent).toContain("Turned on: movie-a. movie-b too (in sync)."));
});

it("says a movie added with Monitored off was added, not monitored (no library names are served here: default names)", async () => {
  const two = versions.slice(0, 2).map((v) => ({ ...v, label: v.key === "movie-web" ? "4K WEB" : "4K Remux" }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, versions: two } },
    { method: "POST", path: "/titles", reply: json({ created: true, versions: ["movie-remux", "movie-web"], new: ["movie-remux", "movie-web"], title: { id: 9, name: "Alpha", monitored: false, versions: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<><AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} /><Toaster /></>);
  await user.click(await screen.findByRole("switch", { name: "Monitored" }));
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(screen.getAllByTestId("toast").at(-1)?.textContent).toContain("Added, not monitored: WEB, Remux."));
  expect(screen.getAllByTestId("toast").at(-1)?.textContent).not.toContain("Turned on");
});

it("names beside Add libraries and search every library it searches, the ones the title is already in too", async () => {
  const lib = (key: string, label: string, on: boolean) => ({ key, label, kind: "movie", enable: "", fits: true, default: true, on, searchable: true, grab_state: { ...gates, will_grab: true } });
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, title_id: 5, versions: [lib("movie-web", "4K WEB", true), lib("movie-remux", "4K Remux", false)] } },
  ]);
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", title_id: 5 }} onClose={() => {}} />);
  expect((await screen.findByTestId("add-submit-search")).textContent).toBe("Add libraries and search");
  expect(screen.getByTestId("add-searches").textContent).toBe("Searches: 4K WEB, 4K Remux");
});

it("offers Add alone for a new series added with Monitor None, with the later-seasons switch either way", async () => {
  for (const later of [true, false]) {
    const fetch = mockFetch(routes(seriesOptions));
    const user = userEvent.setup();
    renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
    await user.selectOptions(await screen.findByTestId("add-monitor"), "none");
    expect(screen.queryByTestId("add-submit-search")).toBeNull();
    expect(screen.getByTestId("add-monitor-new")).toBeTruthy();
    if (!later) await user.click(screen.getByTestId("add-monitor-new"));
    expect(screen.queryByTestId("add-submit-search")).toBeNull();
    expect(screen.getByTestId("add-submit").textContent).toBe("Add");
    await user.click(screen.getByTestId("add-submit"));
    expect(await bodyOf(fetch)).toEqual({ kind: "series", tmdb_id: 8, title: "Kappa", year: 2025, anime: false, versions: ["tv-web"], search: false, monitor: "none", ...(later ? {} : { monitor_new: "none" }) });
    cleanup();
  }
});

it("says a series added with Monitor None was added, not monitored", async () => {
  mockFetch([...routes(seriesOptions).slice(0, 2), { method: "POST", path: "/titles", reply: json({ ...posted, versions: ["tv-web"], title: { ...posted.title, monitored: true } }) }]);
  const user = userEvent.setup();
  renderPage(<><AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} /><Toaster /></>);
  await user.selectOptions(await screen.findByTestId("add-monitor"), "none");
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(screen.getAllByTestId("toast").at(-1)?.textContent).toContain("Added, not monitored: WEB."));
  expect(screen.getAllByTestId("toast").at(-1)?.textContent).not.toContain("Turned on");
});

it("offers Add alone when Choose seasons has every box cleared, and both again once one is ticked", async () => {
  const own = { ...seriesOptions, seasons: [{ season: 0 }, { season: 1 }, { season: 2 }] };
  const fetch = mockFetch(routes(own));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  await user.selectOptions(await screen.findByTestId("add-monitor"), "choose");
  const ticks = within(screen.getByTestId("add-seasons")).getAllByRole("checkbox");
  await user.click(ticks[1]);
  expect(screen.getByTestId("add-submit-search")).toBeTruthy();
  await user.click(ticks[2]);
  expect(screen.queryByTestId("add-submit-search")).toBeNull();
  await user.click(ticks[0]);
  expect(screen.getByTestId("add-submit-search")).toBeTruthy();
  await user.click(ticks[0]);
  expect(screen.getByTestId("add-monitor-new")).toBeTruthy();
  await user.click(screen.getByTestId("add-submit"));
  expect(JSON.stringify(await bodyOf(fetch))).toBe(
    '{"kind":"series","tmdb_id":8,"title":"Kappa","year":2025,"anime":false,"versions":["tv-web"],"seasons":[{"season":0,"monitored":false},{"season":1,"monitored":false},{"season":2,"monitored":false}],"search":false}',
  );
});
