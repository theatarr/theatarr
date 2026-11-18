// @vitest-environment jsdom
// The title page's Libraries table: one row per current library, provider pills and the file name, the held
// row's limits, the removed library's leftover line, chosen columns kept, and the details panel. Invented keys.
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { Toaster } from "@/components/toaster";
import { noteLocation } from "@/lib/last-list";
import { KeptFiles } from "./copy-actions";
import { LibraryRows } from "./library-rows";

afterEach(() => {
  window.localStorage.clear();
  vi.unstubAllGlobals();
});

const live = { can_edit: true, can_search: true, live_releases: true, grab_state: { paused: false, pause_forced: false, will_grab: true } };
const watch = { can_edit: true, can_search: false, live_releases: false, search_reason: "held by the server", grab_state: { paused: false, pause_forced: false, will_grab: false } };
const web = {
  id: 1, title_id: 7, key: "movie-web", label: "WEB", monitored: true, has_file: true, state: "filled", cached_on: ["rd", "tb"], control: live,
  upgrade: { state: "cutoff_met" },
  current: { quality: "WEBDL-2160p", size_bytes: 20 * 1024 ** 3, score: 1500, formats: [{ name: "DV HDR10", score: 1500 }], path: "/media/movies/Alpha (2020)/Alpha.2020.2160p.WEB-A.mkv" },
};
// An adopted file: no stored formats, so the API sends neither score nor formats.
const remux = {
  id: 2, title_id: 7, key: "movie-remux", label: "Remux", monitored: true, has_file: true, state: "filled", cached_on: [], control: watch,
  upgrade: { state: "below_cutoff" }, current: { quality: "Remux-2160p", size_bytes: 60 * 1024 ** 3, path: "/media/remux/Alpha (2020)/Alpha.2020.Remux.mkv" },
};
const title = { id: 7, kind: "movie", name: "Alpha" };
const manager = (extra: object[] = []) => mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["browse", "see_files", "manage"] } }, ...extra as never[]]);
const rowOf = (key: string) => screen.getAllByTestId("library-row").find((r) => r.getAttribute("data-version") === key)!;

