// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { withDistinctLabels } from "@/lib/copies";
import { CopyCards, History } from "./versions";

const copy = { id: 1, title_id: 7, key: "movie-web", label: "WEB", monitored: true, control: { mode: "active" as const, can_edit: true, can_search: true, live_releases: true } };

describe("copy cards", () => {
  it("shows a Search now failure and drops the earlier outcome", async () => {
    let calls = 0;
    mockFetch([
      { method: "GET", path: "/me", reply: { permissions: [] } },
      { method: "POST", path: "/titles/7/search", reply: () => ++calls === 1 ? { started: true, message: "Search started for WEB." } : json({ error: "search backend down" }, 500) },
    ]);
    const user = userEvent.setup();
    renderPage(<CopyCards versions={[copy] as never} />);
    await user.click(screen.getByTestId("version-search-movie-web"));
    expect(await screen.findByText("Search started for WEB.")).toBeTruthy();
    await user.click(screen.getByTestId("version-search-movie-web"));
    expect(await screen.findByText(/Search failed: search backend down/)).toBeTruthy();
    expect(screen.queryByText("Search started for WEB.")).toBeNull();
  });
});

it("hands missing and cutoff copies to Wanted for admins", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["admin", "browse", "see_files"] } }]);
  renderPage(<CopyCards versions={[{ ...copy, state: "wanted" }, { ...copy, id: 2, key: "movie-remux", state: "filled", has_file: true }] as never} />);
  const links = await screen.findAllByRole("link", { name: "See in Wanted" });
  expect(links.map(l => l.getAttribute("href"))).toEqual(["/wanted?version=movie-web"]);
  expect(screen.getByRole("link", { name: "See in Cutoff unmet" }).getAttribute("href")).toBe("/wanted/cutoff?version=movie-remux");
});


it("links search history decisions for admins only", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["admin", "browse", "see_files"] } }]);
  renderPage(<History events={[{ event: "searched", date: "2026-11-02T00:00:00Z", run_id: 1, decision_id: 7 }]} />);
  expect((await screen.findByRole("link", { name: "Why this decision" })).getAttribute("href")).toBe("/admin/advanced/journal/7");
});

it("keeps decision handoffs out of a manager's history", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } }]);
  renderPage(<History events={[{ event: "searched", date: "2026-11-02T00:00:00Z", decision_id: 7 }]} />);
  expect(await screen.findByText("Searched")).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Why this decision" })).toBeNull();
});

it("shows both series queues and no handoffs for shadow copies", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["admin", "browse", "see_files"] } }]);
  renderPage(<CopyCards versions={[
    { ...copy, state: "wanted", episodes: { missing: 1 }, upgrade: { below: 2 } },
    { ...copy, id: 2, key: "shadow", state: "wanted", episodes: { missing: 1 }, upgrade: { below: 2 }, control: { ...copy.control, mode: "shadow" } },
  ] as never} />);
  expect((await screen.findAllByRole("link", { name: "See in Wanted" })).map(l => l.getAttribute("href"))).toEqual(["/wanted?version=movie-web"]);
  expect(screen.getAllByRole("link", { name: "See in Cutoff unmet" }).map(l => l.getAttribute("href"))).toEqual(["/wanted/cutoff?version=movie-web"]);
});


it("keeps collision labels on Theatarr-managed cards and unique labels plain", () => {
  mockFetch([]);
  renderPage(<CopyCards versions={withDistinctLabels([
    { ...copy, key: "anime-tv", label: "Anime" },
    { ...copy, id: 2, key: "anime-web", label: "Anime" },
    { ...copy, id: 3 },
  ])} />);
  expect(screen.getByRole("heading", { name: "Anime · anime-tv" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Anime · anime-web" })).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Anime" })).toBeNull();
  expect(screen.getByRole("heading", { name: "WEB" })).toBeTruthy();
});

it("says Preview only once on a Shadow library's card, keeping a server's own hold reason", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files"] } }]);
  const gs = { grab: true, router_enabled: true, router_forced_off: false, library_enabled: true, will_grab: false };
  renderPage(<CopyCards versions={[
    { ...copy, control: { mode: "shadow", can_edit: true, can_search: false, live_releases: false, search_reason: "shadow mode", grab_state: { ...gs, mode: "shadow" } } },
    { ...copy, id: 2, key: "movie-remux", label: "Remux", control: { mode: "active", can_edit: true, can_search: false, live_releases: true, search_reason: "indexers cooling down", grab_state: { ...gs, mode: "active" } } },
  ] as never} />);
  await screen.findAllByTestId("copy-card");
  expect(document.body.textContent?.match(/Preview only/g)).toHaveLength(1);
  expect(screen.getByTestId("control-note").textContent).toContain("Downloads off (indexers cooling down)");
});
