// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Catalog } from "./catalog";
import { Toaster } from "@/components/toaster";
import { searchOff } from "@/components/library/title-table";
import { expectNamedControls, mockFetch, renderPage } from "@/test/page";

// The lists open the shell's search overlay; the provider lives in the shell, so the context is stubbed here.
const search = vi.hoisted(() => ({ openOverlay: vi.fn() }));
vi.mock("@/components/search/search-context", () => ({ useSearchUI: () => search }));

const shadow = { mode: "shadow", can_edit: true, can_search: false, live_releases: false };
const managed = { mode: "active", can_edit: true, can_search: true, live_releases: true };

const titles = [
  {
    id: 1, kind: "series", name: "The Pellbrook Inquiries", year: 2017, monitored: true, network: "Northstar Network",
    next_up: { date: "2099-11-04", season: 3, number: 6 }, size_bytes: 184e9, added_at: "2026-10-28T00:00:00Z", title_added_at: "2025-10-01T12:00:00Z",
    versions: [
      { id: 11, key: "tv-2160p", label: "4K", monitored: true, has_file: true, quality: "WEBDL-2160p", state: "wanted", control: shadow,
        coverage: { have: 13, total: 17, aired: 17, upcoming: 1, in_flight: 2 },
        acquisition: { stage: "downloading", words: "Downloading (37%)", progress: 37, download_id: 5, downloads: 2, episodes: 2, stages: { downloading: 1, queued: 1 }, updated_at: "2026-10-29T00:00:00Z" } },
      { id: 12, key: "tv-web", label: "1080p WEB", monitored: true, has_file: true, quality: "WEBDL-1080p", state: "cutoff_met", control: managed,
        coverage: { have: 17, total: 17, aired: 17, upcoming: 1 } },
    ],
  },
  {
    id: 2, kind: "series", name: "Ferrous Bay", year: 2019, monitored: true,
    versions: [
      { id: 21, key: "tv-2160p", label: "4K", monitored: false, has_file: true, quality: "WEBDL-2160p", state: "wanted", control: managed,
        coverage: { have: 8, total: 18, aired: 18, upcoming: 0 } },
    ],
  },
];

const me = { id: 7, username: "owner", permissions: ["see_files", "manage", "admin"], csrf_token: "t" };
const routes = (extra: { method: string; path: string; reply: unknown }[] = []) => [
  { method: "GET", path: "/auth/me", reply: me },
  { method: "GET", path: "/titles", reply: { items: titles, total: 2, offset: 0 } },
  { method: "GET", path: "/titles/facets", reply: {} },
  ...extra,
];

beforeEach(() => window.localStorage.clear());
afterEach(() => window.localStorage.clear());

