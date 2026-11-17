// @vitest-environment jsdom
import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Seasons } from "./seasons";
import { Toaster } from "@/components/toaster";
import { expectNamedControls, mockFetch, renderPage } from "@/test/page";

const readOnly = { can_edit: false, edit_reason: "Your account may not change this", can_search: false, live_releases: false };
const managed = { can_edit: true, can_search: true, live_releases: true };
const downloading = { stage: "downloading", words: "Downloading (37%)", progress: 37, download_id: 9, downloads: 1, episodes: 1, updated_at: "2026-10-29T00:00:00Z" };

const cell = (has: boolean, extra = {}) => ({ state: has ? "cutoff_met" : "wanted", monitored: true, has_file: has, quality: has ? "WEBDL-1080p" : undefined, ...extra });
const episodes = {
  title_id: 1,
  versions: [
    { id: 1, key: "anime-tv", label: "Anime Blu-ray", monitored: true, control: readOnly },
    { id: 2, key: "anime-web", label: "Anime WEB", monitored: true, control: managed },
  ],
  seasons: [
    {
      season: 1, episodes: 3, aired: 3,
      versions: {
        "anime-tv": { have: 1, missing: 1, below_cutoff: 0, upgrade_seen: 0, monitored: 3, in_flight: 1, stages: { downloading: 1, queued: 1 } },
        "anime-web": { have: 3, missing: 0, below_cutoff: 0, upgrade_seen: 0, monitored: 3 },
      },
    },
  ],
  episodes: [
    { id: 101, season: 1, number: 12, absolute: 12, name: "Paper Lantern", air_date: "2026-10-01", aired: true, monitored: true, versions: { "anime-tv": cell(true), "anime-web": cell(true) } },
    { id: 102, season: 1, number: 13, absolute: 13, name: "Copper Harbour", air_date: "2026-10-08", aired: true, monitored: true, versions: { "anime-tv": cell(false, { acquisition: downloading }), "anime-web": cell(true) } },
    { id: 103, season: 1, number: 14, absolute: 14, name: "Iron Harbour", air_date: "2026-10-15", aired: true, monitored: true, versions: { "anime-tv": cell(false), "anime-web": cell(true) } },
  ],
};
const detail = {
  title_id: 1,
  title_name: "Tsukikage Signal",
  episode: episodes.episodes[1],
  versions: [
    { key: "anime-tv", label: "Anime Blu-ray", state: "wanted", monitored: true, has_file: false, control: readOnly },
    { key: "anime-web", label: "Anime WEB", state: "cutoff_met", monitored: true, has_file: true, control: managed },
  ],
};
const routes = [
  { method: "GET", path: "/auth/me", reply: { id: 1, username: "owner", permissions: ["see_files", "manage"], csrf_token: "t" } },
  { method: "GET", path: "/titles/1/episodes", reply: episodes },
  { method: "GET", path: "/titles/1/episodes/102", reply: detail },
];

describe("Series page copies", () => {
  it("badges each library's season in All, and one library's badge and facts once it is picked, with anime numbering", async () => {
    mockFetch(routes);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    const header = await screen.findByTestId("season-header");
    const tv = within(header).getAllByTestId("season-badge").find((b) => b.getAttribute("data-vkey") === "anime-tv") as HTMLElement;
    expect(tv.textContent).toBe("Anime Blu-ray1 / 3");
    expect(tv.getAttribute("data-tone")).toBe("bad");
    expect(within(header).getAllByTestId("season-badge").find((b) => b.getAttribute("data-vkey") === "anime-web")?.getAttribute("data-tone")).toBe("ok");
    expect(within(header).getByTestId("season-summary").textContent).toBe("3 of 3 available · 2 missing in Anime Blu-ray");
    expect(await screen.findByText("E13 · absolute 13")).toBeTruthy();
    const row = screen.getByRole("button", { name: /Episode 13/ });
    expect(within(row).getByText("Downloading · 37%")).toBeTruthy();
    expect(within(row).getByRole("progressbar")).toBeTruthy();
    expectNamedControls();
    await user.click(screen.getByRole("radio", { name: "Anime Blu-ray" }));
    expect(within(header).getByTestId("season-badge").textContent).toBe("1 / 3");
    expect(within(header).getByTestId("season-facts").textContent).toBe("1 missing · 1 queued · 1 downloading");
    expect(within(header).queryByTestId("season-summary")).toBeNull();
  });

  it("says the next search in plain words when the server sends it for the library in view", async () => {
    const at = new Date(Date.now() + 3 * 3600_000).toISOString();
    const timed = { ...episodes, seasons: [{ ...episodes.seasons[0], versions: { ...episodes.seasons[0].versions, "anime-web": { ...episodes.seasons[0].versions["anime-web"], next_search_at: at, next_search_why: "new" } } }] };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: timed }]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    await userEvent.setup().click(await screen.findByRole("radio", { name: "Anime WEB" }));
    expect(screen.getByTestId("season-facts").textContent).toBe("Next search: in 3 h (new release)");
  });

  it("shows the per-library badges only to accounts that see files; others keep the season summary", async () => {
    mockFetch(routes);
    const first = renderPage(<Seasons id="1" anime />, "/title/1");
    expect((await screen.findAllByTestId("season-badge")).length).toBe(2);
    first.unmount();
    vi.unstubAllGlobals();
    mockFetch([{ ...routes[0], reply: { id: 9, username: "family", permissions: [], csrf_token: "t" } }, routes[1]]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    expect(await screen.findByTestId("season-summary")).toBeTruthy();
    expect(screen.queryByTestId("season-badge")).toBeNull();
    expect(screen.queryByTestId("copy-selector")).toBeNull();
    expect(screen.queryByText("1 / 3")).toBeNull();
  });

  it("filters the episodes in progress", async () => {
    mockFetch(routes);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    const chip = await screen.findByRole("button", { name: /In progress/ });
    expect(chip.textContent).toContain("1");
    await user.click(chip);
    expect(screen.getAllByRole("button", { name: /^Episode \d+/ }).map((e) => e.getAttribute("aria-label"))).toEqual(["Episode 13: Copper Harbour"]);
  });

  it("tells same-named copies apart in the badges, the summary, the notices and the episode table", async () => {
    const same = { ...episodes, versions: episodes.versions.map((v) => ({ ...v, label: "Anime" })) };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: same }]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    const bars = await screen.findByTestId("season-header");
    expect(within(bars).getByText("Anime")).toBeTruthy();
    expect(within(bars).getByText("Anime (2)")).toBeTruthy();
    expect(screen.queryByText(/anime-tv|anime-web/)).toBeNull();
    expect(screen.getByText(/2 missing in Anime\b(?! \(2\))/)).toBeTruthy();
    expect(screen.getByText(/Monitoring is read-only \(Anime\)/)).toBeTruthy();
    expect(screen.getAllByText("Anime (2)").length).toBeGreaterThan(1);
  });

  it("puts the copy's acquisition and an Activity link in the episode overview", async () => {
    mockFetch(routes);
    renderPage(<Seasons id="1" anime />, "/title/1?episode=102");
    const dialog = await screen.findByRole("dialog");
    const acq = await within(dialog).findByTestId("episode-acquisition");
    expect(acq.getAttribute("data-stage")).toBe("downloading");
    expect(within(acq).getByText("Downloading · 37%")).toBeTruthy();
    expect(within(acq).getByRole("link", { name: /Activity/ }).getAttribute("href")).toBe("/activity?show=downloading");
    expect(within(dialog).getAllByTestId("episode-acquisition")).toHaveLength(1);
  });
});