describe("Libraries table", () => {
  it("shows a movie in two libraries as two rows with provider pills and the file name, blank where a fact is missing", async () => {
    manager();
    renderPage(<LibraryRows title={title as never} versions={[web, remux] as never} />);
    await waitFor(() => expect(screen.getAllByTestId("library-row")).toHaveLength(2));
    const a = within(rowOf("movie-web"));
    expect(a.getAllByTestId("cached-on").map((p) => p.textContent)).toEqual(["RD", "TB"]);
    expect(a.getByTestId("library-file").textContent).toBe("Alpha.2020.2160p.WEB-A.mkv");
    expect(a.getByTestId("library-file").getAttribute("title")).toBe(web.current.path);
    expect(a.getByText("1,500").getAttribute("title")).toBe("DV HDR10 +1,500");
    const b = rowOf("movie-remux");
    // A score never recorded reads "—", as History says it.
    expect(b.querySelector('[data-column="score"]')?.textContent).toBe("—");
    expect(b.querySelector('[data-column="cached_on"]')?.textContent).toBe("—");
    expect(within(b).getByText("Preferred quality not met")).toBeTruthy();
  });

  it("gives a held row no Interactive search and a disabled Search with the reason", async () => {
    manager();
    renderPage(<LibraryRows title={title as never} versions={[web, remux] as never} />);
    const b = await waitFor(() => rowOf("movie-remux"));
    await waitFor(() => expect(within(b).getByTestId("version-search-movie-remux")).toBeTruthy());
    expect(within(b).queryByTestId("version-interactive-movie-remux")).toBeNull();
    const search = within(b).getByTestId("version-search-movie-remux") as HTMLButtonElement;
    expect(search.disabled).toBe(true);
    expect(search.parentElement?.getAttribute("title")).toContain("Downloads off (held by the server)");
    expect(within(rowOf("movie-web")).getByTestId("version-interactive-movie-web")).toBeTruthy();
  });

  it("has no row for a removed library and names its leftover links", async () => {
    manager([{ method: "GET", path: "/titles/7/removal", reply: { title_id: 7, name: "Alpha", title: false, copies: [], leftover: [{ key: "movie-remux", label: "Remux", links: 2, downloads: 0 }] } }]);
    const user = userEvent.setup();
    const links = [1, 2].map((id) => ({ id, kind: "symlink", state: "active", path: `/media/remux/Alpha/${id}.mkv` }));
    renderPage(<LibraryRows title={title as never} versions={[web, { ...remux, removed: true, links }] as never} />);
    await waitFor(() => expect(screen.getAllByTestId("library-row")).toHaveLength(1));
    expect(screen.getByTestId("library-leftover").textContent).toContain("Still linked from a library this title was removed from: Remux (2 files).");
    await user.click(await screen.findByTestId("leftover-remove-movie-remux"));
    expect(await screen.findByTestId("remove-dialog")).toBeTruthy();
  });

  it("keeps a hidden column hidden after a remount", async () => {
    manager();
    const user = userEvent.setup();
    const first = renderPage(<LibraryRows title={title as never} versions={[web] as never} />);
    await waitFor(() => expect(rowOf("movie-web").querySelector('[data-column="size"]')).toBeTruthy());
    await user.click(screen.getByTestId("library-columns"));
    // A column whose hint would only repeat its name shows the name alone.
    expect(within(screen.getByTestId("column-list")).getByText("Quality").parentElement?.textContent).toBe("Quality");
    expect(within(screen.getByTestId("column-list")).getByText("Complete torrents on your accounts, as last checked; not necessarily the one playback uses.")).toBeTruthy();
    await user.click(within(screen.getByTestId("column-list")).getByRole("checkbox", { name: /^Size/ }));
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(rowOf("movie-web").querySelector('[data-column="size"]')).toBeNull();
    first.unmount();
    renderPage(<LibraryRows title={title as never} versions={[web] as never} />);
    // Kept per account: the stored choice applies once the account is known.
    await waitFor(() => expect(rowOf("movie-web").querySelector('[data-column="size"]')).toBeNull());
    expect(rowOf("movie-web").querySelector('[data-column="file"]')).toBeTruthy();
  });

  it("shows a Search now failure and drops the earlier outcome", async () => {
    let calls = 0;
    manager([{ method: "POST", path: "/titles/7/search", reply: () => ++calls === 1 ? { started: true, message: "Search started for WEB." } : json({ error: "search backend down" }, 500) }]);
    const user = userEvent.setup();
    renderPage(<LibraryRows title={title as never} versions={[web] as never} />);
    await user.click(await screen.findByTestId("version-search-movie-web"));
    expect(await screen.findByText("Search started for WEB.")).toBeTruthy();
    await user.click(screen.getByTestId("version-search-movie-web"));
    expect(await screen.findByText(/Search failed: search backend down/)).toBeTruthy();
    expect(screen.queryByText("Search started for WEB.")).toBeNull();
  });
});

