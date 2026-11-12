// @vitest-environment jsdom
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import { ActivityPageView } from "./activity";
import { json, mockFetch, renderPage } from "@/test/page";

const user = { id: 9, username: "someone", permissions: ["browse", "request"], csrf_token: "t" };

function show(path: string, me = user) {
  const fetch = mockFetch([{ method: "GET", path: "/auth/me", reply: me }]);
  renderPage(
    <Routes>
      <Route path="/activity" element={<div data-testid="now-route" />} />
      <Route path="/activity/:tab" element={<ActivityPageView />} />
    </Routes>,
    path,
  );
  return fetch;
}

describe("Activity tabs", () => {
  it("says an unknown segment is not a tab instead of showing Queue", async () => {
    const fetch = show("/activity/unknown");
    expect((await screen.findByTestId("activity-unknown-tab")).textContent).toContain("There is no Activity tab “unknown”");
    expect(screen.getAllByRole("tab").every((t) => t.getAttribute("aria-selected") === "false")).toBe(true);
    // No downloads were asked for: Now is not rendered behind it.
    expect(fetch.requests.some((r) => r.path === "/activity")).toBe(false);
  });

  it("does not show Blocklist to a user", async () => {
    show("/activity/blocklist");
    expect(await screen.findByTestId("activity-unknown-tab")).toBeTruthy();
  });

  it("shows the Decision journal tab and its list to an admin", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/auth/me", reply: { ...user, permissions: ["see_files", "manage", "admin"] } },
      { method: "GET", path: "/journal", reply: { items: [{ id: "dl:4", kind: "grab", at: "2026-11-07T10:00:00Z", title: { id: 3, name: "Ferrous Bay" }, summary: "Grabbed" }] } },
    ]);
    renderPage(<Routes><Route path="/activity/:tab" element={<ActivityPageView />} /></Routes>, "/activity/journal?title=3");
    expect((await screen.findByRole("tab", { name: "Decision journal" })).getAttribute("aria-selected")).toBe("true");
    expect((await screen.findByRole("link", { name: "Ferrous Bay" })).getAttribute("href")).toBe("/activity/journal/dl%3A4");
    expect(fetch.calls("GET", "/journal")[0].search).toContain("title=3");
    expect(fetch.requests.some((r) => r.path === "/activity")).toBe(false);
  });

  it("does not show the Decision journal to a user or to a manager who sees files", async () => {
    for (const permissions of [["browse", "request"], ["browse", "see_files", "manage"]]) {
      const fetch = show("/activity/journal", { ...user, permissions });
      expect((await screen.findByTestId("activity-unknown-tab")).textContent).toContain("There is no Activity tab “journal”");
      expect(screen.queryByRole("tab", { name: "Decision journal" })).toBeNull();
      expect(fetch.requests.some((r) => r.path === "/journal")).toBe(false);
      cleanup();
    }
  });

  it("sends an admin viewing as a user from the Decision journal back to Queue", async () => {
    const fetch = show("/activity/journal?as=user", { ...user, permissions: ["see_files", "manage", "admin"] });
    expect(await screen.findByTestId("now-route")).toBeTruthy();
    expect(fetch.requests.some((r) => r.path === "/journal")).toBe(false);
  });

  it("sends an admin viewing as a user from Blocklist back to Queue", async () => {
    show("/activity/blocklist?as=user", { ...user, permissions: ["see_files", "manage", "admin"] });
    expect(await screen.findByTestId("now-route")).toBeTruthy();
  });
});


it("fetches full provider history only when Inspect opens", async () => {
  const attempts = [{ provider: "rd", outcome: "deferred", at: "2026-10-30T00:00:00Z", message: "Old attempt outside preview" }];
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { ...user, permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: 1, now: [{ id: 7, title: { id: 1, name: "Alpha", kind: "movie", poster: "" }, version: "web", state: "failed", step: "adding", steps: [], words: "Provider failed", progress: 0, created_at: "2026-10-30T00:00:00Z", updated_at: "2026-10-30T00:00:00Z", attempts: [], attempts_more: true }] } },
    { method: "GET", path: "/activity/downloads/7/attempts", reply: { attempts } },
  ]);
  renderPage(<ActivityPageView />, "/activity");
  const inspect = await screen.findByRole("button", { name: "Inspect" });
  expect(fetch.calls("GET", "/activity/downloads/7/attempts")).toHaveLength(0);
  await userEvent.click(inspect);
  expect(await screen.findByText("Old attempt outside preview")).toBeTruthy();
  await waitFor(() => expect(fetch.calls("GET", "/activity/downloads/7/attempts")).toHaveLength(1));
});


it("names the download client of a client-routed download where it names the provider", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { ...user, permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: 1, now: [{ id: 8, title: { id: 1, name: "Alpha", kind: "movie", poster: "" }, version: "web", state: "downloading", step: "downloading", steps: [], words: "Downloading (40%)", progress: 40, created_at: "2026-10-30T00:00:00Z", updated_at: "2026-10-30T00:00:00Z", client: "decypharr-movies", detail: "Downloading on client decypharr-movies (40%)" }] } },
  ]);
  renderPage(<ActivityPageView />, "/activity");
  await userEvent.click(await screen.findByRole("button", { name: "Inspect" }));
  expect(await screen.findByText("client decypharr-movies")).toBeTruthy();
});