it("says restrictions only about the library in view", async () => {
  mockFetch(routes);
  const user = userEvent.setup();
  renderPage(<Seasons id="1" anime />, "/title/1");
  expect((await screen.findByTestId("episode-restrictions")).textContent).toContain("Anime Blu-ray");
  await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
  expect(screen.queryByTestId("episode-restrictions")).toBeNull();
  await user.click(screen.getByRole("radio", { name: "Anime Blu-ray" }));
  expect(screen.getByTestId("episode-restrictions").textContent).toContain("Anime Blu-ray");
});

it("leaves Pause for every library to the title page's bar: no notice or restriction line in the tab", async () => {
  const control = { ...managed, can_search: false, search_reason: "Paused: nothing is searched or downloaded", grab_state: { paused: true, pause_forced: false, will_grab: false } };
  mockFetch([{ ...routes[0], reply: { id: 1, permissions: ["admin", "manage", "see_files"] } }, { method: "GET", path: "/titles/1/episodes", reply: { ...episodes, versions: [{ ...episodes.versions[1], control }] } }]);
  renderPage(<Seasons id="1" anime />, "/title/1");
  await screen.findByTestId("season-header");
  expect(screen.queryByTestId("downloads-off-notice")).toBeNull();
  expect(screen.queryByTestId("episode-restrictions")).toBeNull();
});

it("names phone library radios distinctly", async () => {
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
  mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: { ...episodes, versions: episodes.versions.map(v => ({ ...v, label: "Anime" })) } }]);
  renderPage(<Seasons id="1" anime />, "/title/1");
  expect(await screen.findByRole("radio", { name: "Anime" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Anime (2)" })).toBeTruthy();
  // Phones have no All: the first library is in view.
  expect(screen.queryByRole("radio", { name: "All libraries" })).toBeNull();
  expect(screen.getByRole("radio", { name: "Anime" }).getAttribute("aria-checked")).toBe("true");
});

it("leaves season settings destinations as text for non-admin managers", async () => {
  mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: { ...episodes, versions: [{ ...episodes.versions[1], control: { ...managed, can_search: false } }] } }]);
  renderPage(<Seasons id="1" anime />, "/title/1");
  const note = await screen.findByTestId("episode-restrictions");
  expect(note.textContent).toBe("Anime WEB: Downloads off — Settings › Libraries");
  expect(within(note).queryByRole("link")).toBeNull();
});

describe("Interactive search from the episodes", () => {
  const title = { method: "GET", path: "/titles/1", reply: { id: 1, name: "Tsukikage Signal", kind: "series", versions: [{ id: 2, key: "anime-web", label: "Anime WEB", control: managed }] } };
  const current = { method: "GET", path: "/titles/1/removal", reply: { title_id: 1, name: "Tsukikage Signal", title: true, leftover: [], copies: [{ key: "anime-tv", label: "Anime Blu-ray", links: 0, downloads: 0 }, { key: "anime-web", label: "Anime WEB", links: 0, downloads: 0 }] } };
  const answer = { title_id: 1, version: "anime-web", versions: ["anime-web"], live_allowed: true, source: "live", results: 0, accepted: 0, candidates: [], grab: true };

  it("searches a season from its header", async () => {
    const api = mockFetch([...routes, title, current, { method: "GET", path: "/titles/1/releases", reply: answer }]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    const header = await screen.findByTestId("season-interactive");
    await userEvent.setup().click(within(header).getByRole("button", { name: "Interactive search" }));
    expect(await screen.findByTestId("interactive-search")).toBeTruthy();
    expect(await screen.findByText("No releases found.")).toBeTruthy();
    expect(api.calls("GET", "/titles/1/releases").map((r) => r.search)).toEqual(["?version=anime-web&live=1&season=1"]);
  });

  it("searches one episode from its row without opening the episode overview", async () => {
    const api = mockFetch([...routes, title, current, { method: "GET", path: "/titles/1/episodes/102/releases", reply: answer }]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    await screen.findByTestId("season-summary");
    const row = document.querySelector('[data-episode="1x13"]') as HTMLElement;
    await userEvent.setup().click(await within(row).findByRole("button", { name: "Interactive search for S01E13" }));
    expect((await screen.findByTestId("interactive-subject")).textContent).toContain("S01E13");
    expect(await screen.findByText("No releases found.")).toBeTruthy();
    expect(api.calls("GET", "/titles/1/episodes/102/releases").map((r) => r.search)).toEqual(["?version=anime-web&live=1"]);
    expect(api.calls("GET", "/titles/1/episodes/102")).toHaveLength(0);
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
  });

  it("offers none when the series' only Live copy was removed (the episodes route no longer lists it)", async () => {
    const api = mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: { ...episodes, versions: episodes.versions.slice(0, 1) } }]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    await screen.findByTestId("season-header");
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("interactive-search-open")).toBeNull();
    expect(api.calls("GET", "/titles/1/removal")).toHaveLength(0);
  });

  it("offers no Interactive search to an account that does not manage", async () => {
    mockFetch([{ ...routes[0], reply: { id: 9, username: "family", permissions: ["see_files"], csrf_token: "t" } }, routes[1]]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    await screen.findByTestId("season-summary");
    expect(screen.queryByTestId("interactive-search-open")).toBeNull();
  });
});

