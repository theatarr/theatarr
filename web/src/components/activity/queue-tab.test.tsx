// @vitest-environment jsdom
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { focusManager } from "@tanstack/react-query";
import { afterEach, expect, it } from "vitest";
import { Toaster } from "@/components/toaster";
import { mockFetch, renderPage } from "@/test/page";
import { QueueTab } from "./queue-tab";

const me = { id: 9, username: "manager", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const base = { title: { id: 1, name: "Alpha", kind: "movie", poster: "" }, version: "movie-web", step: "downloading", steps: [], progress: 40, created_at: "2026-10-30T00:00:00Z", updated_at: "2026-10-30T00:00:00Z" };
const rows = [
  { ...base, id: 7, state: "downloading", words: "Downloading (40%)", release: "Alpha.2026.2160p.WEB-DL", info_hash: "aa", provider_name: "Real-Debrid", cached: true, size: 2_000_000_000, quality: "WEBDL-2160p" },
  { ...base, id: 8, title: { id: 2, name: "Beta", kind: "movie", poster: "" }, state: "failed", words: "Failed", detail: "Download failed", error: "miss on every provider", release: "Beta.2026.1080p", info_hash: "bb" },
  { ...base, id: 9, title: { id: 3, name: "Gamma", kind: "movie", poster: "" }, state: "failed", words: "Failed", release: "Gamma.2026.1080p", info_hash: "cc" },
];

function show(extra: { method: string; path: string; reply: unknown }[] = [], path = "/activity") {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: rows.length, now: rows } },
    ...extra,
  ]);
  renderPage(
    <>
      <QueueTab admin show="all" onShow={() => {}} />
      <Toaster />
    </>,
    path,
  );
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
  expect(await screen.findByText("Removed 2 of 3. Not removed: Beta (already ended).")).toBeTruthy();
  for (const id of [7, 8, 9]) expect(fetch.calls("POST", `/activity/downloads/${id}/remove`)).toHaveLength(1);
  expect(fetch.calls("POST", "/activity/downloads/7/remove")[0].body).toEqual({ blocklist: false, search: false });
});

const downloading = (id: number, name: string) => ({ ...base, id, title: { id, name, kind: "movie", poster: "" }, state: "downloading", words: "Downloading", release: `${name}.2160p`, info_hash: `h${id}` });

/** The Queue (filter show) with the toaster; now() is what each GET /activity answers. */
function showLive(now: () => unknown[], showFilter: "all" | "downloading", extra: { method: string; path: string; reply: unknown }[]) {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: () => ({ state: "now", admin: true, total: now().length, now: now() }) },
    ...extra,
  ]);
  renderPage(
    <>
      <QueueTab admin show={showFilter} onShow={() => {}} />
      <Toaster />
    </>,
    "/activity",
  );
  return fetch;
}

const refetch = () => {
  focusManager.setFocused(false);
  focusManager.setFocused(true);
};

it("acts only on the chosen rows still shown when Remove selected is confirmed", async () => {
  let now: unknown[] = [downloading(21, "Delta"), downloading(22, "Echo")];
  const fetch = showLive(() => now, "downloading", [{ method: "POST", path: "/activity/downloads/21/remove", reply: { id: 21, state: "downloading", ended: true, blocklisted: false, message: "Removed" } }, { method: "POST", path: "/activity/downloads/22/remove", reply: { id: 22, state: "downloading", ended: true, blocklisted: false, message: "Removed" } }]);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("checkbox", { name: "Select all 2 shown" }));
  await user.click(screen.getByRole("button", { name: "Remove selected…" }));
  expect(screen.getByRole("button", { name: "Remove 2 downloads" })).toBeTruthy();
  // A refresh moves Echo out of Downloading while the dialog is open.
  now = [downloading(21, "Delta"), { ...downloading(22, "Echo"), state: "ready", words: "Download complete" }];
  refetch();
  const confirm = await screen.findByRole("button", { name: "Remove 1 download" });
  await user.click(confirm);
  expect(await screen.findByText("Removed 1 download.")).toBeTruthy();
  expect(fetch.calls("POST", "/activity/downloads/21/remove")).toHaveLength(1);
  expect(fetch.calls("POST", "/activity/downloads/22/remove")).toHaveLength(0);
});

