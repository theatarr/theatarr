// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { Seasons } from "./seasons";
import { mockFetch, renderPage } from "@/test/page";

const managed = { can_edit: true, can_search: true, live_releases: true };
const cell = (has: boolean) => ({ state: has ? "cutoff_met" : "wanted", monitored: true, has_file: has });
const episodes = {
  title_id: 1,
  versions: [{ id: 2, key: "tv", label: "TV", monitored: true, control: managed }],
  seasons: [{ season: 1, episodes: 3, aired: 3, versions: { tv: { have: 2, missing: 1, below_cutoff: 0, upgrade_seen: 0, monitored: 3 } } }],
  episodes: [
    { id: 201, season: 1, number: 1, name: "Pilot", monitored: true, versions: { tv: cell(true) } },
    { id: 202, season: 1, number: 2, name: "Second", monitored: true, versions: { tv: cell(true) } },
    { id: 203, season: 1, number: 3, name: "Third", monitored: true, versions: { tv: cell(false) } },
  ],
};
const path = "/library/tv/Show (2020)/Season 01/Show (2020) - S01E01 - Pilot.mkv";
const detail = {
  title_id: 1,
  title_name: "Show",
  episode: episodes.episodes[0],
  versions: [{ key: "tv", label: "TV", state: "cutoff_met", monitored: true, has_file: true, control: managed, file: { link_id: 9, path, quality: "Bluray-1080p", episodes: ["S01E01"] } }],
};
const me = (permissions: string[]) => ({ method: "GET", path: "/auth/me", reply: { id: 1, username: "owner", permissions, csrf_token: "t" } });
const routes = [{ method: "GET", path: "/titles/1/episodes", reply: episodes }, { method: "GET", path: "/titles/1/episodes/201", reply: detail }];

async function openDialog() {
  const user = userEvent.setup();
  renderPage(<Seasons id="1" anime={false} />, "/title/1?episode=201");
  const drawer = await screen.findByRole("dialog");
  // The file's facts are open in the Details tab.
  await within(drawer).findByTestId("episode-file");
  await user.click(within(drawer).getByTestId("change-episodes"));
  const dialog = await screen.findByTestId("change-episodes-dialog");
  return { user, dialog };
}

it("changes a file's episodes from the episode overview, asking before it replaces another file", async () => {
  const conflict = { status: 409, body: { error: "S01E02 already has a file in this library (/library/tv/x.mkv); choose replace to unlink it" } };
  const done = { version: "tv", previous_path: path, path: "/library/tv/new.mkv", previous: ["S01E01"], episodes: ["S01E02", "S01E03"], replaced: ["/library/tv/x.mkv"], message: "The file now holds S01E02, S01E03; the file it replaced is in the trash" };
  const api = mockFetch([me(["admin", "manage", "see_files"]), ...routes, { method: "POST", path: "/links/9/episodes", reply: conflict }, { method: "POST", path: "/links/9/episodes", reply: done }]);
  const { user, dialog } = await openDialog();
  const box = (name: RegExp) => within(dialog).getByRole("checkbox", { name }) as HTMLInputElement;
  await within(dialog).findByRole("checkbox", { name: /S01E03/ });
  expect([box(/S01E01/), box(/S01E02/), box(/S01E03/)].map((b) => b.checked)).toEqual([true, false, false]);
  // Nothing changed yet: nothing to send.
  expect(within(dialog).getByRole("button", { name: "Change episodes" }).hasAttribute("disabled")).toBe(true);
  await user.click(box(/S01E01/));
  expect(within(dialog).getByRole("button", { name: "Change episodes" }).hasAttribute("disabled")).toBe(true);
  await user.click(box(/S01E02/));
  await user.click(box(/S01E03/));
  await user.click(within(dialog).getByRole("button", { name: "Change episodes" }));
  expect(await within(dialog).findByText(/choose replace to unlink it/)).toBeTruthy();
  await user.click(within(dialog).getByTestId("change-episodes-replace"));
  await user.click(within(dialog).getByRole("button", { name: "Change episodes" }));
  expect(await within(dialog).findByText(done.message)).toBeTruthy();
  expect(api.calls("POST", "/links/9/episodes").map((r) => r.body)).toEqual([
    { episode_ids: [202, 203], replace: false },
    { episode_ids: [202, 203], replace: true },
  ]);
});

it("offers Change episodes to admins only", async () => {
  mockFetch([me(["manage", "see_files"]), ...routes]);
  renderPage(<Seasons id="1" anime={false} />, "/title/1?episode=201");
  const drawer = await screen.findByRole("dialog");
  // The file's facts are open in the Details tab.
  await within(drawer).findByTestId("episode-file");
  expect(within(drawer).getByText(path)).toBeTruthy();
  expect(within(drawer).queryByTestId("change-episodes")).toBeNull();
});

it("disables Change episodes, with the reason, for a file taken over from Sonarr or Radarr", async () => {
  const adopted = { ...detail, versions: [{ ...detail.versions[0], file: { ...detail.versions[0].file, no_download: true } }] };
  mockFetch([me(["admin", "manage", "see_files"]), { method: "GET", path: "/titles/1/episodes", reply: episodes }, { method: "GET", path: "/titles/1/episodes/201", reply: adopted }]);
  renderPage(<Seasons id="1" anime={false} />, "/title/1?episode=201");
  const drawer = await screen.findByRole("dialog");
  await within(drawer).findByTestId("episode-file");
  expect(within(drawer).getByTestId("change-episodes").hasAttribute("disabled")).toBe(true);
  expect(within(drawer).getByTestId("change-episodes-why").textContent).toBe("Taken over from Sonarr or Radarr: use Manual import to change its episodes");
});

it("filters the episodes, keeps hidden ticks, and sends only a changed set", async () => {
  const done = { version: "tv", previous_path: path, path: "/library/tv/new.mkv", previous: ["S01E01"], episodes: ["S01E01", "S01E03"], replaced: [], message: "The file now holds S01E01, S01E03" };
  const api = mockFetch([me(["admin", "manage", "see_files"]), ...routes, { method: "POST", path: "/links/9/episodes", reply: done }]);
  const { user, dialog } = await openDialog();
  await within(dialog).findByRole("checkbox", { name: /S01E03/ });
  await user.type(within(dialog).getByRole("textbox", { name: "Filter episodes" }), "third");
  expect(within(dialog).queryByRole("checkbox", { name: /S01E02/ })).toBeNull();
  expect(within(dialog).queryByRole("checkbox", { name: /S01E01/ })).toBeNull();
  await user.click(within(dialog).getByRole("checkbox", { name: /S01E03/ }));
  await user.clear(within(dialog).getByRole("textbox", { name: "Filter episodes" }));
  // S01E01, hidden by the filter, stayed ticked.
  expect((within(dialog).getByRole("checkbox", { name: /S01E01/ }) as HTMLInputElement).checked).toBe(true);
  // Ticking and unticking back to the stored set disables the confirm again.
  await user.click(within(dialog).getByRole("checkbox", { name: /S01E02/ }));
  await user.click(within(dialog).getByRole("checkbox", { name: /S01E02/ }));
  await user.click(within(dialog).getByRole("button", { name: "Change episodes" }));
  expect(await within(dialog).findByText(done.message)).toBeTruthy();
  expect(api.calls("POST", "/links/9/episodes").map((r) => r.body)).toEqual([{ episode_ids: [201, 203], replace: false }]);
});
