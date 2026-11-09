// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VersionsPage } from ".";
import { expectNamedControls, json, mockFetch, renderPage } from "@/test/page";

const version = (library = "/library/movies") => ({
  id: 1, key: "movie-web", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", roots: [library],
  config: { mode: "shadow", kind: "movie", anime: false, profile: "web", library, storage: "debrid", indexers: ["public"], providers: ["rd"], client: "radarr", import: "symlink", upgrades: "auto", follows: "" },
});
const versions = (generation = 4, library = "/library/movies", source = "db") => ({
  source, generation, versions: [version(library)], profiles: { web: "web-set" }, format_sets: ["web-set"], clients: ["radarr"], indexers: ["public"], providers: ["rd"],
  switches: { router_enabled: true, library_enabled: false, notify_enabled: true, versions: [{ key: "movie-web", mode: "shadow" }] },
});
const viewBindings = (generation = 4) => ({ source: "db", generation, views: [{ id: 1, key: "radarr", versions: ["movie-web"], profiles: {} }] });
const getRoutes = (versionReply = versions(), viewsReply = viewBindings()) => [
  { method: "GET", path: "/settings/versions", reply: { ...versionReply, switches: { ...versionReply.switches, versions: versionReply.versions.map((v) => ({ key: v.key, mode: v.config.mode, grab_state: { mode: v.config.mode, grab: "grab" in v.config && v.config.grab === true, router_enabled: versionReply.switches.router_enabled, router_forced_off: false } })) } } },
  { method: "GET", path: "/settings/view-bindings", reply: viewsReply },
];
const effects = [
  { version: "movie-web", effect: "Enable writes to /library/movies" },
  { version: "movie-web", effect: "Add provider rd to the active route" },
];

