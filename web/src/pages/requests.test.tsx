// @vitest-environment jsdom
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { RequestsPage } from "./requests";

const on = { grab: true, router_enabled: true, router_forced_off: false, will_grab: true };

describe("request progress grab gates", () => {
  it.each([{}, { grab: false }, { router_enabled: false }, { router_forced_off: true }])("explains paused requests for %j and links the switches for admins", async (closed) => {
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
  it("asks in the app's own dialog before cancelling your request", async () => {
    const api = mockFetch([
      { method: "GET", path: "/auth/me", reply: { username: "joiner", permissions: ["browse", "request"], csrf_token: "fixture" } },
      { method: "GET", path: "/requests/1", reply: json(detail()) },
      { method: "DELETE", path: "/requests/1", reply: { status: 204 } },
    ]);
    renderPage(<Routes><Route path="/requests/:tab" element={<RequestsPage />} /></Routes>, "/requests/1");
    const ue = userEvent.setup();
    await ue.click(await screen.findByTestId("request-cancel"));
    const d = await screen.findByTestId("request-cancel-confirm");
    expect(d.querySelector("h2")?.textContent).toBe("Cancel your request for Alpha?");
    await ue.click(within(d).getByTestId("confirm-cancel"));
    expect(api.calls("DELETE", "/requests/1")).toHaveLength(0);
    await ue.click(screen.getByTestId("request-cancel"));
    await ue.click(within(await screen.findByTestId("request-cancel-confirm")).getByTestId("confirm-ok"));
    await waitFor(() => expect(api.calls("DELETE", "/requests/1")).toHaveLength(1));
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

  it("goes back to the last page with rows when the list shrinks under a later page", async () => {
    const user = userEvent.setup();
    // Page 2's one request was decided before it was read: offset 50 now has nothing, and the total is 50.
    const fetch = mockFetch([
      { method: "GET", path: "/auth/me", reply: manager },
      { method: "GET", path: "/requests", reply: (r: { search: string }) => new URLSearchParams(r.search).get("offset") === "50"
        ? { items: [], total: 50, counts: { pending: 50 } } : { items: [row(1)], total: 51, counts: { pending: 51 } } },
    ]);
    renderPage(<Routes><Route path="/requests/:tab?" element={<RequestsPage />} /></Routes>, "/requests");
    await screen.findAllByText("Film 1");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(fetch.requests.filter((r) => r.path === "/requests").length).toBeGreaterThanOrEqual(3));
    expect(await screen.findAllByText("Film 1")).not.toHaveLength(0);
    expect(screen.queryByText("Nothing waits for a decision.")).toBeNull();
  });
});

describe("problem reports", () => {
  it("lets a manager switch from the open reports to their own, fixed ones with the reply", async () => {
    const user = userEvent.setup();
    const report = (id: number, status: string, reply = "") => ({ id, title_id: 5, title_kind: "movie", name: `Film ${id}`, episode_id: 0, kind: "picture", kind_label: "Picture", status, reply, mine: id === 2, created_at: "2026-10-30T12:00:00Z", updated_at: "2026-10-30T12:00:00Z" });
    const fetch = mockFetch([
      { method: "GET", path: "/auth/me", reply: { username: "mgr", permissions: ["browse", "request", "manage_requests"], csrf_token: "fixture" } },
      { method: "GET", path: "/reports", reply: (r: { search: string }) => ({ items: new URLSearchParams(r.search).get("mine") ? [report(2, "fixed", "New copy in place")] : [report(1, "open")] }) },
    ]);
    renderPage(<Routes><Route path="/requests/:tab?" element={<RequestsPage />} /></Routes>, "/requests/problems");
    await screen.findAllByText("Film 1");
    expect(screen.getByRole("button", { name: "Open" }).getAttribute("aria-pressed")).toBe("true");
    await user.click(screen.getByRole("button", { name: "My reports" }));
    expect((await screen.findByTestId("problem-reply")).textContent).toBe("New copy in place");
    expect(screen.queryAllByText("Film 1")).toHaveLength(0);
    expect(screen.queryByTestId("problem-fix")).toBeNull();
    expect(fetch.requests.some((r) => r.path === "/reports" && new URLSearchParams(r.search).get("mine") === "1")).toBe(true);
  });

  it("shows a manager the pending and open counts on To decide and Problems, the two parts of the Requests badge", async () => {
    mockFetch([
      { method: "GET", path: "/auth/me", reply: { username: "mgr", permissions: ["browse", "request", "manage_requests"], csrf_token: "fixture" } },
      { method: "GET", path: "/requests/summary", reply: { pending: 3, problems: 2, mine_open: 0, badge: 5 } },
      { method: "GET", path: "/requests", reply: { items: [], counts: {}, total: 0 } },
    ]);
    renderPage(<Routes><Route path="/requests/:tab?" element={<RequestsPage />} /></Routes>, "/requests");
    await waitFor(() => expect(screen.getByRole("tab", { name: /Problems/ }).textContent).toBe("Problems2"));
    expect(screen.getByRole("option", { name: "Problems (2)" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: /To decide/ }).textContent).toBe("To decide3");
    expect(screen.getByRole("option", { name: "To decide (3)" })).toBeTruthy();
  });
});

describe("the approval editor", () => {
  const series = { id: 1, kind: "series", name: "Kappa", anime: false, tmdb_id: 34, status: "pending", mine: false, seasons: [], created_at: "2026-10-30T12:00:00Z", updated_at: "2026-10-30T12:00:00Z", approved_at_creation: false,
    versions: [{ key: "tv", label: "TV", available: false, grab_state: on }], progress: [{ key: "tv", words: "Waiting for approval", approval: "Records the request" }] };
  const setup = () => {
    const api = mockFetch([
      { method: "GET", path: "/auth/me", reply: { username: "admin", permissions: ["admin", "manage_requests"], csrf_token: "fixture" } },
      { method: "GET", path: "/requests/1", reply: json(series) },
      { method: "GET", path: "/add/options", reply: { kind: "series", versions: [{ key: "tv", label: "TV", fits: true, on: false, grab_state: on, searchable: true }], seasons: [{ season: 0, episodes: 1 }, { season: 1, episodes: 8 }, { season: 2, episodes: 8 }] } },
      { method: "POST", path: "/requests/1/approve", reply: json({}) },
    ]);
    renderPage(<Routes><Route path="/requests/:tab" element={<RequestsPage />} /></Routes>, "/requests/1");
    return api;
  };
  const approveBody = async (api: ReturnType<typeof mockFetch>) => {
    await waitFor(() => expect(api.calls("POST", "/requests/1/approve")).toHaveLength(1));
    return api.calls("POST", "/requests/1/approve")[0].body;
  };

  it("cannot approve a series with no season selected; untouched and every season ticked send what they sent", async () => {
    const user = userEvent.setup();
    let api = setup();
    await user.click(await screen.findByTestId("request-edit"));
    const editor = await screen.findByTestId("approve-editor");
    const approve = within(editor).getByRole("button", { name: "Approve these" }) as HTMLButtonElement;
    await within(editor).findByRole("button", { name: "Season 2" });
    expect(approve.disabled).toBe(false);
    await user.click(within(editor).getByRole("button", { name: "Season 1" }));
    await user.click(within(editor).getByRole("button", { name: "Season 2" }));
    expect(approve.disabled).toBe(true);
    expect(within(editor).getByText("Pick at least one season")).toBeTruthy();
    // Every season ticked again: [] (every season), as today.
    await user.click(within(editor).getByRole("button", { name: "Season 1" }));
    await user.click(within(editor).getByRole("button", { name: "Season 2" }));
    expect(within(editor).queryByText("Pick at least one season")).toBeNull();
    await user.click(approve);
    expect(await approveBody(api)).toEqual({ versions: ["tv"], seasons: [] });
    cleanup();

    // Untouched: no seasons sent.
    api = setup();
    await user.click(await screen.findByTestId("request-edit"));
    const again = await screen.findByTestId("approve-editor");
    await within(again).findByRole("button", { name: "Season 2" });
    await user.click(within(again).getByRole("button", { name: "Approve these" }));
    expect(await approveBody(api)).toEqual({ versions: ["tv"] });
  });
});