describe("row details", () => {
  it("links no Wanted list that would not list the library: not monitored, or its quality met", async () => {
    manager();
    const user = userEvent.setup();
    const met = { ...web, upgrade: { state: "below_cutoff", quality: "WEBDL-2160p", score: 1500, quality_met: true, below: 1 } };
    const off = { ...web, id: 3, key: "movie-1080", label: "1080p", monitored: false, has_file: false, state: "wanted", current: undefined };
    const offHeld = { ...remux, monitored: false };
    renderPage(<LibraryRows title={title as never} versions={[met, off, offHeld] as never} />);
    for (const key of ["movie-web", "movie-1080", "movie-remux"]) await user.click(await screen.findByTestId(`copy-details-${key}`));
    for (const key of ["movie-web", "movie-1080", "movie-remux"]) {
      const panel = screen.getByTestId(`library-row-details-${key}`);
      expect(within(panel).queryByRole("link", { name: "See in Wanted" })).toBeNull();
      expect(within(panel).queryByRole("link", { name: "See in Cutoff unmet" })).toBeNull();
    }
  });

  it("changes the minimum availability from the details panel, and hands missing and cutoff copies to Wanted", async () => {
    const fetch = manager([
      { method: "POST", path: "/titles/minimum-availability", reply: { changed: 1 } },
      { method: "GET", path: "/titles/7", reply: {} },
    ]);
    const user = userEvent.setup();
    const missing = { ...web, id: 3, key: "movie-1080", label: "1080p", has_file: false, state: "wanted", minimum_availability: "inCinemas", current: undefined };
    renderPage(<LibraryRows title={title as never} versions={[web, missing] as never} />);
    await user.click(await screen.findByTestId("copy-details-movie-1080"));
    await user.click(screen.getByTestId("copy-details-movie-web"));
    const panel = screen.getByTestId("library-row-details-movie-1080");
    const select = within(panel).getByTestId("version-minimum-availability-movie-1080") as HTMLSelectElement;
    expect(select.value).toBe("inCinemas");
    await user.selectOptions(select, "announced");
    await waitFor(() => expect(fetch.calls("POST", "/titles/minimum-availability")).toHaveLength(1));
    expect(fetch.calls("POST", "/titles/minimum-availability")[0].body).toEqual({ title_ids: [7], versions: ["movie-1080"], minimum_availability: "announced" });
    expect(within(panel).getByRole("link", { name: "See in Wanted" }).getAttribute("href")).toBe("/wanted?version=movie-1080&title=7");
    expect(within(screen.getByTestId("library-row-details-movie-web")).getByRole("link", { name: "See in Cutoff unmet" }).getAttribute("href")).toBe("/wanted/cutoff?version=movie-web&title=7");
    expect(within(panel).getByTestId("copy-remove-movie-1080").textContent).toContain("Remove from this library");
  });
});

