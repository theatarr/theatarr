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
    await user.click(screen.getByRole("switch", { name: "Prefer SeaDex" }));
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
    const grab = screen.getByRole("switch", { name: "Allow downloads for this library" });
    expect(grab.getAttribute("aria-checked")).toBe("false");
    await user.click(grab);
    expect(grab.getAttribute("aria-checked")).toBe("true");
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
    await user.click(screen.getByText("Advanced"));
    expect((screen.getByRole("combobox", { name: /^Client/ }) as HTMLSelectElement).value).toBe("");
    expect(within(screen.getByRole("combobox", { name: /^Client/ })).getByRole("option", { name: "None" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/versions")[0].body).toMatchObject({ config: { client: "", storage: "debrid" } });
  });

  it("shows no Storage or Import mode to a library that uses neither seedbox nor hardlink or copy, and keeps Advanced closed", async () => {
    mockFetch(getRoutes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const advanced = screen.getByTestId("library-advanced") as HTMLDetailsElement;
    expect(advanced.open).toBe(false);
    for (const name of ["Write roots", "Send grabs to", "Follows"]) expect(screen.queryByRole(name === "Write roots" ? "textbox" : "combobox", { name })).toBeNull();
    await user.click(screen.getByText("Advanced"));
    expect(advanced.open).toBe(true);
    expect(screen.getByRole("textbox", { name: "Write roots" })).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Send grabs to" })).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Follows" })).toBeTruthy();
    expect(screen.queryByRole("combobox", { name: "Storage" })).toBeNull();
    expect(screen.queryByRole("combobox", { name: "Import mode" })).toBeNull();
    expectNamedControls();
    await user.click(screen.getByText("Advanced"));
    expect(advanced.open).toBe(false);
  });

  it("opens Advanced for a library that follows another, sends its grabs to a client or has an extra write root", async () => {
    const user = userEvent.setup();
    for (const [config, roots] of [[{ follows: "movie-remux" }, undefined], [{ add_via: "client" }, undefined], [{}, ["/library/movies", "/library/extra"]]] as const) {
      mockFetch(getRoutes({ ...versions(), versions: [{ ...version(), roots: roots ? [...roots] : version().roots, config: { ...version().config, ...config } }] }));
      const page = renderPage(<VersionsPage />);
      await user.click(await screen.findByRole("button", { name: "Edit" }));
      expect((screen.getByTestId("library-advanced") as HTMLDetailsElement).open).toBe(true);
      expect(screen.getByRole("textbox", { name: "Write roots" })).toBeTruthy();
      page.unmount();
    }
  });

  it("opens Advanced when the server refuses a field inside it", async () => {
    const refusal = { error: "settings refused: versions[movie-web].client: unknown client \"gone\"", fields: [{ path: "versions[movie-web].client", message: "unknown client \"gone\"" }] };
    mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json(refusal, 422) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const advanced = screen.getByTestId("library-advanced") as HTMLDetailsElement;
    expect(advanced.open).toBe(false);
    await user.click(screen.getByRole("button", { name: "Save" }));
    const alert = await within(advanced).findByText("unknown client \"gone\"");
    expect(advanced.open).toBe(true);
    expect(alert.closest("div")?.textContent).toContain("Client");
  });

  it("saves an untouched library with the body it was loaded with, and no name", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect((screen.getByRole("textbox", { name: "Name" }) as HTMLInputElement).value).toBe("");
    // Typing a name and removing it again leaves no trace either.
    await user.type(screen.getByRole("textbox", { name: "Name" }), "Films");
    expect(screen.getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
    await user.clear(screen.getByRole("textbox", { name: "Name" }));
    expect(screen.queryByTestId("unsaved-count")).toBeNull();
    expect(screen.queryAllByTestId("field-changed")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toEqual({ generation: 4, key: "movie-web", config: version().config, roots: version().roots });
  });

  it("names a library, and clears a stored name by sending it empty", async () => {
    const named = { ...version(), config: { ...version().config, name: "Films" } };
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.type(screen.getByRole("textbox", { name: "Name" }), "Films in 4K");
    expect(screen.getByRole("button", { name: "Undo change to Name" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toEqual({ generation: 4, key: "movie-web", config: { ...version().config, name: "Films in 4K" }, roots: version().roots });
    page.unmount();

    const again = mockFetch([...getRoutes({ ...versions(), versions: [named] }), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect((screen.getByRole("textbox", { name: "Name" }) as HTMLInputElement).value).toBe("Films");
    await user.clear(screen.getByRole("textbox", { name: "Name" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(again.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(again.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { name: "" } });
  });

  it("sends the two switches as booleans in the usual body, with their Changed marks and Undo", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("switch", { name: "Allow downloads for this library" }));
    await user.click(screen.getByRole("switch", { name: "Prefer SeaDex" }));
    expect(screen.getByTestId("unsaved-count").textContent).toBe("2 unsaved changes");
    await user.click(screen.getByRole("button", { name: "Undo change to Prefer SeaDex" }));
    expect(screen.getByRole("switch", { name: "Prefer SeaDex" }).getAttribute("aria-checked")).toBe("false");
    await user.click(screen.getByRole("switch", { name: "Prefer SeaDex" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toEqual({ generation: 4, key: "movie-web", config: { ...version().config, grab: true, prefer_seadex: true }, roots: version().roots });
  });

  it("labels the modes and the upgrade rules in plain words", async () => {
    mockFetch(getRoutes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const options = (name: string) => within(screen.getByRole("combobox", { name })).getAllByRole("option").map((option) => option.textContent);
    expect(options("Mode")).toEqual(["Shadow (preview only)", "Active (manages files)"]);
    expect(options("Upgrades")).toEqual(["Automatic", "Ask first", "Never"]);
    expect(screen.getByText("What happens when a better release would replace a file this library already has.")).toBeTruthy();
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
    await user.click(screen.getByRole("switch", { name: "Prefer SeaDex" }));
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
    await user.click(screen.getByText("Advanced"));
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
    await user.click(within(form).getByText("Advanced"));
    const roots = within(form).getByRole("textbox", { name: "Write roots" });
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
    // Discard also puts Advanced back as the stored record has it: closed.
    expect((within(form).getByTestId("library-advanced") as HTMLDetailsElement).open).toBe(false);
    await user.click(within(form).getByText("Advanced"));
    expect((within(form).getByRole("textbox", { name: "Write roots" }) as HTMLInputElement).value).toBe("/library/movies");
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

  const radarrPresets = { flavour: "radarr", source: "bundled", presets: [
    { trash_id: "fd16", name: "Remux + WEB 2160p", flavour: "radarr", description: "", group: 1, qualities: ["Remux-2160p"], cutoff: "Remux-2160p", formats: 56, modifiers: [] },
    { trash_id: "0537", name: "WEB 2160p", flavour: "radarr", description: "", group: 2, qualities: ["WEB 2160p"], cutoff: "WEB 2160p", formats: 39, modifiers: [] },
  ] };
  const sonarrPresets = { flavour: "sonarr", source: "bundled", presets: [
    { trash_id: "72da", name: "WEB-1080p", flavour: "sonarr", description: "", group: 1, qualities: ["WEB 1080p"], cutoff: "WEB 1080p", formats: 30, modifiers: [] },
  ] };
  const withPresets = (versionsReply: unknown = grouped) => [
    { method: "GET", path: "/settings/versions", reply: versionsReply },
    { method: "GET", path: "/settings/view-bindings", reply: viewBindings() },
    { method: "GET", path: "/settings/quality/overview", reply: quality },
    { method: "GET", path: "/settings/presets", reply: (request: { search: string }) => request.search.includes("sonarr") ? sonarrPresets : radarrPresets },
  ];
  const optionsOf = (select: HTMLSelectElement, group: string) => [...select.querySelectorAll<HTMLOptionElement>(`optgroup[label="${group}"] option`)].map((o) => `${o.value}=${o.text}`);

  it("offers every TRaSH default of the library's kind beside the stored profiles, with a hint on the ones already copied", async () => {
    const fetch = mockFetch(withPresets());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await screen.findByRole("region", { name: "Movies" });
    await user.click(screen.getByRole("button", { name: "Add library" }));
    const releases = within(await screen.findByTestId("version-form")).getByRole("region", { name: "Which releases" });
    const profile = within(releases).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    await waitFor(() => expect(optionsOf(profile, "TRaSH defaults")).toHaveLength(2));
    expect(optionsOf(profile, "Your profiles")).toEqual(["web=web", "sonarr-web=sonarr-web", "anime-tv=anime-tv"]);
    // The default "web" was made from stays listed, with the copy named; a guessed origin (sonarr-web) is no copy.
    expect(optionsOf(profile, "TRaSH defaults")).toEqual(["preset:fd16=Remux + WEB 2160p", "preset:0537=WEB 2160p (copy: web)"]);
    expect(profile.value).toBe("web");
    await user.selectOptions(profile, "preset:0537");
    const note = within(releases).getByTestId("preset-pick-note");
    expect(note.textContent).toContain("TRaSH default WEB 2160p: saving first creates a profile of your own from it");
    expect(note.textContent).toContain("pick web under Your profiles");
    expect(within(releases).queryByRole("link", { name: "Open the profile" })).toBeNull();
    // Series libraries get Sonarr's defaults; a picked movie default does not carry over.
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "series");
    await waitFor(() => expect(optionsOf(profile, "TRaSH defaults")).toEqual(["preset:72da=WEB-1080p"]));
    expect(profile.value).toBe("sonarr-web");
    expect(fetch.calls("GET", "/settings/presets").map((c) => c.search)).toEqual(["?flavour=radarr", "?flavour=sonarr"]);
  });

  it("saves a library with a picked TRaSH default: the profile is created first, named after the library, then the library names it", async () => {
    const fetch = mockFetch([
      ...withPresets(),
      { method: "POST", path: "/settings/presets/create", reply: json({ generation: 5, key: "movie-web", binding: "" }) },
      { method: "PUT", path: "/settings/versions/movie-web", reply: json({ error: "settings refused: versions[movie-web].library: directory must exist" }, 422) },
      { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    const row = (await screen.findAllByText("movie-web"))[0].closest("tr")!;
    await user.click(within(row).getByTestId("edit"));
    const form = await screen.findByTestId("version-form");
    const profile = within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    await waitFor(() => expect(optionsOf(profile, "TRaSH defaults")).toHaveLength(2));
    await user.selectOptions(profile, "preset:fd16");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create")[0].body).toEqual({ generation: 4, key: "movie-web", flavour: "radarr", preset: "fd16", modifiers: [] });
    // The library is saved at the generation the profile made, naming the profile: "preset:" never reaches the server.
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ generation: 5, config: { profile: "movie-web", kind: "movie" } });
    // The library save failed: the profile exists and is selected, and a retry does not create it again.
    expect((await within(form).findByTestId("preset-profile-made")).textContent).toContain("The profile movie-web was created from the TRaSH default Remux + WEB 2160p and is selected.");
    expect((within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement).value).toBe("movie-web");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(2));
    expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(1);
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[1].body).toMatchObject({ generation: 5, config: { profile: "movie-web" } });
  });

  it("confirms creating the profile of a picked TRaSH default, repeats that request with its token, then saves the library", async () => {
    const effects = [{ version: "movie-web", effect: "Stored settings that are not running yet enable grabs for movie-web" }];
    const fetch = mockFetch([
      ...withPresets(),
      { method: "POST", path: "/settings/presets/create", reply: json({ error: "explicit confirmation required", effects, confirmation: { generation: 4, token: "preset-confirmation", effects } }, 422) },
      { method: "POST", path: "/settings/presets/create", reply: json({ generation: 5, key: "movie-web", binding: "" }) },
      { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    const row = (await screen.findAllByText("movie-web"))[0].closest("tr")!;
    await user.click(within(row).getByTestId("edit"));
    const form = await screen.findByTestId("version-form");
    const profile = within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    await waitFor(() => expect(optionsOf(profile, "TRaSH defaults")).toHaveLength(2));
    await user.selectOptions(profile, "preset:fd16");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    // The refusal is a confirmation, not a validation error: the standard dialog lists the effects.
    const dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effects[0].effect)).toBeTruthy();
    expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    const [first, second] = fetch.calls("POST", "/settings/presets/create");
    expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(2);
    expect(first.headers["x-confirmation-token"]).toBeUndefined();
    expect(second.headers["x-confirmation-token"]).toBe("preset-confirmation");
    expect(second.body).toEqual(first.body);
    expect(second.body).toEqual({ generation: 4, key: "movie-web", flavour: "radarr", preset: "fd16", modifiers: [] });
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ generation: 5, config: { profile: "movie-web" } });
  });

  it("names the profile of a picked TRaSH default with the next free key when the library's key is taken", async () => {
    // "tv-web" is a profile already and "tv-web-2" a format set; the server then refuses "tv-web-3" (a format set it
    // alone knows), so "tv-web-4" is used.
    const taken = { ...grouped, profiles: { ...grouped.profiles, "tv-web": "tv-web" }, format_sets: ["web-set", "tv-web", "tv-web-2"] };
    const fetch = mockFetch([
      ...withPresets(taken),
      { method: "POST", path: "/settings/presets/create", reply: json({ error: "settings refused: key: a format set with this key exists", fields: [{ path: "key", message: "a format set with this key exists" }] }, 422) },
      { method: "POST", path: "/settings/presets/create", reply: json({ generation: 5, key: "tv-web-4", binding: "" }) },
      { method: "PUT", path: "/settings/versions/tv-web", reply: json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    const row = (await screen.findAllByText("tv-web"))[0].closest("tr")!;
    await user.click(within(row).getByTestId("edit"));
    const form = await screen.findByTestId("version-form");
    const profile = within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    await waitFor(() => expect(optionsOf(profile, "TRaSH defaults")).toEqual(["preset:72da=WEB-1080p"]));
    await user.selectOptions(profile, "preset:72da");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/tv-web")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create").map((c) => (c.body as { key: string; flavour: string }).key)).toEqual(["tv-web-3", "tv-web-4"]);
    expect(fetch.calls("POST", "/settings/presets/create")[1].body).toEqual({ generation: 4, key: "tv-web-4", flavour: "sonarr", preset: "72da", modifiers: [] });
    expect(fetch.calls("PUT", "/settings/versions/tv-web")[0].body).toMatchObject({ generation: 5, config: { profile: "tv-web-4" } });
  });

  it("lets a fresh install with no profile pick a TRaSH default for its first library", async () => {
    const empty = { ...versions(), versions: [], profiles: {}, format_sets: [], switches: { ...versions().switches, versions: [] } };
    const fetch = mockFetch([
      { method: "GET", path: "/settings/versions", reply: empty },
      { method: "GET", path: "/settings/view-bindings", reply: viewBindings() },
      { method: "GET", path: "/settings/quality/overview", reply: { ...quality, profiles: [], defaults: {} } },
      { method: "GET", path: "/settings/presets", reply: radarrPresets },
      { method: "POST", path: "/settings/presets/create", reply: json({ generation: 5, key: "movies", binding: "" }) },
      { method: "POST", path: "/settings/versions", reply: json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Add library" }));
    const form = await screen.findByTestId("version-form");
    const profile = within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    await waitFor(() => expect(optionsOf(profile, "TRaSH defaults")).toHaveLength(2));
    expect(profile.value).toBe("");
    expect(profile.querySelector('optgroup[label="Your profiles"]')).toBeNull();
    await user.selectOptions(profile, "preset:0537");
    // Without a key there is nothing to name the profile after: nothing is created.
    await user.click(within(form).getByRole("button", { name: "Save" }));
    expect(await within(form).findByText(/Give the library a key first/)).toBeTruthy();
    expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(0);
    await user.type(within(form).getByRole("textbox", { name: "Key" }), "movies");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create")[0].body).toEqual({ generation: 4, key: "movies", flavour: "radarr", preset: "0537", modifiers: [] });
    expect(fetch.calls("POST", "/settings/versions")[0].body).toMatchObject({ generation: 5, key: "movies", config: { profile: "movies", mode: "shadow" } });
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