describe("Acting on the libraries in view", () => {
  const outcome = { started: true, versions: ["anime-web"], skipped: [], queued: 1, found: 0, message: "Searching 1 library" };
  const changed = { versions: ["anime-web"], skipped: [], episodes: 3, message: "Monitoring changed in 1 library" };
  const acting = [
    ...routes,
    { method: "PUT", path: "/titles/1/seasons/1/monitor", reply: changed },
    { method: "POST", path: "/titles/1/seasons/1/search", reply: outcome },
    { method: "PUT", path: "/titles/1/episodes/monitor", reply: changed },
    { method: "POST", path: "/titles/1/episodes/search", reply: outcome },
  ];

  it("sends the season header's switch and Search to every library in All and to the picked library alone", async () => {
    const api = mockFetch(acting);
    const user = userEvent.setup();
    renderPage(<><Seasons id="1" anime /><Toaster /></>, "/title/1");
    const header = await screen.findByTestId("season-header");
    await user.click(within(header).getByRole("switch", { name: "Stop monitoring season 1 in every library" }));
    await user.click(within(header).getByRole("button", { name: "Search season 1 now in every library" }));
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/seasons/1/search")).toHaveLength(1));
    expect(api.calls("PUT", "/titles/1/seasons/1/monitor")[0].body).toEqual({ monitored: false });
    expect(api.calls("POST", "/titles/1/seasons/1/search")[0].body).toEqual({});
    expect(await screen.findByText("Search started")).toBeTruthy();
    expect(screen.getByText("Searching 1 library")).toBeTruthy();

    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    await user.click(within(header).getByRole("switch", { name: "Stop monitoring season 1 in Anime WEB" }));
    await user.click(within(header).getByRole("button", { name: "Search season 1 now in Anime WEB" }));
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/seasons/1/search")).toHaveLength(2));
    expect(api.calls("PUT", "/titles/1/seasons/1/monitor")[1].body).toEqual({ monitored: false, versions: ["anime-web"] });
    expect(api.calls("POST", "/titles/1/seasons/1/search")[1].body).toEqual({ versions: ["anime-web"] });
  });

  it("disables a season's Search when no library that searches monitors an episode of it, and keeps Interactive search (T36)", async () => {
    // anime-tv monitors the season but may not search; anime-web searches but monitors none of it.
    const s = episodes.seasons[0];
    const none = { ...episodes, seasons: [{ ...s, versions: { ...s.versions, "anime-web": { ...s.versions["anime-web"], monitored: 0 } } }] };
    const api = mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: none }, ...acting.slice(3)]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    const header = await screen.findByTestId("season-header");
    const search = within(header).getByRole("button", { name: "Search season 1 now in every library" });
    expect((search as HTMLButtonElement).disabled).toBe(true);
    // A disabled button takes no hover: the reason is on its wrapper.
    expect(within(header).getByTestId("season-search-why-1").getAttribute("title")).toBe("No monitored episodes in this season");
    expect((within(within(header).getByTestId("season-interactive")).getByRole("button") as HTMLButtonElement).disabled).toBe(false);
    // Picked alone, the library that searches still monitors none of it.
    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    expect((within(header).getByRole("button", { name: "Search season 1 now in Anime WEB" }) as HTMLButtonElement).disabled).toBe(true);
    expect(api.calls("POST", "/titles/1/seasons/1/search")).toHaveLength(0);
  });

  it("disables a season's Search in a picked library that does not monitor the series, though another library monitors the season", async () => {
    const off = { ...episodes, versions: [{ ...episodes.versions[0], control: managed }, { ...episodes.versions[1], monitored: false }] };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: off }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await user.click(await screen.findByRole("radio", { name: "Anime WEB" }));
    const header = screen.getByTestId("season-header");
    expect((within(header).getByRole("button", { name: "Search season 1 now in Anime WEB" }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(header).getByTestId("season-search-why-1").getAttribute("title")).toBe("Anime WEB does not monitor this series");
  });

  it("keeps a season's Search in a library that monitors none of it while another library that searches does (the server widens to it)", async () => {
    const s = episodes.seasons[0];
    const other = {
      ...episodes,
      versions: episodes.versions.map((v) => ({ ...v, control: managed })),
      seasons: [{ ...s, versions: { ...s.versions, "anime-web": { ...s.versions["anime-web"], monitored: 0 } } }],
    };
    const api = mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: other }, ...acting.slice(3)]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await user.click(await screen.findByRole("radio", { name: "Anime WEB" }));
    const header = screen.getByTestId("season-header");
    const search = within(header).getByRole("button", { name: "Search season 1 now in Anime WEB" }) as HTMLButtonElement;
    expect(search.disabled).toBe(false);
    await user.click(search);
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/seasons/1/search")).toHaveLength(1));
    expect(api.calls("POST", "/titles/1/seasons/1/search")[0].body).toEqual({ versions: ["anime-web"] });
  });

  it("sends a row's switch and Search for that episode in the library in view", async () => {
    const api = mockFetch(acting);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await user.click(await screen.findByRole("radio", { name: "Anime WEB" }));
    const row = document.querySelector('[data-episode="1x13"]') as HTMLElement;
    await user.click(within(row).getByRole("switch", { name: "Stop monitoring S01E13 in Anime WEB" }));
    await user.click(within(row).getByRole("button", { name: "Search S01E13 now in Anime WEB" }));
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/episodes/search")).toHaveLength(1));
    expect(api.calls("PUT", "/titles/1/episodes/monitor")[0].body).toEqual({ monitored: false, versions: ["anime-web"], episode_ids: [102] });
    expect(api.calls("POST", "/titles/1/episodes/search")[0].body).toEqual({ versions: ["anime-web"], episode_ids: [102] });
    // The row's icons do not open the episode overview.
    expect(api.calls("GET", "/titles/1/episodes/102")).toHaveLength(0);
  });

  it("shows a chosen column for the library in view, and cache pills by default", async () => {
    const withFacts = { ...episodes, episodes: episodes.episodes.map((e) => ({ ...e, versions: { ...e.versions, "anime-web": cell(true, { release_group: "NTb", cached_on: ["rd"] }) } })) };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: withFacts }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    // All libraries: the matrix, no Columns button.
    await screen.findByTestId("episode-table");
    expect(screen.queryByTestId("episode-columns")).toBeNull();
    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    const table = screen.getByTestId("episode-table");
    expect(table.getAttribute("data-library")).toBe("anime-web");
    expect(within(table).getAllByText("RD")).toHaveLength(3);
    expect(within(table).queryByRole("columnheader", { name: "Release group" })).toBeNull();
    await user.click(screen.getByTestId("episode-columns"));
    await user.click(within(screen.getByTestId("column-list")).getByRole("checkbox", { name: /Release group/ }));
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(within(screen.getByTestId("episode-table")).getByRole("columnheader", { name: "Release group" })).toBeTruthy();
    expect(within(screen.getByTestId("episode-table")).getAllByText("NTb")).toHaveLength(3);
  });

  it("offers the file's media, languages and formats as columns, blank for a file without them", async () => {
    const media = { video_codec: "x265", video_dynamic_range: "DV HDR10", audio_codec: "EAC3 Atmos", audio_channels: 5.1, audio_languages: "eng/kor", subtitles: "eng/fre/spa/ger/jpn/kor" };
    const facts = { media, languages: ["English", "Korean"], formats: [{ name: "DV Boost", score: 1500 }] };
    const withMedia = { ...episodes, episodes: episodes.episodes.map((e) => ({ ...e, versions: { ...e.versions, "anime-web": cell(true, e.id === 101 ? facts : {}) } })) };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: withMedia }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await user.click(await screen.findByRole("radio", { name: "Anime WEB" }));
    await user.click(screen.getByTestId("episode-columns"));
    const added = ["Video codec", "Dynamic range", "Audio", "Audio languages", "Subtitles", "Languages", "Formats"];
    const list = screen.getByTestId("column-list");
    for (const name of added) await user.click(within(list).getByRole("checkbox", { name: new RegExp(`^${name} ?The `) }));
    await user.click(screen.getByRole("button", { name: "Done" }));
    const table = screen.getByTestId("episode-table");
    const heads = within(table).getAllByRole("columnheader").map((h) => h.textContent);
    const at = (ep: string, head: string) => {
      const row = table.querySelector(`[data-episode="${ep}"]`) as HTMLElement;
      return row.querySelectorAll("td")[heads.indexOf(head)] as HTMLElement;
    };
    const want: Record<string, string> = {
      "Video codec": "x265", "Dynamic range": "DV HDR10", Audio: "EAC3 Atmos 5.1", "Audio languages": "English, Korean",
      Subtitles: "English, French +4", Languages: "English, Korean", Formats: "DV Boost+1500",
    };
    for (const name of added) {
      expect(at("1x12", name).textContent, name).toBe(want[name]);
      expect(at("1x13", name).textContent, name).toBe("");
    }
    expect(within(at("1x12", "Subtitles")).getByTitle("English, French, Spanish, German, Japanese, Korean")).toBeTruthy();
  });

  it("keeps a row short: five formats flow in a wide cell, long language lists stay on one line with +N", async () => {
    const subs = ["eng", "ara", "bul", "zho", "hrv", "ces", "dan", "nld", "est", "fin", "fre", "ger", "ell", "heb", "hun", "ind", "ita", "jpn", "kor", "lav", "lit", "may", "nor", "pol", "por", "rum", "rus", "srp", "slk", "slv", "spa", "swe", "tha", "tur", "ukr", "vie"];
    const media = { audio_languages: "eng/fre/ger/spa/ita", subtitles: subs.join("/") };
    const formats = ["DV Boost", "HDR10 Boost", "Atmos", "Streaming Service", "Repack/Proper"].map((name) => ({ name, score: 100 }));
    const facts = { media, languages: ["English", "French", "German", "Spanish"], formats };
    const withMedia = { ...episodes, episodes: episodes.episodes.map((e) => ({ ...e, versions: { ...e.versions, "anime-web": cell(true, e.id === 101 ? facts : {}) } })) };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: withMedia }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await user.click(await screen.findByRole("radio", { name: "Anime WEB" }));
    await user.click(screen.getByTestId("episode-columns"));
    const list = screen.getByTestId("column-list");
    // The column choice is kept between tests: tick only the ones still off.
    for (const name of ["Audio languages", "Subtitles", "Languages", "Formats"]) {
      const box = within(list).getByRole("checkbox", { name: new RegExp(`^${name} ?The `) });
      if (!(box as HTMLInputElement).checked && box.getAttribute("aria-checked") !== "true") await user.click(box);
    }
    await user.click(screen.getByRole("button", { name: "Done" }));
    const table = screen.getByTestId("episode-table");
    const heads = within(table).getAllByRole("columnheader").map((h) => h.textContent);
    const at = (head: string) => (table.querySelector('[data-episode="1x12"]') as HTMLElement).querySelectorAll("td")[heads.indexOf(head)] as HTMLElement;
    expect(at("Subtitles").textContent).toBe("English, Arabic +34");
    expect(within(at("Subtitles")).getByTitle(/^English, Arabic, Bulgarian, .*, Vietnamese$/)).toBeTruthy();
    expect(at("Audio languages").textContent).toBe("English, French, German +2");
    expect(at("Languages").textContent).toBe("English, French, German +1");
    for (const head of ["Subtitles", "Audio languages", "Languages"]) expect(at(head).className, head).toContain("whitespace-nowrap");
    expect(at("Formats").className).toContain("min-w-[20rem]");
    expect(at("Formats").className).toContain("[&_span]:whitespace-nowrap");
    expect(at("Formats").textContent).toBe(formats.map((f) => `${f.name}+100`).join(""));
  });

  it("links an episode's Held on to its torrent for admins, as the episode window does", async () => {
    const hash = "a".repeat(40);
    const withHash = { ...episodes, episodes: episodes.episodes.map((e) => ({ ...e, versions: { ...e.versions, "anime-web": cell(true, { cached_on: ["rd"], info_hash: hash }) } })) };
    mockFetch([{ ...routes[0], reply: { ...routes[0].reply, permissions: ["see_files", "manage", "admin"] } }, { method: "GET", path: "/titles/1/episodes", reply: withHash }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await user.click(await screen.findByRole("radio", { name: "Anime WEB" }));
    const links = within(screen.getByTestId("episode-table")).getAllByTestId("torrent-link");
    expect(links).toHaveLength(3);
    expect(links[0].getAttribute("href")).toBe(`/admin/torrents?q=${hash.slice(0, 12)}`);
  });

  it("drops the ticked episodes when the library in view changes", async () => {
    const api = mockFetch(acting);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await user.click(await screen.findByRole("radio", { name: "Anime Blu-ray" }));
    await user.click(within(document.querySelector('[data-episode="1x12"]') as HTMLElement).getByRole("checkbox", { name: "Select episode 12" }));
    expect(screen.getByTestId("bulk-scope").textContent).toBe("1 selected · Anime Blu-ray");
    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    expect(screen.queryByTestId("bulk-bar")).toBeNull();
    await user.click(within(document.querySelector('[data-episode="1x13"]') as HTMLElement).getByRole("checkbox", { name: "Select episode 13" }));
    await user.click(screen.getByTestId("bulk-unmonitor"));
    await vi.waitFor(() => expect(api.calls("PUT", "/titles/1/episodes/monitor")).toHaveLength(1));
    expect(api.calls("PUT", "/titles/1/episodes/monitor")[0].body).toEqual({ monitored: false, versions: ["anime-web"], episode_ids: [102] });
  });

  it("does not bring old ticks back after a round trip through another view", async () => {
    const api = mockFetch(acting);
    const user = userEvent.setup();
    const tick = async (ep: string, n: number) => user.click(within(document.querySelector(`[data-episode="${ep}"]`) as HTMLElement).getByRole("checkbox", { name: `Select episode ${n}` }));
    renderPage(<Seasons id="1" anime />, "/title/1");
    await screen.findByRole("button", { name: /Episode 12/ });
    await tick("1x12", 12);
    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    await user.click(screen.getByRole("radio", { name: "All libraries" }));
    expect(screen.queryByTestId("bulk-bar")).toBeNull();
    await tick("1x13", 13);
    expect(screen.getByTestId("bulk-scope").textContent).toBe("1 selected · every library");
    await user.click(screen.getByTestId("bulk-search"));
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/episodes/search")).toHaveLength(1));
    expect(api.calls("POST", "/titles/1/episodes/search")[0].body).toEqual({ episode_ids: [102] });
  });

  it("does not bring old ticks back after a resize to phone and back", async () => {
    let listener: (() => void) | undefined;
    const mql = { matches: true, addEventListener: (_: string, l: () => void) => (listener = l), removeEventListener: vi.fn() };
    vi.stubGlobal("matchMedia", () => mql);
    const api = mockFetch(acting);
    const user = userEvent.setup();
    const tick = async (ep: string, n: number) => user.click(within(document.querySelector(`[data-episode="${ep}"]`) as HTMLElement).getByRole("checkbox", { name: `Select episode ${n}` }));
    renderPage(<Seasons id="1" anime />, "/title/1");
    await screen.findByRole("button", { name: /Episode 12/ });
    await tick("1x12", 12);
    const resize = async (wide: boolean) => {
      mql.matches = wide;
      await act(async () => listener?.());
    };
    await resize(false);
    expect(screen.queryByTestId("bulk-bar")).toBeNull();
    await resize(true);
    expect(screen.queryByTestId("bulk-bar")).toBeNull();
    await tick("1x13", 13);
    await user.click(screen.getByTestId("bulk-search"));
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/episodes/search")).toHaveLength(1));
    expect(api.calls("POST", "/titles/1/episodes/search")[0].body).toEqual({ episode_ids: [102] });
  });

  it("acts only on ticked episodes the season's filter shows, and drops the hidden ones", async () => {
    const api = mockFetch(acting);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    await screen.findByRole("button", { name: /Episode 12/ });
    await user.click(within(screen.getByTestId("episode-table")).getByRole("checkbox", { name: "Select every episode shown" }));
    expect(screen.getByTestId("bulk-scope").textContent).toBe("3 selected · every library");
    await user.click(within(screen.getByTestId("season-filters")).getByRole("button", { name: /Missing/ }));
    expect(screen.getByTestId("bulk-scope").textContent).toBe("2 selected · every library");
    await user.click(screen.getByTestId("bulk-unmonitor"));
    await vi.waitFor(() => expect(api.calls("PUT", "/titles/1/episodes/monitor")).toHaveLength(1));
    expect(api.calls("PUT", "/titles/1/episodes/monitor")[0].body).toEqual({ monitored: false, episode_ids: [102, 103] });
    // Back under All, the episode the filter hid stays unticked.
    await user.click(within(screen.getByTestId("season-filters")).getByRole("button", { name: /^All/ }));
    expect(screen.getByTestId("bulk-scope").textContent).toBe("2 selected · every library");
    expect((screen.getByRole("checkbox", { name: "Select episode 12" }) as HTMLInputElement).checked).toBe(false);
  });

  it("drops the ticks of a season that is collapsed", async () => {
    const two = {
      ...episodes,
      seasons: [...episodes.seasons, { ...episodes.seasons[0], season: 2 }],
      episodes: [...episodes.episodes, { ...episodes.episodes[0], id: 201, season: 2, number: 1, name: "Second Season" }],
    };
    const api = mockFetch([...acting.filter((r) => r.path !== "/titles/1/episodes"), { method: "GET", path: "/titles/1/episodes", reply: two }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    // Season 2 is open first (the latest aired); open season 1 too.
    await user.click(await screen.findByRole("button", { name: "Season 1" }));
    await user.click(screen.getByRole("checkbox", { name: "Select episode 12" }));
    await user.click(screen.getByRole("button", { name: "Season 1" }));
    expect(screen.queryByTestId("bulk-bar")).toBeNull();
    await user.click(screen.getByRole("checkbox", { name: "Select episode 1" }));
    expect(screen.getByTestId("bulk-scope").textContent).toBe("1 selected · every library");
    await user.click(screen.getByTestId("bulk-search"));
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/episodes/search")).toHaveLength(1));
    expect(api.calls("POST", "/titles/1/episodes/search")[0].body).toEqual({ episode_ids: [201] });
    // Opened again, season 1's episode stays unticked.
    await user.click(screen.getByRole("button", { name: "Season 1" }));
    expect((screen.getByRole("checkbox", { name: "Select episode 12" }) as HTMLInputElement).checked).toBe(false);
  });

  it("names the library in view on the bulk bar and acts only there", async () => {
    const api = mockFetch(acting);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    const tick = async () => user.click(within(document.querySelector('[data-episode="1x13"]') as HTMLElement).getByRole("checkbox", { name: "Select episode 13" }));
    await screen.findByRole("button", { name: /Episode 13/ });
    await tick();
    expect(screen.getByTestId("bulk-scope").textContent).toBe("1 selected · every library");
    await user.click(screen.getByRole("radio", { name: "Anime Blu-ray" }));
    await tick();
    expect(screen.getByTestId("bulk-scope").textContent).toBe("1 selected · Anime Blu-ray");
    // Anime Blu-ray is read-only and may not search: nothing reaches Anime WEB from here.
    expect((screen.getByTestId("bulk-monitor") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("bulk-search") as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    await tick();
    expect(screen.getByTestId("bulk-scope").textContent).toBe("1 selected · Anime WEB");
    await user.click(screen.getByTestId("bulk-search"));
    await vi.waitFor(() => expect(api.calls("POST", "/titles/1/episodes/search")).toHaveLength(1));
    expect(api.calls("POST", "/titles/1/episodes/search")[0].body).toEqual({ versions: ["anime-web"], episode_ids: [102] });
  });
});

describe("Episode window", () => {
  it("has Details, History and Last search tabs, with the file's facts and Held on open in Details", async () => {
    const withFile = { ...detail, versions: [detail.versions[0], { ...detail.versions[1], file: { quality: "WEBDL-1080p", release_title: "Tsukikage.Signal.S01E13.1080p.WEB-DL-NTb", cached_on: ["rd"], score: 4200, instance: "theatarr" }, history: [] }] };
    mockFetch([routes[0], routes[1], { method: "GET", path: "/titles/1/episodes/102", reply: withFile }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1?episode=102");
    const dialog = await screen.findByRole("dialog");
    expect((await within(dialog).findAllByRole("tab")).map((t) => t.textContent)).toEqual(["Details", "History", "Last search"]);
    const file = await within(dialog).findByTestId("episode-file");
    expect(within(file).getByText("Tsukikage.Signal.S01E13.1080p.WEB-DL-NTb")).toBeTruthy();
    expect(within(file).getByText("Held on")).toBeTruthy();
    expect(within(file).getByText("RD")).toBeTruthy();
    // The score through the number format; Theatarr's own link has no "From" row.
    expect(within(file).getByText("4,200")).toBeTruthy();
    expect(within(file).queryByText("From")).toBeNull();
    await user.click(within(dialog).getByRole("tab", { name: "History" }));
    expect(within(dialog).getAllByTestId("episode-history")).toHaveLength(2);
    expect(within(dialog).queryByTestId("episode-file")).toBeNull();
  });

  it("says Nothing stored once on Last search when no result is stored", async () => {
    const none = { title_id: 1, version: "anime-web", versions: ["anime-web"], live_allowed: false, source: "none", results: 0, accepted: 0, candidates: [], rejections: {}, outcomes: {}, grab: false,
      grab_state: { paused: false, pause_forced: false, will_grab: false } };
    mockFetch([routes[0], routes[1], { method: "GET", path: "/titles/1/episodes/102", reply: detail }, { method: "GET", path: "/titles/1/episodes/102/releases", reply: none }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1?episode=102");
    const dialog = await screen.findByRole("dialog");
    await user.click(await within(dialog).findByRole("tab", { name: "Last search" }));
    expect((await within(dialog).findAllByText("Nothing stored")).length).toBeGreaterThan(0);
    expect(within(dialog).queryByText("No releases stored for this episode.")).toBeNull();
  });

  it("says an episode's Add to other providers adds the whole pack when the file came in one", async () => {
    const withFile = { ...detail, versions: [detail.versions[0], { ...detail.versions[1], file: { quality: "WEBDL-1080p", cached_on: ["rd"], add_to: ["tb"], info_hash: "a".repeat(40) }, history: [] }] };
    mockFetch([routes[0], routes[1], { method: "GET", path: "/titles/1/episodes/102", reply: withFile }]);
    renderPage(<Seasons id="1" anime />, "/title/1?episode=102");
    const file = await within(await screen.findByRole("dialog")).findByTestId("episode-file");
    expect(within(file).getByTestId("add-to-providers").getAttribute("title")).toContain("this file's torrent (the whole pack, when it came in one) to");
  });
});

describe("One status vocabulary", () => {
  // Episode 101 held below its preferred quality with nothing better found; 102 held with an upgrade found; 103 an
  // aired gap in a library that is not monitored.
  const below = { state: "below_cutoff", quality: "WEBDL-1080p", score: 1500, cutoff: { quality: "WEBDL-1080p", score: 10000, upgrades: true } };
  const words = {
    title_id: 1,
    versions: [
      { id: 2, key: "anime-web", label: "Anime WEB", monitored: true, control: managed },
      { id: 3, key: "anime-off", label: "Anime old", monitored: false, control: managed },
    ],
    seasons: [{
      season: 1, episodes: 3, aired: 3,
      versions: {
        "anime-web": { have: 3, missing: 0, below_cutoff: 3, upgrade_seen: 1, monitored: 3, upgrade: below },
        "anime-off": { have: 0, missing: 0, below_cutoff: 0, upgrade_seen: 0, monitored: 3 },
      },
    }],
    episodes: [
      { id: 101, season: 1, number: 1, name: "One", air_date: "2026-10-01", aired: true, monitored: true, versions: { "anime-web": cell(true, { state: "filled" }), "anime-off": cell(false) } },
      { id: 102, season: 1, number: 2, name: "Two", air_date: "2026-10-08", aired: true, monitored: true, versions: { "anime-web": cell(true, { state: "filled", upgrade_seen: true }), "anime-off": cell(false) } },
      { id: 103, season: 1, number: 3, name: "Three", air_date: "2026-10-15", aired: true, monitored: true, versions: { "anime-web": cell(true, { state: "filled" }), "anime-off": cell(false) } },
    ],
  };
  const window101 = {
    title_id: 1, title_name: "Tsukikage Signal", episode: words.episodes[0],
    versions: [
      { key: "anime-web", label: "Anime WEB", state: "filled", monitored: true, library_monitored: true, has_file: true, control: managed, upgrade: below },
      { key: "anime-off", label: "Anime old", state: "wanted", monitored: true, library_monitored: false, has_file: false, control: managed },
    ],
  };
  it("says the same words in the season header, the episode cells and the episode window", async () => {
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: words }, { method: "GET", path: "/titles/1/episodes/101", reply: window101 }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime={false} />, "/title/1");
    await screen.findByText("One");
    // The matrix: Available in the held library (Upgrade found beside it for E02), Not monitored, never Missing, in the
    // library that is not monitored.
    const row = (n: number) => screen.getByRole("button", { name: new RegExp(`^Episode ${n}:`) });
    expect(within(row(1)).getAllByText("Available")).toHaveLength(1);
    expect(within(row(1)).getByText("Not monitored")).toBeTruthy();
    expect(within(row(2)).getByTestId("cell-upgrade").textContent).toBe("Upgrade found");
    expect(screen.queryByText("Missing")).toBeNull();
    expect(screen.queryByText("Preferred quality not met")).toBeNull();
    expect(within(screen.getByTestId("season-header")).getByTestId("season-summary").textContent).toBe("3 of 3 available · 1 upgrade found");
    // One library in view: the season header's quality word, and its Why? headed by the same word.
    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    const header = screen.getByTestId("season-header");
    expect(within(header).getByTestId("upgrade-status").textContent).toContain("Preferred quality not met");
    expect(within(header).getByTestId("season-facts").textContent).toContain("1 upgrade found");
    await user.click(within(header).getByTestId("upgrade-why"));
    const why = await screen.findByTestId("upgrade-why-dialog");
    expect(within(why).getByRole("heading").textContent).toBe("Preferred quality not met");
    // Three plain lines: the file held, where upgrades stop, and what was found.
    expect([...within(why).getByTestId("why-lines").querySelectorAll("p")].map((p) => p.textContent)).toEqual([
      "You have: WEBDL-1080p, score 1,500.",
      "Upgrades stop at: WEBDL-1080p and a score of 10,000.",
      "No better release seen yet.",
    ]);
    await user.click(within(why).getByRole("button", { name: "Close" }));
    // The episode window says what the row says: Available, then the quality word with Why?; Not monitored for the
    // library that is not monitored.
    await user.click(screen.getByRole("button", { name: /^Episode 1:/ }));
    const dialog = await screen.findByRole("dialog");
    // The window's status line across libraries, and the library's own word.
    expect((await within(dialog).findByTestId("episode-status")).textContent).toBe("Available");
    expect(within(dialog).getAllByText("Available")).toHaveLength(2);
    expect(within(dialog).getByTestId("upgrade-status").textContent).toContain("Preferred quality not met");
    expect(within(dialog).getByText("Not monitored")).toBeTruthy();
    expect(within(dialog).queryByText("Missing")).toBeNull();
  });

  it("says in the season's Why? that the season pack found was tried, from the library's history", async () => {
    const release = "Tsukikage.Signal.S01.2160p.WEB-DL-GRP";
    const seen = new Date(Date.now() - 2 * 86400000).toISOString();
    const found = { ...below, state: "upgrade_available", next: { release, quality: "WEBDL-2160p", score: 6200, seen_at: seen } };
    const pack = { ...words, seasons: [{ ...words.seasons[0], versions: { ...words.seasons[0].versions, "anime-web": { ...words.seasons[0].versions["anime-web"], upgrade: found } } }] };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: pack }]);
    const user = userEvent.setup();
    const history = { "anime-web": [{ event: "failed", date: seen, release_title: release, reason: "Not cached on RD, AD and TB", missed_on: ["rd", "ad", "tb"], upgrade: true }] };
    renderPage(<Seasons id="1" anime={false} history={history as never} />, "/title/1");
    await screen.findByText("One");
    await user.click(screen.getByRole("radio", { name: "Anime WEB" }));
    await user.click(within(screen.getByTestId("season-header")).getByTestId("upgrade-why"));
    const why = await screen.findByTestId("upgrade-why-dialog");
    const lines = [...within(why).getByTestId("why-lines").querySelectorAll("p")].map((p) => p.textContent);
    expect(lines[2]).toMatch(/Better release found .+: Tsukikage\.Signal\.S01\.2160p\.WEB-DL-GRP \(WEBDL-2160p, score 6,200\)\. Tried .+: not cached on RD, AD and TB\. Your file is unchanged\.$/);
  });

  it("says Better pack found beside an episode whose find is a pack that keeps some episodes' files", async () => {
    const mixed = { ...below, state: "upgrade_available", next: { release: "Tsukikage.Signal.S01.2160p.WEB-DL-MIX", quality: "WEBDL-2160p", score: 4000,
      reason: "better quality: WEBDL-2160p over WEBDL-1080p; 1 episode(s) keep their file", seen_at: new Date().toISOString() } };
    const pack = { ...words, episodes: words.episodes.map((e) => e.id === 102 ? { ...e, versions: { ...e.versions, "anime-web": cell(true, { state: "filled", upgrade_seen: true, upgrade: mixed }) } } : e) };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: pack }]);
    renderPage(<Seasons id="1" anime={false} />, "/title/1");
    await screen.findByText("One");
    const row = screen.getByRole("button", { name: /^Episode 2:/ });
    expect(within(row).getByTestId("cell-upgrade").textContent).toBe("Better pack found");
  });

  it("gives a library that does not monitor the title no Upgrade found, and keeps its closed-link warning in the window", async () => {
    const held = { ...words, episodes: words.episodes.map((e) => ({ ...e, versions: { ...e.versions, "anime-off": cell(true, { state: "filled", upgrade_seen: true }) } })) };
    const linksOff = { ...managed, grab_state: { paused: false, pause_forced: false, will_grab: true, library_writes: false } };
    const win = { ...window101, versions: [window101.versions[0], { ...window101.versions[1], has_file: true, state: "filled", control: linksOff }] };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: held }, { method: "GET", path: "/titles/1/episodes/101", reply: win }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime={false} />, "/title/1");
    await screen.findByText("One");
    await user.click(screen.getByRole("radio", { name: "Anime old" }));
    await screen.findByText("One");
    expect(screen.queryByTestId("cell-upgrade")).toBeNull();
    // Nor in the cell's tooltip.
    for (const cellEl of document.querySelectorAll('[data-col="status"]')) expect(cellEl.getAttribute("title") ?? "").not.toMatch(/Upgrade found|Preferred quality/);
    await user.click(screen.getByRole("button", { name: /^Episode 1:/ }));
    const dialog = await screen.findByRole("dialog");
    expect((await within(dialog).findByTestId("episode-library-off")).textContent).toContain("Anime old is not monitored for this title");
    expect(within(dialog).getByTestId("links-off")).toBeTruthy();
  });
});

