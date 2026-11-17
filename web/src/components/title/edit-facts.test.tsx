// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { Seasons } from "./seasons";
import { mockFetch, renderPage } from "@/test/page";

const managed = { can_edit: true, can_search: true, live_releases: true };
const episodes = {
  title_id: 1,
  versions: [{ id: 2, key: "tv", label: "TV", monitored: true, control: managed }],
  seasons: [{ season: 1, episodes: 1, aired: 1, versions: { tv: { have: 1, missing: 0, below_cutoff: 0, upgrade_seen: 0, monitored: 1 } } }],
  episodes: [{ id: 201, season: 1, number: 1, name: "Pilot", monitored: true, versions: { tv: { state: "filled", monitored: true, has_file: true } } }],
};
const path = "/library/tv/Show (2020)/Season 01/Show (2020) - S01E01 - Pilot.mkv";
const detail = {
  title_id: 1,
  title_name: "Show",
  episode: episodes.episodes[0],
  versions: [{ key: "tv", label: "TV", state: "filled", monitored: true, has_file: true, control: managed, file: { link_id: 9, path, quality: "WEBDL-1080p", episodes: ["S01E01"] } }],
};
const facts = { link_id: 9, path, quality: "WEBDL-1080p", revision: 1, languages: ["English"], release_group: "GRP", quality_choices: ["WEBDL-1080p", "Bluray-1080p"], language_choices: ["English", "French"] };
const routes = [{ method: "GET", path: "/titles/1/episodes", reply: episodes }, { method: "GET", path: "/titles/1/episodes/201", reply: detail }];
const me = (permissions: string[]) => ({ method: "GET", path: "/auth/me", reply: { id: 1, username: "owner", permissions, csrf_token: "t" } });

it("corrects a file's facts from the episode overview, prefilled with what is stored", async () => {
  const done = { message: "Saved: WEBDL-1080p → Bluray-1080p Proper. Score: TV 0.", scores: [{ library: "TV", score: 0 }] };
  const api = mockFetch([me(["manage", "see_files"]), ...routes, { method: "GET", path: "/links/9/facts", reply: facts }, { method: "PATCH", path: "/links/9/facts", reply: done }]);
  const user = userEvent.setup();
  renderPage(<Seasons id="1" anime={false} />, "/title/1?episode=201");
  const drawer = await screen.findByRole("dialog");
  await within(drawer).findByTestId("episode-file");
  await user.click(within(drawer).getByTestId("link-edit-facts"));
  const dialog = await screen.findByTestId("edit-facts-dialog");
  const quality = (await within(dialog).findByTestId("edit-facts-quality")) as HTMLSelectElement;
  expect(quality.value).toBe("WEBDL-1080p");
  expect((within(dialog).getByTestId("edit-facts-group") as HTMLInputElement).value).toBe("GRP");
  await user.selectOptions(quality, "Bluray-1080p");
  await user.selectOptions(within(dialog).getByTestId("edit-facts-revision"), "2");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
  expect(await within(dialog).findByText(done.message)).toBeTruthy();
  expect(api.calls("PATCH", "/links/9/facts").map((r) => r.body)).toEqual([{ quality: "Bluray-1080p", revision: 2, languages: ["English"], release_group: "GRP" }]);
});

it("offers Edit file facts to managers only", async () => {
  mockFetch([me(["see_files"]), ...routes]);
  renderPage(<Seasons id="1" anime={false} />, "/title/1?episode=201");
  const drawer = await screen.findByRole("dialog");
  await within(drawer).findByTestId("episode-file");
  expect(within(drawer).queryByTestId("link-edit-facts")).toBeNull();
});
