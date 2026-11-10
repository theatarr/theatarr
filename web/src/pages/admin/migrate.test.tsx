// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Inventory, Plan, Step, VerifyReport } from "@/api/migrate";
import { expectNamedControls, json, mockFetch, renderPage, type Route } from "@/test/page";
import { AdvancedPage } from "./advanced";
import { MigratePage } from "./migrate";

const at = "2026-10-30T12:00:00Z";
const key = "invented-arr-key";
const checkpoint = { at, backup: "/backups/before-migrate", settings: "/backups/before-migrate.settings.yaml", generation: 4 };
const quality: Step = { id: "quality/hd", kind: "quality", what: "Create HD profile", why: "Keep the arr scores", reversible: true, undo: "Delete HD in Settings", automatic: true, status: "pending" };
const version: Step = { id: "version/movies", kind: "version", what: "Create movies version in shadow", why: "Compare before handing over", reversible: true, undo: "Delete movies in Versions", automatic: true, status: "pending", depends_on: [quality.id] };
const manual: Step = { id: "seerr/1", kind: "manual", what: "Switch Seerr later", why: "A human performs cutover", reversible: true, undo: "Point Seerr back", automatic: false, status: "manual" };
const blocked: Step = { id: "version/local", kind: "version", what: "Local version", why: "Local paths need checking", reversible: true, undo: "Delete local", automatic: true, status: "pending", blocked: "Add a path mapping and plan again" };
const inventory: Inventory = {
  schema_version: 1, inspected_at: at,
  arr: [{ name: "radarr", flavour: "radarr", url: "http://radarr:7878", titles: { total: 12, monitored: 10, with_files: 9, anime: 0 }, root_folders: [{ path: "/media/movies", titles: 12 }], quality_profiles: [{ id: 1, name: "HD", titles: 12 }], indexers: [{ name: "Public feed", protocol: "torrent" }], download_clients: [{ name: "Bridge", implementation: "QBittorrent", protocol: "torrent" }] }],
  prowlarr: { url: "http://prowlarr:9696", indexers: [{ id: 1, name: "Public feed", protocol: "torrent" }, { id: 2, name: "Leave out", protocol: "torrent" }], applications: [{ name: "Radarr sync" }] },
  seerr: { url: "http://seerr:5055", radarr: [{ name: "Movies server" }], sonarr: [] },
  recyclarr: { path: "/config/recyclarr.yml", instances: [{ name: "radarr" }] },
};
const plan: Plan = {
  schema_version: 1, id: 1, name: "Household", created_at: at, updated_at: at, options: { name: "Household" },
  libraries: [{ name: "Movies", kind: "movie", versions: [{ key: "movies", instance: "radarr", library: "/media/movies", profile: "HD" }] }],
  indexers: [{ key: "public", name: "Public feed", choice: "prowlarr" }],
  steps: [quality, version, manual, blocked], checkpoints: [],
  gaps: [
    { topic: "secret", subject: "Public feed", what: "Enter its API key in Connections", why: "Source secrets are never copied" },
    { topic: "usenet", subject: "NZB feed", what: "Usenet cannot migrate", why: "Debrid versions use torrents" },
    { topic: "local_library", subject: "Local", what: "Check local files", why: "They are outside the debrid mount" },
  ],
};
const done: Plan = { ...plan, checkpoints: [checkpoint], steps: [{ ...quality, status: "done" }, { ...version, status: "done" }, manual, { ...blocked, automatic: false, status: "manual" }] };
const report: VerifyReport = { schema_version: 1, plan_id: 1, at, pending: ["Enter Public feed's credential"], rollback: checkpoint, versions: [{ key: "movies", instance: "radarr", ready: false, checks: [{ name: "provider", status: "FAIL", message: "No provider enabled", fix: "Test a provider in Connections" }, { name: "profile", status: "PASS", message: "HD is frozen" }], cutover: ["Review shadow agreement before handover"], rollback: ["Return to shadow and point Seerr back"] }] };
const preview = { ...inventory, plan: { ...plan, id: 0 } };
const list: Route = { method: "GET", path: "/migrate/plans", reply: [{ id: 1, name: "Household", created_at: at, updated_at: at, steps: { pending: 3, manual: 1 } }] };
const loaded = (p: Plan = plan): Route[] => [list, { method: "GET", path: "/migrate/plans/1", reply: { plan: p, inventory } }];

