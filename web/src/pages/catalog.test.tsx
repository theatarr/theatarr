// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Catalog } from "./catalog";
import { expectNamedControls, mockFetch, renderPage } from "@/test/page";

const shadow = { mode: "shadow", can_edit: true, can_search: false, live_releases: false };
const managed = { mode: "active", can_edit: true, can_search: true, live_releases: true };

const titles = [
  {
    id: 1, kind: "series", name: "The Pellbrook Inquiries", year: 2017, monitored: true, network: "Northstar Network",
    next_up: { date: "2099-11-04", season: 3, number: 6 }, size_bytes: 184e9, added_at: "2026-10-28T00:00:00Z",
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

  it("shows a column per copy with its stage and bar, and sorts by a copy", async () => {
    const fetch = mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(await screen.findByText("13 / 17 available")).toBeTruthy();
    expect(screen.queryByTestId("column-owner")).toBeNull();
    // In the copy cell and in Next / activity.
    const stage = screen.getAllByRole("link", { name: "Downloading · 37%" });
    expect(stage).toHaveLength(2);
    expect(stage[0].getAttribute("href")).toBe("/activity?show=downloading");
    expect(screen.getAllByTestId("copy-flight").length).toBeGreaterThan(0);
    expect(screen.getByText("Not in this library")).toBeTruthy();
    // Every copy's switch is editable, also one an arr imports.
    expect(screen.getByRole("switch", { name: "Monitor The Pellbrook Inquiries 4K" }).getAttribute("aria-disabled")).toBeNull();
    expect(screen.getByRole("switch", { name: "Monitor The Pellbrook Inquiries 1080p WEB" }).getAttribute("aria-disabled")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Sort by 4K completion" }));
    await waitFor(() => expect(fetch.requests.some((r) => r.path === "/titles" && r.search.includes("sort=completion") && r.search.includes("sort_version=tv-2160p"))).toBe(true));
    expectNamedControls();
  });

  it("keeps a sort picked from the dropdown, dropping the copy and the order with it", async () => {
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
    await user.selectOptions(pick, "rating");
    await waitFor(() => expect(pick.value).toBe("rating"));
    expect(screen.getByRole("button", { name: "Descending" })).toBeTruthy();
  });

  it("monitors one copy through the bulk route, never a search", async () => {
    const fetch = mockFetch(routes([
      { method: "POST", path: "/titles/monitor", reply: { dry_run: false, items: [], changed: 1, unchanged: 0, missing: 0, stale: 0, message: "Unmonitored 1 library" } },
    ]));
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await user.click(await screen.findByRole("switch", { name: "Monitor The Pellbrook Inquiries 1080p WEB" }));
    await waitFor(() => expect(fetch.calls("POST", "/titles/monitor")).toHaveLength(1));
    expect(fetch.calls("POST", "/titles/monitor")[0].body).toEqual({ title_ids: [1], versions: ["tv-web"], monitored: false });
    expect(fetch.requests.filter((r) => r.method !== "GET").map((r) => r.path)).toEqual(["/titles/monitor"]);
  });

  it("picks and reorders columns, kept per user", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("The Pellbrook Inquiries");
    expect(screen.getByRole("columnheader", { name: /Next \/ activity/ })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Columns" }));
    const dialog = await screen.findByRole("dialog", { name: "Library columns" });
    await user.click(within(dialog).getByRole("checkbox", { name: /Network \/ studio/ }));
    await user.click(within(dialog).getByRole("button", { name: "Move Network / studio up" }));
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.getByText("Northstar Network")).toBeTruthy();
    const kept = JSON.parse(window.localStorage.getItem("theatarr.library.columns.7") ?? "{}");
    expect(kept.shown).toContain("network");
    expect(kept.order.indexOf("network")).toBeLessThan(kept.order.indexOf("added"));
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
    await user.click(screen.getByRole("checkbox", { name: "Select all shown titles" }));
    const bar = screen.getByRole("region", { name: "Bulk edit" });
    expect(within(bar).getByText("2 titles selected")).toBeTruthy();
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
    await user.click(await screen.findByRole("button", { name: "Saltglass Meridian: libraries" }));
    const dialog = await screen.findByRole("dialog", { name: "Saltglass Meridian libraries" });
    const remux = within(dialog).getByText("4K Remux").closest("[data-copy]") as HTMLElement;
    expect(within(remux).getByText("Remux-2160p")).toBeTruthy();
    expect(within(remux).getByRole("link", { name: "Upgrade downloading · 37%" })).toBeTruthy();
    expect(within(remux).getByText("The current file stays available until the new one is linked.")).toBeTruthy();
    expect(within(remux).getByRole("switch", { name: "Monitor Saltglass Meridian 4K Remux" }).getAttribute("aria-disabled")).toBeNull();
    expect(within(dialog).getByRole("link", { name: "Open title page" }).getAttribute("href")).toBe("/title/9");
  });

  it("stacks rows on a phone with no table", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: !q.includes("min-width"), media: q, addEventListener() {}, removeEventListener() {} }));
    mockFetch(routes());
    renderPage(<Catalog id="series" />, "/series");
    await screen.findByTestId("title-list");
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByTestId("copy-owner")).toBeNull();
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
    await user.click(within(dialog).getByRole("checkbox", { name: "Network / studio" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "Next episode / release" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "Quality" }));
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.getByText("Northstar Network")).toBeTruthy();
    expect(screen.getByText("S03E06 · Nov 4, 2099")).toBeTruthy();
    expect(screen.getByText("4K: WEBDL-2160p · 1080p WEB: WEBDL-1080p")).toBeTruthy();
    expect(screen.getByTestId("poster-grid").className).toContain("poster-size-small");
    expect(JSON.parse(window.localStorage.getItem("theatarr.library.posters.7") ?? "{}").size).toBe("small");
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
      { id: 7, kind: "movie", name: "Sundial", year: 2099, monitored: true, release_date: "2099-06-01", availability: "missing",
        versions: [{ id: 71, key: "movie-4k", label: "4K", monitored: true, has_file: false, state: "wanted", control: managed }] },
      { id: 8, kind: "movie", name: "Grey Orchard", year: 2020, monitored: false, release_date: "2020-05-01", availability: "missing",
        versions: [{ id: 81, key: "movie-4k", label: "4K", monitored: false, has_file: false, state: "wanted", control: managed }] },
    ];
    mockFetch(routes().map((r) => (r.path === "/titles" ? { ...r, reply: { items: movies, total: 4, offset: 0 } } : r)));
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
    expect(within(two).getByRole("link", { name: /^Copper Sky \(2022\), Available,/ })).toBeTruthy();
    expect(within(two).getAllByTestId("status-bar")).toHaveLength(1);
    expect(within(two).getByTestId("status-bar").getAttribute("data-tone")).toBe("ok");
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
    expect(within(off).getByRole("link", { name: /not monitored/ })).toBeTruthy();
    expect(within(one).queryByTestId("unmonitored-chip")).toBeNull();
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
    expect(screen.queryByRole("button", { name: /: libraries$/ })).toBeNull();
  });

  it("offers no per-library has/lacks filter, which those who see files keep", async () => {
    const versions = [{ key: "movie-remux", label: "4K Remux", titles: 2, with_file: 1, wanted: 1, filled: 1, cutoff_met: 0 }];
    for (const [perms, shown] of [[[], false], [["see_files"], true]] as const) {
      const fetch = mockFetch(household.map((r) => (r.path === "/titles/facets" ? { ...r, reply: { total: 2, versions } } : r.path === "/auth/me" ? { ...r, reply: { ...r.reply, permissions: perms } } : r)));
      const { unmount } = renderPage(<Catalog id="movies" />, "/movies");
      const panel = (await screen.findAllByTestId("filters-panel"))[0];
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

  it("names them apart in the table headers and the filter panel", async () => {
    window.localStorage.setItem("theatarr.catalog.view", "table");
    mockFetch(twinRoutes);
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("Brindlewood Bakers");
    expect(screen.getByRole("button", { name: "Sort by 4K · tv-2160p completion" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sort by 4K · tv-2160p-theatarr completion" })).toBeTruthy();
    const panel = (await screen.findAllByTestId("filters-panel"))[0];
    expect(await within(panel).findByText("4K · tv-2160p")).toBeTruthy();
    expect(within(panel).getByText("4K · tv-2160p-theatarr")).toBeTruthy();
    expect(within(panel).queryByText("4K")).toBeNull();
  });

  it("names them apart on posters", async () => {
    mockFetch(twinRoutes);
    renderPage(<Catalog id="series" />, "/series");
    await screen.findAllByText("Brindlewood Bakers");
    const lines = screen.getAllByTestId("library-line");
    expect(within(lines[0]).getByText("4K · tv-2160p")).toBeTruthy();
    expect(within(lines[1]).getByText("4K · tv-2160p-theatarr")).toBeTruthy();
    expect(within(screen.getByTestId("poster-grid")).getAllByTestId("status-bar").map((e) => e.getAttribute("aria-label"))).toEqual([
      "4K · tv-2160p: 24 of 24 episodes linked",
      "4K · tv-2160p-theatarr: 12 of 24 episodes linked",
    ]);
  });
});
