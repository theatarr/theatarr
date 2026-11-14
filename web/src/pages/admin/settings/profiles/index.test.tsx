// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { QualityOverview } from "@/api/settings-profiles";
import { overview } from "@/components/settings/fixtures";
import { expectNamedControls, json, mockFetch, renderPage } from "@/test/page";
import { ProfilesPage } from ".";
import { parseSize, profileFlavours, setSize } from "./sizes";
import type { Config } from "../quality/fields";

const webConfig = {
  qualities: [{ group: "WEB 2160p", qualities: ["WEBRip-2160p", "WEBDL-2160p"] }, "Remux-1080p", { group: "WEB 1080p", qualities: ["WEBRip-1080p", "WEBDL-1080p"] }],
  quality_order: [{ group: "WEB 2160p", qualities: ["WEBRip-2160p", "WEBDL-2160p"] }, "Remux-1080p", "Bluray-1080p", { group: "WEB 1080p", qualities: ["WEBRip-1080p", "WEBDL-1080p"] }],
  cutoff: "WEB 2160p", formats: { "WEB Tier 01": 1700, "DD+": 2500, "Our Group": 150 }, min_score: 0, cutoff_score: 10000, min_upgrade_step: 1, upgrades: true, format_set: "radarr-4k-web",
};
const qualityOverview = (o: Partial<QualityOverview> = {}): QualityOverview => ({
  source: "db", generation: 4, trash: ["radarr"], categories: ["movie", "series", "anime_movie", "anime_series"], defaults: { movie: "radarr-4k-web" },
  profiles: [
    {
      id: 7, key: "radarr-4k-web", flavour: "radarr", qualities: ["WEB 2160p", "Remux-1080p", "WEB 1080p"], cutoff: "WEB 2160p", format_set: "radarr-4k-web", formats: 3,
      used_by: [{ key: "movie-web", mode: "active", kind: "movie", anime: false, active: true }, { key: "movie-web-shadow", mode: "shadow", kind: "movie", anime: false, active: false }],
      origin: { preset: "0537bd27bcf8da604f30a862343b1742", name: "WEB 2160p", flavour: "radarr", modifiers: ["fallbacks_1080p"] }, guessed: true,
      diff: { preset: "WEB 2160p + 1080p fallbacks", qualities_added: [], qualities_removed: ["Bluray-1080p"], cutoff: null, settings: [], formats_added: [{ format: "DD+", trash_id: "abc", ours: 2500, trash: null }, { format: "Our Group", ours: 150, trash: null }], formats_missing: [], scores_changed: [], summary: ["− Bluray-1080p", "DD+ +2500 (not in the preset)", "Our Group +150 (ours)"], same: false },
      binding: "trash-radarr", score_set: "default",
      scores: [
        { format: "WEB Tier 01", trash_id: "c20f", score: 1700, trash: 1700, state: "follows", definition: "trash", group: "Tiers and repacks (from TRaSH profiles)" },
        { format: "DD+", trash_id: "abc", score: 2500, trash: 1750, state: "pinned", definition: "trash", group: "[Audio] Audio Formats" },
        { format: "Our Group", score: 150, trash: null, state: "ours", definition: "ours", group: "Our formats" },
      ],
    },
    { id: 8, key: "web-2160p", flavour: "", qualities: ["WEBDL-2160p"], cutoff: "WEBDL-2160p", format_set: "", formats: 0, used_by: [], origin: null, guessed: false, diff: null, binding: "", score_set: "", scores: [] },
  ],
  formats: [],
  ...o,
});
const records = { generation: 4, records: [{ id: 7, key: "radarr-4k-web", config: webConfig }, { id: 8, key: "web-2160p", config: { qualities: ["WEBDL-2160p"], cutoff: "WEBDL-2160p", formats: {} } }] };
// The page lists the TRaSH defaults of every flavour that has a tree, so it reads the presets on load.
const routes = (ov = qualityOverview(), settings = overview()) => [
  { method: "GET", path: "/settings", reply: settings },
  { method: "GET", path: "/settings/quality/overview", reply: ov },
  { method: "GET", path: "/settings/profiles", reply: records },
  { method: "GET", path: "/settings/presets", reply: (request: { search: string }) => request.search.includes("sonarr") ? sonarrPresets : presets },
];
const presets = { flavour: "radarr", presets: [
  { trash_id: "fd16", name: "Remux + WEB 2160p", flavour: "radarr", description: "Remux and WEB", group: 1, qualities: ["Remux-2160p", "WEB 2160p"], cutoff: "Remux-2160p", formats: 56, modifiers: ["fallbacks_1080p", "bluray_2160p", "block_dv_no_hdr"] },
  { trash_id: "0537", name: "WEB 2160p", flavour: "radarr", description: "WEBDL 2160p", group: 2, qualities: ["WEB 2160p"], cutoff: "WEB 2160p", formats: 39, modifiers: ["fallbacks_1080p", "block_dv_no_hdr"] },
] };
const sonarrPresets = { flavour: "sonarr", source: "bundled", commit: "ed6d7226232c6194730c945938985dfa68c75e6b", commit_date: "2026-10-26", presets: [
  { trash_id: "72dae", name: "WEB-1080p", flavour: "sonarr", description: "", group: 1, qualities: ["WEB 1080p"], cutoff: "WEB 1080p", formats: 30, modifiers: [] },
] };
const built = {
  profile: { qualities: [{ group: "WEB 2160p", qualities: ["WEBRip-2160p", "WEBDL-2160p"] }, "Remux-1080p"], quality_order: [{ group: "WEB 2160p", qualities: ["WEBRip-2160p", "WEBDL-2160p"] }, "Remux-1080p", "Bluray-1080p"], cutoff: "WEB 2160p", formats: { "WEB Tier 01": 1700, AMZN: 0 }, min_score: 0, cutoff_score: 10000, min_upgrade_step: 1, upgrades: true },
  origin: { preset: "0537", name: "WEB 2160p", flavour: "radarr", modifiers: ["fallbacks_1080p"] }, trash_ids: { "WEB Tier 01": "c20f", AMZN: "b3b3" },
  groups: { "WEB Tier 01": "Tiers and repacks (from TRaSH profiles)", AMZN: "[Streaming Services] General" }, unscored: ["AMZN"], notes: [], formats: 2,
};

