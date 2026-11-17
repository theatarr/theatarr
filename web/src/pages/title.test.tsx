// @vitest-environment jsdom
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import { TitlePage } from "./title";
import { SearchProvider } from "@/components/search/search-context";
import { json, mockFetch, renderPage } from "@/test/page";

const me = { id: 3, username: "manager", permissions: ["see_files", "manage"], csrf_token: "t" };
const title = { id: 70, kind: "movie", name: "Juniper Signal", year: 2021, monitored: true, versions: [] };

describe("Title page", () => {
  it("says why a metadata refresh failed", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: me },
      { method: "GET", path: "/titles/70", reply: title },
      { method: "POST", path: "/titles/70/refresh", reply: json({ error: "metadata refresh is off (no TMDb key)" }, 503) },
    ]);
    const user = userEvent.setup();
    renderPage(
      <SearchProvider>
        <Routes>
          <Route path="/title/:id" element={<TitlePage />} />
        </Routes>
      </SearchProvider>,
      "/title/70?tab=about",
    );
    await user.click(await screen.findByRole("button", { name: /Refresh metadata/ }));
    expect((await screen.findByTestId("title-refresh-error")).textContent).toBe("Refresh failed: metadata refresh is off (no TMDb key)");
    expect(screen.getByRole("button", { name: /Refresh metadata/ })).toBeTruthy();
  });

  it("dates About by the general release day, says Releases for one to come and Latest file for the newest file", async () => {
    const page = (t: object) => {
      mockFetch([
        { method: "GET", path: "/auth/me", reply: me },
        { method: "GET", path: "/titles/70", reply: { ...title, ...t } },
      ]);
      return renderPage(
        <SearchProvider>
          <Routes>
            <Route path="/title/:id" element={<TitlePage />} />
          </Routes>
        </SearchProvider>,
        "/title/70?tab=about",
      );
    };
    // A wide re-release moved TMDb's date and the chosen cinema date to 2026; the general release day is 1988.
    const old = page({ year: 1988, release_date: "2026-09-04", in_cinemas: "2026-09-04", released: "1988-07-16", digital_release: "2026-09-04", added_at: new Date().toISOString() });
    const facts = await screen.findByTestId("title-facts");
    expect(facts.textContent).toContain("ReleasedJul 16, 1988");
    expect(facts.textContent).toContain("Latest file");
    expect(facts.textContent).not.toMatch(/Added/);
    old.unmount();
    page({ year: 2027, release_date: "2999-06-18", released: "2999-06-18" });
    expect((await screen.findByTestId("title-facts")).textContent).toContain("Releases");
  });

  it("reads a title every copy of which was removed as one not in the library, with Add and its kept files", async () => {
    const links = [{ id: 1, path: "/movies/Juniper Signal (2021)/a.mkv", kind: "symlink", state: "active" }, { id: 2, path: "/movies/Juniper Signal (2021)/b.mkv", kind: "symlink", state: "active" }];
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { ...me, permissions: [...me.permissions, "add"] } },
      { method: "GET", path: "/titles/70", reply: { ...title, kind: "series", monitored: false, versions: [{ id: 5, key: "tv-2160p", state: "filled", monitored: false, removed: true, links }] } },
      { method: "GET", path: "/add/options", reply: { kind: "series", anime: false, title_id: 70, in_library: false, router_enabled: true, episodes_known: false, can_search: false, seasons: [], versions: [] } },
      { method: "GET", path: "/titles/70/removal", reply: { title_id: 70, name: "Juniper Signal", title: false, copies: [], leftover: [{ key: "tv-2160p", label: "4K", links: 2, downloads: 0 }] } },
    ]);
    renderPage(
      <SearchProvider>
        <Routes>
          <Route path="/title/:id" element={<TitlePage />} />
        </Routes>
      </SearchProvider>,
      "/title/70",
    );
    expect((await screen.findByTestId("title-readiness")).textContent).toBe("Not in the library");
    expect((await screen.findByTestId("kept-files")).textContent).toBe("2 files kept from before it was removed: Delete them");
    // Delete them: deleting the files is the point, so only the debrid box is offered, and no library is left to share a file.
    await userEvent.setup().click(screen.getByTestId("leftover-remove-tv-2160p"));
    expect(await screen.findByTestId("remove-debrid")).toBeTruthy();
    expect(screen.queryByTestId("remove-files")).toBeNull();
    expect(screen.getByTestId("remove-dialog").textContent).not.toContain("A file another library of the title uses stays");
    await userEvent.setup().click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByTestId("library-rows")).toBeNull();
    expect(screen.queryByRole("button", { name: /Rename files/ })).toBeNull();
    expect(screen.queryByText("Not monitored")).toBeNull();
    expect(screen.getByTestId("title-facts").textContent).not.toContain("Monitored");
    // No episode list: About and History.
    expect(screen.queryByRole("tab", { name: "Episodes" })).toBeNull();
    expect(screen.getByRole("tab", { name: "History" })).toBeTruthy();
    expect((await screen.findAllByTestId("hero-add"))[0].textContent).toContain("Add");
  });

  it("says Pause once above the Libraries table", async () => {
    const grab_state = { paused: true, pause_forced: false, will_grab: false };
    const fetch = mockFetch([
      { method: "GET", path: "/auth/me", reply: me },
      { method: "GET", path: "/titles/70", reply: { ...title, versions: [{ id: 5, key: "anime-tv-theatarr", label: "Anime", state: "wanted", monitored: true,
        control: { can_edit: true, can_search: false, live_releases: true, search_reason: "Paused: nothing is searched or downloaded", grab_state } }] } },
      { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 70, router_enabled: false, episodes_known: false, can_search: false, seasons: [],
        versions: [{ key: "anime-tv-theatarr", kind: "movie", enable: "", fits: true, default: true, on: true, searchable: false, label: "Anime", grab_state }] } },
    ]);
    renderPage(
      <SearchProvider>
        <Routes>
          <Route path="/title/:id" element={<TitlePage />} />
        </Routes>
      </SearchProvider>,
      "/title/70",
    );
    // One notice for the page; no row repeats Paused, and no note repeats the global switch.
    expect((await screen.findByTestId("downloads-off-notice")).textContent).toContain("Paused: nothing is searched, downloaded or linked for any library");
    await waitFor(() => expect(fetch.calls("GET", "/add/options")).toHaveLength(1));
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(screen.queryByTestId("title-no-search")).toBeNull();
    expect(screen.queryByTestId("control-note")).toBeNull();
    expect(screen.getAllByTestId("downloads-off-notice")).toHaveLength(1);
    expect(within(screen.getByTestId("library-row")).queryByText("Paused")).toBeNull();
    expect(document.body.textContent?.split("for any library")).toHaveLength(2);
    // Search now stays, disabled, with Pause as its tooltip (not another line on the page).
    const search = screen.getByTestId("title-search-now");
    expect(search.hasAttribute("disabled")).toBe(true);
    expect(search.getAttribute("title")).toBe("Paused: nothing is searched, downloaded or linked for any library.");
  });

  it("offers no hero action when every library that fits is on the title, other libraries aside", async () => {
    const adder = { ...me, permissions: ["see_files", "manage", "add"] };
    const lib = { kind: "movie", enable: "", fits: true, default: true, on: true, searchable: true, label: "WEB" };
    for (const [versions, want] of [[[{ ...lib, key: "movie-web" }, { ...lib, key: "movie-remux" }, { ...lib, key: "anime-movie", fits: false, default: false, on: false }], null], [[{ ...lib, key: "movie-web" }, { ...lib, key: "movie-2160", on: false }], "Add to another library"], [[{ ...lib, key: "movie-web", on: false }], "Monitor again"],
      // Both libraries held, one monitored: its switch in the Libraries table does it, no hero action.
      [[{ ...lib, key: "movie-web" }, { ...lib, key: "movie-remux", on: false }], null]] as const) {
      document.body.innerHTML = "";
      const api = mockFetch([
        { method: "GET", path: "/auth/me", reply: adder },
        { method: "GET", path: "/titles/70", reply: { ...title, versions: [{ id: 5, key: "movie-web", label: "WEB", state: "wanted", monitored: versions[0].on },
          ...(versions.length === 2 && versions[1].key === "movie-remux" && !versions[1].on ? [{ id: 6, key: "movie-remux", label: "Remux", state: "wanted", monitored: false }] : [])] } },
        { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 70, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions } },
      ]);
      renderPage(<SearchProvider><Routes><Route path="/title/:id" element={<TitlePage />} /></Routes></SearchProvider>, "/title/70");
      if (want) {
        await waitFor(() => expect(screen.getAllByTestId("hero-add")[0].textContent?.trim()).toBe(want));
        expect(screen.getAllByTestId("hero-add")[0].hasAttribute("disabled")).toBe(false);
      } else {
        // Nothing in the hero, and no bar pinned over the phone's content.
        await screen.findByTestId("title-hero");
        await waitFor(() => expect(api.calls("GET", "/add/options").length).toBeGreaterThan(0));
        await new Promise((r) => setTimeout(r, 20));
        expect(screen.queryByTestId("hero-add")).toBeNull();
        expect(screen.queryByTestId("title-sticky-action")).toBeNull();
      }
    }
  });

  it("reads Getting ready to a household account for a missing title with a download in hand", async () => {
    const household = { id: 8, username: "demo-limited", permissions: ["browse", "request"], csrf_token: "t" };
    const missing = { ...title, availability: "missing", release_date: "2020-01-01", versions: [{ id: 5, key: "movie-remux", label: "4K Remux", state: "wanted", monitored: true, has_file: false }] };
    // In a library without a file: "Not available yet", never "Not in the library yet".
    for (const [acquiring, want] of [[true, "Getting ready"], [false, "Not available yet"]] as const) {
      mockFetch([
        { method: "GET", path: "/auth/me", reply: household },
        { method: "GET", path: "/titles/70", reply: { ...missing, acquiring } },
        { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 70, router_enabled: false, episodes_known: false, can_search: false, seasons: [], versions: [] } },
        { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
      ]);
      const { unmount } = renderPage(
        <SearchProvider>
          <Routes>
            <Route path="/title/:id" element={<TitlePage />} />
          </Routes>
        </SearchProvider>,
        "/title/70",
      );
      expect((await screen.findByTestId("title-readiness")).textContent).toBe(want);
      unmount();
    }
  });

  it("names the libraries in the Request box when its answer differs from the top line", async () => {
    const household = { id: 8, username: "demo-limited", permissions: ["browse", "request"], request_versions: ["movie-web"], csrf_token: "t" };
    const off = { paused: true, pause_forced: false, will_grab: false };
    const held = { id: 5, key: "movie-remux", label: "4K", state: "filled", monitored: true, has_file: true, current: { quality: "2160p" } };
    const web = { id: 6, key: "movie-web", label: "4K", state: "wanted", monitored: true, has_file: false, control: { can_edit: false, can_search: false, live_releases: false, grab_state: off } };
    const opt = (key: string) => ({ key, label: "4K", kind: "movie", enable: "", fits: true, default: false, on: true, searchable: false, grab_state: off });
    for (const [versions, want] of [[[held, web], "In 4K (2): not available yet"], [[{ ...held, control: web.control }], "Available"]] as const) {
      mockFetch([
        { method: "GET", path: "/auth/me", reply: { ...household, request_versions: [versions.at(-1)!.key] } },
        { method: "GET", path: "/titles/70", reply: { ...title, versions } },
        { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 70, router_enabled: true, episodes_known: false, can_search: false, seasons: [], versions: versions.map((v) => opt(v.key)) } },
        { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
      ]);
      const { unmount } = renderPage(
        <SearchProvider>
          <Routes>
            <Route path="/title/:id" element={<TitlePage />} />
          </Routes>
        </SearchProvider>,
        "/title/70",
      );
      expect((await screen.findByTestId("title-readiness")).textContent).toBe("Ready to watch in 4K");
      await waitFor(() => expect(screen.getByTestId("title-request-settled").textContent?.trim()).toBe(want));
      // Never a library key for an account without See files.
      expect(document.body.textContent).not.toContain("movie-web");
      unmount();
    }
  });
});
describe("Title page facts (J5)", () => {
  const show = (t: object, path = "/title/70") => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: me },
      { method: "GET", path: "/titles/70", reply: { ...title, ...t } },
      { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 70, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions: [] } },
      { method: "GET", path: "/activity/downloads/41/attempts", reply: { attempts: [{ provider: "rd", outcome: "skipped", code: "miss", message: "not cached", at: "2026-11-14T10:00:00Z" }] } },
    ]);
    renderPage(<SearchProvider><Routes><Route path="/title/:id" element={<TitlePage />} /></Routes></SearchProvider>, path);
  };
  const lib = { id: 5, key: "movie-web", label: "4K WEB", state: "wanted", monitored: true, has_file: false, control: { can_edit: true, can_search: true, live_releases: true, grab_state: { paused: false, pause_forced: false, library_writes: true, will_grab: true } } };

  it("says the download's stage in the hero while nothing is held, not Missing", async () => {
    show({ availability: "missing", release_date: "2020-01-01", versions: [{ ...lib, acquisition: { stage: "starting", words: "", download_id: 1, downloads: 1, updated_at: "2026-11-14T10:00:00Z" } }] });
    expect((await screen.findByTestId("title-readiness")).textContent).toBe("Waiting to start");
  });

  it("names each library's latest event under the status, a failed attempt beside Upgrade found", async () => {
    const recent = new Date().toISOString() // now, so it reads "today" at any hour;
    show({ availability: "available", versions: [
      { ...lib, has_file: true, state: "filled", current: { quality: "WEBDL-2160p", files: 1 }, upgrade: { state: "upgrade_available" }, history: [{ event: "failed", date: recent, reason: "Not cached on RD and TB", missed_on: ["rd", "tb"], upgrade: true, download_id: 41 }] },
      { ...lib, id: 6, key: "movie-remux", label: "4K Remux", has_file: true, state: "cutoff_met", current: { quality: "Remux-2160p", files: 1 }, history: [{ event: "imported", date: recent }] },
    ] });
    const events = await screen.findByTestId("title-events");
    expect(events.textContent).toContain("4K WEB: Last upgrade attempt today: not cached on RD and TB");
    expect(events.textContent).toContain("4K Remux: Imported today");
    // The row keeps Upgrade found and shows the attempt beside it.
    const row = (await screen.findAllByTestId("library-failed"))[0];
    expect(row.textContent).toBe("Last upgrade attempt today: not cached on RD and TB");
    // Details opens that attempt: its provider attempts.
    await userEvent.setup().click(screen.getByTestId("title-event-details"));
    const dialog = await screen.findByTestId("attempt-dialog");
    expect((await within(dialog).findByTestId("attempt-list")).textContent).toContain("RD");
  });

  it("says a library's first download beside another library's Available", async () => {
    show({ availability: "available", versions: [
      { ...lib, key: "movie-remux", label: "4K Remux", has_file: true, state: "cutoff_met", current: { quality: "Remux-2160p", files: 1 } },
      { ...lib, acquisition: { stage: "downloading", progress: 40, words: "", download_id: 1, downloads: 1, updated_at: "2026-11-14T10:00:00Z" } },
    ] });
    expect((await screen.findByTestId("title-readiness")).textContent).toBe("Available in 4K");
    expect(screen.getByTestId("title-stage").textContent).toBe("4K WEB: Downloading · 40%");
  });

  it("says why a missing film is missing and what next", async () => {
    show({ availability: "missing", release_date: "2020-01-01", auto_search: { missing: false, upgrades: true, enabled: true }, versions: [{ ...lib, rss: true }] });
    expect((await screen.findAllByTestId("library-why"))[0].textContent).toBe("Not searched yet. Automatic backlog searching is off; new releases are still picked up from RSS. Use Search now to request a search.");
  });

  it("lists the title's tags on About, each opening its list filtered by it", async () => {
    show({ tags: ["debrid-upgrade", "kids & teens"] }, "/title/70?tab=about");
    const tags = await screen.findByTestId("title-tags");
    expect([...tags.querySelectorAll("a")].map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["debrid-upgrade", "/movies?tag=debrid-upgrade"],
      ["kids & teens", "/movies?tag=kids%20%26%20teens"],
    ]);
  });

  it("links a film to its collection on About", async () => {
    show({ collection: { tmdb_id: 8091, name: "Alien Collection" } }, "/title/70?tab=about");
    expect((await screen.findByTestId("title-collection")).getAttribute("href")).toBe("/collections/8091");
  });
});

