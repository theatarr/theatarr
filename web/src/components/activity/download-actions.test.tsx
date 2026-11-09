// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import type { ActivityDownload } from "@/api/activity";
import { mockFetch, renderPage } from "@/test/page";
import { DownloadActions } from "./download-actions";

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
