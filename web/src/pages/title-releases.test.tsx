// @vitest-environment jsdom
// The Last search page (/title/:id/releases): a season's link (?season=N) asks for that season's own stored search
// and says so; a season with nothing stored says that; without season the page is as before. Invented keys.
import { screen, waitFor } from "@testing-library/react";
import { Route, Routes } from "react-router";
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
