// @vitest-environment jsdom
// The add drawer's library rows (audit 2): one checkbox per library, identity and state on their own rows, the
// library state from grab_state, the global notice said once, and "n will download" from the ticked rows' will_grab.
// Invented keys and names.
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { AddSheet } from "./add-sheet";

// Search now is remembered per browser: each test starts as a browser with nothing stored.
beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

const gates = { grab: true, router_enabled: true, router_forced_off: false, library_enabled: true, library_writes: true };
const long = "A library name that is long enough to wrap at four hundred p";
const versions = [
  { key: "movie-web", label: long, mode: "active", grab_state: { ...gates, mode: "active", will_grab: true } },
  { key: "movie-remux", label: "Remux", mode: "active", grab_state: { ...gates, mode: "active", grab: false, will_grab: false } },
  { key: "movie-1080", label: "1080p", mode: "shadow", grab_state: { ...gates, mode: "shadow", will_grab: false, library_writes: false } },
  { key: "movie-4k", label: "4K", mode: "active", grab_state: { ...gates, mode: "active", will_grab: true } },
  { key: "movie-hdr", label: "HDR", mode: "active", grab_state: { ...gates, mode: "active", will_grab: true, library_writes: false } },
  { key: "movie-dv", label: "Dolby Vision", mode: "shadow", grab_state: { ...gates, mode: "shadow", will_grab: false, library_writes: false } },
  { key: "movie-sd", label: "SD", mode: "active", grab_state: { ...gates, mode: "active", will_grab: true } },
].map((v) => ({ ...v, kind: "movie", enable: "", fits: true, default: v.key !== "movie-sd", on: false, searchable: v.grab_state.will_grab }));
const options = { kind: "movie", anime: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions };

it("lists every library with its state, counts what will download and sends the same add", async () => {
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
  expect(within(web).getByText("movie-web")).toBeTruthy();
  expect(within(web).getByText("Downloads on")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-remux")).getByText("Downloads off")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-1080")).getByText("Preview only")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-hdr")).getByTestId("links-off")).toBeTruthy();
  // The checkbox is named by the library and described by its state.
  const box = within(web).getByRole("checkbox", { name: new RegExp(long) });
  expect(document.getElementById(box.getAttribute("aria-describedby") ?? "")?.textContent).toContain("Downloads on");
  // Six ticked by default; movie-web, movie-4k and movie-hdr will download.
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries · 3 will download");
  await user.click(within(screen.getByTestId("add-version-movie-sd")).getByRole("checkbox"));
  expect(screen.getByTestId("add-count").textContent).toBe("7 libraries · 4 will download");
  await user.click(box);
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries · 3 will download");
  expect(screen.queryByTestId("downloads-off-notice")).toBeNull();
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  expect(fetch.calls("POST", "/titles")[0].body).toEqual({ kind: "movie", tmdb_id: 7, title: "Alpha", year: 2024, anime: false, versions: ["movie-remux", "movie-1080", "movie-4k", "movie-hdr", "movie-dv", "movie-sd"], search: false });
});

it("turns Search now on from its switch or the row's words and sends it with the add", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: options },
    { method: "POST", path: "/titles", reply: json({ created: true, new: ["movie-web"], title: { id: 9, name: "Alpha", versions: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  const sw = await screen.findByRole("switch", { name: "Search now" });
  expect(sw.getAttribute("aria-checked")).toBe("false");
  await user.click(screen.getByText("Search now"));
  expect(sw.getAttribute("aria-checked")).toBe("true");
  await user.click(sw);
  expect(sw.getAttribute("aria-checked")).toBe("false");
  await user.click(sw);
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  expect((fetch.calls("POST", "/titles")[0].body as { search: boolean }).search).toBe(true);
});

it("says new downloads are off once, not on every row", async () => {
  const off = versions.map((v) => ({ ...v, searchable: false, grab_state: { ...v.grab_state, router_enabled: false, will_grab: false } }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, router_enabled: false, versions: off } },
  ]);
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  expect((await screen.findAllByTestId("downloads-off-notice"))).toHaveLength(1);
  expect(screen.queryByTestId("add-no-search")).toBeNull();
  expect(within(screen.getByTestId("add-version-movie-web")).getByText("Paused globally")).toBeTruthy();
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries · 0 will download");
  // Not an admin: no settings link.
  expect(within(screen.getByTestId("downloads-off-notice")).queryByRole("link")).toBeNull();
});

const tv = { key: "tv-web", label: "TV", kind: "series", mode: "active", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: { ...gates, mode: "active", will_grab: true } };
const seriesOptions = { kind: "series", anime: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [{ season: 1, episodes: 8 }], versions: [tv] };
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

it("sends today's request for a new series when every new choice is left alone", async () => {
  const fetch = mockFetch(routes(seriesOptions));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "series", name: "Kappa", year: 2025, tmdb_id: 8 }} onClose={() => {}} />);
  expect(((await screen.findByTestId("add-monitor")) as HTMLSelectElement).value).toBe("");
  expect(screen.getByTestId("add-monitor-new").getAttribute("aria-checked")).toBe("true");
  expect((screen.getByTestId("add-series-type") as HTMLSelectElement).value).toBe("standard");
  expect(screen.queryByTestId("add-minimum-availability")).toBeNull();
  expect(screen.getByRole("switch", { name: "Search now" }).getAttribute("aria-checked")).toBe("false");
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

it("remembers Search now in this browser and starts off without a stored value", async () => {
  mockFetch(routes(options));
  const user = userEvent.setup();
  const first = renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  const sw = await screen.findByRole("switch", { name: "Search now" });
  expect(sw.getAttribute("aria-checked")).toBe("false");
  await user.click(sw);
  expect(localStorage.getItem("theatarr.add.search")).toBe("true");
  first.unmount();
  const second = renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  const again = await screen.findByRole("switch", { name: "Search now" });
  expect(again.getAttribute("aria-checked")).toBe("true");
  await user.click(again);
  expect(localStorage.getItem("theatarr.add.search")).toBeNull();
  second.unmount();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  expect((await screen.findByRole("switch", { name: "Search now" })).getAttribute("aria-checked")).toBe("false");
});

it("does not turn Search now back on when storing the choice fails", async () => {
  localStorage.setItem("theatarr.add.search", "true");
  // A full storage: writes fail, removals work.
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
  mockFetch(routes(options));
  const user = userEvent.setup();
  const first = renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  const sw = await screen.findByRole("switch", { name: "Search now" });
  expect(sw.getAttribute("aria-checked")).toBe("true");
  await user.click(sw);
  first.unmount();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  expect((await screen.findByRole("switch", { name: "Search now" })).getAttribute("aria-checked")).toBe("false");
});

it("works with Search now off when the browser's storage throws", async () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
  const fetch = mockFetch(routes(options));
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  const sw = await screen.findByRole("switch", { name: "Search now" });
  expect(sw.getAttribute("aria-checked")).toBe("false");
  await user.click(sw);
  expect(sw.getAttribute("aria-checked")).toBe("true");
  await user.click(screen.getByTestId("add-submit"));
  expect(((await bodyOf(fetch)) as { search: boolean }).search).toBe(true);
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