describe("fix round 1", () => {
  it("keeps a row removal scoped to its library when it is the title's last, deletes the files by default, and goes back to the list with a notice", async () => {
    const links = [{ id: 9, kind: "symlink", state: "active", path: "/media/remux/Alpha/a.mkv" }];
    let removed = false;
    const fetch = manager([
      // Before the removal the scope is the library; afterwards the server has nothing to scope (400).
      { method: "GET", path: "/titles/7/removal", reply: () => removed
        ? json({ error: "the title has no copy \"movie-web\"" }, 400)
        : { title_id: 7, name: "Alpha", title: true, copies: [{ key: "movie-web", label: "WEB", links: 1, downloads: 0 }], leftover: [] } },
      { method: "POST", path: "/titles/7/remove", reply: () => {
        removed = true;
        return {
          title: true, cleanup: true, failed: [],
          message: "Removed Alpha from the library; 1 library file deleted; its debrid torrents are being cleaned up (History shows the outcome).",
          kept: [{ path: "/media/movies/Alpha/b.mkv", reason: "another library still uses it" }],
        };
      } },
      { method: "GET", path: "/titles/7", reply: {} },
    ]);
    const user = userEvent.setup();
    function Where() {
      return <div data-testid="where">{useLocation().pathname}</div>;
    }
    let setVersions: (v: object[]) => void = () => {};
    function Page() {
      const [versions, set] = useState<object[]>([web, { ...remux, removed: true, links }]);
      setVersions = set;
      return <LibraryRows title={title as never} versions={versions as never} />;
    }
    noteLocation("/series?availability=missing");
    noteLocation("/title/7");
    renderPage(<><Page /><Where /><Toaster /></>, "/title/7");
    await user.click(await screen.findByTestId("copy-details-movie-web"));
    await user.click(screen.getByTestId("copy-remove-movie-web"));
    const box = (await screen.findByTestId("remove-files")) as HTMLInputElement;
    expect(box.checked).toBe(true);
    expect((screen.getByTestId("remove-debrid") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByTestId("remove-dialog").textContent).toContain("Import lists will not restore the removed libraries; add it yourself to bring it back");
    // Removing the title's last library offers the list exclusion, off by default (phase X: by the scope's title).
    expect((screen.getByTestId("remove-exclude") as HTMLInputElement).checked).toBe(false);
    await user.click(screen.getByTestId("confirm-ok"));
    await waitFor(() => expect(fetch.calls("POST", "/titles/7/remove")).toHaveLength(1));
    expect(fetch.calls("POST", "/titles/7/remove")[0].body).toEqual({ versions: ["movie-web"], unlink: true, debrid: true });
    // The title data comes back without the row: no error, and the scope is not read again (it would answer 400).
    act(() => setVersions([{ ...web, removed: true }, { ...remux, removed: true, links }]));
    expect(screen.queryAllByTestId("library-row")).toHaveLength(0);
    // The title is in no library now: back to the list last opened, with the outcome as a notice that opens History.
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/series"));
    expect(screen.queryByTestId("remove-dialog")).toBeNull();
    const notice = screen.getAllByTestId("toast").at(-1) as HTMLElement;
    expect(notice.textContent).toContain("Removed Alpha from the library; 1 library file deleted");
    expect(notice.textContent).toContain("another library still uses it");
    expect(within(notice).getAllByRole("link")[0].getAttribute("href")).toBe("/title/7?tab=history");
    await new Promise((r) => setTimeout(r, 50));
    expect(fetch.calls("GET", "/titles/7/removal")).toHaveLength(1);
    expect(screen.queryByText(/has no copy/)).toBeNull();
  });

  it("keeps the files and torrents when the box is cleared, and stays on the page for a library the title is not left without", async () => {
    const fetch = manager([
      { method: "GET", path: "/titles/7/removal", reply: { title_id: 7, name: "Alpha", title: false, copies: [{ key: "movie-web", label: "WEB", links: 1, downloads: 0 }], leftover: [] } },
      { method: "POST", path: "/titles/7/remove", reply: { title: false, cleanup: false, failed: [], kept: [], message: "Removed Alpha from WEB; its files stay linked and its debrid torrents stay." } },
      { method: "GET", path: "/titles/7", reply: {} },
    ]);
    const user = userEvent.setup();
    function Where() {
      return <div data-testid="where">{useLocation().pathname}</div>;
    }
    renderPage(<><LibraryRows title={title as never} versions={[web, remux] as never} /><Where /><Toaster /></>, "/title/7");
    await user.click(await screen.findByTestId("copy-details-movie-web"));
    await user.click(screen.getByTestId("copy-remove-movie-web"));
    await user.click(await screen.findByTestId("remove-files"));
    expect(screen.getByTestId("confirm-keeps").textContent).toContain("Its debrid torrents stay");
    // The torrents cannot go while the files stay: the second box is off and says why.
    const debrid = screen.getByTestId("remove-debrid") as HTMLInputElement;
    expect(debrid.checked).toBe(false);
    expect(debrid.disabled).toBe(true);
    expect(screen.getByTestId("remove-debrid-why").textContent).toContain("kept files would stop playing");
    await user.click(screen.getByTestId("confirm-ok"));
    await waitFor(() => expect(fetch.calls("POST", "/titles/7/remove")).toHaveLength(1));
    expect(fetch.calls("POST", "/titles/7/remove")[0].body).toEqual({ versions: ["movie-web"], unlink: false, debrid: false });
    await waitFor(() => expect(screen.queryByTestId("remove-dialog")).toBeNull());
    expect(screen.getByTestId("where").textContent).toBe("/title/7");
    expect(screen.getAllByTestId("toast").at(-1)?.textContent).toContain("its debrid torrents stay");
  });

  it("removes a library with the one kept in sync with it: names it, its files and torrents with the boxes, and offers the list exclusion when nothing is left", async () => {
    const fetch = manager([
      { method: "GET", path: "/titles/7/removal", reply: { title_id: 7, name: "Alpha", title: true, leftover: [], copies: [
        { key: "movie-remux", label: "Remux", links: 1, downloads: 0, sync_of: "movie-web" }, { key: "movie-web", label: "WEB", links: 1, downloads: 0 }] } },
      { method: "POST", path: "/titles/7/remove", reply: { title: true, cleanup: false, failed: [], kept: [], message: "Removed Alpha from the library." } },
      { method: "GET", path: "/titles/7", reply: {} },
    ]);
    const user = userEvent.setup();
    renderPage(<><LibraryRows title={title as never} versions={[web, remux] as never} /><Toaster /></>, "/title/7");
    await user.click(await screen.findByTestId("copy-details-movie-web"));
    await user.click(screen.getByTestId("copy-remove-movie-web"));
    const does = () => screen.getByTestId("confirm-does").textContent;
    await waitFor(() => expect(does()).toContain("Remux is kept in sync with WEB and is removed too, its files and debrid torrents too"));
    // Each library with its own files; which lose their files, with no total.
    expect(does()).toContain("Alpha is removed from every library it is in (Remux (1 file), WEB (1 file))");
    expect(does()).toContain("The library files of Remux and WEB are deleted from the library");
    await user.click(screen.getByTestId("remove-files"));
    expect(does()).toContain("Remux is kept in sync with WEB and is removed too");
    expect(does()).not.toContain("its files");
    expect(screen.getByTestId("confirm-keeps").textContent).toContain("The library files of Remux and WEB stay linked");
    await user.click(screen.getByTestId("remove-files"));
    await user.click(screen.getByTestId("remove-exclude"));
    await user.click(screen.getByTestId("confirm-ok"));
    await waitFor(() => expect(fetch.calls("POST", "/titles/7/remove")).toHaveLength(1));
    // The named library only: the server takes the one in sync again.
    expect(fetch.calls("POST", "/titles/7/remove")[0].body).toEqual({ versions: ["movie-web"], unlink: true, debrid: true, exclude: true });
  });

  it("counts a file two removed libraries kept in sync share once in the kept files line", async () => {
    manager();
    const shared = { id: 9, kind: "symlink", state: "active", path: "/media/movies/Alpha/a.mkv" };
    const versions = [{ ...web, removed: true, links: [shared] }, { ...remux, removed: true, links: [shared, { ...shared, id: 10, path: "/media/remux/Alpha/b.mkv", state: "dead" }] }];
    renderPage(<KeptFiles titleId={7} versions={versions as never} manage={false} />);
    expect((await screen.findByTestId("kept-files")).textContent).toBe("2 files kept from before it was removed.");
  });

  it("says a row's search may serve the title's other Live libraries", async () => {
    manager();
    renderPage(<LibraryRows title={title as never} versions={[web, { ...remux, control: live }] as never} />);
    const search = await screen.findByTestId("version-search-movie-web");
    expect(search.parentElement?.getAttribute("title")).toBe("Search WEB now. Other libraries that want this title may use the results too.");
  });

  it("has no Why? beside a plainly Available copy", async () => {
    manager();
    renderPage(<LibraryRows title={title as never} versions={[web] as never} />);
    await waitFor(() => expect(within(rowOf("movie-web")).getByTestId("upgrade-status")).toBeTruthy());
    expect(within(rowOf("movie-web")).queryByTestId("upgrade-why")).toBeNull();
  });

  it("shows the score the server sends, formats or not, and a missing one as —", async () => {
    // The server leaves out a score with no scoring evidence (linkScoreSQL); an adopted file's calculated score, with no
    // formats stored, stays.
    manager();
    const page = renderPage(<LibraryRows title={title as never} versions={[{ ...web, current: { ...web.current, formats: undefined } }] as never} />);
    await waitFor(() => expect(rowOf("movie-web").querySelector('[data-column="score"]')?.textContent).toBe("1,500"));
    page.unmount();
    renderPage(<LibraryRows title={title as never} versions={[{ ...web, current: { ...web.current, formats: undefined, score: undefined } }] as never} />);
    await waitFor(() => expect(rowOf("movie-web").querySelector('[data-column="score"]')?.textContent).toBe("—"));
    expect(screen.getByTestId("score-not-recorded").getAttribute("title")).toBe("Not scored");
  });

  it("offers Why? in Details when the Status column is hidden, and on a series row", async () => {
    window.localStorage.setItem("theatarr.library.title-libraries.3", JSON.stringify({ order: ["status", "quality"], shown: ["quality"] }));
    manager();
    const user = userEvent.setup();
    const page = renderPage(<LibraryRows title={title as never} versions={[{ ...web, upgrade: { state: "below_cutoff" } }] as never} />);
    await waitFor(() => expect(rowOf("movie-web").querySelector('[data-column="status"]')).toBeNull());
    await user.click(screen.getByTestId("copy-details-movie-web"));
    expect(within(screen.getByTestId("library-row-details-movie-web")).getByTestId("upgrade-why")).toBeTruthy();
    page.unmount();
    window.localStorage.clear();
    renderPage(<LibraryRows title={{ ...title, kind: "series" } as never} versions={[{ ...web, episodes: { have: 3, total: 3, missing: 0 }, upgrade: { state: "below_cutoff" } }] as never} />);
    await user.click(await screen.findByTestId("copy-details-movie-web"));
    expect(within(screen.getByTestId("library-row-details-movie-web")).getByTestId("upgrade-why")).toBeTruthy();
  });

  it("offers Library default, shows the default in force by name, and sends default to go back to it", async () => {
    window.localStorage.setItem("theatarr.library.title-libraries.3", JSON.stringify({ order: ["minimum_availability"], shown: ["minimum_availability"] }));
    const fetch = manager([{ method: "POST", path: "/titles/minimum-availability", reply: { changed: 1 } }, { method: "GET", path: "/titles/7", reply: {} }]);
    const user = userEvent.setup();
    const page = renderPage(<LibraryRows title={title as never} versions={[{ ...web, minimum_availability: "released", minimum_availability_default: true }] as never} />);
    const select = (await screen.findByTestId("version-minimum-availability-movie-web")) as HTMLSelectElement;
    expect(select.value).toBe("default");
    expect([...select.options].map((o) => o.textContent)).toEqual(["Library default (Released)", "Announced", "In cinemas", "Released"]);
    page.unmount();
    renderPage(<LibraryRows title={title as never} versions={[{ ...web, minimum_availability: "announced" }] as never} />);
    const chosen = (await screen.findByTestId("version-minimum-availability-movie-web")) as HTMLSelectElement;
    expect(chosen.value).toBe("announced");
    expect(chosen.options[0].textContent).toBe("Library default");
    await user.selectOptions(chosen, "default");
    await waitFor(() => expect(fetch.calls("POST", "/titles/minimum-availability")).toHaveLength(1));
    expect(fetch.calls("POST", "/titles/minimum-availability")[0].body).toEqual({ title_ids: [7], versions: ["movie-web"], minimum_availability: "default" });
  });

  it("shows an imported tba without offering it, says when nothing changed, and gives a non-manager text", async () => {
    window.localStorage.setItem("theatarr.library.title-libraries.3", JSON.stringify({ order: ["minimum_availability"], shown: ["minimum_availability"] }));
    manager([{ method: "POST", path: "/titles/minimum-availability", reply: { changed: 0 } }]);
    const user = userEvent.setup();
    const page = renderPage(<LibraryRows title={title as never} versions={[{ ...web, minimum_availability: "tba" }] as never} />);
    const imported = (await screen.findByTestId("version-minimum-availability-movie-web")) as HTMLSelectElement;
    expect(imported.value).toBe("tba");
    expect([...imported.options].map((o) => o.value)).toEqual(["default", "tba", "announced", "inCinemas", "released"]);
    await user.selectOptions(imported, "released");
    expect(await screen.findByText(/the movie is not in this library now/)).toBeTruthy();
    page.unmount();
    mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["browse", "see_files"] } }]);
    renderPage(<LibraryRows title={title as never} versions={[{ ...web, minimum_availability: "inCinemas" }] as never} />);
    await waitFor(() => expect(rowOf("movie-web").querySelector('[data-column="minimum_availability"]')?.textContent).toBe("In cinemas"));
    expect(screen.queryByTestId("version-minimum-availability-movie-web")).toBeNull();
  });
  it("links an admin from Held on to the file's torrent on Admin › Torrents; a manager gets no link", async () => {
    const hash = "abcdef0123456789abcdef0123456789abcdef01";
    const versions = [{ ...web, current: { ...web.current, info_hash: hash } }];
    mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage", "admin"] } }]);
    renderPage(<LibraryRows title={title as never} versions={versions as never} />);
    const link = await within(await waitFor(() => rowOf("movie-web"))).findByTestId("torrent-link");
    expect(link.getAttribute("href")).toBe("/admin/torrents?q=abcdef012345");
    expect(link.closest('[data-column="cached_on"]')).toBeTruthy();
  });

  it("gives a manager no Torrent link", async () => {
    manager();
    renderPage(<LibraryRows title={title as never} versions={[{ ...web, current: { ...web.current, info_hash: "ab".repeat(20) } }] as never} />);
    await screen.findByTestId("version-search-movie-web");
    expect(screen.queryByTestId("torrent-link")).toBeNull();
  });
});


