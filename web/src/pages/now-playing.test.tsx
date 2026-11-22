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

describe("Now playing with a Plex sentence and its detail", () => {
  it("shows a sentence that already names Plex without the prefix, and the raw error as a wrapping second line", async () => {
    const sentence = "Plex does not answer at http://plex:32400: check the address under Settings › Plex & playback";
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions: ["browse", "see_files", "manage"], csrf_token: "t" } },
      { method: "GET", path: "/nowplaying", reply: { enabled: true, sessions: [], error: sentence, error_detail: "dial tcp 10.0.0.9:32400: connect: connection refused" } },
    ]);
    renderPage(<NowPlayingPage />, "/now-playing");
    const card = (await screen.findByText(sentence)).closest("div") as HTMLElement;
    expect(card.textContent).not.toContain("Plex: Plex");
    const detail = screen.getByTestId("plex-error-detail");
    expect(detail.textContent).toBe("dial tcp 10.0.0.9:32400: connect: connection refused");
    expect(detail.className).toContain("[overflow-wrap:anywhere]");
    expect(detail.className.split(/\s+/).some((c) => c === "truncate" || /^line-clamp-/.test(c))).toBe(false);
  });

  it("adds no second prefix to a lowercase plex: error (fix round 2)", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions: ["browse", "see_files", "manage"], csrf_token: "t" } },
      { method: "GET", path: "/nowplaying", reply: { enabled: true, sessions: [], error: "plex: GET /status/sessions: 500" } },
    ]);
    renderPage(<NowPlayingPage />, "/now-playing");
    expect(await screen.findByText("plex: GET /status/sessions: 500")).toBeTruthy();
    expect(screen.queryByText(/Plex: plex:/)).toBeNull();
  });

  it("keeps a member's short Plex words as they are, with no detail line", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions: ["browse"], csrf_token: "t" } },
      { method: "GET", path: "/nowplaying", reply: { enabled: true, sessions: [], error: "Plex does not answer" } },
    ]);
    renderPage(<NowPlayingPage />, "/now-playing");
    expect(await screen.findByText("Plex does not answer")).toBeTruthy();
    expect(screen.queryByText(/^Plex: /)).toBeNull();
    expect(screen.queryByTestId("plex-error-detail")).toBeNull();
  });
});

describe("Now playing's settings links", () => {
  it("gives a configured rclone connection one Edit, and the Plex link looks like a link", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "owner", permissions: ["browse", "see_files", "manage", "admin"], csrf_token: "t" } },
      { method: "GET", path: "/nowplaying", reply: { enabled: false, sessions: [] } },
      { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 1, sections: [{ key: "library", live: true, config: { mount: { rclone_rc: { url: "http://10.0.0.5:5572" } } }, yaml: "", secrets: [] }] } },
    ]);
    renderPage(<NowPlayingPage />, "/now-playing");
    expect(await screen.findByText(/rclone remote control at 10\.0\.0\.5:5572/)).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Edit" })).toHaveLength(1);
    expect(screen.queryByRole("link", { name: "rclone remote control settings" })).toBeNull();
    const plex = screen.getByRole("link", { name: "Plex & playback" });
    expect(plex.className).toContain("text-[var(--accent-ink)]");
    expect(plex.className).toContain("underline");
  });
});

describe("Now playing's files that stopped working", () => {
  function show(permissions: string[], incidents?: unknown[]) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { id: 9, username: "someone", permissions, csrf_token: "t" } },
      { method: "GET", path: "/nowplaying", reply: { enabled: true, sessions: [], ...(incidents ? { incidents } : {}) } },
    ]);
    renderPage(<NowPlayingPage />, "/now-playing");
  }
  const at = new Date(Date.now() - 2 * 3600_000).toISOString();
  const incidents = [
    { id: 3, title_id: 7, title: "Show (2020)", version: "tv-web", episodes: ["S02E07"], files: 2, viewer: "ann", player: "Living room TV",
      state: "replaced", status: "Replaced with another release", old_release: "Show.S02.1080p.WEB-DL-OLD", new_release: "Show.S02E07.1080p.BluRay-NEW",
      message: "the player was stopped with the message: This file stopped working. Looking for another copy. Try again in a few minutes.", at },
    { id: 2, title_id: 8, title: "Film (2019)", version: "movie-web", files: 1, state: "unavailable", status: "No other copy yet", message: "the player was not stopped: the playback had already ended", at },
    { id: 1, title_id: 9, title: "Other (2018)", version: "movie-web", files: 1, state: "readable", status: "Readable again", message: "Plex refused to stop the player: status 401", at },
  ];
  it("lists each with the outcome, when, who watched, the message and a link to the title", async () => {
    show(["browse", "see_files", "manage"], incidents);
    const rows = await screen.findAllByTestId("now-playing-incident");
    expect(screen.getByRole("heading", { name: "Files that stopped working" })).toBeTruthy();
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringMatching(/^Show \(2020\) · S02E07(.*)Replaced with another release/),
      expect.stringMatching(/^Film \(2019\)(.*)No other copy yet/),
      expect.stringMatching(/^Other \(2018\)(.*)Readable again/),
    ]);
    expect(rows[0].textContent).toContain("(2 h ago) · ann on Living room TV · 2 files of the release stopped working");
    expect(rows[0].textContent).toContain("The player was stopped with the message: This file stopped working. Looking for another copy. Try again in a few minutes.");
    expect(rows[0].textContent).toContain("Broken copy: Show.S02.1080p.WEB-DL-OLD");
    expect(rows[0].textContent).toContain("Replaced by: Show.S02E07.1080p.BluRay-NEW");
    expect(rows[1].textContent).toContain("The player was not stopped: the playback had already ended.");
    expect(rows[2].textContent).toContain("Plex refused to stop the player: status 401.");
    expect(screen.getByRole("link", { name: "Show (2020) · S02E07" }).getAttribute("href")).toBe("/title/7");
    expect(rows[0].innerHTML).not.toMatch(/truncate|text-ellipsis|line-clamp/);
    // The library, for those who see files; a long name wraps inside the card.
    const tag = rows[0].querySelector("[data-vkey]");
    expect(tag?.getAttribute("data-vkey")).toBe("tv-web");
    expect(tag?.className).toContain("whitespace-normal");
    expect(tag?.className).not.toMatch(/(^|\s)whitespace-nowrap(\s|$)/);
  });
  it("gives a member their own rows without the library, the file count or the releases", async () => {
    show(["browse", "request"], [{ id: 3, title_id: 7, title: "Show (2020)", episodes: ["S02E07"], viewer: "ann", player: "Living room TV",
      state: "replaced", status: "Replaced with another release", at }]);
    const rows = await screen.findAllByTestId("now-playing-incident");
    expect(rows[0].querySelector("[data-vkey]")).toBeNull();
    expect(rows[0].textContent).not.toMatch(/files of the release|Broken copy|Replaced by/);
    expect(rows[0].textContent).toContain("The player was not stopped.");
  });
  it("shows no list when there is none", async () => {
    show(["browse", "request"]);
    expect(await screen.findByText("Nothing is playing.")).toBeTruthy();
    expect(screen.queryByTestId("now-playing-incidents")).toBeNull();
    expect(screen.queryByText("Files that stopped working")).toBeNull();
  });
});
