// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Seasons } from "./seasons";
import { expectNamedControls, mockFetch, renderPage } from "@/test/page";

const followed = { mode: "shadow", managed_by: "sonarr_anime", can_edit: false, edit_reason: "Managed by sonarr_anime until cutover", can_search: false, live_releases: false };
const managed = { mode: "active", can_edit: true, can_search: true, live_releases: true };
const downloading = { stage: "downloading", words: "Downloading (37%)", progress: 37, download_id: 9, downloads: 1, episodes: 1, updated_at: "2026-10-29T00:00:00Z" };

const cell = (has: boolean, extra = {}) => ({ state: has ? "cutoff_met" : "wanted", monitored: true, has_file: has, quality: has ? "WEBDL-1080p" : undefined, ...extra });
const episodes = {
  title_id: 1,
  versions: [
    { id: 1, key: "anime-tv", label: "Anime Blu-ray", monitored: true, control: followed },
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
    { id: 101, season: 1, number: 12, absolute: 12, name: "Paper Lantern", air_date: "2026-10-01", monitored: true, versions: { "anime-tv": cell(true), "anime-web": cell(true) } },
    { id: 102, season: 1, number: 13, absolute: 13, name: "Copper Harbour", air_date: "2026-10-08", monitored: true, versions: { "anime-tv": cell(false, { acquisition: downloading }), "anime-web": cell(true) } },
    { id: 103, season: 1, number: 14, absolute: 14, name: "Iron Harbour", air_date: "2026-10-15", monitored: true, versions: { "anime-tv": cell(false), "anime-web": cell(true) } },
  ],
};
const detail = {
  title_id: 1,
  title_name: "Tsukikage Signal",
  episode: episodes.episodes[1],
  versions: [
    { key: "anime-tv", label: "Anime Blu-ray", state: "wanted", monitored: true, has_file: false, control: followed },
    { key: "anime-web", label: "Anime WEB", state: "cutoff_met", monitored: true, has_file: true, control: managed },
  ],
};
const routes = [
  { method: "GET", path: "/auth/me", reply: { id: 1, username: "owner", permissions: ["see_files", "manage"], csrf_token: "t" } },
  { method: "GET", path: "/titles/1/episodes", reply: episodes },
  { method: "GET", path: "/titles/1/episodes/102", reply: detail },
];

describe("Series page copies", () => {
  it("shows each copy's season bar, stages, ownership and the episode stage with anime numbering", async () => {
    mockFetch(routes);
    renderPage(<Seasons id="1" anime />, "/title/1");
    const bars = await screen.findByTestId("season-copies");
    const tv = within(bars).getByText("Anime Blu-ray").closest("[data-season-copy]") as HTMLElement;
    expect(within(tv).getByText("1 / 3")).toBeTruthy();
    expect(within(tv).getByTestId("copy-flight")).toBeTruthy();
    expect(within(tv).getByTestId("season-stages").textContent).toBe("1 queued · 1 downloading");
    expect(await screen.findByText("mirrors sonarr_anime")).toBeTruthy();
    expect(screen.getByText("Theatarr-managed")).toBeTruthy();
    expect(screen.getByText("E13 · absolute 13")).toBeTruthy();
    const row = screen.getByRole("button", { name: /Episode 13/ });
    expect(within(row).getByText("Downloading · 37%")).toBeTruthy();
    expect(within(row).getByRole("progressbar")).toBeTruthy();
    expectNamedControls();
  });

  it("shows the per-library season bars only to accounts that see files; others keep the season summary", async () => {
    mockFetch(routes);
    const first = renderPage(<Seasons id="1" anime />, "/title/1");
    expect(await screen.findByTestId("season-copies")).toBeTruthy();
    first.unmount();
    vi.unstubAllGlobals();
    mockFetch([{ ...routes[0], reply: { id: 9, username: "family", permissions: [], csrf_token: "t" } }, routes[1]]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    expect(await screen.findByTestId("season-summary")).toBeTruthy();
    expect(screen.queryByTestId("season-copies")).toBeNull();
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

  it("tells same-named copies apart in the bars, the summary, the notices and the episode table", async () => {
    const same = { ...episodes, versions: episodes.versions.map((v) => ({ ...v, label: "Anime" })) };
    mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: same }]);
    renderPage(<Seasons id="1" anime />, "/title/1");
    const bars = await screen.findByTestId("season-copies");
    expect(within(bars).getByText("Anime · anime-tv")).toBeTruthy();
    expect(within(bars).getByText("Anime · anime-web")).toBeTruthy();
    expect(screen.queryByText("Anime")).toBeNull();
    expect(screen.getByText(/2 missing in Anime · anime-tv/)).toBeTruthy();
    expect(screen.getByText(/Monitoring is read-only \(Anime · anime-tv\)/)).toBeTruthy();
    expect(screen.getAllByText("Anime · anime-web").length).toBeGreaterThan(1);
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

it.each([
  [false, true, "Settings › Download clients", "/admin/settings/downloads", "New downloads are paused for every library", "downloads-off-notice"],
  [true, false, "Settings › Libraries", "/admin/settings/libraries", "Anime WEB: Downloads off", "episode-restrictions"],
] as const)("links the closed download gate (router %s, library grab %s) without policy keys", async (router_enabled, grab, name, href, words, testid) => {
  const control = { ...managed, can_search: false, search_reason: router_enabled ? "version grab is off: nothing is searched or grabbed" : "router.enabled is false: nothing is searched or grabbed", grab_state: { mode: "active", grab, router_enabled, router_forced_off: false, will_grab: false, library_enabled: true } };
  mockFetch([{ ...routes[0], reply: { id: 1, permissions: ["admin", "manage", "see_files"] } }, { method: "GET", path: "/titles/1/episodes", reply: { ...episodes, versions: [{ ...episodes.versions[1], control }] } }]);
  renderPage(<Seasons id="1" anime />, "/title/1");
  // Paused or locked off for every library is the page notice, like other pages; not a line in the restrictions.
  const note = await screen.findByTestId(testid);
  if (testid === "downloads-off-notice") expect(screen.queryByTestId("episode-restrictions")).toBeNull();
  expect(note.textContent).toContain(words);
  expect(note.textContent).not.toContain("router.enabled");
  expect(within(note).getByRole("link", { name }).getAttribute("href")).toBe(href);
});

it("names phone library radios distinctly", async () => {
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
  mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: { ...episodes, versions: episodes.versions.map(v => ({ ...v, label: "Anime" })) } }]);
  renderPage(<Seasons id="1" anime />, "/title/1");
  expect(await screen.findByRole("radio", { name: "Anime · anime-tv" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Anime · anime-web" })).toBeTruthy();
});

it("leaves season settings destinations as text for non-admin managers", async () => {
  mockFetch([routes[0], { method: "GET", path: "/titles/1/episodes", reply: { ...episodes, versions: [{ ...episodes.versions[1], control: { ...managed, can_search: false } }] } }]);
  renderPage(<Seasons id="1" anime />, "/title/1");
  const note = await screen.findByTestId("episode-restrictions");
  expect(note.textContent).toBe("Anime WEB: Downloads off — Settings › Libraries");
  expect(within(note).queryByRole("link")).toBeNull();
});
