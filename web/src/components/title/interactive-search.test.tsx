// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, expect, it } from "vitest";
import { mockFetch, renderPage, type Route } from "@/test/page";
import { InteractiveSearchButton } from "./interactive-search";

const ME: Route = { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } };
const control = (live: boolean) => ({ mode: live ? "active" : "shadow", can_edit: true, can_search: live, live_releases: live });
const TITLE: Route = {
  method: "GET",
  path: "/titles/7",
  reply: {
    id: 7, name: "Alpha", year: 2020, kind: "movie",
    versions: [
      { id: 1, key: "movie-web", label: "WEB", control: control(true) },
      { id: 2, key: "movie-remux", label: "Remux", control: control(true) },
      { id: 3, key: "movie-arr", label: "Arr", control: control(false) },
    ],
  },
};

const accepted = {
  rank: 1, release: "Alpha.2020.2160p.WEB-DL-GRP", info_hash: "aaa", accepted: true, score: 120, quality: "WEBDL-2160p", revision: 2,
  indexer: "Nyaa", seeders: 120, leechers: 8, cached_on: ["rd"], languages: ["English"], published: new Date(Date.now() - 5 * 3_600_000).toISOString(),
  formats: [{ name: "HDR", score: 120 }],
};
const rejected = { rank: 2, release: "Alpha.2020.720p.WEB-OTHER", info_hash: "bbb", accepted: false, score: -10, quality: "WEBDL-720p", rejections: [{ reason: "quality", message: "WEBDL-720p is not wanted" }] };
const answer = (version = "movie-web", more: object = {}) => ({
  title_id: 7, version, versions: ["movie-web", "movie-remux"], mode: "active", live_allowed: true, source: "live", results: 2, accepted: 1,
  candidates: [accepted, rejected], rejections: { quality: 1 }, outcomes: { aaa: { outcome: "failed", provider: "rd", reason: "RD status error" } }, grab: true, ...more,
});
const sent = { version: "movie-web", release: accepted.release, info_hash: "aaa", accepted: true, overridden: false, message: "Sent to the router" };

beforeEach(() => window.localStorage.clear());

const open = () => fireEvent.click(screen.getByTestId("interactive-search-open"));
const table = () => screen.findByTestId("releases-table");
const grabIn = async (n: number) => within(await table()).getAllByTestId("release-grab")[n];