describe("Seasons order and kept monitoring (J5)", () => {
  it("lists seasons newest first, specials last", async () => {
    const { newestFirst } = await import("./seasons");
    expect(newestFirst([{ season: 0 }, { season: 1 }, { season: 3 }, { season: 2 }]).map((s) => s.season)).toEqual([3, 2, 1, 0]);
  });

  it("says an unmonitored library keeps its switches, drawn inactive, not unchecked", async () => {
    const off = { ...episodes, versions: [{ ...episodes.versions[1], monitored: false }] };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: off }]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    expect((await screen.findByTestId("episodes-kept")).textContent).toContain("Anime WEB is not monitored");
    const kept = await screen.findAllByTestId("switch-kept");
    expect(kept.length).toBeGreaterThan(0);
    // The stored flag stays on: the season's switch is checked, only drawn inactive.
    expect(within(kept[0]).getByRole("switch").getAttribute("aria-checked")).toBe("true");
  });

  it("gives an episode's checkbox a touch area that never opens the episode", async () => {
    mockFetch(routes);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime />, "/title/1");
    const box = await screen.findByRole("checkbox", { name: "Select episode 12" });
    const area = box.closest("[data-testid=episode-check]") as HTMLElement;
    await user.click(area);
    expect((box as HTMLInputElement).checked).toBe(true);
    expect(window.location.search).not.toContain("episode=");
  });
});

