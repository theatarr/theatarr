// @vitest-environment jsdom
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { QueueTab } from "./queue-tab";

const me = { id: 9, username: "manager", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const base = { title: { id: 1, name: "Alpha", kind: "movie", poster: "" }, version: "movie-web", step: "downloading", steps: [], progress: 40, created_at: "2026-10-30T00:00:00Z", updated_at: "2026-10-30T00:00:00Z" };
const rows = [
  { ...base, id: 7, state: "downloading", words: "Downloading (40%)", release: "Alpha.2026.2160p.WEB-DL", info_hash: "aa", provider_name: "Real-Debrid", cached: true, size: 2_000_000_000, quality: "WEBDL-2160p" },
  { ...base, id: 8, title: { id: 2, name: "Beta", kind: "movie", poster: "" }, state: "failed", words: "Failed", release: "Beta.2026.1080p", info_hash: "bb" },
  { ...base, id: 9, title: { id: 3, name: "Gamma", kind: "movie", poster: "" }, state: "failed", words: "Failed", release: "Gamma.2026.1080p", info_hash: "cc" },
];

function show(extra: { method: string; path: string; reply: unknown }[] = []) {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: rows.length, now: rows } },
    ...extra,
  ]);
  renderPage(<QueueTab admin show="all" onShow={() => {}} />, "/activity");
  return fetch;
}

afterEach(() => {
  cleanup();
  window.localStorage.clear();
});

it("shows release, provider and size as columns without opening Details", async () => {
  window.localStorage.setItem("theatarr.library.activity-queue.9", JSON.stringify({ order: ["release", "library", "quality", "provider", "size", "progress"], shown: ["release", "library", "quality", "provider", "size", "progress"] }));
  show();
  const row = (await screen.findAllByTestId("download")).find((r) => r.textContent?.includes("Alpha"))!;
  expect(row.querySelector('[data-column="release"]')?.textContent).toBe("Alpha.2026.2160p.WEB-DL");
  expect(row.querySelector('[data-column="provider"]')?.textContent).toContain("Real-Debrid");
  expect(row.querySelector('[data-column="provider"]')?.textContent).toContain("cached");
  expect(row.querySelector('[data-column="size"]')?.textContent).toBe("1.9 GiB");
  expect(screen.queryByTestId("download-open")).toBeNull();
});

it("remembers a hidden column", async () => {
  show();
  const user = userEvent.setup();
  expect(await screen.findByRole("columnheader", { name: /Size/ })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Columns" }));
  const list = screen.getByTestId("column-list");
  await user.click(within(list).getByRole("checkbox", { name: /^Size/ }));
  await user.click(screen.getByRole("button", { name: "Done" }));
  expect(screen.queryByRole("columnheader", { name: /Size/ })).toBeNull();
  cleanup();
  show();
  expect(await screen.findByRole("columnheader", { name: /Provider/ })).toBeTruthy();
  expect(screen.queryByRole("columnheader", { name: /Size/ })).toBeNull();
});

it("removes with blocklist and search again when the box is ticked", async () => {
  const fetch = show([{ method: "POST", path: "/activity/downloads/7/remove", reply: { id: 7, state: "failed", ended: true, message: "Removed" } }]);
  const user = userEvent.setup();
  const row = (await screen.findAllByTestId("download")).find((r) => r.textContent?.includes("Alpha"))!;
  await user.click(within(row).getByRole("button", { name: "Remove" }));
  expect(screen.getByText(/torrent at Real-Debrid stays/)).toBeTruthy();
  await user.click(screen.getByRole("checkbox", { name: "Also blocklist this release and search again" }));
  await user.click(screen.getByRole("button", { name: "Remove, blocklist and search" }));
  await waitFor(() => expect(fetch.calls("POST", "/activity/downloads/7/remove")).toHaveLength(1));
  expect(fetch.calls("POST", "/activity/downloads/7/remove")[0].body).toEqual({ blocklist: true, search: true });
});

it("removes the selected downloads one by one and names the one that was not removed", async () => {
  const fetch = show([
    { method: "POST", path: "/activity/downloads/7/remove", reply: { id: 7, state: "failed", ended: true, message: "Removed" } },
    { method: "POST", path: "/activity/downloads/8/remove", reply: { status: 409, body: { error: "already ended" } } },
    { method: "POST", path: "/activity/downloads/9/remove", reply: { id: 9, state: "failed", ended: false, message: "Dismissed" } },
  ]);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("checkbox", { name: "Select all 3 shown" }));
  expect(screen.getByTestId("selection-line").textContent).toContain("3 selected");
  await user.click(screen.getByRole("button", { name: "Remove selected…" }));
  const dialog = screen.getByTestId("remove-downloads-dialog");
  expect(within(dialog).getByText("Removes 3 downloads from Theatarr's queue")).toBeTruthy();
  expect(within(dialog).getByText(/torrent at the debrid provider stays/)).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Remove 3 downloads" }));
  expect((await within(dialog).findByTestId("confirm-done")).textContent).toBe("Removed 2 of 3. Not removed: Beta (already ended).");
  for (const id of [7, 8, 9]) expect(fetch.calls("POST", `/activity/downloads/${id}/remove`)).toHaveLength(1);
  expect(fetch.calls("POST", "/activity/downloads/7/remove")[0].body).toEqual({ blocklist: false, search: false });
});
