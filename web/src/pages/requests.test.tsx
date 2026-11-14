// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { RequestsPage } from "./requests";

const on = { mode: "active", grab: true, router_enabled: true, router_forced_off: false, will_grab: true };

describe("request progress grab gates", () => {
  it.each([{ mode: "shadow" }, { grab: false }, { router_enabled: false }, { router_forced_off: true }])("explains paused requests for %j and links the switches for admins", async (closed) => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { username: "admin", permissions: ["admin", "manage_requests"], csrf_token: "fixture" } },
      { method: "GET", path: "/requests/1", reply: json({ id: 1, kind: "movie", name: "Alpha", anime: false, title_id: 12, tmdb_id: 34, status: "approved", mine: true, seasons: [], created_at: "2026-10-30T12:00:00Z", updated_at: "2026-10-30T12:00:00Z", approved_at_creation: true,
        versions: [{ key: "web", label: "WEB", available: false, grab_state: {...on,...closed,will_grab:false} }], progress: [{ key: "web", words: "Waiting for a copy", approval: "Records the request" }] }) },
      { method: "GET", path: "/add/options", reply: { kind: "movie", versions: [
        { key: "web", label: "WEB", fits: true, on: true, grab_state: { ...on, ...closed } },
        { key: "unrequested", label: "Other", fits: true, on: true, grab_state: on },
      ], seasons: [] } },
    ]);
    renderPage(<Routes><Route path="/requests/:tab" element={<RequestsPage />} /></Routes>, "/requests/1");
    const timeline = await screen.findByTestId("request-timeline");
    await waitFor(() => expect(within(timeline).getByText("Not being fetched · Now")).toBeTruthy());
    expect(timeline.textContent).toContain("downloads are off");
    expect(within(timeline).queryByText("Getting ready · Now")).toBeNull();
    await waitFor(() => expect(within(timeline).queryByText("Downloads on")).toBeNull());
    await userEvent.click(await within(timeline).findByRole("button", { name: "Why?" }));
    expect(within(timeline).getAllByRole("link").some((link) => link.getAttribute("href")?.startsWith("/admin/settings"))).toBe(true);
    // The library chip is a dense row: its reasons open from a small button, with the settings links for admins.
    const chip = document.querySelector<HTMLElement>('[data-vkey="web"]')!;
    await userEvent.click(within(chip).getByRole("button", { name: /^Why .+\?$/ }));
    const list = await screen.findByRole("dialog");
    expect(within(list).getByTestId("state-why")).toBeTruthy();
    await waitFor(() => expect(within(list).getAllByRole("link").every((link) => link.getAttribute("href")?.startsWith("/admin/settings"))).toBe(true));
  });
});

describe("a request the user joined", () => {
  const detail = (user?: { id: number; username: string; display_name: string }) => ({ id: 1, kind: "movie", name: "Alpha", anime: false, title_id: 12, tmdb_id: 34, status: "pending", mine: true, seasons: [], created_at: "2026-10-30T12:00:00Z", updated_at: "2026-10-30T12:00:00Z", approved_at_creation: false,
    ...(user ? { user } : {}), versions: [{ key: "web", label: "WEB", available: false, grab_state: on }], progress: [{ key: "web", words: "Waiting for approval", approval: "Records the request" }] });
  const show = (reply: object) => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { username: "joiner", permissions: ["browse", "request"], csrf_token: "fixture" } },
      { method: "GET", path: "/requests/1", reply: json(reply) },
    ]);
    renderPage(<Routes><Route path="/requests/:tab" element={<RequestsPage />} /></Routes>, "/requests/1");
  };
  it("says someone else asked first without naming them (the server sends no other requester)", async () => {
    show(detail());
    const timeline = await screen.findByTestId("request-timeline");
    expect(timeline.textContent).toContain("Someone else asked first; you joined");
    expect(timeline.textContent).not.toMatch(/\bBy /);
  });
  it("still names the requester when the server sends one", async () => {
    show(detail({ id: 9, username: "joiner", display_name: "Joiner" }));
    const timeline = await screen.findByTestId("request-timeline");
    expect(timeline.textContent).toContain("By Joiner");
    expect(timeline.textContent).not.toContain("Someone else");
  });
});

it("keeps each copy's progress under a failed request's plain failure words", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { username: "admin", permissions: ["admin", "manage_requests", "see_files"], csrf_token: "fixture" } },
    { method: "GET", path: "/requests/1", reply: json({ id: 1, kind: "movie", name: "Alpha", anime: false, title_id: 12, tmdb_id: 34, status: "failed", mine: false, seasons: [], created_at: "2026-10-30T12:00:00Z", updated_at: "2026-10-30T12:00:00Z", decided_at: "2026-10-30T12:00:00Z", approved_at_creation: false,
      versions: [{ key: "web", label: "WEB", available: false, grab_state: { ...on, will_grab: false, grab: false } }], progress: [{ key: "web", words: "Download failed", approval: "Records the request", updated_at: "2026-10-30T13:00:00Z" }] }) },
  ]);
  renderPage(<Routes><Route path="/requests/:tab" element={<RequestsPage />} /></Routes>, "/requests/1");
  const timeline = await screen.findByTestId("request-timeline");
  expect(within(timeline).getByText("Did not work")).toBeTruthy();
  expect(timeline.textContent).toContain("This did not work. A manager can look at it.");
  expect(timeline.textContent).not.toContain("downloads are off");
  expect(within(timeline).getByText("Download failed")).toBeTruthy();
});

describe("the request queue", () => {
  const manager = { username: "admin", permissions: ["admin", "manage_requests"], csrf_token: "fixture" };
  const row = (id: number) => ({ id, kind: "movie", name: `Film ${id}`, anime: false, tmdb_id: id, status: "pending", mine: false, seasons: [], versions: [], created_at: "2026-10-30T12:00:00Z", updated_at: "2026-10-30T12:00:00Z" });
  it("opens All with Open after To decide, and pages past the first 50", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch([
      { method: "GET", path: "/auth/me", reply: manager },
      { method: "GET", path: "/requests", reply: (r: { search: string }) => ({ items: [row(new URLSearchParams(r.search).get("offset") === "50" ? 51 : 1)], total: 120, counts: { pending: 120 } }) },
    ]);
    renderPage(<Routes><Route path="/requests/:tab?" element={<RequestsPage />} /></Routes>, "/requests");
    await screen.findAllByText("Film 1");
    await user.click(screen.getByRole("tab", { name: "All" }));
    await waitFor(() => expect(screen.getByRole("button", { name: /^Open/ }).getAttribute("aria-pressed")).toBe("true"));
    expect(screen.getByTestId("pager-range").textContent).toBe("1–50 of 120");
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findAllByText("Film 51")).not.toHaveLength(0);
    expect(fetch.requests.some((r) => r.path === "/requests" && new URLSearchParams(r.search).get("offset") === "50")).toBe(true);
  });
});