it("searches once on opening, says it is waiting, then shows the columns", async () => {
  let reply: (v: unknown) => void = () => {};
  const api = mockFetch([ME, TITLE, { method: "GET", path: "/titles/7/releases", reply: () => new Promise((r) => { reply = r; }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  expect((await screen.findByTestId("interactive-waiting")).textContent).toContain("Searching the indexers for WEB…");
  expect(screen.getByTestId("interactive-subject").textContent).toBe("Alpha (2020)");
  reply(answer());
  const t = await table();
  expect(api.calls("GET", "/titles/7/releases").map((g) => g.search)).toEqual(["?version=movie-web&live=1"]);
  const row = within(t).getAllByRole("row").find((r) => r.dataset.releaseHash === "aaa")!;
  expect(row.textContent).toContain("5 h");
  expect(row.textContent).toContain("Nyaa");
  expect(within(row).getByTestId("release-peers").textContent).toBe("120 / 8");
  expect(within(row).getByTestId("release-cached").textContent).toBe("RD");
  expect(row.textContent).toContain("English");
  expect(within(row).getByTestId("release-proper")).toBeTruthy();
  expect(within(row).getByTestId("release-score").textContent).toBe("+120");
  expect(within(row).getByTestId("release-score").getAttribute("title")).toBe("HDR +120");
  expect(within(row).getByTestId("release-outcome").textContent).toBe("Failed · RD status error");
  expect(screen.queryByTestId("interactive-waiting")).toBeNull();
});

it("grabs an accepted release at once, without asking", async () => {
  const api = mockFetch([ME, TITLE, { method: "GET", path: "/titles/7/releases", reply: answer() }, { method: "POST", path: "/titles/7/releases/grab", reply: sent }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  fireEvent.click(await grabIn(0));
  await within(await table()).findByTestId("release-sent");
  expect(screen.queryByTestId("grab-dialog")).toBeNull();
  expect(api.calls("POST", "/titles/7/releases/grab").map((g) => g.body)).toEqual([{ version: "movie-web", info_hash: "aaa", override: false }]);
});

it("asks first for a rejected release, and once for one the server rejects at grab time", async () => {
  const api = mockFetch([
    ME, TITLE,
    { method: "GET", path: "/titles/7/releases", reply: answer() },
    { method: "POST", path: "/titles/7/releases/grab", reply: { ...sent, info_hash: "bbb", accepted: false, overridden: true } },
    { method: "POST", path: "/titles/7/releases/grab", reply: { status: 409, body: { error: "rejected for movie-web", detail: ["blocklisted since the search"] } } },
    { method: "POST", path: "/titles/7/releases/grab", reply: { ...sent, overridden: true } },
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const grab = await grabIn(1);
  expect(grab.textContent).toContain("Grab…");
  fireEvent.click(grab);
  let dialog = await screen.findByTestId("grab-dialog");
  expect(dialog.textContent).toContain("WEBDL-720p is not wanted");
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(0);
  fireEvent.click(within(dialog).getByText("Grab anyway"));
  await screen.findByTestId("confirm-done");
  fireEvent.click(screen.getByTestId("confirm-cancel"));
  await waitFor(() => expect(screen.queryByTestId("grab-dialog")).toBeNull());
  // The accepted one: sent at once, refused late, then one confirmation overrides.
  fireEvent.click(await grabIn(0));
  dialog = await screen.findByTestId("grab-dialog");
  expect(dialog.textContent).toContain("blocklisted since the search");
  fireEvent.click(within(dialog).getByText("Grab anyway"));
  await screen.findByTestId("confirm-done");
  expect(api.calls("POST", "/titles/7/releases/grab").map((g) => g.body)).toEqual([
    { version: "movie-web", info_hash: "bbb", override: true },
    { version: "movie-web", info_hash: "aaa", override: false },
    { version: "movie-web", info_hash: "aaa", override: true },
  ]);
});

it("the grab dialog stays until its grab settles", async () => {
  let settle: (v: unknown) => void = () => {};
  const api = mockFetch([
    ME, TITLE,
    { method: "GET", path: "/titles/7/releases", reply: answer() },
    { method: "POST", path: "/titles/7/releases/grab", reply: () => new Promise((r) => { settle = r; }) },
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  fireEvent.click(await grabIn(1));
  fireEvent.click(within(await screen.findByTestId("grab-dialog")).getByTestId("confirm-ok"));
  await waitFor(() => expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(1));
  fireEvent.click(screen.getByTestId("confirm-cancel"));
  expect(screen.getByTestId("grab-dialog")).toBeTruthy();
  settle({ ...sent, info_hash: "bbb" });
  await screen.findByTestId("confirm-done");
  fireEvent.click(screen.getByTestId("confirm-cancel"));
  await waitFor(() => expect(screen.queryByTestId("grab-dialog")).toBeNull());
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(1);
});

it("a hidden column stays hidden after closing and opening again", async () => {
  mockFetch([ME, TITLE, { method: "GET", path: "/titles/7/releases", reply: answer() }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  expect(within(await table()).getByRole("columnheader", { name: "Indexer" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Columns" }));
  const picker = await screen.findByRole("dialog", { name: "Release columns" });
  fireEvent.click(within(picker).getByRole("checkbox", { name: /^Indexer/ }));
  fireEvent.click(within(picker).getByRole("button", { name: "Done" }));
  expect(within(await table()).queryByRole("columnheader", { name: "Indexer" })).toBeNull();
  fireEvent.click(screen.getByTestId("interactive-close"));
  await waitFor(() => expect(screen.queryByTestId("interactive-search")).toBeNull());
  open();
  await waitFor(async () => expect(within(await table()).getByRole("columnheader", { name: "Title" })).toBeTruthy());
  expect(within(await table()).queryByRole("columnheader", { name: "Indexer" })).toBeNull();
  expect(JSON.parse(window.localStorage.getItem("theatarr.library.release-columns.1") ?? "{}").shown).not.toContain("indexer");
});

it("a refused live search shows why and offers no Grab", async () => {
  mockFetch([ME, TITLE, { method: "GET", path: "/titles/7/releases", reply: answer("movie-web", { source: "journal", grab: false, message: "Live search failed: no indexer answered; showing the last stored decision" }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const box = await screen.findByTestId("interactive-fallback");
  expect(box.textContent).toContain("Live search failed: no indexer answered");
  expect(box.textContent).toContain("cannot be grabbed");
  await table();
  expect(screen.queryByTestId("release-grab")).toBeNull();
});

it("searches a season or an episode, each library once per opening, and again on reopening", async () => {
  const api = mockFetch([
    ME, TITLE,
    { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => answer(new URLSearchParams(r.search).get("version") ?? "") },
    { method: "GET", path: "/titles/7/episodes/55/releases", reply: answer() },
  ]);
  const { unmount } = renderPage(
    <>
      <InteractiveSearchButton titleId={7} version="movie-web" season={2} />
    </>,
  );
  open();
  await table();
  fireEvent.click(screen.getByRole("tab", { name: /movie-remux/i }));
  await waitFor(() => expect(api.calls("GET", "/titles/7/releases")).toHaveLength(2));
  await table();
  fireEvent.click(screen.getByRole("tab", { name: /movie-web/i }));
  await table();
  expect(api.calls("GET", "/titles/7/releases").map((g) => g.search)).toEqual(["?version=movie-web&live=1&season=2", "?version=movie-remux&live=1&season=2"]);
  expect(screen.queryByRole("tab", { name: /movie-arr/i })).toBeNull();
  fireEvent.click(screen.getByTestId("interactive-close"));
  open();
  await waitFor(() => expect(api.calls("GET", "/titles/7/releases")).toHaveLength(3));
  unmount();
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" episode={{ id: 55, code: "S02E05" }} />);
  open();
  await table();
  expect(screen.getByTestId("interactive-subject").textContent).toContain("S02E05");
  expect(api.calls("GET", "/titles/7/episodes/55/releases").map((g) => g.search)).toEqual(["?version=movie-web&live=1"]);
});