describe("Versions page", () => {
  it("saves curator preference separately from quality and grab settings", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Prefer SeaDex" }), "true");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { prefer_seadex: true, mode: "shadow", profile: "web" } });
  });
  it("saves the provider preference of the cache tie-break", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect((screen.getByRole("combobox", { name: "Provider preference" }) as HTMLSelectElement).value).toBe("ordered");
    await user.selectOptions(screen.getByRole("combobox", { name: "Provider preference" }), "none");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { provider_preference: "none", providers: ["rd"] } });
  });
  it("uses runtime gates for the list and bindings, and previews the draft against the forced-off router", async () => {
    const saved = { ...version(), config: { ...version().config, mode: "active", grab: true } };
    const data = { ...versions(), versions: [saved], switches: {
      ...versions().switches, router_enabled: false, sources: { router: "forced off by environment" },
      versions: [{ key: saved.key, mode: "shadow", grab_state: { mode: "shadow", grab: false, router_enabled: false, router_forced_off: true } }],
    } };
    mockFetch([
      { method: "GET", path: "/settings/versions", reply: data },
      { method: "GET", path: "/settings/view-bindings", reply: viewBindings() },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    const edit = await screen.findByRole("button", { name: "Edit" });
    const row = edit.closest("tr")!;
    expect(within(row).getByText("Shadow")).toBeTruthy();
    expect(within(row).getByText("Preview only")).toBeTruthy();
    await user.click(within(row).getByRole("button", { name: "Why?" }));
    expect([...within(row).getByTestId("state-why").querySelectorAll("li")].map((li) => li.textContent?.split(" · ")[0])).toEqual([
      "Mode is Shadow: it previews and compares, it downloads nothing",
      "Allow downloads for this library is off",
      "New downloads are locked off by the environment (THEATARR_FORCE_OFF)",
    ]);
    // The page says the global state once.
    expect(screen.getByTestId("downloads-off-notice").textContent).toContain("locked off");
    await user.click(edit);
    const form = screen.getByTestId("version-form");
    expect(within(form).getByText("Locked off")).toBeTruthy();
    expect(within(form).getByTestId("links-off").textContent).toBe("New files won't be linked");
    expect(screen.queryByText("Downloads on")).toBeNull();
  });

  it("shows whether an active version grabs and edits its grab switch", async () => {
    const active = versions();
    const remux = { ...version(), id: 2, key: "movie-remux" };
    Object.assign(remux.config, { mode: "active", grab: true });
    active.versions = [{ ...version(), config: { ...version().config, mode: "active" } }, remux];
    const fetch = mockFetch([...getRoutes(active), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ error: "confirmation required", effects: [{ version: "movie-web", effect: "Active version movie-web grabs" }] }, 422) }, { method: "POST", path: "/settings/versions/confirm", reply: json({ generation: 4, token: "t", effects: [] }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    const on = (await screen.findAllByText("Downloads on")).filter((badge) => badge.closest("tr"));
    expect(on.map((badge) => badge.closest("tr")?.getAttribute("data-record"))).toEqual(["movie-remux"]);
    const row = document.querySelector<HTMLElement>('tr[data-record="movie-web"]')!;
    expect(within(row).queryByText("Downloads on")).toBeNull();
    await user.click(within(row).getByRole("button", { name: "Edit" }));
    const grab = screen.getByRole("combobox", { name: /^Allow downloads for this library/ });
    expect((grab as HTMLSelectElement).value).toBe("false");
    await user.selectOptions(grab, "true");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("dialog", { name: "Confirm library change" });
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { grab: true } });
  });

  it("keeps a multi-character create key focused and links to naming", async () => {
    mockFetch(getRoutes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />, "/admin/settings/libraries");

    await user.click(await screen.findByRole("button", { name: "Add library" }));
    expectNamedControls();
    const key = screen.getByRole("textbox", { name: "Key" });
    await user.type(key, "movie-remux");
    expect((key as HTMLInputElement).value).toBe("movie-remux");
    expect(screen.getByRole("link", { name: "Edit naming" }).getAttribute("href")).toBe("/admin/settings/libraries/naming");
  });

  it("lets a debrid version have no download client", async () => {
    const fetch = mockFetch([
      ...getRoutes({ ...versions(), clients: [] }),
      { method: "POST", path: "/settings/versions", reply: json({ generation: 5, revision: "next" }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Add library" }));
    await user.type(screen.getByRole("textbox", { name: "Key" }), "movie-new");
    expect((screen.getByRole("combobox", { name: /^Client/ }) as HTMLSelectElement).value).toBe("");
    expect(within(screen.getByRole("combobox", { name: /^Client/ })).getByRole("option", { name: "None" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/versions")[0].body).toMatchObject({ config: { client: "", storage: "debrid" } });
  });

  it("offers only debrid storage and symlink imports to a library that uses neither seedbox nor hardlink or copy", async () => {
    mockFetch(getRoutes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const options = (name: string) => within(screen.getByRole("combobox", { name })).getAllByRole("option").map((option) => option.textContent);
    expect(options("Storage")).toEqual(["debrid"]);
    expect(options("Import mode")).toEqual(["symlink"]);
  });

  it("keeps a saved seedbox storage and hardlink import, labelled not supported yet, and saves them unchanged", async () => {
    const legacy = { ...version(), config: { ...version().config, storage: "seedbox", import: "hardlink" } };
    const fetch = mockFetch([...getRoutes({ ...versions(), versions: [legacy] }), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const storage = screen.getByRole("combobox", { name: "Storage" }) as HTMLSelectElement;
    const importMode = screen.getByRole("combobox", { name: "Import mode" }) as HTMLSelectElement;
    expect(storage.value).toBe("seedbox");
    expect(importMode.value).toBe("hardlink");
    expect(within(storage).getAllByRole("option").map((option) => option.textContent)).toEqual(["debrid", "seedbox (not supported yet)"]);
    expect(within(importMode).getAllByRole("option").map((option) => option.textContent)).toEqual(["symlink", "hardlink (not supported yet)"]);
    await user.selectOptions(screen.getByRole("combobox", { name: "Prefer SeaDex" }), "true");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { storage: "seedbox", import: "hardlink", prefer_seadex: true } });
  });

  it("still offers the saved unsupported value after the draft moves away from it", async () => {
    const legacy = { ...version(), config: { ...version().config, import: "copy" } };
    mockFetch(getRoutes({ ...versions(), versions: [legacy] }));
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const importMode = screen.getByRole("combobox", { name: "Import mode" }) as HTMLSelectElement;
    await user.selectOptions(importMode, "symlink");
    expect(importMode.value).toBe("symlink");
    expect(within(importMode).getAllByRole("option").map((option) => option.textContent)).toEqual(["symlink", "copy (not supported yet)"]);
  });

  it("sends a library's grabs to a download client", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const send = screen.getByRole("combobox", { name: "Send grabs to" }) as HTMLSelectElement;
    expect(send.value).toBe("providers");
    expect(within(send).getByRole("option", { name: "Debrid providers" })).toBeTruthy();
    await user.selectOptions(send, "client");
    expect(screen.getByRole("combobox", { name: /^Client \(a decypharr client/ })).toBeTruthy();
    expect(screen.getByText(/Grabs go to download client radarr/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { add_via: "client", client: "radarr" } });
  });

  it("lists a client-routed library's client in place of its providers", async () => {
    const routed = versions();
    routed.versions = [{ ...version(), config: { ...version().config, add_via: "client" } as ReturnType<typeof version>["config"] }];
    mockFetch(getRoutes(routed));
    renderPage(<VersionsPage />);
    expect((await screen.findAllByText("via client radarr")).length).toBeGreaterThan(0);
  });

  it("lists every write effect, confirms, and saves with the returned token", async () => {
    const fetch = mockFetch([
      ...getRoutes(),
      { method: "POST", path: "/settings/versions", reply: json({ error: "confirmation required", effects }, 422) },
      { method: "POST", path: "/settings/versions/confirm", reply: json({ generation: 4, token: "version-confirmation", effects }) },
      { method: "POST", path: "/settings/versions", reply: json({ generation: 5, revision: "next" }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Add library" }));
    await user.type(screen.getByRole("textbox", { name: "Key" }), "movie-new");
    await user.click(screen.getByRole("button", { name: "Save" }));

    const dialog = await screen.findByRole("dialog", { name: "Confirm library change" });
    for (const effect of effects) expect(within(dialog).getByText(effect.effect)).toBeTruthy();
    expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1);
    expect(fetch.calls("POST", "/settings/versions/confirm")).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Confirm this change" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(2));
    expect(fetch.calls("POST", "/settings/versions/confirm")[0].body).toMatchObject({ generation: 4, key: "movie-new" });
    expect(fetch.calls("POST", "/settings/versions")[1].body).toMatchObject({ generation: 4, key: "movie-new", confirmation_token: "version-confirmation" });
  });

  it("does not save after the operator cancels a write confirmation", async () => {
    const fetch = mockFetch([
      ...getRoutes(),
      { method: "POST", path: "/settings/versions", reply: json({ error: "confirmation required", effects }, 422) },
      { method: "POST", path: "/settings/versions/confirm", reply: json({ generation: 4, token: "unused", effects }) },
      { method: "POST", path: "/settings/versions", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Add library" }));
    await user.type(screen.getByRole("textbox", { name: "Key" }), "movie-new");
    const save = screen.getByRole("button", { name: "Save" });
    await user.click(save);
    const dialog = await screen.findByRole("dialog", { name: "Confirm library change" });
    await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Cancel" })));
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(document.activeElement).toBe(save));
    expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1);
    expect(fetch.calls("POST", "/settings/versions/confirm")).toHaveLength(0);
  });

  it("confirms stopping management and sends the token on the delete request", async () => {
    const fetch = mockFetch([
      ...getRoutes(),
      { method: "POST", path: "/settings/versions/confirm", reply: json({ generation: 4, token: "delete-confirmation", effects: [effects[0]] }) },
      { method: "DELETE", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Stop managing library" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm library change" });
    expect(within(dialog).getByText(effects[0].effect)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Confirm this change" }));
    await waitFor(() => expect(fetch.calls("DELETE", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("DELETE", "/settings/versions/movie-web")[0].search).toContain("generation=4");
    // The token travels in a header, never in the URL (access logs, history).
    expect(fetch.calls("DELETE", "/settings/versions/movie-web")[0].search).not.toContain("confirmation_token");
    expect(fetch.calls("DELETE", "/settings/versions/movie-web")[0].headers["x-confirmation-token"]).toBe("delete-confirmation");
  });

  it("marks library edits, undoes one, counts them and discards back to the stored record", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const form = screen.getByTestId("version-form");
    expect(within(form).queryByTestId("unsaved-count")).toBeNull();
    await user.selectOptions(within(form).getByRole("combobox", { name: "Upgrades" }), "off");
    const root = within(form).getByRole("textbox", { name: "Root" });
    await user.clear(root);
    await user.type(root, "/library/other");
    // A separately added write root survives: Root and Write roots are marked but have no Undo.
    const roots = within(form).getByRole("textbox", { name: "Write roots (comma separated)" });
    await user.type(roots, ", /library/extra");
    expect(within(form).getAllByTestId("field-changed")).toHaveLength(3);
    expect(within(form).queryByRole("button", { name: "Undo change to Root" })).toBeNull();
    expect(within(form).queryByRole("button", { name: "Undo change to Write roots" })).toBeNull();
    expect(within(form).getByTestId("unsaved-count").textContent).toBe("3 unsaved changes");
    await user.click(within(form).getByRole("button", { name: "Undo change to Upgrades" }));
    expect((roots as HTMLInputElement).value).toBe("/library/other, /library/extra");
    expect(within(form).getByTestId("unsaved-count").textContent).toBe("2 unsaved changes");
    await user.click(within(form).getByTestId("version-discard"));
    expect((within(form).getByRole("textbox", { name: "Root" }) as HTMLInputElement).value).toBe("/library/movies");
    expect((within(form).getByRole("textbox", { name: "Write roots (comma separated)" }) as HTMLInputElement).value).toBe("/library/movies");
    expect(within(form).queryByTestId("unsaved-count")).toBeNull();
    expect(within(form).queryAllByTestId("field-changed")).toHaveLength(0);
    expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(0);
  });

  it("marks, counts and undoes a new library's key", async () => {
    mockFetch(getRoutes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Add library" }));
    const form = screen.getByTestId("version-form");
    await user.type(within(form).getByRole("textbox", { name: "Key" }), "movie-new");
    expect(within(form).getAllByTestId("field-changed")).toHaveLength(1);
    expect(within(form).getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
    await user.click(within(form).getByRole("button", { name: "Undo change to Key" }));
    expect((within(form).getByRole("textbox", { name: "Key" }) as HTMLInputElement).value).toBe("");
    expect(within(form).queryByTestId("unsaved-count")).toBeNull();
  });

  it("lists only the effects the server returns when stopping management", async () => {
    mockFetch([
      ...getRoutes(),
      { method: "POST", path: "/settings/versions/confirm", reply: json({ generation: 4, token: "delete-confirmation", effects: [] }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Stop managing library" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm library change" });
    expect(within(dialog).queryByText("Stop managing this library.")).toBeNull();
    expect(within(dialog).queryAllByTestId("confirm-group")).toHaveLength(0);
  });

  it("lets a stale editor reload the new generation and keep its changes", async () => {
    let reads = 0;
    const fetch = mockFetch([
      { method: "GET", path: "/settings/versions", reply: () => json(versions(++reads === 1 ? 4 : 5, "/library/remote")) },
      { method: "GET", path: "/settings/view-bindings", reply: viewBindings() },
      { method: "PUT", path: "/settings/versions/movie-web", reply: json({ error: "settings changed since this generation was read; reload and retry", generation: 5 }, 409) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const root = screen.getByRole("textbox", { name: "Root" });
    await user.clear(root);
    await user.type(root, "/library/local-edit");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "Reload, keep my changes" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: /Edit library movie-web/ })).toBeTruthy());
    expect((screen.getByRole("textbox", { name: "Root" }) as HTMLInputElement).value).toBe("/library/local-edit");
    expect(fetch.calls("GET", "/settings/versions")).toHaveLength(2);
  });

  it("lets a stale editor reload and discard its changes", async () => {
    let reads = 0;
    mockFetch([
      { method: "GET", path: "/settings/versions", reply: () => json(versions(++reads === 1 ? 4 : 5, "/library/remote")) },
      { method: "GET", path: "/settings/view-bindings", reply: viewBindings() },
      { method: "PUT", path: "/settings/versions/movie-web", reply: json({ error: "settings changed since this generation was read; reload and retry", generation: 5 }, 409) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const root = screen.getByRole("textbox", { name: "Root" });
    await user.clear(root);
    await user.type(root, "/library/local-edit");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "Reload, discard my changes" }));
    await waitFor(() => expect((screen.getByRole("textbox", { name: "Root" }) as HTMLInputElement).value).toBe("/library/remote"));
    expect(screen.getByRole("heading", { name: /Edit library movie-web/ })).toBeTruthy();
  });

  it("keeps database libraries editable without a read-only note", async () => {
    mockFetch(getRoutes(versions(4, "/library/movies", "db"), { ...viewBindings(), source: "db" }));
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect((screen.getByRole("button", { name: "Add library" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("textbox", { name: "Root" }) as HTMLInputElement).matches(":disabled")).toBe(false);
    expect(screen.queryByText(/Read-only:/)).toBeNull();
    expect(screen.queryByText(/policy files/)).toBeNull();
  });

  it("points to Connected apps for which arr apps see each library", async () => {
    mockFetch(getRoutes());
    renderPage(<VersionsPage />);
    const link = await screen.findByRole("link", { name: "Connected apps" });
    expect(link.getAttribute("href")).toBe("/admin/settings/apps");
    expect(screen.queryByRole("button", { name: "Edit bindings" })).toBeNull();
  });
});

describe("Versions grouped by kind", () => {
  const series = { ...version(), id: 2, key: "tv-web", config: { ...version().config, kind: "series", profile: "sonarr-web", library: "/library/tv", mode: "active" } };
  const grouped = { ...versions(), versions: [version(), series], profiles: { web: "web-set", "sonarr-web": "sonarr-set", "anime-tv": "anime-set" } };
  const summary = (key: string, origin: unknown, o: Record<string, unknown> = {}) => ({ id: 1, key, flavour: "radarr", qualities: [], cutoff: "", format_set: "", formats: 0, used_by: [], held: 0, origin, guessed: false, diff: null, binding: "", score_set: "", scores: [], ...o });
  const quality = {
    source: "db", generation: 4, trash: ["radarr"], categories: ["movie", "series", "anime_movie", "anime_series"], formats: [],
    defaults: { movie: "web", series: "sonarr-web", anime_series: "anime-tv" },
    profiles: [
      summary("web", { preset: "0537", name: "WEB 2160p", flavour: "radarr", modifiers: ["fallbacks_1080p"] }),
      summary("sonarr-web", { preset: "d1d6", name: "WEB-1080p", flavour: "sonarr", modifiers: [] }, { guessed: true }),
      summary("anime-tv", null),
    ],
  };
  const routes = () => [
    { method: "GET", path: "/settings/versions", reply: grouped },
    { method: "GET", path: "/settings/view-bindings", reply: viewBindings() },
    { method: "GET", path: "/settings/quality/overview", reply: quality },
  ];

  it("groups versions by kind and reads where, profile with its TRaSH origin, and sources", async () => {
    mockFetch(routes());
    renderPage(<VersionsPage />);
    const movies = await screen.findByRole("region", { name: "Movies" });
    const tv = screen.getByRole("region", { name: "Series" });
    expect(within(movies).getAllByText("movie-web").length).toBeGreaterThan(0);
    expect(within(movies).queryByText("tv-web")).toBeNull();
    expect(within(tv).getAllByText("tv-web").length).toBeGreaterThan(0);
    expect(within(movies).queryByText("from TRaSH WEB 2160p + 1080p fallbacks")).toBeNull();
    expect(within(tv).queryByText("from TRaSH WEB-1080p (closest preset)")).toBeNull();
    expect(within(movies).getAllByText("/library/movies").length).toBeGreaterThan(0);
    expect(within(movies).getAllByText("public").length).toBeGreaterThan(0);
    expect(within(movies).getAllByText("rd").length).toBeGreaterThan(0);
    expect(screen.queryByRole("region", { name: "Anime series" })).toBeNull();
  });

  it("splits the editor into Where, Which releases and Sources and preselects the default profile per kind", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await screen.findByRole("region", { name: "Movies" });
    await user.click(screen.getByRole("button", { name: "Add library" }));
    const form = await screen.findByTestId("version-form");
    const where = within(form).getByRole("region", { name: "Where" });
    const releases = within(form).getByRole("region", { name: "Which releases" });
    const sources = within(form).getByRole("region", { name: "Sources" });
    expect(within(where).getByRole("textbox", { name: "Key" })).toBeTruthy();
    expect(within(sources).getByRole("group", { name: "Providers (fallback order)" })).toBeTruthy();
    const profile = within(releases).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    expect(profile.value).toBe("web");
    expect(within(releases).getByRole("link", { name: "Open the profile" }).getAttribute("href")).toBe("/admin/settings/profiles?edit=web");
    await user.selectOptions(within(where).getByRole("combobox", { name: "Kind" }), "series");
    expect(profile.value).toBe("sonarr-web");
    const anime = within(where).getByRole("combobox", { name: "Anime" }) as HTMLSelectElement;
    expect([...anime.options].map((o) => `${o.value}=${o.text}`)).toEqual(["false=Not anime", "true=Anime only", "both=Anime and not"]);
    await user.selectOptions(within(where).getByRole("combobox", { name: "Anime" }), "true");
    expect(profile.value).toBe("anime-tv");
    expect(within(releases).getByText(/our own profile/)).toBeTruthy();
    // A profile picked by hand stays when the kind changes.
    await user.selectOptions(profile, "web");
    await user.selectOptions(within(where).getByRole("combobox", { name: "Kind" }), "movie");
    expect(profile.value).toBe("web");
  });
});


it("reports a failed live root check without implying a settings save was refused", async () => {
  const fetch = mockFetch([...getRoutes(), { method: "POST", path: "/settings/versions/validate-root", reply: json({ error: "settings refused: root: directory must exist" }, 422) }]);
  const user = userEvent.setup();
  renderPage(<VersionsPage />);
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await screen.findByText("root check: directory must exist");
  expect(screen.queryByText(/settings refused/)).toBeNull();
  expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(0);
});

it("shows a relative root's refusal in plain words beside Root and Write roots", async () => {
  const refusal = { error: "settings refused: library.versions[movie-web].roots[0]: \"relative-folder\" must be an absolute path", fields: [
    { path: "library.versions[movie-web].roots[0]", message: "\"relative-folder\" must be an absolute path" },
    { path: "versions[movie-web].library", message: "\"relative-folder\" must be an absolute path" },
  ] };
  mockFetch([...getRoutes(),
    { method: "POST", path: "/settings/versions/validate-root", reply: json({ error: "settings refused: root: must be an absolute path, such as /media/movies", fields: [{ path: "root", message: "must be an absolute path, such as /media/movies" }] }, 422) },
    { method: "PUT", path: "/settings/versions/movie-web", reply: json(refusal, 422) }]);
  const user = userEvent.setup();
  renderPage(<VersionsPage />);
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await screen.findByText("root check: must be an absolute path, such as /media/movies");
  await user.click(screen.getByRole("button", { name: "Save" }));
  const alerts = await screen.findAllByText("\"relative-folder\" must be an absolute path");
  expect(alerts).toHaveLength(2);
  expect(alerts[0].closest("[data-changed], div")?.textContent).toContain("Root");
  expect(alerts[1].closest("div")?.textContent).toContain("Write roots");
  expect(screen.queryByText(/under none of the roots/)).toBeNull();
});

it("shows a new library's relative root refusal beside Root and Write roots", async () => {
  const refusal = { error: "settings refused: library.versions[movie-new].roots[0]: \"relative-folder\" must be an absolute path", fields: [
    { path: "library.versions[movie-new].roots[0]", message: "\"relative-folder\" must be an absolute path" },
    { path: "versions[movie-new].library", message: "\"relative-folder\" must be an absolute path" },
  ] };
  mockFetch([...getRoutes(),
    { method: "POST", path: "/settings/versions/validate-root", reply: json({ error: "settings refused: root: must be an absolute path, such as /media/movies", fields: [{ path: "root", message: "must be an absolute path, such as /media/movies" }] }, 422) },
    { method: "POST", path: "/settings/versions", reply: json(refusal, 422) }]);
  const user = userEvent.setup();
  renderPage(<VersionsPage />);
  await user.click(await screen.findByRole("button", { name: "Add library" }));
  const form = screen.getByTestId("version-form");
  await user.type(within(form).getByRole("textbox", { name: "Key" }), "movie-new");
  await user.type(within(form).getByRole("textbox", { name: "Root" }), "relative-folder");
  await user.click(screen.getByRole("button", { name: "Save" }));
  const alerts = await screen.findAllByText("\"relative-folder\" must be an absolute path");
  expect(alerts).toHaveLength(2);
  expect(alerts[0].closest("div")?.textContent).toContain("Root");
  expect(alerts[1].closest("div")?.textContent).toContain("Write roots");
});

