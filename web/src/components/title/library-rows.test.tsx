// @vitest-environment jsdom
// The title page's Libraries table: one row per current library, provider pills and the file name, the Watch-only
// row's limits, the removed library's leftover line, chosen columns kept, and the details panel. Invented keys.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { LibraryRows } from "./library-rows";

afterEach(() => window.localStorage.clear());

const live = { mode: "active" as const, can_edit: true, can_search: true, live_releases: true, grab_state: { mode: "active", paused: false, pause_forced: false, will_grab: true } };
const watch = { mode: "shadow" as const, can_edit: true, can_search: false, live_releases: false, search_reason: "shadow mode", grab_state: { mode: "shadow", paused: false, pause_forced: false, will_grab: false } };
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
    expect(b.querySelector('[data-column="score"]')?.textContent).toBe("");
    expect(b.querySelector('[data-column="cached_on"]')?.textContent).toBe("—");
    expect(within(b).getByText("Best quality known")).toBeTruthy();
  });

  it("gives a Watch-only row its tag, no Interactive search and a disabled Search with the reason", async () => {
    manager();
    renderPage(<LibraryRows title={title as never} versions={[web, remux] as never} />);
    const b = await waitFor(() => rowOf("movie-remux"));
    await waitFor(() => expect(within(b).getByTestId("version-search-movie-remux")).toBeTruthy());
    expect(within(b).getByText("Watch only")).toBeTruthy();
    expect(document.body.textContent?.match(/Watch only/g)).toHaveLength(1);
    expect(within(b).queryByTestId("version-interactive-movie-remux")).toBeNull();
    const search = within(b).getByTestId("version-search-movie-remux") as HTMLButtonElement;
    expect(search.disabled).toBe(true);
    expect(search.parentElement?.getAttribute("title")).toContain("Watch only");
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
    expect(within(panel).getByRole("link", { name: "See in Wanted" }).getAttribute("href")).toBe("/wanted?version=movie-1080");
    expect(within(screen.getByTestId("library-row-details-movie-web")).getByRole("link", { name: "See in Cutoff unmet" }).getAttribute("href")).toBe("/wanted/cutoff?version=movie-web");
    expect(within(panel).getByTestId("copy-remove-movie-1080").textContent).toContain("Remove from this library");
  });
});