describe("Library table", () => {
  beforeEach(() => window.localStorage.setItem("theatarr.catalog.view", "table"));
  afterEach(() => vi.unstubAllGlobals());

  it("shows the column it is sorted by while its sort lasts, so the order follows a visible cell", async () => {
    mockFetch(routes());
    renderPage(<Catalog id="movies" />, "/movies?sort=next");
    await screen.findAllByRole("columnheader");
    await waitFor(() => expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Title", "Next release", "Status", "Libraries", "Studio", "Size", "Added", "Actions"]));
  });

  // The frame is 800 px wide; the table as wide as its columns make it.
  const measured = async (tableWidth: number) => {
    const observers: (() => void)[] = [];
    vi.stubGlobal("ResizeObserver", class { constructor(cb: () => void) { observers.push(cb); } observe() {} disconnect() {} });
    const rect = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      return { width: this.tagName === "TABLE" ? tableWidth : 800 } as DOMRect;
    });
    mockFetch(routes());
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    const table = document.querySelector("table") as HTMLElement;
    rect.mockRestore();
    return table;
  };

  it("scrolls sideways when the columns are wider than the frame, so none is cut off", async () => {
    const table = await measured(1200);
    await waitFor(() => expect(table.parentElement?.className).toContain("overflow-x-auto"));
    expect(table.getAttribute("data-sticky")).toBeNull();
  });

  it("keeps the header sticky when the table fits its frame", async () => {
    const table = await measured(800);
    await waitFor(() => expect(table.getAttribute("data-sticky")).toBe("true"));
    expect(table.parentElement?.className).toContain("overflow-x-clip");
  });

  it("draws one line per title: the default series columns, one pill per library and the stage as Status", async () => {
    mockFetch(routes());
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Title", "Status", "Libraries", "Network", "Next airing", "Seasons", "Size", "Actions"]);
    const row = screen.getByRole("link", { name: "The Pellbrook Inquiries" }).closest("tr") as HTMLElement;
    expect(within(row).getAllByTestId("library-pill").map((p) => [p.textContent, p.getAttribute("data-tone")])).toEqual([
      ["4K · 13/17", "ok"],
      ["1080p WEB · 17/17", "ok"],
    ]);
    expect(within(row).getByRole("link", { name: "Downloading · 37%" }).getAttribute("href")).toBe("/activity?show=downloading");
    // No monitor switch on the row: it is in the Libraries dialog a pill opens.
    expect(within(row).queryByRole("switch")).toBeNull();
    // Ferrous Bay's library is not monitored but the title is.
    const other = screen.getByRole("link", { name: "Ferrous Bay" }).closest("tr") as HTMLElement;
    expect(within(other).getByTestId("library-pill").textContent).toBe("4K · 8/18");
    expectNamedControls();
  });

  it("puts a download after the badge word when the title already has a file, and alone when it has none", async () => {
    const downloading = { stage: "downloading", words: "Downloading (64%)", progress: 64, download_id: 4, downloads: 1, updated_at: "2026-10-29T00:00:00Z" };
    const movies = [
      { id: 6, kind: "movie", name: "Corvid Hour", year: 2022, monitored: true, availability: "partial",
        versions: [
          { id: 61, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: managed, acquisition: downloading },
          { id: 62, key: "movie-web", label: "WEB", monitored: true, has_file: true, quality: "WEBDL-2160p", state: "cutoff_met", control: managed },
        ] },
      { id: 7, kind: "movie", name: "Sundial", year: 2021, monitored: true, availability: "missing",
        versions: [{ id: 71, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: managed, acquisition: downloading }] },
    ];
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: movies, total: 2, offset: 0 } } : r)));
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Corvid Hour");
    const status = (name: string) => (screen.getByRole("link", { name }).closest("tr") as HTMLElement).querySelector("td[data-column=status]") as HTMLElement;
    expect(within(status("Corvid Hour")).getByTestId("status-cell").textContent).toBe("Partly available");
    // The short stage: the percent, with the stage's words as name and tooltip, linked as before.
    const stage = within(status("Corvid Hour")).getByRole("link", { name: "Downloading · 64%" });
    expect([stage.textContent, stage.getAttribute("title"), stage.getAttribute("href")]).toEqual(["64%", "Downloading · 64%", "/activity?show=downloading"]);
    expect(within(status("Sundial")).queryByTestId("status-cell")).toBeNull();
    expect(status("Sundial").textContent).toBe("Downloading · 64%");
  });

  it("shows a download icon after the badge word for a stage without a percent", async () => {
    const movie = { id: 6, kind: "movie", name: "Kestrel Nine", year: 2022, monitored: true, availability: "partial",
      versions: [
        { id: 61, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: managed,
          acquisition: { stage: "queued", words: "Queued", download_id: 4, downloads: 1, updated_at: "2026-10-29T00:00:00Z" } },
        { id: 62, key: "movie-web", label: "WEB", monitored: true, has_file: true, quality: "WEBDL-2160p", state: "cutoff_met", control: managed },
      ] };
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: [movie], total: 1, offset: 0 } } : r)));
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Kestrel Nine");
    const cell = (screen.getByRole("link", { name: "Kestrel Nine" }).closest("tr") as HTMLElement).querySelector("td[data-column=status]") as HTMLElement;
    const stage = within(cell).getByRole("link", { name: "Queued" });
    expect([stage.textContent, stage.getAttribute("title"), !!stage.querySelector("svg")]).toEqual(["", "Queued", true]);
    expect(within(cell).getByTestId("status-cell").textContent).toBe("Partly available");
  });

  it("shows the stage in words after the badge word on a phone card", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: !q.includes("min-width"), media: q, addEventListener() {}, removeEventListener() {} }));
    const movie = { id: 6, kind: "movie", name: "Kestrel Nine", year: 2022, monitored: true, availability: "partial",
      versions: [
        { id: 61, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: managed,
          acquisition: { stage: "complete", words: "Download complete", download_id: 4, downloads: 1, updated_at: "2026-10-29T00:00:00Z" } },
        { id: 62, key: "movie-web", label: "WEB", monitored: true, has_file: true, quality: "WEBDL-2160p", state: "cutoff_met", control: managed },
      ] };
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: [movie], total: 1, offset: 0 } } : r)));
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Kestrel Nine");
    const card = screen.getByRole("link", { name: "Kestrel Nine" }).closest("li") as HTMLElement;
    expect(within(card).getByTestId("status-cell").textContent).toBe("Partly available");
    expect(within(card).getByRole("link", { name: "Download complete" }).textContent).toBe("Download complete");
  });

  it("uses the badge words for Status and a red pill for the missing library of a partly held movie", async () => {
    const movies = [
      { id: 6, kind: "movie", name: "Copper Sky", year: 2022, monitored: true, release_date: "2022-05-01", availability: "partial",
        versions: [
          { id: 61, key: "movie-4k", label: "4K", monitored: true, has_file: true, quality: "Remux-2160p", state: "filled", control: managed },
          { id: 62, key: "movie-web", label: "WEB", monitored: true, has_file: false, state: "wanted", control: managed },
        ] },
      { id: 7, kind: "movie", name: "Sundial", year: 2099, monitored: false, release_date: "2099-06-01", availability: "upcoming",
        versions: [{ id: 71, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: managed }] },
      // Held below its preferred quality (every profile's cutoff is out of reach): Available, the quality word under it.
      { id: 9, kind: "movie", name: "Low Tide", year: 2020, monitored: true, release_date: "2020-05-01", availability: "available", upgrade_state: "best_source_seen",
        versions: [{ id: 91, key: "movie-4k", label: "4K", monitored: true, has_file: true, quality: "WEBDL-1080p", state: "filled", control: managed }] },
      { id: 10, kind: "movie", name: "High Tide", year: 2020, monitored: true, release_date: "2020-05-01", availability: "available", upgrade_seen: true, upgrade_state: "upgrade_available",
        versions: [{ id: 101, key: "movie-4k", label: "4K", monitored: true, has_file: true, quality: "WEBDL-1080p", state: "filled", control: managed }] },
    ];
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: movies, total: 4, offset: 0 } } : r)));
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Copper Sky");
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Title", "Status", "Libraries", "Studio", "Size", "Added", "Actions"]);
    const row = screen.getByRole("link", { name: "Copper Sky" }).closest("tr") as HTMLElement;
    expect(within(row).getByTestId("status-cell").textContent).toBe("Partly available");
    expect(within(row).queryByText(/preferred quality not met/)).toBeNull();
    expect(within(row).getAllByTestId("library-pill").map((p) => [p.textContent, p.getAttribute("data-tone"), p.getAttribute("aria-label")])).toEqual([
      ["4K · Remux-2160p", "ok", "4K: has a file"],
      ["WEB · Missing", "bad", "WEB: missing"],
    ]);
    const coming = screen.getByRole("link", { name: "Sundial" }).closest("tr") as HTMLElement;
    expect(within(coming).getByTestId("status-cell").textContent).toBe("Upcoming · Jun 1, 2099");
    expect(within(coming).queryByTestId("status-quality")).toBeNull();
    const low = screen.getByRole("link", { name: "Low Tide" }).closest("tr") as HTMLElement;
    expect([within(low).getByTestId("status-cell").textContent, within(low).getByTestId("status-quality").textContent]).toEqual(["Available", "Best quality known"]);
    const high = screen.getByRole("link", { name: "High Tide" }).closest("tr") as HTMLElement;
    expect([within(high).getByTestId("status-cell").textContent, within(high).getByTestId("status-quality").textContent]).toEqual(["Available", "Upgrade found"]);
    expect(within(coming).getByTestId("library-pill").textContent).toBe("4K · Not released yet");
    expect(within(coming).getByText("Not monitored")).toBeTruthy();
  });

  it("says a library without a file that is not monitored has no file, so a Partly row explains itself", async () => {
    const movies = [
      { id: 6, kind: "movie", name: "Plumtree", year: 2022, monitored: true, release_date: "2022-05-01", availability: "partial", upgrade_state: "cutoff_met",
        versions: [
          { id: 61, key: "movie-4k", label: "4K", monitored: true, has_file: true, quality: "Remux-2160p", state: "cutoff_met", control: managed },
          { id: 62, key: "movie-web", label: "WEB", monitored: false, has_file: false, state: "wanted", control: managed },
        ] },
    ];
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: movies, total: 1, offset: 0 } } : r)));
    renderPage(<Catalog id="movies" />, "/movies?availability=partial");
    await screen.findAllByText("Plumtree");
    const web = screen.getAllByTestId("library-pill")[1];
    expect(web.textContent).toBe("WEB · No file · Not monitored");
    expect(web.getAttribute("aria-label")).toBe("WEB: no file, not monitored");
    expect(screen.getByRole("button", { name: "Plumtree, WEB: no file, not monitored" })).toBeTruthy();
  });

  it("opens the Libraries dialog from a pill, and its switch monitors through the bulk route, never a search", async () => {
    const fetch = mockFetch(routes([
      { method: "POST", path: "/titles/monitor", reply: { dry_run: false, items: [], changed: 1, unchanged: 0, missing: 0, stale: 0, message: "Unmonitored 1 library" } },
    ]));
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await user.click(await screen.findByRole("button", { name: "The Pellbrook Inquiries, 1080p WEB: 17 of 17 episodes linked" }));
    const dialog = await screen.findByRole("dialog", { name: "The Pellbrook Inquiries libraries" });
    await user.click(within(dialog).getByRole("switch", { name: "Monitor The Pellbrook Inquiries 1080p WEB" }));
    await waitFor(() => expect(fetch.calls("POST", "/titles/monitor")).toHaveLength(1));
    expect(fetch.calls("POST", "/titles/monitor")[0].body).toEqual({ title_ids: [1], versions: ["tv-web"], monitored: false });
    expect(fetch.requests.filter((r) => r.method !== "GET").map((r) => r.path)).toEqual(["/titles/monitor"]);
  });

  it("sorts Added by the day the title was added and Latest file by the newest file", async () => {
    const fetch = mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    await user.click(screen.getByRole("button", { name: "Columns" }));
    const dialog = await screen.findByRole("dialog", { name: "Columns" });
    await user.click(within(dialog).getByRole("checkbox", { name: /^Added/ }));
    await user.click(within(dialog).getByRole("checkbox", { name: /^Latest file/ }));
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    const last = () => fetch.requests.filter((r) => r.path === "/titles").at(-1)!.search;
    await user.click(screen.getByRole("button", { name: "Sort by added" }));
    await waitFor(() => expect(last()).toContain("sort=created"));
    expect(screen.getByRole("columnheader", { name: /^Added/ }).getAttribute("aria-sort")).toBe("descending");
    await user.click(screen.getByRole("button", { name: "Sort by latest file" }));
    await waitFor(() => expect(last()).toContain("sort=added"));
    expect(last()).not.toContain("sort=created");
    expect(screen.getByRole("columnheader", { name: /^Latest file/ }).getAttribute("aria-sort")).toBe("descending");
    // The same header again flips the order.
    await user.click(screen.getByRole("button", { name: "Sort by latest file" }));
    await waitFor(() => expect(last()).toContain("order=asc"));
    // Added shows the day the title was added, Latest file how long ago the newest file came.
    const row = screen.getByRole("link", { name: "The Pellbrook Inquiries" }).closest("tr") as HTMLElement;
    expect(row.querySelector('[data-column="added"]')?.textContent).toBe("Oct 1, 2025");
    expect(row.querySelector('[data-column="latest_file"]')?.textContent).toMatch(/ago$/);
  });

  it("keeps a sort picked from the menu, dropping the copy and the order with it", async () => {
    const fetch = mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series?sort=completion&sort_version=tv-2160p&order=asc");
    await screen.findAllByText("The Pellbrook Inquiries");
    const pick = screen.getByRole("combobox", { name: "Sort by" }) as HTMLSelectElement;
    await user.selectOptions(pick, "year");
    await waitFor(() => expect(fetch.requests.some((r) => r.path === "/titles" && r.search.includes("sort=year"))).toBe(true));
    expect(pick.value).toBe("year");
    const last = fetch.requests.filter((r) => r.path === "/titles").at(-1)!;
    expect(last.search).not.toContain("sort_version");
    expect(last.search).not.toContain("order=");
    // The order then flips from the sort's own default and both stick.
    await user.click(screen.getByRole("button", { name: "Descending" }));
    await waitFor(() => expect(fetch.requests.some((r) => r.path === "/titles" && r.search.includes("sort=year") && r.search.includes("order=asc"))).toBe(true));
    expect(pick.value).toBe("year");
    await user.selectOptions(pick, "network");
    await waitFor(() => expect(pick.value).toBe("network"));
    // Network starts A to Z, as the server does.
    expect(screen.getByRole("button", { name: "Ascending" })).toBeTruthy();
  });

  it("keeps columns per list: a choice on Movies does not change Series", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    const { unmount } = renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("The Pellbrook Inquiries");
    await user.click(screen.getByRole("button", { name: "Columns" }));
    const dialog = await screen.findByRole("dialog", { name: "Columns" });
    expect(within(dialog).queryByRole("checkbox", { name: /^Seasons/ })).toBeNull();
    await user.click(within(dialog).getByRole("checkbox", { name: /^Runtime/ }));
    await user.click(within(dialog).getByRole("button", { name: "Move Runtime up" }));
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.getByRole("columnheader", { name: /Runtime/ })).toBeTruthy();
    const kept = JSON.parse(window.localStorage.getItem("theatarr.library.columns.movies.7") ?? "{}");
    expect(kept.shown).toContain("runtime");
    expect(kept.order.indexOf("runtime")).toBeLessThan(kept.order.indexOf("certification"));
    unmount();
    mockFetch(routes());
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.queryByRole("columnheader", { name: /Runtime/ })).toBeNull();
    expect(screen.getByRole("columnheader", { name: /Seasons/ })).toBeTruthy();
    expect(window.localStorage.getItem("theatarr.library.columns.series.7")).toBeNull();
  });

  it("keeps the view per list, starting from the old shared one", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    const { unmount } = renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.getByRole("radio", { name: "Table" }).getAttribute("aria-checked")).toBe("true");
    await user.click(screen.getByRole("radio", { name: "Posters" }));
    expect(screen.getByTestId("poster-grid")).toBeTruthy();
    unmount();
    mockFetch(routes());
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.getByRole("table")).toBeTruthy();
  });

  it("selects titles, previews with unchanged copies apart, and applies with expect", async () => {
    const preview = {
      dry_run: true, changed: 1, unchanged: 1, missing: 0, stale: 0, message: "Would monitor 1 library · 1 already monitored",
      items: [
        { title_id: 1, title: "The Pellbrook Inquiries", version: "tv-2160p", label: "4K", outcome: "unchanged", from: true, to: true },
        { title_id: 2, title: "Ferrous Bay", version: "tv-2160p", label: "4K", outcome: "changed", from: false, to: true },
      ],
    };
    const fetch = mockFetch(routes([
      { method: "POST", path: "/titles/monitor", reply: (r: { body?: { dry_run?: boolean } }) => (r.body?.dry_run ? preview : { ...preview, dry_run: false, message: "Monitored 1 library · 1 already monitored" }) },
    ]));
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("checkbox", { name: "Select all 2 shown" }));
    const bar = screen.getByRole("region", { name: "Bulk edit" });
    expect(within(bar).getByText("2 titles selected")).toBeTruthy();
    // A manager (here also admin) gets Search now.
    expect(within(bar).getByRole("button", { name: "Search now" })).toBeTruthy();
    await user.selectOptions(within(bar).getByRole("combobox", { name: "Target libraries" }), "tv-2160p");
    expect(within(bar).getByTestId("bulk-local").textContent).toContain("2 editable libraries");
    expectNamedControls();
    await user.click(within(bar).getByRole("button", { name: "Review changes" }));
    const dialog = await screen.findByRole("dialog", { name: "Review bulk edit" });
    await within(dialog).findByText("Would monitor 1 library · 1 already monitored");
    expect(within(within(dialog).getByTestId("bulk-same")).getByText("The Pellbrook Inquiries")).toBeTruthy();
    expect(fetch.calls("POST", "/titles/monitor")[0].body).toEqual({ title_ids: [1, 2], versions: ["tv-2160p"], monitored: true, dry_run: true });
    await user.click(within(dialog).getByRole("button", { name: "Apply to 1 library" }));
    await within(dialog).findByText("Monitored 1 library · 1 already monitored");
    const calls = fetch.calls("POST", "/titles/monitor");
    expect(calls[calls.length - 1].body).toEqual({ title_ids: [1, 2], versions: ["tv-2160p"], monitored: true, expect: [{ title_id: 2, version: "tv-2160p", monitored: false }] });
    expect(fetch.requests.filter((r) => r.method !== "GET").every((r) => r.path === "/titles/monitor")).toBe(true);
  });

  it("opens a title's copies in a dialog with each copy's file, stage and monitoring", async () => {
    const movie = {
      id: 9, kind: "movie", name: "Saltglass Meridian", year: 2018, monitored: true,
      versions: [
        { id: 91, key: "movie-remux", label: "4K Remux", monitored: true, has_file: true, quality: "Remux-2160p", state: "filled", size_bytes: 30e9, control: shadow,
          acquisition: { stage: "downloading", words: "Downloading (37%)", progress: 37, download_id: 3, downloads: 1, upgrade: true, updated_at: "2026-10-29T00:00:00Z" } },
        { id: 92, key: "movie-web", label: "4K WEB", monitored: true, has_file: true, quality: "WEBDL-2160p", state: "cutoff_met", control: managed },
      ],
    };
    mockFetch([
      { method: "GET", path: "/auth/me", reply: me },
      { method: "GET", path: "/titles", reply: { items: [movie], total: 1, offset: 0 } },
      { method: "GET", path: "/titles/facets", reply: {} },
    ]);
    const user = userEvent.setup();
    renderPage(<Catalog id="movies" />, "/movies");
    await user.click(await screen.findByRole("button", { name: /^Saltglass Meridian, 4K Remux: / }));
    const dialog = await screen.findByRole("dialog", { name: "Saltglass Meridian libraries" });
    const remux = within(dialog).getByText("4K Remux").closest("[data-copy]") as HTMLElement;
    expect(within(remux).getByText("Remux-2160p")).toBeTruthy();
    expect(within(remux).getByRole("link", { name: "Upgrade downloading · 37%" })).toBeTruthy();
    expect(within(remux).getByText("The current file stays available until the new one is linked.")).toBeTruthy();
    expect(within(remux).getByRole("switch", { name: "Monitor Saltglass Meridian 4K Remux" }).getAttribute("aria-disabled")).toBeNull();
    expect(within(dialog).getByRole("link", { name: "Open title page" }).getAttribute("href")).toBe("/title/9");
  });

  it("stacks rows on a phone with no table: title and Status, the pills, then each column as a line", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: !q.includes("min-width"), media: q, addEventListener() {}, removeEventListener() {} }));
    mockFetch(routes());
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.queryByRole("table")).toBeNull();
    const card = screen.getByRole("link", { name: "The Pellbrook Inquiries" }).closest("li") as HTMLElement;
    expect(within(card).getByRole("link", { name: "Downloading · 37%" })).toBeTruthy();
    expect(within(card).getAllByTestId("library-pill")).toHaveLength(2);
    expect(within(card).getByText("Network")).toBeTruthy();
    expect(within(card).queryByText("Status")).toBeNull();
  });
});