async function enterSource(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("Instance 1 URL"), "http://radarr:7878");
  await user.type(screen.getByLabelText("Instance 1 API key"), key);
}

describe("migration wizard", () => {
  it("walks every step, inspects all source kinds, saves choices, applies selected steps and verifies", async () => {
    const user = userEvent.setup();
    const local = vi.spyOn(Storage.prototype, "setItem");
    const fetch = mockFetch([
      { ...list, reply: [] },
      { method: "POST", path: "/migrate/inspect", reply: preview },
      { method: "POST", path: "/migrate/inspect", reply: { ...inventory, plan } },
      { method: "GET", path: "/migrate/plans/1", reply: { plan, inventory } },
      { method: "POST", path: "/migrate/plans/1/apply", reply: done },
      { method: "POST", path: "/migrate/plans/1/verify", reply: report },
    ]);
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate");
    expectNamedControls();
    expect(screen.getByLabelText("Instance 1 API key").getAttribute("type")).toBe("password");
    expect(screen.getByLabelText("Instance 1 API key").getAttribute("autocomplete")).toBe("off");
    await enterSource(user);
    await user.click(screen.getByRole("button", { name: "Add Sonarr/Radarr instance" }));
    await user.type(screen.getByLabelText("Instance 2 name"), "sonarr");
    await user.type(screen.getByLabelText("Instance 2 URL"), "http://sonarr:8989");
    await user.type(screen.getByLabelText("Instance 2 API key"), "invented-sonarr-key");
    await user.type(screen.getByLabelText("Prowlarr URL"), "http://prowlarr:9696");
    await user.type(screen.getByLabelText("Prowlarr API key"), "invented-prowlarr-key");
    await user.type(screen.getByLabelText("Seerr / Overseerr URL"), "http://seerr:5055");
    await user.type(screen.getByLabelText("Seerr / Overseerr API key"), "invented-seerr-key");
    await user.type(screen.getByLabelText("Recyclarr config path (on the Theatarr server)"), "/config/recyclarr.yml");
    await user.click(screen.getByRole("button", { name: "Run inspect" }));
    expect(await screen.findByRole("heading", { name: "Instances (1) · Libraries (1)" })).toBeTruthy();
    expect(screen.getByText("Titles: 12 · Monitored: 10 · With files: 9 · Anime: 0")).toBeTruthy();
    for (const name of ["Root folders (1)", "Profiles (1)", "Download clients (1)", "Credentials to enter (1)", "Usenet (1)", "Local libraries (1)"]) expect(screen.getByRole("heading", { name })).toBeTruthy();
    expect(screen.getByText("Why: Source secrets are never copied")).toBeTruthy();
    expect(fetch.calls("POST", "/migrate/inspect")[0].body).toMatchObject({ sources: { arr: [{ name: "radarr", flavour: "radarr", key: "ARR_0_KEY" }, { name: "sonarr", flavour: "sonarr", key: "ARR_1_KEY" }], prowlarr: { key: "PROWLARR_KEY" }, seerr: { key: "SEERR_KEY" }, recyclarr: "/config/recyclarr.yml" }, keys: { ARR_0_KEY: key, ARR_1_KEY: "invented-sonarr-key", PROWLARR_KEY: "invented-prowlarr-key", SEERR_KEY: "invented-seerr-key" }, save_plan: false });
    await user.click(screen.getByRole("button", { name: "Continue to plan" }));
    await user.type(screen.getByLabelText("Plan name"), "Household");
    await user.selectOptions(screen.getByLabelText("Public feed migration"), "native");
    await user.selectOptions(screen.getByLabelText("Leave out migration"), "skip");
    await user.click(screen.getByRole("button", { name: "Create plan" }));
    expect(await screen.findByRole("heading", { name: "3. Plan · Household" })).toBeTruthy();
    expect(fetch.calls("POST", "/migrate/inspect")[1].body).toMatchObject({ options: { name: "Household", native: ["Public feed"], skip: ["Leave out"] }, save_plan: true });
    expect(screen.getByText("Why: Keep the arr scores")).toBeTruthy();
    expect(screen.getByText("Reversible: yes · Undo: Delete HD in Settings")).toBeTruthy();
    expect((screen.getByRole("checkbox", { name: manual.what }) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("checkbox", { name: blocked.what }) as HTMLInputElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: "Continue to apply" }));
    await user.click(screen.getByRole("button", { name: "Apply selected steps" }));
    expect(await screen.findByText(`Backup: ${checkpoint.backup}`)).toBeTruthy();
    expect(screen.getByText(`Settings: ${checkpoint.settings}`)).toBeTruthy();
    expect(fetch.calls("POST", "/migrate/plans/1/apply")[0].body).toEqual({ steps: [quality.id, version.id], confirm: [] });
    await user.click(screen.getByRole("button", { name: "Continue to verify" }));
    await user.click(screen.getByRole("button", { name: "Run verify" }));
    expect(await screen.findByText("No provider enabled")).toBeTruthy();
    expect(screen.getByText("Next: Test a provider in Connections")).toBeTruthy();
    expect(screen.getByText("Enter Public feed's credential")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Settings › Libraries" }).getAttribute("href")).toBe("/admin/settings/libraries");
    expect(screen.getByRole("link", { name: "Migration comparison (shadow)" }).getAttribute("href")).toBe("/admin/advanced/arr/comparison");
    expect(screen.getByRole("link", { name: "docs/migration.md" }).getAttribute("href")).toContain("docs/migration.md#2-shadow-compare-with-the-arr");
    expect(screen.queryByRole("button", { name: /enable|go live|switch router|switch library/i })).toBeNull();
    expect(fetch.requests.filter((r) => r.path !== "/migrate/inspect").some((r) => JSON.stringify(r).includes(key))).toBe(false);
    expect(local).not.toHaveBeenCalled();
    local.mockRestore();
  }, 60000);

  it("points arr users at Find a setting, not at a file the app does not serve", async () => {
    mockFetch(loaded());
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate");
    expect((await screen.findByRole("link", { name: "Find a setting" })).getAttribute("href")).toBe("/admin/settings");
    expect(screen.queryByText(/arr-map/)).toBeNull();
  });

  it("starts a fresh wizard after saving while keeping the saved plan resumable", async () => {
    const user = userEvent.setup();
    mockFetch([
      ...loaded(),
      { method: "POST", path: "/migrate/inspect", reply: preview },
      { method: "POST", path: "/migrate/inspect", reply: { ...inventory, plan } },
    ]);
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate");
    await enterSource(user);
    await user.click(screen.getByRole("button", { name: "Run inspect" }));
    await user.click(await screen.findByRole("button", { name: "Continue to plan" }));
    await user.click(screen.getByRole("button", { name: "Create plan" }));
    await user.click(await screen.findByRole("link", { name: "Start another migration" }));
    expect(await screen.findByRole("heading", { name: "1. Sources" })).toBeTruthy();
    expect((screen.getByLabelText("Instance 1 API key") as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText("Instance 1 URL") as HTMLInputElement).value).toBe("");
    expect((screen.getByRole("button", { name: "1. Sources" }) as HTMLButtonElement).disabled).toBe(false);
    await user.click(await screen.findByRole("link", { name: "Resume Household (#1)" }));
    expect(await screen.findByRole("heading", { name: "3. Plan · Household" })).toBeTruthy();
  });

  it("lists plans to resume, restores review and maintains dependencies when selecting steps", async () => {
    const user = userEvent.setup();
    mockFetch(loaded());
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate");
    await user.click(await screen.findByRole("link", { name: "Resume Household (#1)" }));
    expect(await screen.findByRole("heading", { name: "3. Plan · Household" })).toBeTruthy();
    await user.click(screen.getByRole("checkbox", { name: quality.what }));
    expect((screen.getByRole("checkbox", { name: version.what }) as HTMLInputElement).checked).toBe(false);
    await user.click(screen.getByRole("checkbox", { name: version.what }));
    expect((screen.getByRole("checkbox", { name: quality.what }) as HTMLInputElement).checked).toBe(true);
    await user.click(screen.getByRole("checkbox", { name: version.what }));
    expect((screen.getByRole("checkbox", { name: quality.what }) as HTMLInputElement).checked).toBe(true);
  });

  it("resumes an in-progress plan at apply and a completed automatic plan at verify", async () => {
    mockFetch(loaded({ ...plan, checkpoints: [checkpoint], steps: [{ ...quality, status: "done" }, version, manual] }));
    const first = renderPage(<MigratePage />, "/admin/advanced/arr/migrate?plan=1");
    expect(await screen.findByRole("heading", { name: "4. Apply · Household" })).toBeTruthy();
    expect(screen.getByText(`Backup: ${checkpoint.backup}`)).toBeTruthy();
    first.unmount();
    mockFetch(loaded(done));
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate?plan=1");
    expect(await screen.findByRole("heading", { name: "5. Verify · Household" })).toBeTruthy();
  });

  it("reviews stored plan confirmations, cancels without applying, and confirms one step at a time", async () => {
    const user = userEvent.setup();
    const waiting: Plan = { ...plan, checkpoints: [checkpoint], steps: [{ ...quality, status: "done" }, { ...version, status: "needs_confirmation", confirmation: { generation: 4, token: "stored-token", effects: [{ version: "movies", effect: "Adds movies to a view" }] } }, manual] };
    const fetch = mockFetch([...loaded({ ...plan, checkpoints: [checkpoint] }),
      { method: "POST", path: "/migrate/plans/1/apply", reply: waiting },
      { method: "POST", path: "/migrate/plans/1/apply", reply: waiting },
      { method: "POST", path: "/migrate/plans/1/apply", reply: done },
    ]);
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate?plan=1");
    await user.click(await screen.findByRole("button", { name: "Apply selected steps" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm migration effects" });
    expect(within(dialog).getByText("movies: Adds movies to a view")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(fetch.calls("POST", "/migrate/plans/1/apply")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Apply selected steps" }));
    await user.click(await screen.findByRole("button", { name: "Confirm and apply" }));
    await waitFor(() => expect(fetch.calls("POST", "/migrate/plans/1/apply")).toHaveLength(3));
    expect(fetch.calls("POST", "/migrate/plans/1/apply")[2]).toMatchObject({ body: { confirm: [version.id], confirmation_token: "stored-token" } });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("handles a 422 confirmation round trip with the same selection and token", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch([...loaded({ ...plan, checkpoints: [checkpoint] }),
      { method: "POST", path: "/migrate/plans/1/apply", reply: json({ error: "confirmation required", step: version.id, confirmation: { generation: 4, token: "fresh-token" }, effects: [{ version: "movies", effect: "Exposes the migrated view" }] }, 422) },
      { method: "POST", path: "/migrate/plans/1/apply", reply: done },
    ]);
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate?plan=1");
    await user.click(await screen.findByRole("button", { name: "Apply selected steps" }));
    expect(await screen.findByText("movies: Exposes the migrated view")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Confirm and apply" }));
    await waitFor(() => expect(fetch.calls("POST", "/migrate/plans/1/apply")).toHaveLength(2));
    const calls = fetch.calls("POST", "/migrate/plans/1/apply");
    expect(calls[1]).toMatchObject({ body: { steps: [quality.id, version.id], confirm: [version.id], confirmation_token: "fresh-token" } });
  });

  it("shows inspect and plan errors with field details, retries, and keeps keys out of browser storage", async () => {
    const user = userEvent.setup();
    const storage = vi.spyOn(Storage.prototype, "setItem");
    const fetch = mockFetch([{ ...list, reply: [] },
      { method: "POST", path: "/migrate/inspect", reply: json({ error: "Source refused", fields: [{ path: "arr[0].url", message: "Use an http(s) URL" }] }, 400) },
      { method: "POST", path: "/migrate/inspect", reply: preview },
      { method: "POST", path: "/migrate/inspect", reply: json({ error: "Plan refused", fields: [{ path: "options.name", message: "Choose a name" }] }, 400) },
    ]);
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate");
    await enterSource(user);
    await user.click(screen.getByRole("button", { name: "Run inspect" }));
    expect((await screen.findByRole("alert")).textContent).toContain("arr[0].url: Use an http(s) URL");
    expect(screen.getByRole("heading", { name: "2. Inspect" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Retry inspect" }));
    await user.click(await screen.findByRole("button", { name: "Continue to plan" }));
    await user.click(screen.getByRole("button", { name: "Create plan" }));
    expect((await screen.findByRole("alert")).textContent).toContain("options.name: Choose a name");
    expect(fetch.calls("POST", "/migrate/inspect")).toHaveLength(3);
    expect(storage).not.toHaveBeenCalled();
    storage.mockRestore();
  });

  it("shows apply and verify API errors in their steps and never applies an empty selection", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch([...loaded({ ...plan, checkpoints: [checkpoint] }),
      { method: "POST", path: "/migrate/plans/1/apply", reply: json({ error: "Backup directory is not writable" }, 500) },
      { method: "POST", path: "/migrate/plans/1/verify", reply: json({ error: "Verification unavailable" }, 503) },
    ]);
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate?plan=1");
    await user.click(await screen.findByRole("button", { name: "Apply selected steps" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Backup directory is not writable");
    await user.click(screen.getByRole("checkbox", { name: quality.what }));
    expect((screen.getByRole("button", { name: "Apply selected steps" }) as HTMLButtonElement).disabled).toBe(true);
    expect(fetch.calls("POST", "/migrate/plans/1/apply")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Continue to verify" }));
    await user.click(screen.getByRole("button", { name: "Run verify" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Verification unavailable");
  });

  it("supports keyboard step navigation and named controls at phone width", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    const user = userEvent.setup();
    mockFetch(loaded());
    renderPage(<MigratePage />, "/admin/advanced/arr/migrate?plan=1");
    const planButton = await screen.findByRole("button", { name: "3. Plan" });
    planButton.focus();
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "4. Apply" }));
    await user.keyboard("{Enter}");
    const heading = screen.getByRole("heading", { name: "4. Apply · Household" });
    expect(document.activeElement).toBe(heading);
    expectNamedControls();
    screen.getByRole("button", { name: "4. Apply" }).focus();
    await user.keyboard("{End}{Enter}");
    expect(screen.getByRole("heading", { name: "5. Verify · Household" })).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "5. Verify · Household" }));
    screen.getByRole("button", { name: "5. Verify" }).focus();
    await user.keyboard("{Home}{Enter}");
    expect(screen.getByRole("heading", { name: "2. Inspect · Household" })).toBeTruthy();
  });

  it("links the migration area from Advanced", () => {
    renderPage(<AdvancedPage />);
    expect(screen.getByRole("link", { name: /Sonarr\/Radarr migration/ }).getAttribute("href")).toBe("/admin/advanced/arr");
  });
});