describe("Quality profiles page", () => {
  it("lists qualities best first with the cutoff, the origin with our edits, the versions using each profile and unused ones", async () => {
    mockFetch(routes());
    renderPage(<ProfilesPage />, "/admin/settings/profiles");
    const row = (await screen.findAllByText("radarr-4k-web"))[0].closest("tr")!;
    const chips = within(row).getAllByRole("list", { name: "Allowed qualities, best first" })[0];
    expect(within(chips).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["WEB 2160p · cutoff", "Remux-1080p", "WEB 1080p"]);
    expect(within(row).getAllByText("TRaSH WEB 2160p + 1080p fallbacks").length).toBeGreaterThan(0);
    expect(within(row).getAllByText("closest").length).toBeGreaterThan(0);
    expect(within(row).getAllByText("DD+ +2500 (not in the preset)").length).toBeGreaterThan(0);
    expect(within(row).getAllByText("WEB · Live").length).toBeGreaterThan(0);
    const unused = screen.getAllByText("web-2160p")[0].closest("tr")!;
    expect(within(unused).getAllByText("unused").length).toBeGreaterThan(0);
    expect(within(unused).getAllByText("Our own").length).toBeGreaterThan(0);
    // Format sets are no longer a page of their own.
    expect(screen.getByRole("link", { name: "format set editor" }).getAttribute("href")).toBe("/admin/settings/profiles/format_sets");
  });

  it("says why presets are off and links the raw profile editor when no TRaSH tree could be read", async () => {
    mockFetch(routes(qualityOverview({ trash: [], trash_note: "THEATARR_TRASH is set but holds no TRaSH-Guides checkout, and the bundled TRaSH defaults could not be read" })));
    renderPage(<ProfilesPage />, "/admin/settings/profiles");
    const note = await screen.findByTestId("trash-note");
    expect(note.textContent).toContain("THEATARR_TRASH is set but holds no TRaSH-Guides checkout");
    expect(within(note).getByRole("link", { name: "raw profile editor" }).getAttribute("href")).toBe("/admin/settings/profiles?raw=1");
    expect((screen.getByRole("button", { name: "New profile from TRaSH preset" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByTestId("trash-defaults")).toBeNull();
  });

  it("lists every TRaSH default of both flavours from the bundle, with our copies, and reports a broken checkout", async () => {
    const copy = { ...qualityOverview().profiles[1], key: "my-web", origin: { preset: "0537", name: "WEB 2160p", flavour: "radarr" }, guessed: false };
    mockFetch(routes(qualityOverview({
      trash: ["radarr", "sonarr"], trash_source: "bundled", trash_commit: "ed6d7226232c6194730c945938985dfa68c75e6b", trash_commit_date: "2026-10-26",
      trash_note: "THEATARR_TRASH is set but holds no TRaSH-Guides checkout: the TRaSH defaults bundled with this version are used instead",
      profiles: [...qualityOverview().profiles, copy],
    })));
    renderPage(<ProfilesPage />, "/admin/settings/profiles");
    const section = await screen.findByTestId("trash-defaults");
    expect(within(section).getByTestId("trash-source").textContent).toBe("TRaSH defaults bundled with this version (commit ed6d722, 2026-10-26). This list changes with a new version of Theatarr, or follows a TRaSH-Guides checkout when one is mounted (THEATARR_TRASH). Quality sync updates the profiles bound to a source, not this list.");
    expect(within(section).getByRole("link", { name: "Quality sync" }).getAttribute("href")).toBe("/admin/settings/quality-sync");
    const radarr = await within(section).findByTestId("trash-defaults-radarr");
    await within(radarr).findByText("Remux + WEB 2160p");
    expect(within(radarr).getAllByRole("row").slice(1).map((r) => r.getAttribute("data-preset"))).toEqual(["fd16", "0537"]);
    // A stored copy is a hint on the default, which stays listed; a guessed origin is not a copy.
    const used = within(radarr).getByText("WEB 2160p").closest("tr")!;
    expect(within(used).getAllByText("my-web").length).toBeGreaterThan(0);
    expect(within(used).getByRole("button", { name: "Create a copy: WEB 2160p (Movies (Radarr))" })).toBeTruthy();
    expect(within(radarr).getByRole("button", { name: "Use: Remux + WEB 2160p (Movies (Radarr))" })).toBeTruthy();
    const sonarr = within(section).getByTestId("trash-defaults-sonarr");
    expect(await within(sonarr).findByRole("button", { name: "Use: WEB-1080p (Series (Sonarr))" })).toBeTruthy();
    // The bundle is in use, so presets are on; the misconfigured checkout is still reported.
    expect(screen.getByTestId("trash-note").textContent).toContain("THEATARR_TRASH is set but holds no TRaSH-Guides checkout");
    expect((screen.getByRole("button", { name: "New profile from TRaSH preset" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("uses a TRaSH default: the editor opens on a copy under a free key and creates it", async () => {
    const fetch = mockFetch([
      ...routes(),
      { method: "POST", path: "/settings/presets/build", reply: { ...built, origin: { preset: "0537", name: "WEB 2160p", flavour: "radarr" } } },
      { method: "POST", path: "/settings/presets/create", reply: json({ generation: 5, key: "web-2160p-2", binding: "" }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "Use: WEB 2160p (Movies (Radarr))" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/presets/build")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/build")[0].body).toEqual({ flavour: "radarr", preset: "0537", modifiers: [] });
    const sheet = await screen.findByTestId("profile-sheet");
    expect(within(sheet).getByText("TRaSH WEB 2160p")).toBeTruthy();
    // "web-2160p" is a profile already: the suggested key is the next free one.
    expect((within(sheet).getByRole("textbox", { name: "Key" }) as HTMLInputElement).value).toBe("web-2160p-2");
    await user.click(within(sheet).getByRole("button", { name: "Create profile" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create")[0].body).toEqual({ generation: 4, key: "web-2160p-2", flavour: "radarr", preset: "0537", modifiers: [], config: built.profile });
  });

  it("suggests a key no format set has, also a set left behind by a deleted profile", async () => {
    // The sets are read at each Use, not once when the page loads: the set appears while the page is open.
    let sets: { id: number; key: string; config: object }[] = [];
    const fetch = mockFetch([
      ...routes(),
      { method: "GET", path: "/settings/format_sets", reply: () => json({ generation: 4, records: sets }) },
      { method: "POST", path: "/settings/presets/build", reply: { ...built, origin: { preset: "0537", name: "WEB 2160p", flavour: "radarr" } } },
    ]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    const use = await screen.findByRole("button", { name: "Use: WEB 2160p (Movies (Radarr))" });
    sets = [{ id: 3, key: "web-2160p-2", config: {} }];
    await user.click(use);
    const sheet = await screen.findByTestId("profile-sheet");
    expect(fetch.calls("GET", "/settings/format_sets")).toHaveLength(1);
    // "web-2160p" is a profile and "web-2160p-2" a format set no profile uses.
    expect((within(sheet).getByRole("textbox", { name: "Key" }) as HTMLInputElement).value).toBe("web-2160p-3");
  });

  it("saves the defaults Add version preselects", async () => {
    const fetch = mockFetch([...routes(), { method: "PUT", path: "/settings/profile-defaults", reply: json({ source: "db", defaults: { movie: "radarr-4k-web", series: "web-2160p" }, categories: [] }) }]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    const card = await screen.findByTestId("profile-defaults");
    expect((within(card).getByRole("combobox", { name: "Movies" }) as HTMLSelectElement).value).toBe("radarr-4k-web");
    await user.selectOptions(within(card).getByRole("combobox", { name: "Series" }), "web-2160p");
    await user.click(within(card).getByRole("button", { name: "Save defaults" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/profile-defaults")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/profile-defaults")[0].body).toEqual({ defaults: { movie: "radarr-4k-web", series: "web-2160p" } });
  });

  it("builds a profile from a TRaSH preset with modifiers, edits it and creates it", async () => {
    const fetch = mockFetch([
      ...routes(),
      { method: "POST", path: "/settings/presets/build", reply: built },
      { method: "POST", path: "/settings/presets/create", reply: json({ generation: 5, key: "movies-web", binding: "trash-radarr" }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "New profile from TRaSH preset" }));
    const picker = await screen.findByTestId("preset-picker");
    await user.click(within(picker).getByRole("radio", { name: "WEB 2160p" }));
    // Only the modifiers that change this preset are offered.
    expect(within(picker).queryByRole("checkbox", { name: "Allow Bluray-2160p encodes" })).toBeNull();
    await user.click(within(picker).getByRole("checkbox", { name: "Add 1080p fallbacks below the top quality" }));
    await user.click(within(picker).getByRole("button", { name: "Continue to editor" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/presets/build")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/build")[0].body).toEqual({ flavour: "radarr", preset: "0537", modifiers: ["fallbacks_1080p"] });
    expect(fetch.calls("GET", "/settings/presets")[0].search).toBe("?flavour=radarr");

    const sheet = await screen.findByTestId("profile-sheet");
    expectNamedControls();
    expect(within(sheet).getByText("TRaSH WEB 2160p + 1080p fallbacks")).toBeTruthy();
    // Scores carry their state: TRaSH's follow, the unscored one is ours.
    const tier = within(sheet).getByText("WEB Tier 01").closest("li")!;
    expect(within(tier).getByText("follows TRaSH")).toBeTruthy();
    expect(within(within(sheet).getByText("AMZN").closest("li")!).getByText("no TRaSH score")).toBeTruthy();
    // The ladder: allow Bluray-1080p (it lands in order), move the cutoff.
    await user.selectOptions(within(sheet).getByRole("combobox", { name: "Allow another quality" }), "Bluray-1080p");
    await user.click(within(sheet).getByRole("radio", { name: "Cutoff at Remux-1080p" }));
    const score = within(sheet).getByRole("spinbutton", { name: "Score for WEB Tier 01" });
    await user.clear(score);
    await user.type(score, "2000");
    expect(within(tier).getByText("yours")).toBeTruthy();
    await user.type(within(sheet).getByRole("textbox", { name: "Key" }), "movies-web");
    await user.click(within(sheet).getByRole("button", { name: "Create profile" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create")[0].body).toEqual({
      generation: 4, key: "movies-web", flavour: "radarr", preset: "0537", modifiers: ["fallbacks_1080p"],
      config: { ...built.profile, qualities: [...built.profile.qualities, "Bluray-1080p"], cutoff: "Remux-1080p", formats: { "WEB Tier 01": 2000, AMZN: 0 } },
    });
  });

  it("shows the impact of an edit before saving and confirms a change that widens an active version", async () => {
    const effects = [{ version: "movie-web", effect: "Profile radarr-4k-web now allows Bluray-1080p for active version movie-web" }];
    let puts = 0;
    const fetch = mockFetch([
      ...routes(),
      { method: "POST", path: "/settings/profiles/impact", reply: { generation: 4, key: "radarr-4k-web", versions: [], available: true, impact: { files: 5, changed: 3, score_changed: 2, cutoff_changed: 0, upgrade_changed: 1, examples: [{ title: "Dune", version: "movie-web", file: "Dune.mkv", before_score: 4200, after_score: 3200, before_cutoff_met: true, after_cutoff_met: true, before_upgrade: false, after_upgrade: true }] } } },
      { method: "PUT", path: "/settings/profiles/7", reply: () => ++puts === 1 ? json({ error: "explicit confirmation required", effects, confirmation: { generation: 4, token: "profile-confirmation", effects } }, 422) : json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "Edit radarr-4k-web" }));
    const sheet = await screen.findByTestId("profile-sheet");
    expect(within(sheet).getByTestId("profile-affects").textContent).toContain("WEB (Live)");
    // Compare with TRaSH lists our edits against the preset.
    await user.click(within(sheet).getByRole("button", { name: "Compare with TRaSH" }));
    expect(within(within(sheet).getByTestId("origin-diff")).getByText("Bluray-1080p")).toBeTruthy();
    // Grouped as TRaSH groups them, a score of yours marked.
    expect(within(sheet).getByText("[Audio] Audio Formats", { exact: false })).toBeTruthy();
    expect(within(within(sheet).getByText("DD+").closest("li")!).getByText("yours")).toBeTruthy();
    // A score links to the format's definition on Custom formats.
    expect(within(sheet).getByRole("link", { name: "DD+" }).getAttribute("href")).toBe("/admin/settings/formats?filter=DD%2B");
    await user.selectOptions(within(sheet).getByRole("combobox", { name: "Allow another quality" }), "Bluray-1080p");
    expect(within(sheet).queryByRole("button", { name: "Save" })).toBeNull();
    await user.click(within(sheet).getByRole("button", { name: "Review and save" }));
    const impact = await within(sheet).findByTestId("profile-impact");
    expect(impact.textContent).toContain("3 files in the library would be decided differently");
    expect(impact.textContent).toContain("Dune");
    const sent = fetch.calls("POST", "/settings/profiles/impact")[0].body as { config: { qualities: unknown[] } };
    expect(sent).toMatchObject({ generation: 4, key: "radarr-4k-web" });
    expect(sent.config.qualities).toContain("Bluray-1080p");
    expect(fetch.calls("PUT", "/settings/profiles/7")).toHaveLength(0);
    await user.click(within(sheet).getByRole("button", { name: "Save" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effects[0].effect)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/profiles/7")).toHaveLength(2));
    const [first, second] = fetch.calls("PUT", "/settings/profiles/7");
    expect(second.headers["x-confirmation-token"]).toBe("profile-confirmation");
    expect(second.body).toEqual(first.body);
    expect((first.body as { config: unknown }).config).toEqual({ ...webConfig, qualities: [webConfig.qualities[0], "Remux-1080p", "Bluray-1080p", webConfig.qualities[2]] });
  });

  it("adds a standalone custom format to the scores at 0, and takes a typed negative score", async () => {
    const format = (key: string, flavour: string) => ({ name: key, names: [key], flavour, definition: "ours" as const, group: "Our formats", scopes: ["global"], id: 3, key, scored_by: [] });
    const fetch = mockFetch([
      ...routes(qualityOverview({ formats: [format("Mine", "radarr"), format("Our Group", "radarr"), format("Series only", "sonarr")] })),
      { method: "POST", path: "/settings/profiles/impact", reply: { generation: 4, key: "radarr-4k-web", versions: [], available: true, impact: { files: 0, changed: 0, score_changed: 0, cutoff_changed: 0, upgrade_changed: 0, examples: [] } } },
    ]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "Edit radarr-4k-web" }));
    const sheet = await screen.findByTestId("profile-sheet");
    const picker = within(sheet).getByRole("combobox", { name: "Add a custom format" });
    // Only unscored formats of the profile's flavour are offered.
    expect(within(picker).getAllByRole("option").map((o) => o.textContent)).toEqual(["Choose a format", "Mine"]);
    await user.selectOptions(picker, "Mine");
    await user.click(within(sheet).getByRole("button", { name: "Add format" }));
    const score = within(sheet).getByRole("spinbutton", { name: "Score for Mine" });
    expect((score as HTMLInputElement).value).toBe("0");
    expect(within(sheet).queryByTestId("add-format")).toBeNull();
    await user.clear(score);
    await user.type(score, "-30");
    await user.click(within(sheet).getByRole("button", { name: "Review and save" }));
    await within(sheet).findByTestId("profile-impact");
    const sent = fetch.calls("POST", "/settings/profiles/impact")[0].body as { config: { formats: Record<string, number> } };
    expect(sent.config.formats).toEqual({ ...webConfig.formats, Mine: -30 });
  });

  it("edits size limits per quality, the same sizes map the raw editor edits", async () => {
    const sized = { ...webConfig, sizes: { "Remux-1080p": { min: 5, preferred: 0, max: 100, preferred_zero: true }, "WEBRip-1080p": { min: 2, preferred: 0, max: 0 } } };
    const fetch = mockFetch([
      ...routes().map((r) => r.path === "/settings/profiles" ? { ...r, reply: { ...records, records: [{ id: 7, key: "radarr-4k-web", config: sized }, records.records[1]] } } : r),
      { method: "POST", path: "/settings/profiles/impact", reply: { generation: 4, key: "radarr-4k-web", versions: [], available: false, reason: "no files", impact: { files: 0, changed: 0, score_changed: 0, cutoff_changed: 0, upgrade_changed: 0, examples: [] } } },
      { method: "PUT", path: "/settings/profiles/7", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "Edit radarr-4k-web" }));
    const sizes = within(await screen.findByTestId("profile-sheet")).getByTestId("size-limits");
    // A group's members each get a row, best first.
    expect([...sizes.querySelectorAll("[data-quality]")].map((li) => li.getAttribute("data-quality"))).toEqual(["WEBRip-2160p", "WEBDL-2160p", "Remux-1080p", "WEBRip-1080p", "WEBDL-1080p"]);
    const box = (label: string) => within(sizes).getByRole("textbox", { name: label }) as HTMLInputElement;
    // No entry: the arr defaults, blank with the default in the placeholder.
    expect([box("Max size for WEBDL-1080p").value, box("Max size for WEBDL-1080p").placeholder, box("Preferred size for WEBDL-1080p").placeholder]).toEqual(["", "100 default", "95 default"]);
    // An entry: its effective values; preferred_zero is a preferred size of 0, max 0 and plain preferred 0 are none.
    expect([box("Min size for Remux-1080p").value, box("Preferred size for Remux-1080p").value, box("Max size for Remux-1080p").value]).toEqual(["5", "0", "100"]);
    expect([box("Preferred size for WEBRip-1080p").value, box("Max size for WEBRip-1080p").value]).toEqual(["none", "none"]);
    await user.type(box("Min size for WEBDL-2160p"), "35");
    await user.type(box("Max size for WEBDL-1080p"), "200");
    await user.clear(box("Preferred size for Remux-1080p"));
    await user.clear(box("Min size for WEBRip-1080p"));
    await user.click(screen.getByRole("button", { name: "Review and save" }));
    await user.click(await screen.findByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/profiles/7")).toHaveLength(1));
    // Each edit keeps the row's other effective values: the defaults it had, or the entry's.
    expect((fetch.calls("PUT", "/settings/profiles/7")[0].body as { config: { sizes: unknown } }).config.sizes).toEqual({
      "WEBDL-2160p": { min: 35, preferred: 0, max: 0 },
      "WEBDL-1080p": { min: 0, preferred: 95, max: 200 },
      "Remux-1080p": { min: 5, preferred: 0, max: 100 },
      "WEBRip-1080p": { min: 0, preferred: 0, max: 0 },
    });
  });

  it("round-trips size limits against the engine's reading of the sizes map", () => {
    const cfg = (sizes: unknown) => ({ qualities: ["WEBRip-1080p"], sizes }) as Config;
    // Missing entry: one field changes, the other defaults (Sonarr WEBRip-1080p 4 / 95 / 130) are written with it.
    expect(setSize(cfg({}), "sonarr", "WEBRip-1080p", "max", 200).sizes).toEqual({ "WEBRip-1080p": { min: 4, preferred: 95, max: 200 } });
    // Partial entry: missing fields are 0 to the engine, so they stay 0.
    expect(setSize(cfg({ "WEBRip-1080p": { max: 200 } }), "sonarr", "WEBRip-1080p", "min", 5).sizes).toEqual({ "WEBRip-1080p": { min: 5, preferred: 0, max: 200 } });
    // Explicit unlimited is none, kept apart from the default.
    expect(setSize(cfg({}), "sonarr", "WEBRip-1080p", "max", null).sizes).toEqual({ "WEBRip-1080p": { min: 4, preferred: 95, max: 0 } });
    // preferred_zero: typed 0 sets it, clearing the field drops it.
    expect(setSize(cfg({}), "sonarr", "WEBRip-1080p", "preferred", 0).sizes).toEqual({ "WEBRip-1080p": { min: 4, preferred: 0, max: 130, preferred_zero: true } });
    expect(setSize(cfg({ "WEBRip-1080p": { min: 4, preferred: 0, max: 130, preferred_zero: true } }), "sonarr", "WEBRip-1080p", "preferred", undefined).sizes).toEqual({});
    // Back to every default removes the entry.
    expect(setSize(cfg({ "WEBRip-1080p": { min: 4, preferred: 95, max: 200 } }), "sonarr", "WEBRip-1080p", "max", undefined).sizes).toEqual({});
  });

  it("asks whose defaults an unused profile's sizes start from, and blocks saving a size that is not a number", async () => {
    const fetch = mockFetch([
      ...routes(),
      { method: "POST", path: "/settings/profiles/impact", reply: { generation: 4, key: "web-2160p", versions: [], available: false, reason: "no files", impact: { files: 0, changed: 0, score_changed: 0, cutoff_changed: 0, upgrade_changed: 0, examples: [] } } },
      { method: "PUT", path: "/settings/profiles/8", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "Edit web-2160p" }));
    const sizes = within(await screen.findByTestId("profile-sheet")).getByTestId("size-limits");
    // No library uses it: no defaults and no fields until a flavour is picked (not saved).
    expect(within(sizes).queryByRole("textbox")).toBeNull();
    await user.click(within(sizes).getByRole("radio", { name: "Series" }));
    const min = within(sizes).getByRole("textbox", { name: "Min size for WEBDL-2160p" }) as HTMLInputElement;
    expect(min.placeholder).toBe("35 default");
    await user.type(min, "12x");
    await user.tab();
    expect(min.value).toBe("12x");
    expect(within(sizes).getByRole("alert").textContent).toContain("A size is a number");
    expect((screen.getByRole("button", { name: "Review and save" }) as HTMLButtonElement).disabled).toBe(true);
    await user.clear(min);
    await user.type(min, "12,5");
    expect(within(sizes).queryByRole("alert")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Review and save" }));
    await user.click(await screen.findByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/profiles/8")).toHaveLength(1));
    expect((fetch.calls("PUT", "/settings/profiles/8")[0].body as { config: { sizes: unknown } }).config.sizes).toEqual({ "WEBDL-2160p": { min: 12.5, preferred: 95, max: 0 } });
  });

  it("shows a profile used by movie and series libraries read-only, pointing at the raw editor", async () => {
    const ov = qualityOverview();
    ov.profiles[0] = { ...ov.profiles[0], used_by: [...ov.profiles[0].used_by, { key: "tv", mode: "active", kind: "series", anime: false, active: true }] };
    mockFetch(routes(ov));
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "Edit radarr-4k-web" }));
    const sizes = within(await screen.findByTestId("profile-sheet")).getByTestId("size-limits");
    expect(within(sizes).getByTestId("sizes-mixed").textContent).toContain("Used by movie and series libraries, whose default sizes differ");
    expect(within(sizes).getByRole("link", { name: "raw editor" }).getAttribute("href")).toBe("/admin/settings/profiles?raw=1&edit=radarr-4k-web");
    const max = within(sizes).getByRole("textbox", { name: "Max size for WEBDL-1080p" }) as HTMLInputElement;
    expect([max.disabled, max.placeholder]).toEqual([true, "default"]);
  });

  it("reads sizes typed with a decimal comma and the flavours of the libraries using a profile", () => {
    expect([parseSize("max", "12,5"), parseSize("min", "12x"), parseSize("min", "none"), parseSize("max", "0"), parseSize("preferred", "0")]).toEqual([12.5, false, false, null, 0]);
    expect(profileFlavours(["movie", "anime", "series", "movie"])).toEqual(["radarr", "sonarr"]);
  });

  it("offers delete only for an unused profile", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await user.click(await screen.findByRole("button", { name: "Edit radarr-4k-web" }));
    expect(within(await screen.findByTestId("profile-sheet")).queryByRole("button", { name: "Delete" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "Edit web-2160p" }));
    expect(within(await screen.findByTestId("profile-sheet")).getByRole("button", { name: "Delete" })).toBeTruthy();
  });

  it("is read-only while settings cannot be written", async () => {
    const fetch = mockFetch(routes(qualityOverview(), overview({ writable: false })));
    const user = userEvent.setup();
    renderPage(<ProfilesPage />);
    await waitFor(() => expect((screen.getByRole("button", { name: "New profile from TRaSH preset" }) as HTMLButtonElement).disabled).toBe(true));
    expect((within(screen.getByTestId("profile-defaults")).getByRole("button", { name: "Save defaults" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: "View radarr-4k-web" }));
    const sheet = await screen.findByTestId("profile-sheet");
    expect((within(sheet).getByRole("spinbutton", { name: "Score for DD+" }) as HTMLInputElement).disabled).toBe(true);
    expect((within(sheet).getByRole("button", { name: "Review and save" }) as HTMLButtonElement).disabled).toBe(true);
    expect(fetch.requests.filter((r) => r.method !== "GET")).toHaveLength(0);
  });
});

it("retries deleting an unused profile with the server confirmation token", async () => {
  const effects = [{ version: "", effect: "A startup fallback left a different profile running." }];
  const fetch = mockFetch([...routes(), { method: "DELETE", path: "/settings/profiles/8", reply: (r: { headers: Record<string, string> }) =>
    r.headers["x-confirmation-token"] === "profile-delete-proof" ? json({ generation: 5 }) : json({ error: "explicit confirmation required", effects, confirmation: { token: "profile-delete-proof" } }, 422) }]);
  const user = userEvent.setup();
  renderPage(<ProfilesPage />);
  await user.click(await screen.findByRole("button", { name: "Edit web-2160p" }));
  await user.click(within(await screen.findByTestId("profile-sheet")).getByRole("button", { name: "Delete" }));
  await user.click(within(await screen.findByRole("dialog", { name: /Delete profile/ })).getByRole("button", { name: "Confirm delete" }));
  await user.click(within(await screen.findByRole("dialog", { name: "Confirm what this change enables" })).getByRole("button", { name: "Confirm and save" }));
  await waitFor(() => expect(fetch.calls("DELETE", "/settings/profiles/8")).toHaveLength(2));
  const [first, retry] = fetch.calls("DELETE", "/settings/profiles/8");
  expect(retry.headers["x-confirmation-token"]).toBe("profile-delete-proof");
  expect(retry.body).toEqual(first.body);
  expect(retry.search).toBe(first.search);
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Confirm what this change enables" })).toBeNull());
});