describe("Toolbar and filters", () => {
  it("has no filter panel in the page: the Filter button opens the sheet, without the title box", async () => {
    mockFetch(routes().map((r) => (r.path === "/titles/facets" ? { ...r, reply: { total: 2, providers: [{ value: "rd", count: 2 }] } } : r)));
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.queryByTestId("filters-panel")).toBeNull();
    expect(screen.getAllByRole("textbox", { name: "Filter by title" })).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Filter" }));
    const sheet = await screen.findByTestId("filters-panel");
    expect(within(sheet).getByText("Counts are for the whole list, before other filters.")).toBeTruthy();
    expect(within(sheet).queryByRole("textbox", { name: "Filter by title" })).toBeNull();
    // Providers are cache codes, labelled as the pills are.
    expect(within(sheet).getByRole("option", { name: "RD (2)" })).toBeTruthy();
    await user.click(within(sheet).getByRole("button", { name: "Close filters" }));
    await waitFor(() => expect(screen.queryByTestId("filters-panel")).toBeNull());
  });

  it("shows each active filter as a chip; removing one changes the address", async () => {
    const fetch = mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series?availability=missing&genre=Drama,Crime&provider=rd&year_from=2019&year_to=2024&q=dune");
    await screen.findAllByText("The Pellbrook Inquiries");
    const line = screen.getByTestId("active-filters");
    expect(within(line).getAllByRole("button", { name: /^Remove filter: / }).map((b) => b.textContent)).toEqual([
      "Title: “dune”", "Missing", "Provider: RD", "2019–2024", "Drama", "Crime",
    ]);
    expect(screen.getByRole("button", { name: "Filter, 6 active" })).toBeTruthy();
    expect(screen.getByText(/match the filters/)).toBeTruthy();
    await user.click(within(line).getByRole("button", { name: "Remove filter: Missing" }));
    await waitFor(() => expect(fetch.requests.filter((r) => r.path === "/titles").at(-1)!.search).not.toContain("availability"));
    await user.click(within(line).getByRole("button", { name: "Remove filter: Drama" }));
    await waitFor(() => expect(fetch.requests.filter((r) => r.path === "/titles").at(-1)!.search).toContain("genre=Crime"));
    expect(fetch.requests.filter((r) => r.path === "/titles").at(-1)!.search).not.toContain("Drama");
    await user.click(within(line).getByRole("button", { name: "Clear all" }));
    await waitFor(() => expect(screen.queryByTestId("active-filters")).toBeNull());
  });

  it("offers Clear filters when nothing matches", async () => {
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: [], total: 0, offset: 0 } } : r)));
    const user = userEvent.setup();
    renderPage(<Catalog id="movies" />, "/movies?monitored=false");
    expect(await screen.findByText("No titles match these filters.")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(await screen.findByText("No movies yet.")).toBeTruthy();
  });
});