describe("Episode window status (J5)", () => {
  it("reads Not monitored, not Missing, for an episode whose only library is not monitored", async () => {
    const one = { ...episodes, versions: [{ ...episodes.versions[1], monitored: false }] };
    const missing = { ...episodes.episodes[2], versions: { "anime-web": cell(false) } };
    const win = { ...detail, episode: missing, versions: [{ key: "anime-web", label: "Anime WEB", state: "wanted", monitored: true, library_monitored: false, has_file: false, control: managed }] };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: { ...one, episodes: [missing] } }, { method: "GET", path: "/titles/1/episodes/103", reply: win }]);
    renderPage(<Seasons id="1" anime />, "/title/1?episode=103");
    const status = await screen.findByTestId("episode-status");
    expect(status.getAttribute("data-status")).toBe("unmonitored");
    expect(within(await screen.findByTestId("episode-meta")).getByText(/Aired/)).toBeTruthy();
  });

  it("shows the episode's synopsis under its air date, to a household account too (K11)", async () => {
    const household = { method: "GET", path: "/auth/me", reply: { id: 2, username: "kid", permissions: [], csrf_token: "t" } };
    const win = { ...detail, overview: "The lantern goes out." };
    mockFetch([household, routes[1], { method: "GET", path: "/titles/1/episodes/102", reply: win }]);
    renderPage(<Seasons id="1" anime />, "/title/1?episode=102");
    expect((await screen.findByTestId("episode-synopsis")).textContent).toBe("The lantern goes out.");
  });
});

