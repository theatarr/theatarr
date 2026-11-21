// @vitest-environment jsdom
// The Last search page (/title/:id/releases): a season's link (?season=N) asks for that season's own stored search
// and says so; a season with nothing stored says that; without season the page is as before. Invented keys.
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router";
import { expect, it, vi } from "vitest";
import { SearchProvider } from "@/components/search/search-context";
import { mockFetch, renderPage } from "@/test/page";
import { TitleReleasesPage } from "./title-releases";

/** The window's width for useMinWidth (matchMedia's min-width and max-width queries). */
const width = (px: number) =>
  vi.stubGlobal("matchMedia", (q: string) => {
    const min = /min-width:\s*(\d+)px/.exec(q);
    const max = /max-width:\s*(\d+)px/.exec(q);
    return { matches: (!min || px >= Number(min[1])) && (!max || px <= Number(max[1])), media: q, addEventListener() {}, removeEventListener() {} };
  });

const show = { id: 7, name: "Show", year: 2020, kind: "series", versions: [{ id: 1, key: "tv", label: "TV", control: { can_edit: true, can_search: true, live_releases: true } }] };
const none = { title_id: 7, version: "tv", versions: ["tv"], live_allowed: true, source: "none", results: 0, accepted: 0, candidates: [], rejections: {}, outcomes: {}, grab: false };
const stored = { ...none, source: "journal", target: "s02", results: 22, accepted: 0, message: "From a search for S02" };

const open = (path: string, releases: object) => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7", reply: show },
    { method: "GET", path: "/titles/7/releases", reply: releases },
    { method: "GET", path: "/titles/7/release-outcomes", reply: { outcomes: {} } },
    { method: "GET", path: "/add/options", reply: { kind: "series", anime: false, title_id: 7, router_enabled: true, episodes_known: true, can_search: true, seasons: [], versions: [] } },
  ]);
  renderPage(<SearchProvider><Routes><Route path="/title/:id/releases" element={<TitleReleasesPage />} /></Routes></SearchProvider>, path);
  return api;
};

/** Back, as the browser's, and the page's address. */
function Nav() {
  const navigate = useNavigate();
  const loc = useLocation();
  return <><button type="button" onClick={() => navigate(-1)}>Back</button><span data-testid="at">{loc.search}</span></>;
}

/** A series in two libraries (tv, tv4k), nothing stored in either; a live search never answers. */
function twoLibraries() {
  const two = { ...show, versions: [...show.versions, { id: 2, key: "tv4k", label: "TV 4K", control: { can_edit: true, can_search: true, live_releases: true } }] };
  return mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7", reply: two },
    { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => {
      const q = new URLSearchParams(r.search);
      return q.has("live") ? new Promise(() => {}) : { ...none, versions: ["tv", "tv4k"], version: q.get("version") ?? "tv" };
    } },
    { method: "GET", path: "/titles/7/release-outcomes", reply: { outcomes: {} } },
    { method: "GET", path: "/add/options", reply: { kind: "series", anime: false, title_id: 7, router_enabled: true, episodes_known: true, can_search: true, seasons: [], versions: [] } },
  ]);
}