it("keeps the bulk result in a toast when the removal empties the list, with the server's reason once when no search started", async () => {
  let now: unknown[] = [downloading(31, "Foxtrot"), downloading(32, "Golf")];
  const answer = (id: number) => () => {
    now = now.filter((d) => (d as { id: number }).id !== id);
    return { id, state: "downloading", ended: true, blocklisted: true, message: "Removed", search: { started: false, versions: [], skipped: [], queued: 0, found: 0, message: "Nothing searched: Pause is on (movie-web)" } };
  };
  const fetch = showLive(() => now, "all", [
    { method: "POST", path: "/activity/downloads/31/remove", reply: answer(31) },
    { method: "POST", path: "/activity/downloads/32/remove", reply: answer(32) },
  ]);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("checkbox", { name: "Select all 2 shown" }));
  await user.click(screen.getByRole("button", { name: "Remove selected…" }));
  await user.click(screen.getByRole("checkbox", { name: "Also blocklist these releases and search again" }));
  await user.click(screen.getByRole("button", { name: "Remove 2 downloads" }));
  expect(await screen.findByText("Removed 2 downloads. Nothing searched: Pause is on (movie-web)")).toBeTruthy();
  expect(await screen.findByText("Nothing is downloading right now.")).toBeTruthy();
  expect(fetch.calls("POST", "/activity/downloads/31/remove")[0].body).toEqual({ blocklist: true, search: true });
});

it("names a failed download's reason and release, and says it was not added", async () => {
  show();
  const row = (await screen.findAllByTestId("download")).find((r) => r.textContent?.includes("Beta"))!;
  expect(within(row).getByTestId("stage").textContent).toBe("Failed");
  expect(within(row).getByTestId("words").textContent).toBe("miss on every provider");
  expect(within(row).queryByText("Download failed")).toBeNull();
  expect(within(row).getByTestId("failed-release").textContent).toBe("Beta.2026.1080p");
  expect(row.querySelector('[data-column="provider"]')?.textContent).toBe("Not added");
  // A download in progress keeps its own words and shows no release line under the title.
  const alpha = (await screen.findAllByTestId("download")).find((r) => r.textContent?.includes("Alpha"))!;
  expect(within(alpha).queryByTestId("failed-release")).toBeNull();
});

it("opens the download ?open= names, and closing it drops the parameter", async () => {
  show([{ method: "GET", path: "/activity/downloads/8/attempts", reply: { attempts: [] } }], "/activity?open=8");
  const panel = await screen.findByTestId("download-open");
  expect(within(panel).getByText("Beta.2026.1080p")).toBeTruthy();
  expect(screen.getAllByTestId("download-open")).toHaveLength(1);
  const user = userEvent.setup();
  const row = screen.getAllByTestId("download").find((r) => r.textContent?.includes("Beta"))!;
  await user.click(within(row).getByTestId("download-details"));
  await waitFor(() => expect(screen.queryByTestId("download-open")).toBeNull());
});

it("says when the download ?open= names has left the queue", async () => {
  show([], "/activity?open=99");
  expect((await screen.findByTestId("open-missing")).textContent).toContain("Download 99 is no longer in the queue");
});

it("keeps a not-started row's own words, with no reason or release line", async () => {
  const notStarted = { ...base, id: 40, title: { id: 4, name: "Delta", kind: "movie", poster: "" }, state: "failed", not_started: true, words: "Not started", detail: "Downloads were off when it was due to start", error: "downloads were off", release: "Delta.2026.1080p", info_hash: "dd" };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: 1, now: [notStarted] } },
  ]);
  renderPage(<QueueTab admin show="all" onShow={() => {}} />, "/activity");
  const row = await screen.findByTestId("download");
  expect(within(row).getByTestId("words").textContent).toBe("Downloads were off when it was due to start");
  expect(within(row).queryByTestId("failed-release")).toBeNull();
});