describe("Library posters", () => {
  it("draws a series' libraries as lines with counts, the in-flight chip and only the year until more details are chosen", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.getByRole("radio", { name: "Posters" }).getAttribute("aria-checked")).toBe("true");
    const chip = screen.getByTestId("flight-chip");
    expect(chip.textContent).toBe("Downloading 37%");
    const lines = screen.getAllByTestId("library-line");
    expect(lines.map((l) => l.textContent)).toEqual(["4K13/17", "1080p WEB17/17", "8/18"]);
    expect(within(lines[0]).getByRole("img", { name: "4K: 13 of 17 episodes linked, 2 in progress" })).toBeTruthy();
    expect(within(screen.getByTestId("poster-grid")).queryByText("Series")).toBeNull();
    expect(screen.getByText("2017")).toBeTruthy();
    expect(screen.queryByText("S03E06 · Nov 4, 2099")).toBeNull();
    expect(screen.queryByText("Northstar Network")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Poster options" }));
    const dialog = await screen.findByRole("dialog", { name: "Poster options" });
    await user.click(within(dialog).getByRole("button", { name: "Small" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "Network" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "Next airing" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "Quality" }));
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.getByText("Northstar Network")).toBeTruthy();
    expect(screen.getByText("S03E06 · Nov 4, 2099")).toBeTruthy();
    expect(screen.getByText("4K: WEBDL-2160p · 1080p WEB: WEBDL-1080p")).toBeTruthy();
    expect(screen.getByTestId("poster-grid").className).toContain("poster-size-small");
    expect(JSON.parse(window.localStorage.getItem("theatarr.library.posters.series.7") ?? "{}").size).toBe("small");
  });

  it("draws one bar for a movie, a pill per library only when it is in several, and never Missing under Upcoming", async () => {
    const movies = [
      { id: 5, kind: "movie", name: "Harrow Lane", year: 2021, monitored: true, release_date: "2021-05-01", availability: "available", upgrade_state: "cutoff_met",
        versions: [{ id: 51, key: "movie-4k", label: "4K", monitored: true, has_file: true, quality: "Remux-2160p", state: "cutoff_met", control: managed }] },
      { id: 6, kind: "movie", name: "Copper Sky", year: 2022, monitored: true, release_date: "2022-05-01", availability: "partial",
        versions: [
          { id: 61, key: "movie-4k", label: "4K", monitored: true, has_file: true, quality: "Remux-2160p", state: "cutoff_met", control: managed },
          { id: 62, key: "movie-web", label: "WEB", monitored: true, has_file: false, state: "wanted", control: managed },
        ] },
      { id: 7, kind: "movie", name: "Sundial", year: 2099, monitored: true, release_date: "2099-06-01", availability: "upcoming",
        versions: [{ id: 71, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: managed }] },
      { id: 8, kind: "movie", name: "Grey Orchard", year: 2020, monitored: false, release_date: "2020-05-01", availability: "unmonitored",
        versions: [{ id: 81, key: "movie-4k", label: "4K", monitored: false, has_file: false, state: "wanted", control: managed }] },
      { id: 10, kind: "movie", name: "High Tide", year: 2020, monitored: true, release_date: "2020-05-01", availability: "available", upgrade_seen: true, upgrade_state: "upgrade_available",
        versions: [{ id: 101, key: "movie-4k", label: "4K", monitored: true, has_file: true, quality: "WEBDL-1080p", state: "filled", control: managed }] },
    ];
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: movies, total: 5, offset: 0 } } : r)));
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Harrow Lane");
    const card = (name: string) => screen.getByRole("link", { name: new RegExp(`^${name} \\(`) }).parentElement as HTMLElement;

    const one = card("Harrow Lane");
    expect(within(one).getAllByTestId("status-bar")).toHaveLength(1);
    expect(within(one).getByTestId("status-bar").getAttribute("data-tone")).toBe("ok");
    expect(within(one).queryByTestId("library-pills")).toBeNull();
    expect(one.textContent).not.toContain("1/1");
    expect(within(one).queryByText("Movie")).toBeNull();

    // Held in one library and missing in the other: the whole bar takes the badge's tone; the pills say which.
    const two = card("Copper Sky");
    expect(within(two).getByRole("link", { name: /^Copper Sky \(2022\), Partly available,/ })).toBeTruthy();
    expect(within(two).getAllByTestId("status-bar")).toHaveLength(1);
    expect(within(two).getByTestId("status-bar").getAttribute("data-tone")).toBe("warn");
    expect(within(two).getAllByTestId("library-pill").map((p) => [p.getAttribute("aria-label"), p.getAttribute("data-tone")])).toEqual([
      ["4K: has a file", "ok"],
      ["WEB: missing", "bad"],
    ]);

    const coming = card("Sundial");
    expect(within(coming).getByRole("link", { name: /Upcoming/ })).toBeTruthy();
    expect(within(coming).getByTestId("status-bar").getAttribute("data-tone")).toBe("idle");
    expect(within(coming).getByRole("img", { name: "4K: not released yet" })).toBeTruthy();

    const off = card("Grey Orchard");
    expect(within(off).getByTestId("unmonitored-chip").textContent).toBe("Not monitored");
    // The status word itself says Not monitored (neutral), never a red Missing.
    expect(within(off).getByRole("link", { name: /^Grey Orchard \(2020\), Not monitored,/ })).toBeTruthy();
    expect(within(one).queryByTestId("unmonitored-chip")).toBeNull();

    // A better release found: the badge keeps the status word; Upgrade found is on the year line.
    const high = card("High Tide");
    expect(high.querySelector("[data-status]")?.getAttribute("data-status")).toBe("available");
    expect(within(high).getByTestId("poster-quality").textContent).toBe(" · Upgrade found");
    expect(within(high).getByRole("link", { name: /^High Tide \(2020\), Available, Upgrade found,/ })).toBeTruthy();
  });

  it("draws no in-progress stripe for a series library whose download is blocked", async () => {
    const rows = [
      { id: 9, kind: "series", name: "Saltmarsh", year: 2021, monitored: true, availability: "partial",
        versions: [{ id: 91, key: "tv-web", label: "1080p WEB", monitored: true, has_file: true, state: "wanted", control: managed,
          coverage: { have: 2, total: 3, aired: 3, upcoming: 0, in_flight: 1 },
          acquisition: { stage: "blocked", words: "Blocked", download_id: 9, downloads: 1, episodes: 1, upgrade: true, updated_at: "2026-10-29T00:00:00Z" } }] },
    ];
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: rows, total: 1, offset: 0 } } : r)));
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("Saltmarsh");
    expect(screen.getByTestId("flight-chip").textContent).toBe("Blocked");
    const bar = screen.getByRole("img", { name: "1080p WEB: 2 of 3 episodes linked, a download is blocked" });
    expect(bar.querySelector(".status-flight")).toBeNull();
    expect(bar.getAttribute("data-tone")).toBe("ok");
  });

  it("selects on posters into the same bulk bar", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Ferrous Bay" }));
    expect(within(screen.getByRole("region", { name: "Bulk edit" })).getByText("1 title selected")).toBeTruthy();
    expectNamedControls();
  });
});