it("keeps the attempt preview on an Inspect error and can retry full history", async () => {
  const viewer = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { ...user, permissions: ["browse", "see_files"] } },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: 1, now: [{ id: 7, title: { id: 1, name: "Alpha", kind: "movie", poster: "" }, version: "web", state: "failed", step: "adding", steps: [], words: "Provider failed", progress: 0, created_at: "2026-10-30T00:00:00Z", updated_at: "2026-10-30T00:00:00Z", attempts: [{ provider: "rd", outcome: "deferred", message: "Latest preview", at: "2026-10-30T00:00:00Z" }], attempts_more: true }] } },
    { method: "GET", path: "/activity/downloads/7/attempts", reply: json({ error: "Unavailable" }, 503) },
    { method: "GET", path: "/activity/downloads/7/attempts", reply: { attempts: [{ provider: "rd", outcome: "deferred", message: "Recovered full history", at: "2026-10-30T00:00:00Z" }] } },
  ]);
  renderPage(<ActivityPageView />, "/activity");
  await viewer.click(await screen.findByRole("button", { name: "Inspect" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByText("Latest preview")).toBeTruthy();
  expect(screen.getByText("Showing the latest 1 attempt.")).toBeTruthy();
  await viewer.click(screen.getByRole("button", { name: "Retry attempt history" }));
  expect(await screen.findByText("Recovered full history")).toBeTruthy();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("lets a see-files non-admin account open Blocklist", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { ...user, permissions: ["browse", "see_files"] } },
    { method: "GET", path: "/activity", reply: { state: "blocked", admin: true, total: 0, blocked: [] } },
  ]);
  renderPage(<Routes><Route path="/activity/:tab" element={<ActivityPageView />} /></Routes>, "/activity/blocklist");
  expect(await screen.findByRole("tab", { name: "Blocklist" })).toBeTruthy();
  await waitFor(() => expect(fetch.calls("GET", "/activity")).toHaveLength(1));
  expect(screen.queryByTestId("activity-unknown-tab")).toBeNull();
});

describe("Activity for an account without See files", () => {
  const failed = { id: 7, title: { id: 1, name: "Alpha", kind: "movie", poster: "" }, version: "movie-remux", state: "failed", step: "adding", steps: [], words: "Failed", quality: "2160p", progress: 0, created_at: "2026-10-30T00:00:00Z", updated_at: "2026-10-30T00:00:00Z" };
  const event = { id: "dl:7:fail", at: "2026-10-30T00:00:00Z", kind: "failure", title: { id: 1, name: "Alpha", kind: "movie" }, version: "movie-remux", words: "Download failed", tone: "bad" };
  function showAs(permissions: string[]) {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { ...user, permissions } },
      { method: "GET", path: "/activity", reply: (r: { search: string }) => (r.search.includes("state=history") ? { state: "history", admin: false, history: [event] } : { state: "now", admin: false, total: 1, now: [failed] }) },
      { method: "GET", path: "/titles/facets", reply: {} },
    ]);
  }
  it("shows no library keys and says plainly that a failed download did not work", async () => {
    showAs(["browse", "request"]);
    renderPage(<ActivityPageView />, "/activity");
    await userEvent.click(await screen.findByTestId("download-details"));
    expect((await screen.findByTestId("failed-words")).textContent).toBe("This did not work. A manager can look at it.");
    expect(document.querySelector("[data-vkey]")).toBeNull();
    expect(document.body.textContent).not.toContain("movie-remux");
    expect(screen.getByText("4K")).toBeTruthy();
  });
  it("shows no library key on a history row", async () => {
    showAs(["browse", "request"]);
    renderPage(<Routes><Route path="/activity/:tab" element={<ActivityPageView />} /></Routes>, "/activity/history");
    await screen.findByText("Download failed");
    expect(document.querySelector("[data-testid=history-row] [data-vkey]")).toBeNull();
  });
  it("keeps the key and leaves out the plain failure line for those who see files", async () => {
    showAs(["browse", "see_files"]);
    renderPage(<ActivityPageView />, "/activity");
    await userEvent.click(await screen.findByTestId("download-details"));
    expect(document.querySelector('[data-vkey="movie-remux"]')).toBeTruthy();
    expect(screen.queryByTestId("failed-words")).toBeNull();
  });
  it("shows a download ended because downloads were off as Not started, not as a failure", async () => {
    const notStarted = { ...failed, not_started: true, words: "Downloads were off when it was due to start" };
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { ...user, permissions: ["browse", "request"] } },
      { method: "GET", path: "/activity", reply: { state: "now", admin: false, total: 1, now: [notStarted] } },
      { method: "GET", path: "/titles/facets", reply: {} },
    ]);
    renderPage(<ActivityPageView />, "/activity");
    const stages = await screen.findAllByTestId("stage");
    expect(stages.map((s) => s.textContent)).toEqual(stages.map(() => "Not started"));
    expect(screen.getAllByTestId("words")[0].textContent).toBe("Downloads were off when it was due to start");
    await userEvent.click(screen.getAllByTestId("download-details")[0]);
    expect(screen.queryByTestId("failed-words")).toBeNull();
  });
});
