// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { History } from "./versions";

it("keeps the journal link for admins, quieter, as Journal entry", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["admin", "manage", "browse", "see_files"] } }]);
  renderPage(<History events={[{ event: "searched", date: "2026-11-02T00:00:00Z", run_id: 1, decision_id: 7 }]} titleId={7} version="movie-web" />);
  expect((await screen.findByRole("link", { name: "Journal entry" })).getAttribute("href")).toBe("/activity/journal/7");
  expect(screen.queryByRole("link", { name: "Why this decision" })).toBeNull();
});

it("keeps decision handoffs out of a manager's history", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } }]);
  renderPage(<History events={[{ event: "searched", date: "2026-11-02T00:00:00Z", decision_id: 7, latest: true }]} titleId={7} version="movie-web" />);
  expect(await screen.findByText("Searched")).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Journal entry" })).toBeNull();
  expect((await screen.findByRole("link", { name: "See this search's results" })).getAttribute("href")).toBe("/title/7/releases?version=movie-web");
});

describe("release history", () => {
  const manager = () => mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } }]);

  it("sums up a search inline and links only the newest search to its results", async () => {
    manager();
    renderPage(<History titleId={7} version="movie-web" events={[
      { event: "searched", date: "2026-11-02T02:00:00Z", decision_id: 9, results: 12, accepted: 3, summary: "Grabbed #1 via rd → imported", trigger: "automatic search", latest: true, info_hash: "ABC123" },
      { event: "searched", date: "2026-11-02T01:00:00Z", decision_id: 8, results: 4, accepted: 0, summary: "Nothing grabbed: no accepted release among 4 results", trigger: "manual search" },
    ]} />);
    const lines = await screen.findAllByTestId("history-search");
    expect(lines.map((l) => l.textContent)).toEqual(["12 results, 3 accepted · Grabbed #1 via rd → imported", "4 results, 0 accepted · Nothing grabbed: no accepted release among 4 results"]);
    const links = await screen.findAllByRole("link", { name: "See this search's results" });
    expect(links.map((l) => l.getAttribute("href"))).toEqual(["/title/7/releases?version=movie-web&hash=abc123"]);
  });

  it("names the provider and links a grab and a failure to the release's row", async () => {
    manager();
    renderPage(<History titleId={7} version="movie-web" events={[
      { event: "failed", date: "2026-11-02T02:00:00Z", release_title: "Alpha.2020.2160p.WEB-A", info_hash: "aaa111", provider: "rd", reason: "RD status error", source: "theatarr via rd", trigger: "automatic search" },
      { event: "grabbed", date: "2026-11-02T01:00:00Z", release_title: "Alpha.2020.2160p.WEB-A", info_hash: "aaa111", provider: "rd", source: "theatarr via rd", trigger: "automatic search" },
    ]} />);
    expect((await screen.findAllByText((_, el) => el?.tagName === "SPAN" && el.textContent === "via RD")).length).toBe(2);
    expect(screen.getByTestId("history-reason").textContent).toBe("RD status error");
    expect((await screen.findAllByTestId("history-release")).map((l) => l.getAttribute("href"))).toEqual([
      "/title/7/releases?version=movie-web&hash=aaa111", "/title/7/releases?version=movie-web&hash=aaa111"]);
  });

  it("gives no release links to callers who cannot open the releases view", async () => {
    // Without manage: the journal link shows the permissions arrived; the releases view's links stay out.
    mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["admin", "browse", "see_files"] } }]);
    renderPage(<History titleId={7} version="movie-web" events={[
      { event: "grabbed", date: "2026-11-02T01:00:00Z", release_title: "Alpha.2020.2160p.WEB-A", info_hash: "aaa111", provider: "rd", trigger: "RSS" },
      { event: "searched", date: "2026-11-02T00:00:00Z", decision_id: 3, latest: true },
    ]} />);
    expect(await screen.findByRole("link", { name: "Journal entry" })).toBeTruthy();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByText("Alpha.2020.2160p.WEB-A").tagName).toBe("DIV");
  });

  it("says what started each event", async () => {
    manager();
    const triggers = ["automatic search", "automatic upgrade search", "search again after a failure", "RSS", "manual search", "manual grab by alice",
      "manual retry", "approved upgrade", "Seerr request", "arr search (radarr)", "manual grab through the arr API (radarr)", "release push (radarr)", "old manager (radarr_4k)"];
    renderPage(<History events={triggers.map((t, i) => ({ event: "grabbed" as const, date: `2026-11-02T00:${String(i).padStart(2, "0")}:00Z`, trigger: t }))} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: `Show all ${triggers.length}` }));
    expect(screen.getAllByTestId("history-trigger").map((l) => l.textContent)).toEqual(triggers.map((t) => t[0].toUpperCase() + t.slice(1)));
  });
});

it("says Not started, in a neutral tone, for a download never tried", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } }]);
  renderPage(<History events={[{ event: "not_started", date: "2026-11-02T01:00:00Z", release_title: "Alpha.2020.2160p.WEB-A", reason: "Not started: downloads were off" }]} />);
  expect(await screen.findByText("Not started")).toBeTruthy();
  const reason = screen.getByTestId("history-reason");
  expect(reason.textContent).toBe("Not started: downloads were off");
  expect(reason.className).not.toContain("bad");
});