describe("Household view (no See files)", () => {
  // The server redacts the rows: no control or acquisition, coverage only, availability as watchability.
  const plain = [
    { id: 1, kind: "series", name: "The Pellbrook Inquiries", year: 2017, availability: "partial",
      versions: [{ key: "tv-2160p", label: "4K", quality: "2160p", coverage: { have: 13, total: 17, aired: 17, upcoming: 1 } },
        { key: "tv-2160p-theatarr", label: "4K (Theatarr)", coverage: { have: 0, total: 8, aired: 8, upcoming: 0 } }] },
    { id: 2, kind: "series", name: "Ferrous Bay", year: 2019, availability: "available",
      versions: [{ key: "tv-web", label: "1080p WEB", quality: "1080p", coverage: { have: 18, total: 18, aired: 18, upcoming: 0 } }] },
  ];
  const household = [
    { method: "GET", path: "/auth/me", reply: { id: 9, username: "family", permissions: [], csrf_token: "t" } },
    { method: "GET", path: "/titles", reply: { items: plain, total: 2, offset: 0 } },
    { method: "GET", path: "/titles/facets", reply: {} },
  ];

  it("shows one watchability column in the table instead of a column per library", async () => {
    window.localStorage.setItem("theatarr.catalog.view", "table");
    mockFetch(household);
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.getByRole("columnheader", { name: "Availability" })).toBeTruthy();
    expect(screen.getAllByTestId("watch-cell").map((e) => e.textContent)).toEqual(["Some episodes available", "Available in 1080p"]);
    expect(screen.queryByText("0 / 8 available")).toBeNull();
    expect(document.querySelector("[data-copy-col]")).toBeNull();
    expect(screen.queryAllByTestId("library-pill")).toHaveLength(0);
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Title", "Availability", "Network", "Next airing", "Seasons"]);
  });

  it("does not offer the file columns", async () => {
    window.localStorage.setItem("theatarr.catalog.view", "table");
    mockFetch(household);
    const user = userEvent.setup();
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("The Pellbrook Inquiries");
    await user.click(screen.getByRole("button", { name: "Columns" }));
    const dialog = await screen.findByRole("dialog", { name: "Columns" });
    const offered = within(dialog).getAllByRole("button", { name: / up$/ }).map((b) => b.getAttribute("aria-label")!.slice(5, -3));
    expect(offered).toContain("Availability");
    for (const file of ["Libraries", "Quality", "Quality profile", "Size", "Latest file", "Minimum availability", "Provider", "Folder"]) expect(offered).not.toContain(file);
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    const sorts = [...(screen.getByRole("combobox", { name: "Sort by" }) as HTMLSelectElement).options].map((o) => o.textContent);
    expect(sorts).toContain("Added");
    for (const file of ["Latest file", "Size", "Quality", "Latest activity"]) expect(sorts).not.toContain(file);
  });

  it("opens an old address with a sort it is not offered on Title, in the request and the menu", async () => {
    window.localStorage.setItem("theatarr.catalog.view", "table");
    const fetch = mockFetch(household);
    renderPage(<Catalog id="movies" />, "/movies?sort=added&order=asc");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect((screen.getByRole("combobox", { name: "Sort by" }) as HTMLSelectElement).value).toBe("name");
    const sent = fetch.requests.filter((r) => r.path === "/titles");
    expect(sent.at(-1)!.search).toContain("sort=name");
    expect(sent.some((r) => r.search.includes("sort=added"))).toBe(false);
  });

  it("offers no per-library has/lacks filter, which those who see files keep", async () => {
    const versions = [{ key: "movie-remux", label: "4K Remux", titles: 2, with_file: 1, wanted: 1, filled: 1, cutoff_met: 0 }];
    for (const [perms, shown] of [[[], false], [["see_files"], true]] as const) {
      const fetch = mockFetch(household.map((r) => (r.path === "/titles/facets" ? { ...r, reply: { total: 2, versions } } : r.path === "/auth/me" ? { ...r, reply: { ...r.reply, permissions: perms } } : r)));
      const { unmount } = renderPage(<Catalog id="movies" />, "/movies");
      await userEvent.setup().click(await screen.findByTestId("filters-button"));
      const panel = await screen.findByTestId("filters-panel");
      await waitFor(() => expect(fetch.calls("GET", "/titles/facets").length).toBeGreaterThan(0));
      await new Promise((r) => setTimeout(r, 50));
      await waitFor(() => expect(within(panel).queryAllByRole("button", { name: /^Has / }).length > 0).toBe(shown));
      expect(within(panel).queryByText("4K Remux") !== null).toBe(shown);
      expect(within(panel).queryAllByRole("button", { name: /^Lacks / }).length > 0).toBe(shown);
      unmount();
    }
  });

  it("reads Getting ready for a missing title with a download in hand, in the table and on posters", async () => {
    const rows = [
      { id: 3, kind: "series", name: "Lantern Row", year: 2026, availability: "missing", acquiring: true,
        versions: [{ key: "tv-web", label: "1080p WEB", coverage: { have: 0, total: 6, aired: 6, upcoming: 0 } }] },
      { id: 4, kind: "series", name: "Quiet Harbour", year: 2026, availability: "missing",
        versions: [{ key: "tv-web", label: "1080p WEB", coverage: { have: 0, total: 6, aired: 6, upcoming: 0 } }] },
    ];
    const routes = household.map((r) => (r.path === "/titles" ? { ...r, reply: { items: rows, total: 2, offset: 0 } } : r));
    window.localStorage.setItem("theatarr.catalog.view", "table");
    mockFetch(routes);
    const { unmount } = renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("Lantern Row");
    expect(screen.getAllByTestId("watch-cell").map((e) => e.textContent)).toEqual(["Getting ready", "Not available"]);
    unmount();
    window.localStorage.setItem("theatarr.catalog.view", "grid");
    mockFetch(routes);
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("Lantern Row");
    expect(screen.getByRole("link", { name: /^Lantern Row \(2026\), Getting ready$/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /^Quiet Harbour \(2026\), Not available$/ })).toBeTruthy();
  });

  it("draws no per-library bars on posters, only the badge", async () => {
    mockFetch(household);
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.queryAllByTestId("status-bar")).toHaveLength(0);
    expect(screen.queryAllByTestId("library-pill")).toHaveLength(0);
    expect(screen.queryAllByTestId("library-line")).toHaveLength(0);
    expect(screen.queryByTestId("unmonitored-chip")).toBeNull();
    expect(screen.getByRole("link", { name: /^The Pellbrook Inquiries \(2017\), Some episodes$/ })).toBeTruthy();
  });
});

