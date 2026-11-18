// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import type { ActivityBlocked, ActivityDownload, ActivityEvent } from "@/api/activity";
import { mockFetch, renderPage } from "@/test/page";
import { DownloadActions, HistoryRetry, UnblockAction } from "./download-actions";

it("lets managers dismiss a failed download while keeping its history and provider torrent", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { username: "manager", permissions: ["manage", "see_files"], csrf_token: "test" } },
    { method: "POST", path: "/activity/downloads/7/remove", reply: { id: 7, state: "failed", ended: false, message: "Dismissed; history kept" } },
  ]);
  const d = { id: 7, title: { id: 1, name: "Alpha", kind: "movie", poster: "" }, version: "web", state: "failed", release: "Failed release", info_hash: "abc" } as ActivityDownload;
  renderPage(<DownloadActions d={d} />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Dismiss" }));
  expect(await screen.findByText(/failure stays in history/)).toBeTruthy();
  expect(screen.getByText(/torrent at the debrid provider stays/)).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Dismiss download" }));
  await waitFor(() => expect(fetch.calls("POST", "/activity/downloads/7/remove")).toHaveLength(1));
  expect(fetch.calls("POST", "/activity/downloads/7/remove")[0].body).toEqual({ blocklist: false, search: false });
  expect(await screen.findByText("Dismissed; history kept")).toBeTruthy();
});

const ME = { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } };
const failed = { id: "dl:40:fail", version: "movie-web", release: "Alpha.2020.2160p.WEB-DL-GRP", title: { id: 7, name: "Alpha" } } as unknown as ActivityEvent;

it("a retry the server refuses for the library's blocklist asks once, then sends it with unblock", async () => {
  const api = mockFetch([
    ME,
    { method: "POST", path: "/activity/downloads/40/retry", reply: { status: 409, body: { error: "This release is on WEB's blocklist: Stalled at RD", blocked: { library: "WEB", reason: "Stalled at RD" } } } },
    { method: "POST", path: "/activity/downloads/40/retry", reply: { id: 40, version: "movie-web", message: "Sent to the router again" } },
  ]);
  renderPage(<HistoryRetry e={failed} />);
  fireEvent.click(await screen.findByTestId("history-retry"));
  let dialog = await screen.findByTestId("retry-download-dialog");
  expect(dialog.textContent).toContain("a blocklisted one asks first");
  fireEvent.click(within(dialog).getByText("Retry"));
  dialog = await screen.findByTestId("retry-download-dialog");
  await waitFor(() => expect(dialog.textContent).toContain("This release is on WEB's blocklist: Stalled at RD. Retry it anyway? It is taken off the blocklist."));
  expect(screen.queryByTestId("confirm-error")).toBeNull();
  fireEvent.click(within(dialog).getByText("Retry anyway"));
  await screen.findByTestId("confirm-done");
  expect(api.calls("POST", "/activity/downloads/40/retry").map((c) => c.body)).toEqual([undefined, { unblock: true }]);
});

it("a household account sees no Retry", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 2, permissions: ["browse"] } }]);
  renderPage(<HistoryRetry e={failed} />);
  await new Promise((r) => setTimeout(r, 50));
  expect(screen.queryByTestId("history-retry")).toBeNull();
});

it("Unblock's search box is off with the reason while the library cannot search, and the unblock sends no search", async () => {
  const api = mockFetch([
    ME,
    { method: "POST", path: "/activity/blocked/unblock", reply: { version: "movie-web", info_hash: "abc", release: "Alpha.BAD", message: "Unblocked: the release may be grabbed again for 4K WEB" } },
  ]);
  const b = { version: "movie-web", info_hash: "abc", release: "Alpha.BAD", title: { id: 7, name: "Alpha" }, search_reason: "Paused: no automatic search" } as unknown as ActivityBlocked;
  renderPage(<UnblockAction b={b} />);
  fireEvent.click(await screen.findByTestId("blocked-unblock"));
  const dialog = await screen.findByTestId("unblock-dialog");
  const box = within(dialog).getByTestId("unblock-search") as HTMLInputElement;
  expect(box.disabled).toBe(true);
  expect(within(dialog).getByTestId("unblock-search-reason").textContent).toBe("Paused: no automatic search");
  expect(dialog.textContent).not.toContain("starts now");
  fireEvent.click(within(dialog).getByRole("button", { name: "Unblock" }));
  await waitFor(() => expect(api.calls("POST", "/activity/blocked/unblock")).toHaveLength(1));
  expect(api.calls("POST", "/activity/blocked/unblock")[0].body).toMatchObject({ search: false });
});

it("Unblock's search box can be ticked while the library can search", async () => {
  mockFetch([ME]);
  const b = { version: "movie-web", info_hash: "abc", release: "Alpha.BAD", title: { id: 7, name: "Alpha" } } as unknown as ActivityBlocked;
  renderPage(<UnblockAction b={b} />);
  fireEvent.click(await screen.findByTestId("blocked-unblock"));
  const dialog = await screen.findByTestId("unblock-dialog");
  const box = within(dialog).getByTestId("unblock-search") as HTMLInputElement;
  expect(box.disabled).toBe(false);
  fireEvent.click(box);
  await waitFor(() => expect(dialog.textContent).toContain("starts now"));
  expect(within(dialog).queryByTestId("unblock-search-reason")).toBeNull();
});
