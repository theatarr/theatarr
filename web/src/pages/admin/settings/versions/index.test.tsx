// @vitest-environment jsdom
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { bringInSyncGain, VersionsPage } from ".";
import { expectNamedControls, json, mockFetch, renderPage } from "@/test/page";
import { readableCode } from "@/lib/code-words";

/** Stands in for the header Pause: it reads the settings overview. */

const version = (library = "/library/movies") => ({
  id: 1, key: "movie-web", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", roots: [library],
  config: { kind: "movie", anime: false, profile: "web", library, storage: "debrid", indexers: ["public"], providers: ["rd"], client: "radarr", import: "symlink", upgrades: "auto", follows: "" },
});
const versions = (generation = 4, library = "/library/movies", source = "db") => ({
  source, generation, versions: [version(library)], profiles: { web: "web-set" }, format_sets: ["web-set"], clients: ["radarr"], indexers: ["public"], providers: ["rd"],
  switches: { paused: false, pause_forced: false, versions: [{ key: "movie-web" }] },
});
const viewBindings = (generation = 4) => ({ source: "db", generation, views: [{ id: 1, key: "radarr", versions: ["movie-web"], profiles: {} }] });
const getRoutes = (versionReply = versions(), viewsReply = viewBindings()) => [
  { method: "GET", path: "/settings/versions", reply: { ...versionReply, switches: { ...versionReply.switches, versions: versionReply.versions.map((v) => ({ key: v.key, grab_state: { paused: versionReply.switches.paused, pause_forced: versionReply.switches.pause_forced } })) } } },
  { method: "GET", path: "/settings/view-bindings", reply: viewsReply },
];
const effects = [
  { version: "movie-web", effect: "Enable writes to /library/movies" },
  { version: "movie-web", effect: "Add provider rd to the active route" },
];