describe("Same-named copies", () => {
  const twin = [
    {
      id: 3, kind: "series", name: "Brindlewood Bakers", year: 2023, monitored: true,
      versions: [
        { id: 31, key: "tv-2160p", label: "4K", monitored: true, has_file: true, quality: "WEBDL-2160p", state: "filled", control: shadow,
          coverage: { have: 24, total: 24, aired: 24, upcoming: 0 } },
        { id: 32, key: "tv-2160p-theatarr", label: "4K", monitored: true, has_file: true, quality: "WEBDL-2160p", state: "wanted", control: managed,
          coverage: { have: 12, total: 24, aired: 24, upcoming: 0 } },
      ],
    },
  ];
  const facets = {
    total: 1, monitored: 1, unmonitored: 0, availability: {},
    versions: [
      { key: "tv-2160p", label: "4K", titles: 1, with_file: 1, wanted: 0, filled: 1, cutoff_met: 0 },
      { key: "tv-2160p-theatarr", label: "4K", titles: 1, with_file: 1, wanted: 1, filled: 0, cutoff_met: 0 },
    ],
  };
  const twinRoutes = [
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles", reply: { items: twin, total: 1, offset: 0 } },
    { method: "GET", path: "/titles/facets", reply: facets },
  ];

  it("names them apart in the table's pills and the filter sheet", async () => {
    window.localStorage.setItem("theatarr.catalog.view", "table");
    mockFetch(twinRoutes);
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("Brindlewood Bakers");
    expect(screen.getAllByTestId("library-pill").map((p) => p.textContent)).toEqual(["4K · 24/24", "4K (2) · 12/24"]);
    await userEvent.setup().click(screen.getByTestId("filters-button"));
    const panel = await screen.findByTestId("filters-panel");
    expect(await within(panel).findByText("4K")).toBeTruthy();
    expect(within(panel).getByText("4K (2)")).toBeTruthy();
    expect(within(panel).queryByText(/tv-2160p/)).toBeNull();
  });

  it("names them apart on posters", async () => {
    mockFetch(twinRoutes);
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("Brindlewood Bakers");
    const lines = screen.getAllByTestId("library-line");
    expect(within(lines[0]).getByText("4K")).toBeTruthy();
    expect(within(lines[1]).getByText("4K (2)")).toBeTruthy();
    expect(within(screen.getByTestId("poster-grid")).getAllByTestId("status-bar").map((e) => e.getAttribute("aria-label"))).toEqual([
      "4K: 24 of 24 episodes linked",
      "4K (2): 12 of 24 episodes linked",
    ]);
  });
});

