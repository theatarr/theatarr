// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NowPlayingCard } from "./now-playing-card";
import { mockFetch, renderPage } from "@/test/page";

const session = {
  key: "1", user_id: "7", user: "alex", player: "Living room", type: "episode", show: "Show", season: 1, episode: 2,
  title: "Two", state: "buffering", view_offset_ms: 1_500_000, duration_ms: 3_000_000,
  health: { verdict: "buffering", cause: "provider" },
};

describe("Now playing card", () => {
  it("lists who watches what and how it plays, linked to the page", async () => {
    mockFetch([{ method: "GET", path: "/nowplaying", reply: { enabled: true, sessions: [session] } }]);
    renderPage(<NowPlayingCard />);
    expect(await screen.findByText("Show · S01E02 · Two")).toBeTruthy();
    expect(screen.getByText("alex · Living room · 50%")).toBeTruthy();
    expect(screen.getByText("Buffering: likely the debrid provider")).toBeTruthy();
    expect(screen.getAllByRole("link").every((a) => a.getAttribute("href") === "/now-playing")).toBe(true);
  });

  it("renders nothing while nothing plays or the feature is off", async () => {
    for (const reply of [{ enabled: true, sessions: [] }, { enabled: false, sessions: [] }]) {
      const fetch = mockFetch([{ method: "GET", path: "/nowplaying", reply }]);
      const { container, unmount } = renderPage(<NowPlayingCard />);
      await waitFor(() => expect(fetch.calls("GET", "/nowplaying").length).toBe(1));
      expect(container.textContent).toBe("");
      unmount();
    }
  });
});