describe("Versions page", () => {
  it("keeps libraries in sync: names the group on the row, offers only libraries that qualify and saves the group with the library", async () => {
    const lib = (key: string, name: string, anime = false) => ({ ...version(), key, config: { ...version().config, name, anime } });
    const data = { ...versions(), versions: [lib("movie-web", "Web"), lib("movie-remux", "Remux"), lib("anime-movie", "Anime", true), lib("movie-hd", "HD"), lib("movie-uhd", "UHD")],
      sync_groups: [["movie-web", "movie-remux"], ["movie-hd", "movie-uhd"]] };
    const fetch = mockFetch([...getRoutes(data), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    const row = () => page.container.querySelector<HTMLElement>('[data-record="movie-web"]')!;
    await waitFor(() => expect(row()).toBeTruthy());
    expect(within(row()).getByTestId("library-sync").textContent).toBe("In sync with Remux");
    await user.click(within(row()).getByRole("button", { name: "Edit" }));
    const part = await screen.findByTestId("library-sync-part");
    // The help says what happens to a copy removed there: a person's add brings it back, lists and collections do not.
    expect(within(part).getByTestId("library-sync-help").textContent).toBe("A title added to one of these libraries is added to the others too, and a title removed from one is removed from the others, with its files and debrid torrents when the removal deletes them. Adding a title by hand brings it back to a library it was removed from, while lists and collections leave it removed. Monitoring stays each library's own. Only libraries of the same kind and anime setting can be kept in sync.");
    expect((within(part).getByRole("checkbox", { name: "Remux" }) as HTMLInputElement).checked).toBe(true);
    expect(within(part).queryByRole("checkbox", { name: "HD" })).toBeNull();
    expect(within(part).getByTestId("library-sync-refused").textContent).toBe("Not offered: Anime (anime only); HD (in sync with UHD); UHD (in sync with HD).");
    await user.click(within(part).getByRole("checkbox", { name: "Remux" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ sync_with: [] });
  });
  it("warns while the draft's anime setting takes the library out of its saved group, offers no Undo or Bring in sync for it, and the save names the partner it lost", async () => {
    const lib = (key: string, name: string, anime = false) => ({ ...version(), key, config: { ...version().config, name, anime } });
    const data = { ...versions(), versions: [lib("movie-remux", "4K Remux"), lib("movie-web", "4K WEB"), lib("anime-movie", "Anime", true)], sync_groups: [["movie-remux", "movie-web"]] };
    const preview = { group: ["movie-remux", "movie-web"], titles: 1, source: "", searched: 0,
      libraries: [{ key: "movie-remux", label: "4K Remux", count: 0, names: [], more: 0, restored: 0 }, { key: "movie-web", label: "4K WEB", count: 1, names: ["Alpha"], more: 0, restored: 0 }] };
    const fetch = mockFetch([...getRoutes(data), { method: "GET", path: "/settings/versions/movie-remux/bring-in-sync", reply: preview },
      { method: "PUT", path: "/settings/versions/movie-remux", reply: json({ generation: 5, revision: "r", sync_dropped: ["movie-web"] }) }]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    const row = () => page.container.querySelector<HTMLElement>('[data-record="movie-remux"]')!;
    await waitFor(() => expect(row()).toBeTruthy());
    await user.click(within(row()).getByRole("button", { name: "Edit" }));
    const part = await screen.findByTestId("library-sync-part");
    expect(within(part).queryByTestId("library-sync-leaving")).toBeNull();
    expect(await within(part).findByTestId("bring-in-sync")).toBeTruthy();
    const anime = screen.getByRole("combobox", { name: "Anime" });
    await user.selectOptions(anime, "true");
    const leaving = "Saving this takes 4K Remux out of its sync group with 4K WEB.";
    expect(within(part).getByTestId("library-sync-leaving").textContent).toBe(leaving);
    // Keep in sync has nothing to undo: no Undo can put back a partner the anime setting excludes.
    expect(within(part).queryByRole("button", { name: "Undo change to Keep in sync" })).toBeNull();
    expect(within(part).queryByTestId("bring-in-sync")).toBeNull();
    // One change: the anime setting (Keep in sync has no change of its own to count).
    expect(screen.getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
    // Undoing the anime setting itself ends the warning.
    await user.click(screen.getByRole("button", { name: "Undo change to Anime" }));
    expect(within(part).queryByTestId("library-sync-leaving")).toBeNull();
    await user.selectOptions(anime, "true");
    // A library the new setting allows can be picked; the excluded partner is not sent.
    await user.click(within(part).getByRole("checkbox", { name: "Anime" }));
    expect(within(part).getByTestId("library-sync-leaving").textContent).toBe(leaving);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-remux")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-remux")[0].body).toMatchObject({ sync_with: ["anime-movie"] });
    await waitFor(() => expect(screen.getByTestId("version-announce").textContent).toBe("4K Remux saved. It is no longer in sync with 4K WEB."));
  });
  it("undoes a change of Anime or Kind with the group and the movie fields as saved: no change left, nothing excluded sent", async () => {
    const lib = (key: string, name: string, anime = false) => ({ ...version(), key, config: { ...version().config, name, anime, minimum_availability: "announced", availability_delay: 7 } });
    const data = { ...versions(), versions: [lib("movie-remux", "4K Remux"), lib("movie-web", "4K WEB"), lib("anime-movie", "Anime", true)], sync_groups: [["movie-remux", "movie-web"]] };
    const fetch = mockFetch([...getRoutes(data), { method: "GET", path: "/settings/versions/movie-remux/bring-in-sync", reply: { group: [], titles: 0, source: "", searched: 0, libraries: [] } },
      { method: "PUT", path: "/settings/versions/movie-remux", reply: json({ generation: 5, revision: "r" }) }]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    const row = () => page.container.querySelector<HTMLElement>('[data-record="movie-remux"]')!;
    await waitFor(() => expect(row()).toBeTruthy());
    await user.click(within(row()).getByRole("button", { name: "Edit" }));
    const part = await screen.findByTestId("library-sync-part");
    const settled = () => {
      expect(screen.queryByTestId("unsaved-count")).toBeNull();
      expect(within(part).queryByTestId("library-sync-leaving")).toBeNull();
      expect(within(part).queryByTestId("field-changed")).toBeNull();
      expect((within(part).getByRole("checkbox", { name: "4K WEB" }) as HTMLInputElement).checked).toBe(true);
    };
    // Anime on, then its Undo.
    await user.selectOptions(screen.getByRole("combobox", { name: "Anime" }), "true");
    await user.click(screen.getByRole("button", { name: "Undo change to Anime" }));
    settled();
    // Kind to series (which drops the movie fields), then its Undo: the saved minimum availability and delay are back.
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "series");
    await user.click(screen.getByRole("button", { name: "Undo change to Kind" }));
    settled();
    expect((screen.getByRole("combobox", { name: "Minimum availability" }) as HTMLSelectElement).value).toBe("announced");
    expect((screen.getByRole("textbox", { name: "Availability delay (days)" }) as HTMLInputElement).value).toBe("7");
    // Anime on, a partner picked under that rule, then Undo: the pick goes, the saved partner is back.
    await user.selectOptions(screen.getByRole("combobox", { name: "Anime" }), "true");
    await user.click(within(part).getByRole("checkbox", { name: "Anime" }));
    await user.click(screen.getByRole("button", { name: "Undo change to Anime" }));
    settled();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-remux")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-remux")[0].body).not.toHaveProperty("sync_with");
  });

  it("picking the saved Kind or Anime again gives what Undo gives: the group and the movie fields as saved, no change left", async () => {
    const lib = (key: string, name: string, anime = false) => ({ ...version(), key, config: { ...version().config, name, anime, minimum_availability: "announced", availability_delay: 7 } });
    const data = { ...versions(), versions: [lib("movie-remux", "4K Remux"), lib("movie-web", "4K WEB"), lib("movie-hd", "HD"), lib("anime-movie", "Anime", true)], sync_groups: [["movie-remux", "movie-web", "movie-hd"]] };
    const fetch = mockFetch([...getRoutes(data), { method: "GET", path: "/settings/versions/movie-remux/bring-in-sync", reply: { group: [], titles: 0, source: "", searched: 0, libraries: [] } },
      { method: "PUT", path: "/settings/versions/movie-remux", reply: json({ generation: 5, revision: "r" }) }]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    const row = () => page.container.querySelector<HTMLElement>('[data-record="movie-remux"]')!;
    await waitFor(() => expect(row()).toBeTruthy());
    await user.click(within(row()).getByRole("button", { name: "Edit" }));
    const part = await screen.findByTestId("library-sync-part");
    const settled = () => {
      expect(screen.queryByTestId("unsaved-count")).toBeNull();
      expect(within(part).queryByTestId("library-sync-leaving")).toBeNull();
      expect(within(part).queryByTestId("field-changed")).toBeNull();
      for (const name of ["4K WEB", "HD"]) expect((within(part).getByRole("checkbox", { name }) as HTMLInputElement).checked).toBe(true);
    };
    const kind = () => screen.getByRole("combobox", { name: "Kind" });
    const anime = () => screen.getByRole("combobox", { name: "Anime" });
    // Anime only, then Not anime again from the same menu.
    await user.selectOptions(anime(), "true");
    expect(within(part).getByTestId("library-sync-leaving")).toBeTruthy();
    await user.selectOptions(anime(), "false");
    settled();
    // Movies, Series, Movies again: the saved minimum availability and delay are back.
    await user.selectOptions(kind(), "series");
    await user.selectOptions(kind(), "movie");
    settled();
    expect((screen.getByRole("combobox", { name: "Minimum availability" }) as HTMLSelectElement).value).toBe("announced");
    expect((screen.getByRole("textbox", { name: "Availability delay (days)" }) as HTMLInputElement).value).toBe("7");
    // In the group of three, one partner unticked by hand, then Anime only and back: as Undo does, both saved partners are ticked again.
    await user.click(within(part).getByRole("checkbox", { name: "HD" }));
    expect(screen.getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
    await user.selectOptions(anime(), "true");
    await user.selectOptions(anime(), "false");
    settled();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-remux")).toHaveLength(1));
    const body = fetch.calls("PUT", "/settings/versions/movie-remux")[0].body;
    expect(body).not.toHaveProperty("sync_with");
    expect(body).toMatchObject({ config: { kind: "movie", anime: false, minimum_availability: "announced", availability_delay: 7 } });
    await waitFor(() => expect(screen.getByTestId("version-announce").textContent).toBe("4K Remux saved."));
  });
  it("says removals follow the group too, and Bring in sync names the copies it brings back", async () => {
    const lib = (key: string, name: string) => ({ ...version(), key, config: { ...version().config, name } });
    const data = { ...versions(), versions: [lib("movie-web", "Web"), lib("movie-remux", "Remux")], sync_groups: [["movie-web", "movie-remux"]] };
    const preview = { group: ["movie-web", "movie-remux"], titles: 3, source: "Each new copy is monitored like the title's first monitored copy in the group, or its first copy.", searched: 0,
      libraries: [{ key: "movie-web", label: "Web", count: 0, names: [], more: 0, restored: 0 }, { key: "movie-remux", label: "Remux", count: 3, names: ["Alpha", "Beta", "Gamma"], more: 0, restored: 2 }] };
    mockFetch([...getRoutes(data), { method: "GET", path: "/settings/versions/movie-web/bring-in-sync", reply: preview }]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    const row = () => page.container.querySelector<HTMLElement>('[data-record="movie-web"]')!;
    await waitFor(() => expect(row()).toBeTruthy());
    await user.click(within(row()).getByRole("button", { name: "Edit" }));
    const part = await screen.findByTestId("library-sync-part");
    expect(part.textContent).toContain("A title added to one of these libraries is added to the others too, and a title removed from one is removed from the others, with its files and debrid torrents when the removal deletes them. Adding a title by hand brings it back to a library it was removed from, while lists and collections leave it removed. Monitoring stays each library's own.");
    expect(part.textContent).not.toContain("unless it was removed");
    await user.click(await within(part).findByRole("button", { name: "Bring in sync…" }));
    const dialog = await screen.findByTestId("bring-in-sync-dialog");
    expect(dialog.textContent).toContain("Remux gains 3 titles, 2 of them were removed from it before and come back: Alpha, Beta, Gamma");
    expect(dialog.textContent).toContain("Existing copies are left as they are");
    expect(dialog.textContent).not.toContain("Removed copies stay removed");
  });
  it("words what Bring in sync gives a library, with the copies that come back", () => {
    expect(bringInSyncGain({ label: "HD", count: 4, restored: 0 })).toBe("HD gains 4 titles");
    expect(bringInSyncGain({ label: "HD", count: 4, restored: 1 })).toBe("HD gains 4 titles, 1 of them was removed from it before and comes back");
    expect(bringInSyncGain({ label: "HD", count: 1, restored: 1 })).toBe("HD gains 1 title, it was removed from it before and comes back");
  });
  it("saves curator preference separately from quality and grab settings", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("switch", { name: "Prefer SeaDex" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { prefer_seadex: true, profile: "web" } });
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).not.toHaveProperty("sync_with");
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
  it("uses runtime gates for the list and says the forced Pause once", async () => {
    const saved = version();
    const data = { ...versions(), versions: [saved], switches: {
      ...versions().switches, paused: true, pause_forced: true,
      versions: [{ key: saved.key, grab_state: { paused: true, pause_forced: true } }],
    } };
    mockFetch([
      { method: "GET", path: "/settings/versions", reply: data },
      { method: "GET", path: "/settings/view-bindings", reply: viewBindings() },
    ]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    const edit = await screen.findByRole("button", { name: "Edit" });
    const row = edit.closest("tr")!;
    // The words are the running state.
    expect(within(row).getByTestId("state-words").textContent).toBe("Paused");
    await user.click(within(row).getByRole("button", { name: "Why?" }));
    expect([...within(row).getByTestId("state-why").querySelectorAll("li")].map((li) => li.textContent?.split(" · ")[0])).toEqual([
      "Paused by the environment (THEATARR_FORCE_OFF)",
    ]);
    // The page says the global state once.
    expect(screen.getByTestId("downloads-off-notice").textContent).toContain("Paused by the environment (THEATARR_FORCE_OFF)");
    expect(screen.queryByText("Downloads on")).toBeNull();
  });

  it("lines the kinds' tables up with fixed column widths, and labels the stacked values on a phone", async () => {
    mockFetch(getRoutes());
    renderPage(<VersionsPage />);
    const row = (await screen.findByRole("button", { name: "Edit" })).closest("tr")!;
    const table = screen.getAllByTestId("library-table")[0];
    expect(table.className).toContain("md:table-fixed");
    expect(table.querySelector("thead")!.className).toContain("max-md:hidden");
    for (const label of ["Where", "Profile", "Sources"]) expect(within(row.cells[0]).getByText(label)).toBeTruthy();
  });

  it("names the profile and the providers on the row, and saves their keys", async () => {
    const lib = { ...version(), config: { ...version().config, profile: "radarr-4k-zurg", providers: ["rd", "ad", "tb"] } };
    mockFetch(getRoutes({ ...versions(), versions: [lib] }));
    renderPage(<VersionsPage />);
    const row = (await screen.findByRole("button", { name: "Edit" })).closest("tr")!;
    const cell = row.cells[2];
    expect(cell.textContent).toBe(readableCode("radarr-4k-zurg"));
    expect(cell.textContent).not.toBe("radarr-4k-zurg");
    expect(within(cell).getByTitle("radarr-4k-zurg")).toBeTruthy();
    expect(row.cells[3].textContent).toContain("RD › AD › TB");
    expect(row.textContent).not.toContain("rd › ad");
  });

  it("shows Downloads on, and Paused while Pause is on", async () => {
    const active = versions();
    mockFetch(getRoutes(active));
    renderPage(<VersionsPage />);
    const row = (await screen.findByRole("button", { name: "Edit" })).closest("tr")!;
    expect(within(row).getByTestId("state-words").textContent).toBe("Downloads on");
    cleanup();
    mockFetch(getRoutes({ ...active, switches: { ...active.switches, paused: true } }));
    renderPage(<VersionsPage />);
    const paused = (await screen.findByRole("button", { name: "Edit" })).closest("tr")!;
    expect(within(paused).getByTestId("state-words").textContent).toBe("Paused");
    expect(screen.getByTestId("downloads-off-notice").textContent).toContain("Paused: searches, new grabs and imports stop for every library; Interactive search can still look up releases.");
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
    expect(screen.queryByRole("combobox", { name: "Follows" })).toBeNull();
    expect(screen.queryByRole("combobox", { name: "Storage" })).toBeNull();
    expect(screen.queryByRole("combobox", { name: "Import mode" })).toBeNull();
    expectNamedControls();
    await user.click(screen.getByText("Advanced"));
    expect(advanced.open).toBe(false);
  });

  it("opens Advanced for a library that sends its grabs to a client or has an extra write root", async () => {
    const user = userEvent.setup();
    for (const [config, roots] of [[{ add_via: "client" }, undefined], [{}, ["/library/movies", "/library/extra"]]] as const) {
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

  it("leaves minimum availability and its delay out of an untouched save, with or without stored values", async () => {
    const put = { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) };
    const fetch = mockFetch([...getRoutes(), put]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const minimum = () => screen.getByRole("combobox", { name: "Minimum availability" }) as HTMLSelectElement;
    const delay = () => screen.getByRole("textbox", { name: "Availability delay (days)" }) as HTMLInputElement;
    expect(minimum().value).toBe("");
    expect([...minimum().options].map((o) => o.textContent)).toEqual(["Not set", "Announced", "In cinemas", "Released"]);
    expect(delay().value).toBe("");
    // Set and put back, a lone minus sign and letters: no trace.
    await user.selectOptions(minimum(), "inCinemas");
    await user.type(delay(), "-x");
    expect(delay().value).toBe("-");
    expect(screen.getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
    await user.selectOptions(minimum(), "");
    await user.clear(delay());
    expect(screen.queryByTestId("unsaved-count")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    const sent = fetch.calls("PUT", "/settings/versions/movie-web")[0].body as { config: object };
    expect(sent).toEqual({ generation: 4, key: "movie-web", config: version().config, roots: version().roots });
    expect(Object.keys(sent.config).sort()).toEqual(Object.keys(version().config).sort());
    page.unmount();

    // Stored values: shown, and an untouched save sends them as loaded.
    const set = { ...version(), config: { ...version().config, minimum_availability: "announced", availability_delay: -3 } };
    const again = mockFetch([...getRoutes({ ...versions(), versions: [set] }), put]);
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect(minimum().value).toBe("announced");
    expect(delay().value).toBe("-3");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(again.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(again.calls("PUT", "/settings/versions/movie-web")[0].body).toEqual({ generation: 4, key: "movie-web", config: set.config, roots: set.roots });
  });

  it("sets minimum availability and a negative delay, clears stored ones by sending them empty and 0, and offers neither to a series library", async () => {
    const put = { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) };
    const fetch = mockFetch([...getRoutes(), put]);
    const user = userEvent.setup();
    const page = renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Minimum availability" }), "released");
    await user.type(screen.getByRole("textbox", { name: "Availability delay (days)" }), "-14");
    expect(screen.getByRole("button", { name: "Undo change to Minimum availability" })).toBeTruthy();
    expect(screen.getByTestId("unsaved-count").textContent).toBe("2 unsaved changes");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toEqual({ generation: 4, key: "movie-web", config: { ...version().config, minimum_availability: "released", availability_delay: -14 }, roots: version().roots });
    page.unmount();

    const set = { ...version(), config: { ...version().config, minimum_availability: "announced", availability_delay: 7 } };
    const again = mockFetch([...getRoutes({ ...versions(), versions: [set] }), put]);
    const second = renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Minimum availability" }), "");
    await user.clear(screen.getByRole("textbox", { name: "Availability delay (days)" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(again.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(again.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { minimum_availability: "", availability_delay: 0 } });
    second.unmount();

    // A series library has neither field; a movie library turned into one drops what it had.
    const third = mockFetch([...getRoutes({ ...versions(), versions: [set] }), put]);
    const thirdPage = renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "series");
    expect(screen.queryByRole("combobox", { name: "Minimum availability" })).toBeNull();
    expect(screen.queryByRole("textbox", { name: "Availability delay (days)" })).toBeNull();
    // Picked back to Movies, the saved kind: the saved values are back, as with Undo.
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "movie");
    expect((screen.getByRole("combobox", { name: "Minimum availability" }) as HTMLSelectElement).value).toBe("announced");
    expect((screen.getByRole("textbox", { name: "Availability delay (days)" }) as HTMLInputElement).value).toBe("7");
    // Saved as a series library, it sends both cleared.
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "series");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(third.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(third.calls("PUT", "/settings/versions/movie-web")[0].body).toMatchObject({ config: { kind: "series", minimum_availability: "", availability_delay: 0 } });
    thirdPage.unmount();

    // A library saved as series, taken to Movies with both fields set, then to Series and Movies again: both are empty
    // (only a library saved as movies gets its saved values back).
    const series = { ...version(), config: { ...version().config, kind: "series" } };
    mockFetch([...getRoutes({ ...versions(), versions: [series] }), put]);
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "movie");
    await user.selectOptions(screen.getByRole("combobox", { name: "Minimum availability" }), "announced");
    await user.type(screen.getByRole("textbox", { name: "Availability delay (days)" }), "5");
    expect((screen.getByRole("textbox", { name: "Availability delay (days)" }) as HTMLInputElement).value).toBe("5");
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "series");
    await user.selectOptions(screen.getByRole("combobox", { name: "Kind" }), "movie");
    expect((screen.getByRole("combobox", { name: "Minimum availability" }) as HTMLSelectElement).value).toBe("");
    expect((screen.getByRole("textbox", { name: "Availability delay (days)" }) as HTMLInputElement).value).toBe("");
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

  it("sends a switch in the usual body, with its Changed mark and Undo", async () => {
    const fetch = mockFetch([...getRoutes(), { method: "PUT", path: "/settings/versions/movie-web", reply: json({ generation: 5 }) }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("switch", { name: "Prefer SeaDex" }));
    expect(screen.getByTestId("unsaved-count").textContent).toBe("1 unsaved change");
    await user.click(screen.getByRole("button", { name: "Undo change to Prefer SeaDex" }));
    expect(screen.getByRole("switch", { name: "Prefer SeaDex" }).getAttribute("aria-checked")).toBe("false");
    await user.click(screen.getByRole("switch", { name: "Prefer SeaDex" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toEqual({ generation: 4, key: "movie-web", config: { ...version().config, prefer_seadex: true }, roots: version().roots });
  });

  it("labels the upgrade rules in plain words", async () => {
    mockFetch(getRoutes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const options = (name: string) => within(screen.getByRole("combobox", { name })).getAllByRole("option").map((option) => option.textContent);
    expect(options("Upgrades")).toEqual(["Automatic", "Never"]);
    expect(screen.getByText("When a better release turns up for a file this library already has. Automatic replaces it, within the quality profile's upgrade setting and cutoff. Never keeps the file.")).toBeTruthy();
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
    await waitFor(() => expect(screen.getByRole("heading", { name: /Edit library WEB/ })).toBeTruthy());
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
    expect(screen.getByRole("heading", { name: /Edit library WEB/ })).toBeTruthy();
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
  const series = { ...version(), id: 2, key: "tv-web", config: { ...version().config, kind: "series", profile: "sonarr-web", library: "/library/tv" } };
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
    expect(within(movies).getAllByText("RD").length).toBeGreaterThan(0);
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
    // The stored profiles by name in words, as the add sheet shows them; the value stays the key.
    expect(optionsOf(profile, "Your profiles")).toEqual(["web=Web", "sonarr-web=Sonarr web", "anime-tv=Anime TV"]);
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
    // Without a key there is nothing to name the profile after: Save waits for one, nothing is created.
    expect((within(form).getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
    // The reason is there before anything is typed: a plain hint under the key.
    expect(within(form).getByText(/^Enter a name\. Used by connected apps/)).toBeTruthy();
    expect(fetch.calls("POST", "/settings/presets/create")).toHaveLength(0);
    await user.type(within(form).getByRole("textbox", { name: "Key" }), "movies");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/versions")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/presets/create")[0].body).toEqual({ generation: 4, key: "movies", flavour: "radarr", preset: "0537", modifiers: [] });
    expect(fetch.calls("POST", "/settings/versions")[0].body).toMatchObject({ generation: 5, key: "movies", config: { profile: "movies" } });
  });

  it("leaves profiles whose format set is of the other kind out of a series library's choices", async () => {
    const tagged = { ...quality, profiles: [summary("web", null, { format_set: "web-set" }), summary("sonarr-web", null, { flavour: "sonarr", format_set: "sonarr-set" }), summary("anime-tv", null)] };
    mockFetch([...routes().slice(0, 2), { method: "GET", path: "/settings/quality/overview", reply: tagged }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await screen.findByRole("region", { name: "Movies" });
    await user.click(screen.getByRole("button", { name: "Add library" }));
    const form = await screen.findByTestId("version-form");
    const profile = within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    const values = () => [...profile.options].map((o) => o.value);
    await waitFor(() => expect(values()).toEqual(["web", "anime-tv"]));
    await user.selectOptions(within(form).getByRole("combobox", { name: "Kind" }), "series");
    await waitFor(() => expect(values()).toEqual(["sonarr-web", "anime-tv"]));
  });

  it("does not keep the preselected movie profile when a new library becomes a series library without a series default", async () => {
    // No defaults for new libraries: the form opens with the first profile, a movie one.
    mockFetch([...routes().slice(0, 2), { method: "GET", path: "/settings/quality/overview", reply: { ...quality, defaults: {} } }]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await screen.findByRole("region", { name: "Movies" });
    await user.click(screen.getByRole("button", { name: "Add library" }));
    const form = await screen.findByTestId("version-form");
    const profile = within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    const kind = within(form).getByRole("combobox", { name: "Kind" });
    expect(profile.value).toBe("web");
    await user.selectOptions(kind, "series");
    expect(profile.value).toBe("");
    expect(profile.selectedOptions[0].text).toBe("Choose a profile");
    expect(within(form).getByTestId("profile-needed").textContent).toContain("Choose a profile for this series library");
    // A deliberate pick is allowed, the movie profile included, and stays through later kind changes.
    await user.selectOptions(profile, "web");
    expect(profile.value).toBe("web");
    expect(within(form).queryByTestId("profile-needed")).toBeNull();
    expect(within(form).queryByTestId("profile-other-kind")).toBeNull();
    // Kept across a kind change, but not without a word: the picker says which kind it was chosen for.
    await user.selectOptions(kind, "movie");
    expect(profile.value).toBe("web");
    expect(within(form).getByTestId("profile-other-kind").textContent).toContain("chosen while the library was a series library");
    await user.selectOptions(kind, "series");
    expect(profile.value).toBe("web");
    expect(within(form).queryByTestId("profile-other-kind")).toBeNull();
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
    expect([...anime.options].map((o) => `${o.value}=${o.text}`)).toEqual(["false=Not anime", "true=Anime only", "both=Any"]);
    // The kind in words; the stored values stay movie and series.
    expect([...(within(where).getByRole("combobox", { name: "Kind" }) as HTMLSelectElement).options].map((o) => `${o.value}=${o.text}`)).toEqual(["movie=Movies", "series=Series"]);
    await user.selectOptions(within(where).getByRole("combobox", { name: "Anime" }), "true");
    expect(profile.value).toBe("anime-tv");
    expect(within(releases).getByText(/our own profile/)).toBeTruthy();
    // A profile picked by hand stays when the kind changes.
    await user.selectOptions(profile, "web");
    await user.selectOptions(within(where).getByRole("combobox", { name: "Kind" }), "movie");
    expect(profile.value).toBe("web");
  });

  it("keeps a profile picked in the field when the kind changes, also when it is the old kind's default", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await screen.findByRole("region", { name: "Movies" });
    await user.click(screen.getByRole("button", { name: "Add library" }));
    const form = await screen.findByTestId("version-form");
    const profile = within(form).getByRole("combobox", { name: "Profile" }) as HTMLSelectElement;
    expect(profile.value).toBe("web");
    // Picked in the field: the movie default, on purpose. Series has a default of its own, which does not replace it.
    await user.selectOptions(profile, "web");
    await user.selectOptions(within(form).getByRole("combobox", { name: "Kind" }), "series");
    expect(profile.value).toBe("web");
    expect(within(form).getByTestId("profile-other-kind").textContent).toContain("chosen while the library was a movie library");
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


describe("Library rows", () => {
  const withConfig = (over: Record<string, unknown>) => {
    const v = { ...version(), config: { ...version().config, ...over } };
    return getRoutes({ ...versions(), versions: [v] } as ReturnType<typeof versions>);
  };
  const put = (reply: unknown = json({ generation: 5 })) => ({ method: "PUT", path: "/settings/versions/movie-web", reply });
  const row = async () => (await screen.findByRole("button", { name: "Edit" })).closest("tr")!;

  it("leaves an empty client out of the row's storage line", async () => {
    mockFetch(withConfig({ client: "" }));
    renderPage(<VersionsPage />);
    const r = await row();
    expect(within(r).getAllByTestId("library-where").map((line) => line.textContent)).toContain("debrid · symlink");
  });

  it("names the download client only for a library that sends grabs to it", async () => {
    mockFetch(withConfig({ client: "decypharr" }));
    const { unmount } = renderPage(<VersionsPage />);
    expect(within(await row()).getAllByTestId("library-where").map((line) => line.textContent)).toContain("debrid · symlink");
    unmount();
    mockFetch(withConfig({ client: "decypharr", add_via: "client" }));
    renderPage(<VersionsPage />);
    expect(within(await row()).getAllByTestId("library-where").map((line) => line.textContent)).toContain("debrid · decypharr · symlink");
  });

  it("saves an untouched form unchanged", async () => {
    const fetch = mockFetch([...withConfig({}), put()]);
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/versions/movie-web")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/versions/movie-web")[0].body).toEqual({ generation: 4, key: "movie-web", config: version().config, roots: version().roots });
  });

  it("offers no download tick and no state control", async () => {
    mockFetch(withConfig({}));
    const user = userEvent.setup();
    renderPage(<VersionsPage />);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    if (!(screen.getByTestId("library-advanced") as HTMLDetailsElement).open) await user.click(screen.getByText("Advanced"));
    expect(screen.queryByRole("checkbox", { name: "Don't download for this library" })).toBeNull();
    expect(screen.queryByRole("combobox", { name: "State" })).toBeNull();
  });
});