describe("Row actions", () => {
  beforeEach(() => window.localStorage.setItem("theatarr.catalog.view", "table"));
  afterEach(() => vi.unstubAllGlobals());

  const on = { mode: "active", paused: false, pause_forced: false, will_grab: true };
  const live = { ...managed, grab_state: on };
  const movies = [
    { id: 6, kind: "movie", name: "Copper Sky", year: 2022, monitored: true, availability: "missing",
      versions: [
        { id: 61, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: { ...shadow, grab_state: { ...on, mode: "shadow", will_grab: false } } },
        { id: 62, key: "movie-web", label: "WEB", monitored: true, has_file: false, state: "wanted", control: live },
      ] },
    { id: 7, kind: "movie", name: "Sundial", year: 2021, monitored: true, availability: "missing",
      versions: [{ id: 71, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: { ...shadow, grab_state: { ...on, mode: "shadow", will_grab: false } } }] },
  ];
  const movieRoutes = (perms = me.permissions) => [
    { method: "GET", path: "/auth/me", reply: { ...me, permissions: perms } },
    { method: "GET", path: "/titles", reply: { items: movies, total: 2, offset: 0 } },
    { method: "GET", path: "/titles/facets", reply: {} },
  ];
  const rowOf = (name: string) => screen.getByRole("link", { name }).closest("tr") as HTMLElement;

  it("shows the buttons only to managers", async () => {
    mockFetch(movieRoutes(["see_files"]));
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Copper Sky");
    expect(screen.queryByTestId("row-actions")).toBeNull();
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).not.toContain("Actions");
  });

  it("searches that row's title only and shows the answer as a toast", async () => {
    const fetch = mockFetch([...movieRoutes(), { method: "POST", path: "/titles/6/search", reply: { started: true, message: "Searching 1 item(s) for movie-web", versions: ["movie-web"], skipped: [] } }]);
    const user = userEvent.setup();
    renderPage(<><Catalog id="movies" /><Toaster /></>, "/movies");
    await screen.findAllByText("Copper Sky");
    const search = within(rowOf("Copper Sky")).getByRole("button", { name: "Search now, Copper Sky" });
    expect(search.getAttribute("title")).toBe("Search every monitored Live library now");
    await user.click(search);
    expect(await screen.findByText("Search started")).toBeTruthy();
    expect(screen.getByText("Searching 1 item(s) for movie-web")).toBeTruthy();
    expect(fetch.requests.filter((r) => r.method !== "GET").map((r) => [r.path, r.body])).toEqual([["/titles/6/search", {}]]);
  });

  it("disables Search now with the reason when nothing of the title would search", async () => {
    mockFetch(movieRoutes());
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Sundial");
    const row = rowOf("Sundial");
    const search = within(row).getByRole("button", { name: "Search now, Sundial" }) as HTMLButtonElement;
    expect(search.disabled).toBe(true);
    expect(search.getAttribute("title")).toBe("No library of this title is Live.");
    const interactive = within(row).getByRole("button", { name: "Interactive search, Sundial" }) as HTMLButtonElement;
    expect(interactive.disabled).toBe(true);
    expect(interactive.getAttribute("title")).toBe("No library of this title is Live.");
    // Each reason, from the row's own libraries.
    const v = (control: object, monitored = true) => ({ key: "movie-web", label: "WEB", monitored, control }) as never;
    expect(searchOff([v({ ...live, can_search: false, grab_state: { ...on, paused: true, will_grab: false } })])).toBe("Paused: nothing is searched, downloaded or linked for any library.");
    expect(searchOff([v({ ...live, can_search: false, grab_state: { ...on, pause_forced: true, will_grab: false } })])).toBe("Paused by the environment (THEATARR_FORCE_OFF): nothing is searched, downloaded or linked for any library.");
    expect(searchOff([v({ ...shadow, grab_state: { ...on, mode: "shadow", will_grab: false } })])).toBe("No library of this title is Live.");
    expect(searchOff([v(live, false)])).toBe("No library is monitored.");
    expect(searchOff([v(live)])).toBe("");
  });

  it("opens Interactive search on a movie's first Live library", async () => {
    const title = { id: 6, kind: "movie", name: "Copper Sky", year: 2022, versions: movies[0].versions };
    const fetch = mockFetch([
      ...movieRoutes(),
      { method: "GET", path: "/titles/6", reply: title },
      { method: "GET", path: "/titles/6/removal", reply: { copies: [{ key: "movie-4k" }, { key: "movie-web" }] } },
      { method: "GET", path: "/titles/6/releases", reply: { version: "movie-web", source: "live", candidates: [] } },
    ]);
    const user = userEvent.setup();
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Copper Sky");
    await user.click(within(rowOf("Copper Sky")).getByRole("button", { name: "Interactive search, Copper Sky" }));
    expect(await screen.findByRole("dialog", { name: "Interactive search" })).toBeTruthy();
    await waitFor(() => expect(fetch.calls("GET", "/titles/6/releases").map((r) => new URLSearchParams(r.search).get("version"))).toEqual(["movie-web"]));
    expect(fetch.requests.some((r) => r.method !== "GET")).toBe(false);
  });

  it("links a series row to its Episodes tab instead of searching interactively", async () => {
    mockFetch(routes());
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    const row = rowOf("The Pellbrook Inquiries");
    expect(within(row).queryByRole("button", { name: /^Interactive search/ })).toBeNull();
    const link = within(row).getByRole("link", { name: "Choose season or episode, The Pellbrook Inquiries" });
    expect(link.getAttribute("href")).toBe("/title/1?tab=episodes");
    expect(link.getAttribute("title")).toBe("Choose season or episode");
    // The 1080p WEB library is monitored and Live: Search now is on, with the series words.
    expect(within(row).getByRole("button", { name: "Search now, The Pellbrook Inquiries" }).getAttribute("title")).toBe("Search every monitored episode now, in every Live library (missing episodes and upgrades)");
  });

  it("says on a phone card why Search now is disabled, and not in the desktop row", async () => {
    mockFetch(movieRoutes());
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Sundial");
    expect(screen.queryByTestId("row-search-off")).toBeNull();
  });

  it("says on a phone card, above the buttons, why Search now is disabled", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: !q.includes("min-width"), media: q, addEventListener() {}, removeEventListener() {} }));
    mockFetch(movieRoutes());
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Sundial");
    const card = (name: string) => screen.getByRole("link", { name }).closest("li") as HTMLElement;
    expect(within(card("Sundial")).getByTestId("row-search-off").textContent).toBe("No library of this title is Live.");
    expect(within(card("Copper Sky")).queryByTestId("row-search-off")).toBeNull();
  });

  it("puts the same buttons, with their words, at the end of a phone card", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: !q.includes("min-width"), media: q, addEventListener() {}, removeEventListener() {} }));
    mockFetch(movieRoutes());
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("Copper Sky");
    const card = screen.getByRole("link", { name: "Copper Sky" }).closest("li") as HTMLElement;
    expect(within(card).getByRole("button", { name: "Search now, Copper Sky" }).textContent).toBe("Search now");
    expect(within(card).getByRole("button", { name: "Interactive search, Copper Sky" }).textContent).toBe("Interactive search");
  });
});