for (const px of [400, 1440]) {
  it(`asks for a season's own stored search and says it is that season's, at ${px} px`, async () => {
    width(px);
    const api = open("/title/7/releases?version=tv&season=2", stored);
    expect((await screen.findByTestId("releases-facts")).textContent).toBe("22 releases returned by the indexers, 0 accepted");
    expect(api.calls("GET", "/titles/7/releases").map((r) => new URLSearchParams(r.search).get("season"))).toEqual(["2"]);
    expect(screen.getByText(/Show \(2020\), Season 2: the last search of this season Theatarr decided for each library\./)).toBeTruthy();
    expect(screen.getByText(/Interactive search searches the indexers now for this season\./)).toBeTruthy();
  });

  it(`says a season with nothing stored has no stored search, at ${px} px`, async () => {
    width(px);
    open("/title/7/releases?version=tv&season=3", none);
    expect((await screen.findByTestId("releases-source")).textContent).toBe("Nothing stored");
    expect(screen.getByText("No search of season 3 is stored for TV.")).toBeTruthy();
  });

  it(`opens the run a Libraries row reports, or says it stored none, at ${px} px`, async () => {
    width(px);
    const api = open("/title/7/releases?version=tv&run=162&season=2&at=2025-11-12", none);
    expect((await screen.findByTestId("releases-source")).textContent).toBe("Nothing stored");
    const q = new URLSearchParams(api.calls("GET", "/titles/7/releases")[0].search);
    expect([q.get("run"), q.get("season"), q.has("at")]).toEqual(["162", "2", false]);
    expect(screen.getByText("This search stored no release list for TV.")).toBeTruthy();
    // That search, not the library's last one (a later search of the season may exist).
    expect(screen.getByText(/Show \(2020\): the search of Season 2 on Nov 12, 2025 that the Libraries row reports\./)).toBeTruthy();
    expect(screen.queryByText(/the last search/)).toBeNull();
    // An empty stored list asks for no outcomes.
    expect(api.calls("GET", "/titles/7/release-outcomes")).toHaveLength(0);
  });

  it(`heads another library's tab as its own last search, not the pinned run, at ${px} px`, async () => {
    width(px);
    const two = { ...show, versions: [...show.versions, { id: 2, key: "tv4k", label: "TV 4K", control: { can_edit: true, can_search: true, live_releases: true } }] };
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } },
      { method: "GET", path: "/titles/7", reply: two },
      { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => ({ ...none, versions: ["tv", "tv4k"], version: new URLSearchParams(r.search).get("version") ?? "tv" }) },
      { method: "GET", path: "/titles/7/release-outcomes", reply: { outcomes: {} } },
      { method: "GET", path: "/add/options", reply: { kind: "series", anime: false, title_id: 7, router_enabled: true, episodes_known: true, can_search: true, seasons: [], versions: [] } },
    ]);
    renderPage(<SearchProvider><Routes><Route path="/title/:id/releases" element={<TitleReleasesPage />} /></Routes></SearchProvider>, "/title/7/releases?version=tv&run=162&season=2&at=2025-11-12");
    await screen.findByText("This search stored no release list for TV.");
    expect(screen.getByText(/that the Libraries row reports/)).toBeTruthy();
    const user = userEvent.setup();
    const tabs = screen.getAllByRole("tab");
    await user.click(tabs[1]);
    await waitFor(() => expect(screen.getAllByRole("tab")[1].getAttribute("aria-selected")).toBe("true"));
    expect(screen.queryByText(/that the Libraries row reports/)).toBeNull();
    expect(screen.getByText(/Show \(2020\), Season 2: the last search of this season Theatarr decided for each library\./)).toBeTruthy();
    // The run belongs to the linked library: a change of library drops it from the address, so back on that tab the
    // page shows its own last search of the season.
    await user.click(screen.getAllByRole("tab")[0]);
    await waitFor(() => expect(screen.getAllByRole("tab")[0].getAttribute("aria-selected")).toBe("true"));
    expect(screen.queryByText(/that the Libraries row reports/)).toBeNull();
  });

  it(`heads an episode run as the search of that episode, not as a whole-series search, at ${px} px`, async () => {
    width(px);
    const api = open("/title/7/releases?version=tv&run=163&episode=S02E05&at=2025-11-08", { ...stored, target: "s02e05", message: "From a search for S02E05" });
    await screen.findByTestId("releases-facts");
    const q = new URLSearchParams(api.calls("GET", "/titles/7/releases")[0].search);
    expect([q.get("run"), q.has("season"), q.has("episode")]).toEqual(["163", false, false]);
    expect(screen.getByText(/Show \(2020\): the search of S02E05 on Nov 8, 2025 that the Libraries row reports\./)).toBeTruthy();
    expect(screen.queryByText(/the last search/)).toBeNull();
  });

  it(`links an empty season to the library's last search, at ${px} px`, async () => {
    width(px);
    open("/title/7/releases?version=tv&season=3", none);
    await screen.findByText("No search of season 3 is stored for TV.", { exact: false });
    const link = screen.getByRole("link", { name: "Show the last search for TV" });
    expect(link.getAttribute("href")).toBe("/title/7/releases?version=tv");
  });

  it(`keeps the heading and the rows on the same library when the panel is closed and opened again, at ${px} px`, async () => {
    width(px);
    const two = { ...show, versions: [...show.versions, { id: 2, key: "tv4k", label: "TV 4K", control: { can_edit: true, can_search: true, live_releases: true } }] };
    const api = mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } },
      { method: "GET", path: "/titles/7", reply: two },
      { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => ({ ...none, versions: ["tv", "tv4k"], version: new URLSearchParams(r.search).get("version") ?? "tv" }) },
      { method: "GET", path: "/titles/7/release-outcomes", reply: { outcomes: {} } },
      { method: "GET", path: "/add/options", reply: { kind: "series", anime: false, title_id: 7, router_enabled: true, episodes_known: true, can_search: true, seasons: [], versions: [] } },
    ]);
    renderPage(<SearchProvider><Routes><Route path="/title/:id/releases" element={<TitleReleasesPage />} /></Routes></SearchProvider>, "/title/7/releases?version=tv&run=162&season=2&at=2025-11-12");
    await screen.findByText("This search stored no release list for TV.");
    const user = userEvent.setup();
    await user.click(screen.getAllByRole("tab")[1]);
    await waitFor(() => expect(screen.getAllByRole("tab")[1].getAttribute("aria-selected")).toBe("true"));
    const toggle = screen.getByRole("button", { name: /Last search/ });
    await user.click(toggle);
    expect(screen.queryAllByRole("tab")).toHaveLength(0);
    await user.click(toggle);
    // Still the other library: its own Last search, in the heading and the rows.
    await waitFor(() => expect(screen.getAllByRole("tab")[1].getAttribute("aria-selected")).toBe("true"));
    expect(screen.queryByText(/that the Libraries row reports/)).toBeNull();
    expect(screen.getByText(/Show \(2020\), Season 2: the last search of this season Theatarr decided for each library\./)).toBeTruthy();
    const last = new URLSearchParams(api.calls("GET", "/titles/7/releases").at(-1)!.search);
    expect([last.get("version"), last.has("run")]).toEqual(["tv4k", false]);
  });

  it(`keeps the library with the address, so Back shows the library the page names, at ${px} px`, async () => {
    width(px);
    const two = { ...show, versions: [...show.versions, { id: 2, key: "tv4k", label: "TV 4K", control: { can_edit: true, can_search: true, live_releases: true } }] };
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } },
      { method: "GET", path: "/titles/7", reply: two },
      { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => ({ ...none, versions: ["tv", "tv4k"], version: new URLSearchParams(r.search).get("version") ?? "tv" }) },
      { method: "GET", path: "/titles/7/release-outcomes", reply: { outcomes: {} } },
      { method: "GET", path: "/add/options", reply: { kind: "series", anime: false, title_id: 7, router_enabled: true, episodes_known: true, can_search: true, seasons: [], versions: [] } },
    ]);
    renderPage(<SearchProvider><Routes><Route path="/title/:id/releases" element={<><TitleReleasesPage /><Nav /></>} /></Routes></SearchProvider>, "/title/7/releases?version=tv&season=3");
    await screen.findByText("No search of season 3 is stored for TV.");
    const user = userEvent.setup();
    await user.click(screen.getByRole("link", { name: "Show the last search for TV" }));
    await waitFor(() => expect(screen.getByTestId("at").textContent).toBe("?version=tv"));
    await user.click(await screen.findByRole("tab", { name: /TV 4K/ }));
    await waitFor(() => expect(screen.getByTestId("at").textContent).toBe("?version=tv4k"));
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(screen.getByTestId("at").textContent).toBe("?version=tv&season=3"));
    await screen.findByText("No search of season 3 is stored for TV.");
    expect(screen.getAllByRole("tab")[0].getAttribute("aria-selected")).toBe("true");
  });

  it(`keeps a pinned run in the history when another library is picked, so Back returns to it, at ${px} px`, async () => {
    width(px);
    const api = twoLibraries();
    renderPage(<SearchProvider><Routes><Route path="/title/:id/releases" element={<><TitleReleasesPage /><Nav /></>} /></Routes></SearchProvider>, "/title/7/releases?version=tv&run=162&season=2&at=2025-11-12");
    await screen.findByText("This search stored no release list for TV.");
    const user = userEvent.setup();
    await user.click(screen.getByRole("tab", { name: /TV 4K/ }));
    await waitFor(() => expect(screen.getByTestId("at").textContent).toBe("?version=tv4k&season=2"));
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(screen.getByTestId("at").textContent).toBe("?version=tv&run=162&season=2&at=2025-11-12"));
    expect(await screen.findByText(/Show \(2020\): the search of Season 2 on Nov 12, 2025 that the Libraries row reports\./)).toBeTruthy();
    expect(await screen.findByText("This search stored no release list for TV.")).toBeTruthy();
    expect(new URLSearchParams(api.calls("GET", "/titles/7/releases").at(-1)!.search).get("run")).toBe("162");
  });

  it(`closes an open Interactive search when Back changes the library shown, at ${px} px`, async () => {
    width(px);
    twoLibraries();
    // The app's cache (a list seen before shows at once on Back), not the tests' gcTime 0.
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <MemoryRouter initialEntries={["/title/7/releases?version=tv&season=3"]}>
        <QueryClientProvider client={client}>
          <SearchProvider><Routes><Route path="/title/:id/releases" element={<><TitleReleasesPage /><Nav /></>} /></Routes></SearchProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: "Show the last search for TV" }));
    await user.click(await screen.findByRole("tab", { name: /TV 4K/ }));
    await waitFor(() => expect(screen.getByTestId("at").textContent).toBe("?version=tv4k"));
    await user.click(await screen.findByTestId("interactive-search-open"));
    expect(await screen.findByRole("dialog", { name: "Interactive search" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(screen.getByTestId("at").textContent).toBe("?version=tv&season=3"));
    await screen.findByText("No search of season 3 is stored for TV.");
    expect(screen.queryByRole("dialog", { name: "Interactive search" })).toBeNull();
  });

  it(`asks without a season when the link names none, and says what the whole-series search lists, at ${px} px`, async () => {
    width(px);
    const api = open("/title/7/releases?version=tv", stored);
    await screen.findByTestId("releases-facts");
    await waitFor(() => expect(api.calls("GET", "/titles/7/releases")).toHaveLength(1));
    expect(new URLSearchParams(api.calls("GET", "/titles/7/releases")[0].search).has("season")).toBe(false);
    expect(screen.getByText(/Interactive search here searches the indexers now for packs of several seasons; a season or an episode has its own on the title page\./)).toBeTruthy();
    expect(screen.queryByText("No search of season 3 is stored for TV.")).toBeNull();
  });
}
