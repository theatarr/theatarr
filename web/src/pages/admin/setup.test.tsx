// @vitest-environment jsdom
// The setup screen of a fresh install: what is missing, the admin password, a first version from a TRaSH preset that
// starts in Shadow mode with its downloads off, and the preflight report. Invented keys and paths.
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { SetupState } from "@/api/setup";
import type { VersionList } from "@/api/settings-versions";
import { json, mockFetch, renderPage } from "@/test/page";
import { SetupPage } from "./setup";

const fresh: SetupState = {
  first_run: true, settings_source: "db", admin_password_generated: true, versions: 0, versions_with_roots: 0,
  providers: 1, indexers: 1, profiles: 0, api_keys: 0,
  missing: ["No versions: nothing is searched, grabbed or linked", "No TMDb key: titles cannot be added or matched"],
};
const versions: VersionList = {
  source: "db", generation: 3, versions: [], profiles: {}, format_sets: [], clients: [], indexers: ["feed"], providers: ["rd"],
  switches: { router_enabled: false, library_enabled: false, notify_enabled: false, versions: [] },
};
const preset = { trash_id: "a".repeat(32), name: "HD Bluray + WEB", flavour: "radarr", description: "", group: 1, qualities: [], cutoff: "Bluray-1080p", formats: 12, modifiers: [] };