describe("Library files (J5)", () => {
  it("folds the linked files by folder, each name with its full path visible once", async () => {
    manager();
    const user = userEvent.setup();
    const links = [
      { id: 1, kind: "symlink", path: "/media/tv/Delta/Season 01/Delta.S01E01.mkv", state: "active" },
      { id: 2, kind: "symlink", path: "/media/tv/Delta/Season 02/Delta.S02E01.mkv", state: "active" },
    ];
    renderPage(<LibraryRows title={title as never} versions={[{ ...web, links, current: { ...web.current, path: "/media/tv/Delta/Season 01/Delta.S01E01.mkv" } } as never]} />);
    await user.click(await screen.findByTestId("copy-details-movie-web"));
    const files = await screen.findByTestId("library-files");
    expect(within(files).getByText("Library files (2)")).toBeTruthy();
    expect(within(files).getByText("Season 01")).toBeTruthy();
    expect(within(files).getAllByTestId("library-file-path").map((p) => p.textContent)).toEqual(links.map((l) => l.path));
    // The row details no longer repeat it.
    expect(within(screen.getByTestId("library-row-details-movie-web")).queryByText("Path")).toBeNull();
  });

  it("says in Why? and Details that the better release found was tried, with the history row's reason", async () => {
    mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "browse", "see_files", "manage"] } }]);
    const user = userEvent.setup();
    const seen = new Date(Date.now() - 4 * 86400e3).toISOString();
    const release = "Alpha.2020.2160p.WEB-DL-BETTER";
    const v = {
      ...web, current: { ...web.current, info_hash: "ab".repeat(20) },
      upgrade: { state: "upgrade_available", quality: "WEBDL-2160p", score: 4500, quality_met: true, cutoff: { quality: "WEBDL-2160p", score: 10000, upgrades: true },
        next: { release, quality: "WEBDL-2160p", score: 6200, reason: "better format score: 6200 over 4500", source: "search", seen_at: seen } },
      history: [{ event: "failed", date: seen, release_title: release, reason: "Not cached on RD, AD and TB", missed_on: ["rd", "ad", "tb"], upgrade: true }],
    };
    renderPage(<LibraryRows title={title as never} versions={[v] as never} />);
    await user.click(await screen.findByTestId("upgrade-why"));
    const lines = [...within(await screen.findByTestId("upgrade-why-dialog")).getByTestId("why-lines").querySelectorAll("p")].map((p) => p.textContent);
    expect(lines[0]).toBe("You have: WEBDL-2160p, score 4,500.");
    expect(lines[1]).toBe("Upgrades stop at: WEBDL-2160p and a score of 10,000.");
    expect(lines[2]).toMatch(new RegExp(`^The quality is reached, the score is not\\. Better release found .+: ${release.replace(/\./g, "\\.")} \\(WEBDL-2160p, score 6,200\\): better format score, 6,200 over 4,500\\. Tried .+: not cached on RD, AD and TB\\. Your file is unchanged\\.$`));
    await user.click(screen.getByRole("button", { name: "Close" }));
    // Details: one Upgrade line with the same sentence; the library key and the hash last.
    await user.click(screen.getByTestId("copy-details-movie-web"));
    const panel = screen.getByTestId("library-row-details-movie-web");
    expect(within(panel).getByTestId("row-upgrade").textContent).toContain("Tried");
    const labels = [...panel.querySelectorAll("dt")].map((d) => d.textContent);
    expect(labels.filter((l) => l?.startsWith("Upgrade"))).toEqual(["Upgrade"]);
    expect(labels.slice(-2)).toEqual(["Library key", "Hash"]);
  });

  it("heads the switch column Monitored", async () => {
    manager();
    renderPage(<LibraryRows title={title as never} versions={[web] as never} />);
    await waitFor(() => expect(screen.getAllByTestId("library-row")).toHaveLength(1));
    expect(screen.getByRole("columnheader", { name: "Monitored" })).toBeTruthy();
  });
});

