// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NowPlayingPage } from "./now-playing";
import { mockFetch, renderPage } from "@/test/page";

describe("Now playing while it is off", () => {
  function show(permissions: string[]) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions, csrf_token: "t" } },
      { method: "GET", path: "/nowplaying", reply: { enabled: false, sessions: [] } },
    ]);
    renderPage(<NowPlayingPage />, "/now-playing");
  }
  it("links admins to Plex & playback", async () => {
    show(["browse", "see_files", "manage", "admin"]);
    const link = await screen.findByRole("link", { name: "Plex & playback" });
    expect(link.getAttribute("href")).toMatch(/^\/admin\/settings/);
  });
  it.each([[["browse", "see_files", "manage", "manage_requests"]], [["browse", "request"]]])("tells %j to ask an administrator, with no settings link", async (permissions) => {
    show(permissions);
    expect(await screen.findByText("Now playing is off. Ask an administrator to set up Plex.")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Plex & playback" })).toBeNull();
  });
});

describe("Now playing with a Plex error", () => {
  it("shows the error alone: no zero counts and no Nothing is playing", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions: ["browse", "see_files", "manage"], csrf_token: "t" } },
      { method: "GET", path: "/nowplaying", reply: { enabled: true, sessions: [], error: "connection refused" } },
    ]);
    renderPage(<NowPlayingPage />, "/now-playing");
    expect(await screen.findByText("Plex: connection refused")).toBeTruthy();
    expect(screen.queryByText("Nothing is playing.")).toBeNull();
    expect(screen.queryByText("playbacks")).toBeNull();
  });
});
