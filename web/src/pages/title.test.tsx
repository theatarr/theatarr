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

  it("says Pause once above the Libraries table", async () => {
    const grab_state = { mode: "active", paused: true, pause_forced: false, will_grab: false };
    const fetch = mockFetch([
      { method: "GET", path: "/auth/me", reply: me },
      { method: "GET", path: "/titles/70", reply: { ...title, versions: [{ id: 5, key: "anime-tv-theatarr", label: "Anime", state: "wanted", monitored: true,
        control: { mode: "active", can_edit: true, can_search: false, live_releases: true, search_reason: "Paused: nothing is searched or downloaded", grab_state } }] } },
      { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, title_id: 70, router_enabled: false, episodes_known: false, can_search: false, seasons: [],
        versions: [{ key: "anime-tv-theatarr", kind: "movie", mode: "active", enable: "", fits: true, default: true, on: true, searchable: false, label: "Anime", grab_state }] } },
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
  });

  it("reads Getting ready to a household account for a missing title with a download in hand", async () => {
    const household = { id: 8, username: "demo-limited", permissions: ["browse", "request"], csrf_token: "t" };
    const missing = { ...title, availability: "missing", versions: [{ id: 5, key: "movie-remux", label: "4K Remux", state: "wanted", monitored: true, has_file: false }] };
    for (const [acquiring, want] of [[true, "Getting ready"], [false, "Not in the library yet"]] as const) {
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
});
