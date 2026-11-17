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
  seasons: [{ season: 1, episodes: 1, aired: 1, versions: { tv: { have: 1, missing: 0, below_cutoff: 0, upgrade_seen: 0, monitored: 1 } } }],
  episodes: [{ id: 201, season: 1, number: 1, name: "Pilot", monitored: true, versions: { tv: cell(true) } }],
};
const path = "/library/tv/Show (2020)/Season 01/Show (2020) - S01E01E02 - Pilot.mkv";
const detail = (scope: object) => ({
  title_id: 1,
  title_name: "Show",
  episode: episodes.episodes[0],
  versions: [{ key: "tv", label: "TV", state: "cutoff_met", monitored: true, has_file: true, control: managed, file: { link_id: 9, path, quality: "Bluray-1080p", episodes: ["S01E01", "S01E02"], replace_scope: scope } }],
});
const me = { method: "GET", path: "/auth/me", reply: { id: 1, username: "owner", permissions: ["manage", "see_files"], csrf_token: "t" } };

async function drawer() {
  renderPage(<Seasons id="1" anime={false} />, "/title/1?episode=201");
  const d = await screen.findByRole("dialog");
  await within(d).findByTestId("episode-file");
  return d;
}

it("replaces an episode's file with the Problems page's scope, without a report", async () => {
  const scope = { libraries: ["TV", "TV 4K"], episodes: ["S01E01", "S01E02"], pack: true, replace: true };
  const done = { message: "Removed x.mkv; the release is blocklisted in TV and TV 4K. Searching S01E01, S01E02 in TV and TV 4K", search: { started: true, queued: 2, found: 0, versions: ["tv", "tv4k"], skipped: [], message: "" } };
  const api = mockFetch([me, { method: "GET", path: "/titles/1/episodes", reply: episodes }, { method: "GET", path: "/titles/1/episodes/201", reply: detail(scope) }, { method: "POST", path: "/links/9/replace", reply: done }]);
  const user = userEvent.setup();
  const d = await drawer();
  await user.click(within(d).getByTestId("link-replace"));
  const dialog = await screen.findByTestId("replace-confirm");
  expect(within(dialog).getByText(/missing in TV and TV 4K/)).toBeTruthy();
  expect(within(dialog).getByText(/Episodes that lose the file: S01E01, S01E02/)).toBeTruthy();
  expect(within(dialog).getByText(/the whole pack is excluded in TV and TV 4K/)).toBeTruthy();
  expect(within(dialog).queryByText(/report stays open/)).toBeNull();
  await user.click(within(dialog).getByRole("button", { name: "Replace file" }));
  expect(await within(dialog).findByText(done.message)).toBeTruthy();
  expect(api.calls("POST", "/links/9/replace")).toHaveLength(1);
});

it("shows why a file cannot be replaced instead of the action", async () => {
  const scope = { libraries: ["TV"], pack: false, replace: false, why: "TV is not monitored, so no new file would be searched" };
  mockFetch([me, { method: "GET", path: "/titles/1/episodes", reply: episodes }, { method: "GET", path: "/titles/1/episodes/201", reply: detail(scope) }]);
  const d = await drawer();
  expect(within(d).queryByTestId("link-replace")).toBeNull();
  expect(within(d).getByTestId("replace-why").textContent).toBe(`Replace file: ${scope.why}.`);
});