describe("setup", () => {
  it("links to the migration wizard for an existing arr stack", async () => {
    mockFetch([{ method: "GET", path: "/setup", reply: { ...fresh, first_run: false, versions: 1, versions_with_roots: 1 } }]);
    renderPage(<SetupPage />);
    expect((await screen.findByRole("link", { name: "Migrate from arrs" })).getAttribute("href")).toBe("/admin/advanced/arr/migrate");
    expect(screen.getByText(/^1 library, 1 with a library root/)).toBeTruthy();
  });

  it("lists what is missing and creates a first version from a preset in Shadow mode with its downloads off", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch([
      { method: "GET", path: "/setup", reply: fresh },
      { method: "GET", path: "/settings/versions", reply: versions },
      { method: "GET", path: "/settings/presets", reply: { flavour: "radarr", presets: [preset] } },
      { method: "POST", path: "/settings/versions/validate-root", reply: { root: "/media/movies", resolved: "/media/movies", writable: true } },
      { method: "POST", path: "/settings/presets/create", reply: { generation: 4, key: "movies", binding: "trash", records: [], revision: "r" } },
      { method: "POST", path: "/settings/versions", reply: { generation: 5, revision: "r" } },
    ]);
    renderPage(<SetupPage />);
    expect((await screen.findByTestId("setup-missing")).textContent).toContain("No TMDb key");
    expect(screen.getByText(/still the generated one/)).toBeTruthy();
    expect(screen.getByText(/Keep Prowlarr:/)).toBeTruthy();
    expect(screen.getByText(/Native catalog:/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Admin › Settings" }).getAttribute("href")).toBe("/admin/settings");
    expect(screen.getByText(/Connect the debrid mount/).textContent).toContain("Plex URL and token");

    await user.type(await screen.findByLabelText(/Library root/), "/media/movies");
    await user.click(screen.getByRole("button", { name: "Check root" }));
    expect((await screen.findByTestId("root-check")).textContent).toContain("writable");
    await screen.findByRole("option", { name: "TRaSH preset: HD Bluray + WEB" });
    await user.selectOptions(screen.getByLabelText("Quality profile"), `preset:${preset.trash_id}`);
    await user.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByTestId("version-review").textContent).toContain("Shadow mode with its downloads off");
    expect(screen.getByTestId("version-review").textContent).toContain('This creates a new profile "movies" from the TRaSH preset HD Bluray + WEB and the library movies');
    expect(fetch.calls("POST", "/settings/versions")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Create library" }));

    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create")[0].body).toMatchObject({ generation: 3, key: "movies", flavour: "radarr", preset: preset.trash_id });
    const body = fetch.calls("POST", "/settings/versions")[0].body as { generation: number; key: string; roots: string[]; config: Record<string, unknown> };
    expect(body).toMatchObject({ generation: 4, key: "movies", roots: ["/media/movies"] });
    expect(body.config).toMatchObject({ mode: "shadow", grab: false, kind: "movie", profile: "movies", library: "/media/movies", storage: "debrid", indexers: ["feed"], providers: ["rd"], client: "" });
    await waitFor(() => expect(fetch.calls("GET", "/setup").length).toBeGreaterThan(1));
  });

  it("does not say Create makes a profile that already exists", async () => {
    const user = userEvent.setup();
    mockFetch([
      { method: "GET", path: "/setup", reply: fresh },
      { method: "GET", path: "/settings/versions", reply: { ...versions, profiles: { "hd-1080p": "" } } },
      { method: "GET", path: "/settings/presets", reply: { flavour: "radarr", presets: [preset] } },
    ]);
    renderPage(<SetupPage />);
    await user.type(await screen.findByLabelText(/Library root/), "/media/movies");
    await screen.findByRole("option", { name: "Profile: hd-1080p" });
    await user.selectOptions(screen.getByLabelText("Quality profile"), "hd-1080p");
    await user.click(screen.getByRole("button", { name: "Review" }));
    const review = screen.getByTestId("version-review").textContent ?? "";
    expect(review).toContain("This creates the library movies (movie) with the existing profile hd-1080p in Shadow mode");
    expect(review).not.toContain("creates the profile");
  });

  it("does not call a root not writable when the check did not probe it", async () => {
    const user = userEvent.setup();
    mockFetch([
      { method: "GET", path: "/setup", reply: fresh },
      { method: "GET", path: "/settings/versions", reply: versions },
      { method: "GET", path: "/settings/presets", reply: { flavour: "radarr", presets: [preset] } },
      { method: "POST", path: "/settings/versions/validate-root", reply: { root: "/media/movies", resolved: "/media/movies", writable: false } },
    ]);
    renderPage(<SetupPage />);
    await user.type(await screen.findByLabelText(/Library root/), "/media/movies");
    await user.click(screen.getByRole("button", { name: "Check root" }));
    const check = (await screen.findByTestId("root-check")).textContent ?? "";
    expect(check).toContain("Root resolves to /media/movies");
    expect(check).not.toContain("writable");
  });

  it("renders a complete install whose missing list is null, and links sources to their editors", async () => {
    mockFetch([{ method: "GET", path: "/setup", reply: { ...fresh, first_run: false, admin_password_generated: false, versions: 1, versions_with_roots: 1, missing: null as unknown as string[] } }]);
    renderPage(<SetupPage />);
    expect((await screen.findByRole("link", { name: "Settings › Download clients" })).getAttribute("href")).toBe("/admin/settings/downloads");
    expect(screen.getByRole("link", { name: "Settings › Indexers › Add indexer" }).getAttribute("href")).toBe("/admin/settings/indexers");
    expect(screen.getByRole("link", { name: "Libraries" }).getAttribute("href")).toBe("/admin/settings/libraries");
    expect(screen.queryByTestId("setup-missing")).toBeNull();
    // The counts are the sources that would run, in words that agree with the server's "Off until set up" list.
    expect(screen.getByText(/1 enabled provider with an API key\./)).toBeTruthy();
    expect(screen.getByText(/1 enabled indexer ready to search\./)).toBeTruthy();
    expect(screen.queryAllByRole("link").map((l) => l.getAttribute("href"))).not.toContain("/admin/connections");
  });

  it("does not reuse a preset profile made by a failed attempt once the preset changes", async () => {
    const user = userEvent.setup();
    const other = { ...preset, trash_id: "b".repeat(32), name: "UHD Remux" };
    const fetch = mockFetch([
      { method: "GET", path: "/setup", reply: fresh },
      { method: "GET", path: "/settings/versions", reply: versions },
      { method: "GET", path: "/settings/presets", reply: { flavour: "radarr", presets: [preset, other] } },
      { method: "POST", path: "/settings/presets/create", reply: { generation: 4, key: "movies", binding: "trash", records: [], revision: "r" } },
      { method: "POST", path: "/settings/versions", reply: json({ error: "versions.movies: library root overlaps a forbidden root" }, 422) },
      { method: "POST", path: "/settings/versions", reply: { generation: 5, revision: "r" } },
    ]);
    renderPage(<SetupPage />);
    await user.type(await screen.findByLabelText(/Library root/), "/media/movies");
    await screen.findByRole("option", { name: "TRaSH preset: HD Bluray + WEB" });
    await user.selectOptions(screen.getByLabelText("Quality profile"), `preset:${preset.trash_id}`);
    await user.click(screen.getByRole("button", { name: "Review" }));
    await user.click(screen.getByRole("button", { name: "Create library" }));
    expect((await screen.findByRole("alert")).textContent).toContain("forbidden root");
    expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(1);

    // Another preset under the same key: the profile "movies" is preset A's, so the form asks for another key.
    await user.selectOptions(screen.getByLabelText("Quality profile"), `preset:${other.trash_id}`);
    expect(screen.getByText(/was already created from a different preset/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Review" }) as HTMLButtonElement).disabled).toBe(true);

    // Back to the first preset: the retry reuses its profile and says so.
    await user.selectOptions(screen.getByLabelText("Quality profile"), `preset:${preset.trash_id}`);
    await user.click(screen.getByRole("button", { name: "Review" }));
    expect(screen.getByTestId("version-review").textContent).toContain("created earlier from the TRaSH preset HD Bluray + WEB");
    await user.click(screen.getByRole("button", { name: "Create library" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(2));
    expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(1);
  });

  it("offers existing profiles when there is no TRaSH tree for presets", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch([
      { method: "GET", path: "/setup", reply: { ...fresh, profiles: 1 } },
      { method: "GET", path: "/settings/versions", reply: { ...versions, profiles: { "hd": "hd" } } },
      { method: "GET", path: "/settings/presets", reply: { flavour: "radarr", presets: [], unavailable: "THEATARR_TRASH is set but holds no TRaSH-Guides checkout: TRaSH presets need it" } },
      { method: "POST", path: "/settings/versions", reply: { generation: 4, revision: "r" } },
    ]);
    renderPage(<SetupPage />);
    expect((await screen.findByTestId("presets-off")).textContent).toContain("THEATARR_TRASH is set but holds no TRaSH-Guides checkout");
    await user.type(screen.getByLabelText(/Library root/), "/media/movies");
    await user.selectOptions(screen.getByLabelText("Quality profile"), "hd");
    await user.click(screen.getByRole("button", { name: "Review" }));
    await user.click(screen.getByRole("button", { name: "Create library" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(0);
    expect((fetch.calls("POST", "/settings/versions")[0].body as { config: Record<string, unknown> }).config).toMatchObject({ profile: "hd", mode: "shadow", grab: false });
  });

  it("asks before a save the settings API wants confirmed and sends the token", async () => {
    const user = userEvent.setup();
    const effects = [{ version: "movies", effect: "links into /media/movies" }];
    const fetch = mockFetch([
      { method: "GET", path: "/setup", reply: { ...fresh, profiles: 1 } },
      { method: "GET", path: "/settings/versions", reply: { ...versions, profiles: { "hd": "hd" } } },
      { method: "GET", path: "/settings/presets", reply: { flavour: "radarr", presets: [] } },
      { method: "POST", path: "/settings/versions", reply: json({ error: "confirmation required", effects }, 422) },
      { method: "POST", path: "/settings/versions/confirm", reply: { generation: 3, token: "tok", effects } },
      { method: "POST", path: "/settings/versions", reply: { generation: 4, revision: "r" } },
    ]);
    renderPage(<SetupPage />);
    await user.type(await screen.findByLabelText(/Library root/), "/media/movies");
    await user.selectOptions(screen.getByLabelText("Quality profile"), "hd");
    await user.click(screen.getByRole("button", { name: "Review" }));
    await user.click(screen.getByRole("button", { name: "Create library" }));
    expect(await screen.findByText("movies: links into /media/movies")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Confirm and create" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(2));
    expect(fetch.calls("POST", "/settings/versions")[1].body).toMatchObject({ confirmation_token: "tok", config: { mode: "shadow", grab: false } });
  });

  it("changes the generated admin password", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch([
      { method: "GET", path: "/setup", reply: fresh },
      { method: "GET", path: "/settings/versions", reply: versions },
      { method: "GET", path: "/settings/presets", reply: { flavour: "radarr", presets: [] } },
      { method: "POST", path: "/me/password", reply: { body: undefined, status: 204 } },
    ]);
    renderPage(<SetupPage />);
    await user.type(await screen.findByLabelText("Current password"), "generated");
    await user.type(screen.getByLabelText("New password"), "a-long-new-password");
    await user.type(screen.getByLabelText("New password again"), "a-long-new-password");
    await user.click(screen.getByRole("button", { name: "Change password" }));
    await waitFor(() => expect(fetch.calls("POST", "/me/password")).toHaveLength(1));
    expect(fetch.calls("POST", "/me/password")[0].body).toEqual({ current: "generated", new: "a-long-new-password" });
  });

  it("runs the preflight and shows each check with its next step", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch([
      { method: "GET", path: "/setup", reply: { ...fresh, admin_password_generated: false, versions: 1, versions_with_roots: 1, missing: [] } },
      { method: "POST", path: "/preflight", reply: { at: "2026-10-29T10:00:00Z", ok: 1, warn: 0, fail: 1, items: [
        { area: "library", check: "mount", status: "ok", why: "/mnt/debrid answers" },
        { area: "library", check: "root movies", status: "info", why: "read-only by design: followed by movies-theatarr" },
        { area: "metadata", check: "tmdb", status: "fail", why: "metadata.sources.tmdb is not set", next: "Set metadata.sources.tmdb.api_key_env and the key" },
      ] } },
    ]);
    renderPage(<SetupPage />);
    await user.click(await screen.findByRole("checkbox", { name: /Test providers/ }));
    await user.click(screen.getByRole("button", { name: "Run preflight" }));
    expect((await screen.findByTestId("preflight-summary")).textContent).toBe("1 ok · 0 warn · 1 fail");
    expect(screen.getByTestId("preflight-metadata-tmdb").textContent).toContain("Set metadata.sources.tmdb.api_key_env");
    expect(screen.getByTestId("preflight-library-root movies").querySelector('[data-slot="badge"]')?.getAttribute("data-tone")).toBe("info");
    expect(fetch.calls("POST", "/preflight")[0].body).toEqual({ sources: false });
  });
});