describe("Jump to season (H6)", () => {
  const many = (n: number) => ({
    ...episodes,
    seasons: [0, ...Array.from({ length: n }, (_, i) => i + 1)].map((season) => ({ season, episodes: 1, aired: 1, versions: { "anime-web": { have: 0, missing: 1, below_cutoff: 0, upgrade_seen: 0, monitored: 1 } } })),
    episodes: [0, ...Array.from({ length: n }, (_, i) => i + 1)].map((season) => ({ id: 500 + season, season, number: 1, name: `Opener ${season}`, air_date: "2026-01-01", aired: true, monitored: true, versions: { "anime-web": cell(false) } })),
  });

  it("opens and focuses the season picked, Specials included, on a series with more than 8 seasons", async () => {
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: many(10) }]);
    const user = userEvent.setup();
    renderPage(<Seasons id="1" anime={false} />, "/title/1");
    const jump = await screen.findByRole("combobox", { name: "Jump to season" });
    expect([...(jump as HTMLSelectElement).options].map((o) => o.textContent).slice(1, 3)).toEqual(["Season 10", "Season 9"]);
    const head = (n: number) => document.querySelector(`[data-season="${n}"] button[aria-expanded]`) as HTMLElement;
    expect(head(3).getAttribute("aria-expanded")).toBe("false");
    await user.selectOptions(jump, "3");
    await vi.waitFor(() => expect(document.activeElement).toBe(head(3)));
    expect(head(3).getAttribute("aria-expanded")).toBe("true");
    expect(head(3).id).toBe("season-heading-3");
    expect(screen.getByText("Opener 3")).toBeTruthy();
    // The latest season stays open; the selector is back on its prompt.
    expect(head(10).getAttribute("aria-expanded")).toBe("true");
    expect((jump as HTMLSelectElement).value).toBe("");
    await user.selectOptions(jump, "0");
    await vi.waitFor(() => expect(document.activeElement).toBe(head(0)));
    expect(head(0).getAttribute("aria-expanded")).toBe("true");
  });

  it("is not offered on a series with 8 seasons or fewer", async () => {
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: many(8) }]);
    renderPage(<Seasons id="1" anime={false} />, "/title/1");
    await screen.findAllByTestId("season-header");
    expect(screen.queryByRole("combobox", { name: "Jump to season" })).toBeNull();
  });
});