describe("Add button", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    search.openOverlay.mockClear();
  });
  const adder = (titlesReply?: unknown) =>
    routes().map((r) => (r.path === "/auth/me" ? { ...r, reply: { ...me, permissions: [...me.permissions, "add"] } } : r.path === "/titles" && titlesReply ? { ...r, reply: titlesReply } : r));

  it("shows Add movie, Add series or Add anime in the header only to accounts that may add, and opens the search unfiltered", async () => {
    const user = userEvent.setup();
    for (const [id, label] of [["movies", "Add movie"], ["series", "Add series"], ["anime", "Add anime"]] as const) {
      mockFetch(adder());
      const page = renderPage(<Catalog id={id} />, `/${id}`);
      await screen.findAllByText("The Pellbrook Inquiries");
      await user.click(screen.getByRole("button", { name: label }));
      expect(search.openOverlay).toHaveBeenLastCalledWith();
      page.unmount();
    }
    // The owner here manages but has no add permission.
    mockFetch(routes());
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.queryByTestId("catalog-add")).toBeNull();
  });

  it("offers the same button under the empty list's text", async () => {
    mockFetch(adder({ items: [], total: 0, offset: 0 }));
    const user = userEvent.setup();
    renderPage(<Catalog id="movies" />, "/movies");
    await screen.findByText("No movies yet.");
    const buttons = screen.getAllByRole("button", { name: "Add movie" });
    expect(buttons).toHaveLength(2);
    await user.click(buttons[1]);
    expect(search.openOverlay).toHaveBeenCalledTimes(1);
  });
});