describe("a file's media (Y6)", () => {
  const phone = () => vi.stubGlobal("matchMedia", (q: string) => ({ matches: !q.includes("min-width"), media: q, addEventListener() {}, removeEventListener() {} }));
  const subs = ["eng", "ara", "zho", "hrv", "ces", "dan", "nld", "fin", "fre", "ger", "ell", "heb", "hin", "hun", "ind", "ita", "jpn", "kor", "msa", "nor", "pol", "por", "ron", "rus", "spa", "swe", "tha", "tur", "ukr", "vie"];
  const media = { resolution: "3840x2160", video_codec: "x265", video_bit_depth: 10, video_dynamic_range: "DV HDR10", audio_codec: "TrueHD Atmos", audio_channels: 7.1,
    audio_languages: "eng/jpn", subtitles: subs.join("/"), run_time: "2:03:04",
    audio_tracks: [{ codec: "TrueHD Atmos", channels: 7.1, language: "eng" }, { codec: "FLAC", channels: 2, language: "jpn" }] };
  const probed = { ...web, current: { ...web.current, media } };

  it("adds the media line to a movie's phone card, and none for a file without media", async () => {
    phone();
    manager();
    renderPage(<LibraryRows title={title as never} versions={[probed, remux] as never} />);
    await waitFor(() => expect(screen.getAllByTestId("library-row")).toHaveLength(2));
    expect(within(rowOf("movie-web")).getByTestId("library-media").textContent)
      .toBe("3840x2160 · x265 10-bit · DV HDR10 · TrueHD Atmos 7.1 · English, Japanese · subtitles English, Arabic, Chinese, Croatian +26 · 2:03:04");
    expect(within(rowOf("movie-remux")).queryByTestId("library-media")).toBeNull();
  });

  it("shows a Media group in a movie row's Details: every subtitle once in full, each audio track on a line, no tooltip", async () => {
    phone();
    manager();
    const user = userEvent.setup();
    renderPage(<LibraryRows title={title as never} versions={[probed, remux] as never} />);
    await user.click(await screen.findByTestId("copy-details-movie-web"));
    const group = within(screen.getByTestId("library-row-details-movie-web")).getByTestId("media-group");
    const value = (label: string) => within(group).getByText(label).nextElementSibling as HTMLElement;
    expect(value("Video").textContent).toBe("3840x2160 · x265 10-bit · DV HDR10");
    expect([...value("Audio").querySelectorAll("span > span")].map((s) => s.textContent)).toEqual(["English · TrueHD Atmos 7.1", "Japanese · FLAC 2.0"]);
    expect(within(group).queryByText("Audio languages")).toBeNull();
    expect(value("Subtitles").textContent?.split(", ")).toHaveLength(30);
    expect(value("Subtitles").textContent).not.toContain("+");
    expect(value("Runtime").textContent).toBe("2:03:04");
    expect(group.querySelector("[title]")).toBeNull();
    // A file without media has no group.
    await user.click(screen.getByTestId("copy-details-movie-remux"));
    expect(within(screen.getByTestId("library-row-details-movie-remux")).queryByTestId("media-group")).toBeNull();
  });
});
