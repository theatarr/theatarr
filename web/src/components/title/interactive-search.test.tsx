// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, expect, it } from "vitest";
import { mockFetch, renderPage, type Route } from "@/test/page";
import { InteractiveSearchButton } from "./interactive-search";
import { LibraryNamesProvider } from "@/lib/library-names";

const ME: Route = { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } };
const control = (live: boolean) => ({ can_edit: true, can_search: live, live_releases: live });
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

// The title's current copies (the removal scope): a removed copy is still in the title data.
const current = (...keys: string[]): Route => ({
  method: "GET", path: "/titles/7/removal",
  reply: { title_id: 7, name: "Alpha", title: true, leftover: [], copies: keys.map((key) => ({ key, label: key, links: 0, downloads: 0 })) },
});
const CURRENT = current("movie-web", "movie-remux", "movie-arr");

const accepted = {
  rank: 1, release: "Alpha.2020.2160p.WEB-DL-GRP", info_hash: "aaa", accepted: true, score: 120, quality: "WEBDL-2160p", revision: 2,
  indexer: "Nyaa", seeders: 120, leechers: 8, cached_on: ["rd"], languages: ["English"], published: new Date(Date.now() - 5 * 3_600_000).toISOString(),
  formats: [{ name: "HDR", score: 120 }],
};
const rejected = { rank: 2, release: "Alpha.2020.720p.WEB-OTHER", info_hash: "bbb", accepted: false, score: -10, quality: "WEBDL-720p", rejections: [{ reason: "quality", message: "WEBDL-720p is not wanted" }] };
const answer = (version = "movie-web", more: object = {}) => ({
  title_id: 7, version, versions: ["movie-web", "movie-remux"], live_allowed: true, source: "live", results: 2, accepted: 1,
  candidates: [accepted, rejected], rejections: { quality: 1 }, outcomes: { aaa: { outcome: "failed", provider: "rd", reason: "RD status error" } }, grab: true, ...more,
});
const sent = { version: "movie-web", release: accepted.release, info_hash: "aaa", accepted: true, overridden: false, message: "Sent to the router" };

beforeEach(() => window.localStorage.clear());

const open = () => fireEvent.click(screen.getByTestId("interactive-search-open"));
const table = () => screen.findByTestId("releases-table");
const grabIn = async (n: number) => within(await table()).getAllByTestId("release-grab")[n];

it("searches once on opening, says it is waiting, then shows the columns", async () => {
  let reply: (v: unknown) => void = () => {};
  const api = mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases", reply: () => new Promise((r) => { reply = r; }) }]);
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
  const api = mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases", reply: answer() }, { method: "POST", path: "/titles/7/releases/grab", reply: sent }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  fireEvent.click(await grabIn(0));
  await within(await table()).findByTestId("release-sent");
  expect(screen.queryByTestId("grab-dialog")).toBeNull();
  expect(api.calls("POST", "/titles/7/releases/grab").map((g) => g.body)).toEqual([{ version: "movie-web", info_hash: "aaa", override: false }]);
});

it("asks first for a rejected release, and once for one the server rejects at grab time", async () => {
  const api = mockFetch([
    ME, TITLE, CURRENT,
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
    ME, TITLE, CURRENT,
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
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases", reply: answer() }]);
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
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases", reply: answer("movie-web", { source: "journal", grab: false, message: "Live search failed: no indexer answered; showing the last stored decision" }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const box = await screen.findByTestId("interactive-fallback");
  expect(box.textContent).toContain("Live search failed: no indexer answered");
  expect(box.textContent).toContain("cannot be grabbed");
  // The stored decision is the movie's own search: nothing more to say.
  expect(screen.queryByTestId("interactive-fallback-from")).toBeNull();
  await table();
  expect(screen.queryByTestId("release-grab")).toBeNull();
});

it("a refused live search says when the stored results come from another search", async () => {
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases", reply: answer("movie-web", { source: "journal", target: "rss", grab: false, message: "Live search failed: no indexer answered; showing the last stored decision" }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  expect((await screen.findByTestId("interactive-fallback-from")).textContent).toBe("The stored results are from RSS.");
});

it("a refused live search with nothing stored does not speak of stored results", async () => {
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases", reply: answer("movie-web", { source: "none", grab: false, candidates: [], results: 0, accepted: 0, message: "Live search failed: no indexer answered" }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const box = await screen.findByTestId("interactive-fallback");
  expect(box.textContent).toContain("Live search failed: no indexer answered");
  expect(box.textContent).not.toContain("cannot be grabbed");
  expect(box.textContent).toContain("Try Search again.");
});

it("searches a season or an episode, each library once per opening, and again on reopening", async () => {
  const api = mockFetch([
    ME, TITLE, CURRENT,
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
  fireEvent.click(screen.getByRole("tab", { name: "Remux" }));
  await waitFor(() => expect(api.calls("GET", "/titles/7/releases")).toHaveLength(2));
  await table();
  fireEvent.click(screen.getByRole("tab", { name: "WEB" }));
  await table();
  expect(api.calls("GET", "/titles/7/releases").map((g) => g.search)).toEqual(["?version=movie-web&live=1&season=2", "?version=movie-remux&live=1&season=2"]);
  expect(screen.queryByRole("tab", { name: "Arr" })).toBeNull();
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

it("a release sent for one library is not shown as sent for another", async () => {
  mockFetch([
    ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => answer(new URLSearchParams(r.search).get("version") ?? "") },
    { method: "POST", path: "/titles/7/releases/grab", reply: sent },
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  fireEvent.click(await grabIn(0));
  await within(await table()).findByTestId("release-sent");
  fireEvent.click(screen.getByRole("tab", { name: "Remux" }));
  await waitFor(async () => expect(within(await table()).getAllByTestId("release-grab")).toHaveLength(2));
  expect(screen.queryByTestId("release-sent")).toBeNull();
  // Back on WEB, the grab sent there still shows Sent: no second click can send it again.
  fireEvent.click(screen.getByRole("tab", { name: "WEB" }));
  expect(await within(await table()).findByTestId("release-sent")).toBeTruthy();
});

it("leaves a removed copy out: no tab, and a search asked for it opens on a current copy", async () => {
  const api = mockFetch([ME, TITLE, current("movie-remux", "movie-arr"), { method: "GET", path: "/titles/7/releases", reply: answer("movie-remux") }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  await table();
  expect(screen.queryByRole("tab", { name: "WEB" })).toBeNull();
  expect(api.calls("GET", "/titles/7/releases").map((g) => g.search)).toEqual(["?version=movie-remux&live=1"]);
});

it("names each library tab, with the settings key as tooltip", async () => {
  mockFetch([ME, TITLE, CURRENT, { method: "GET", path: "/titles/7/releases", reply: answer() }]);
  renderPage(<LibraryNamesProvider keys><InteractiveSearchButton titleId={7} version="movie-web" /></LibraryNamesProvider>);
  open();
  await table();
  const tabs = screen.getAllByRole("tab");
  expect(tabs.map((t) => t.textContent)).toEqual(["WEB", "Remux"]);
  expect(tabs.map((t) => t.querySelector("[data-vkey]")?.getAttribute("title"))).toEqual(["movie-web", "movie-remux"]);
});

it("a phone card shows the indexer, the episodes covered, languages and flags", async () => {
  const series: Route = { ...TITLE, reply: { ...(TITLE.reply as object), kind: "series" } };
  const pack = { ...accepted, episodes: ["S02E01", "S02E02", "S02E03"], flags: ["freeleech"] };
  mockFetch([ME, series, CURRENT, { method: "GET", path: "/titles/7/releases", reply: answer("movie-web", { candidates: [pack, rejected] }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" season={2} />);
  open();
  const list = await screen.findByTestId("releases-list");
  const card = within(list).getAllByRole("listitem").find((li) => li.dataset.releaseHash === "aaa")!;
  expect(card.textContent).toContain("120 / 8 peers · Nyaa");
  expect(within(card).getByTestId("release-card-episodes").textContent).toBe("S02E01–E03 (3)");
  expect(within(card).getByTestId("release-cached").textContent).toBe("RD");
  expect(card.textContent).toContain("English");
  expect(card.textContent).toContain("Freeleech");
});

// Hundreds of releases: the first 50 render, Show more adds the next; a rejection chip keeps its releases, and the
// "best of" line follows the filter.
it("renders the first 50 releases, shows more on asking, and filters by a rejection chip", async () => {
  const many = Array.from({ length: 120 }, (_, i) => ({ ...rejected, rank: i + 2, info_hash: `h${i}`, release: `Alpha.2020.720p.R${i}`,
    rejections: [{ reason: i < 30 ? "DiskHigherPreference" : "QualityNotWanted", message: "x" }] }));
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases",
    reply: answer("movie-web", { results: 500, accepted: 1, candidates: [accepted, ...many], rejections: { DiskHigherPreference: 30, QualityNotWanted: 90 } }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const rows = async () => within(await table()).getAllByRole("row").length - 1;
  expect(await rows()).toBe(50);
  expect(screen.getByTestId("release-capped").textContent).toBe("Showing the best 121 of 500.");
  fireEvent.click(screen.getByTestId("release-more"));
  expect(await rows()).toBe(100);
  const chip = screen.getAllByTestId("releases-reason").find((b) => b.textContent?.startsWith("The file held is preferred"))!;
  fireEvent.click(chip);
  expect(chip.getAttribute("aria-pressed")).toBe("true");
  expect(await rows()).toBe(30);
  expect(screen.queryByTestId("release-more")).toBeNull();
  expect(screen.getByTestId("release-capped").textContent).toBe("30 of the best 121 of 500 match the filter.");
  fireEvent.click(chip);
  expect(await rows()).toBe(50);
});

it("leads with no release of this title when every result is another title", async () => {
  const other = { ...rejected, rejections: [{ reason: "UnknownMovie", message: "Unknown Movie." }] };
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases",
    reply: answer("movie-web", { results: 1, accepted: 0, indexers: 8, candidates: [other], rejections: { UnknownMovie: 1 }, current: { held: false } }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  expect((await screen.findByTestId("interactive-none-of-title")).textContent).toBe("No release of this title was found on 8 indexers.");
  // The server says the film holds no file in this library: the line says so.
  expect(screen.getByTestId("interactive-current").textContent).toBe("No file held");
});

// The Current file line is the server's file of the requested film or episode, never a candidate's comparison file.
it("says the requested item's own file, as the server reads it", async () => {
  const pack = { ...accepted, episodes: ["S01E01", "S01E02"], existing: { quality: "WEBDL-2160p", score: 900 } };
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases",
    reply: answer("movie-web", { candidates: [pack], current: { held: true, quality: "WEBDL-1080p", score: 1775 } }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  expect((await screen.findByTestId("interactive-current")).textContent).toBe("Current file: WEBDL-1080p, score 1775");
});

// A sort chosen in an earlier opening is not kept: reopening starts in the best-match order, and Show more adds rows.
it("shows more after a sort, reopened", async () => {
  const many = Array.from({ length: 120 }, (_, i) => ({ ...rejected, rank: i + 2, info_hash: `h${i}`, release: `Alpha.2020.720p.R${i}`, score: i }));
  mockFetch([ME, TITLE, CURRENT, CURRENT, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases",
    reply: answer("movie-web", { results: 121, candidates: [accepted, ...many], rejections: { quality: 120 } }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const rows = async () => within(await table()).getAllByRole("row").length - 1;
  expect(await rows()).toBe(50);
  fireEvent.click(within(await table()).getByRole("button", { name: /Score/ }));
  fireEvent.click(screen.getByTestId("interactive-close"));
  open();
  expect(await rows()).toBe(50);
  expect(screen.queryByTestId("release-best-match")).toBeNull();
  fireEvent.click(screen.getByTestId("release-more"));
  expect(await rows()).toBe(100);
});

// Each library tab has its own grid: a filter typed in one is not applied to the other.
it("does not carry a filter typed in one library tab to the other", async () => {
  mockFetch([ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => answer(new URLSearchParams(r.search).get("version") ?? "") }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  await table();
  fireEvent.change(screen.getByTestId("release-filter"), { target: { value: "720p" } });
  await waitFor(async () => expect(within(await table()).getAllByRole("row")).toHaveLength(2));
  fireEvent.click(screen.getByRole("tab", { name: "Remux" }));
  await waitFor(async () => expect(within(await table()).getAllByRole("row")).toHaveLength(3));
  expect((screen.getByTestId("release-filter") as HTMLInputElement).value).toBe("");
});

// The indexer, quality, language and cached-on filters take every release, not only the first page; they last until a
// new search (Search again) and the sort they leave the list in is kept.
it("filters by indexer, quality, language and provider across pages, and a new search starts unfiltered", async () => {
  const many = Array.from({ length: 80 }, (_, i) => ({ ...rejected, rank: i + 2, info_hash: `h${i}`, release: `Alpha.2020.720p.R${i}`, indexer: i % 2 ? "Other" : "Nyaa", cached_on: i === 79 ? ["tb"] : undefined }));
  const api = mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases",
    reply: answer("movie-web", { results: 81, candidates: [accepted, ...many] }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const rows = async () => within(await table()).getAllByRole("row").length - 1;
  expect(await rows()).toBe(50);
  // The last release (rank 81) is on no shown page, yet the provider filter finds it.
  fireEvent.change(screen.getByTestId("release-cached-on"), { target: { value: "tb" } });
  expect(await rows()).toBe(1);
  expect(within(await table()).getByRole("row", { name: /R79/ })).toBeTruthy();
  fireEvent.change(screen.getByTestId("release-cached-on"), { target: { value: "" } });
  fireEvent.change(screen.getByTestId("release-indexer"), { target: { value: "Other" } });
  expect(await rows()).toBe(40);
  fireEvent.change(screen.getByTestId("release-quality"), { target: { value: "WEBDL-2160p" } });
  expect(screen.getByTestId("release-no-match")).toBeTruthy();
  fireEvent.change(screen.getByTestId("release-indexer"), { target: { value: "" } });
  expect(await rows()).toBe(1);
  // One language, and releases without one: the filter is offered.
  fireEvent.change(screen.getByTestId("release-language"), { target: { value: "English" } });
  expect(await rows()).toBe(1);
  // A sort chosen for this answer; a new search starts unfiltered in the best-match order.
  fireEvent.click(within(await table()).getByRole("button", { name: /Score/ }));
  expect(screen.getByTestId("release-best-match")).toBeTruthy();
  fireEvent.click(screen.getByTestId("interactive-again"));
  await waitFor(() => expect(api.calls("GET", "/titles/7/releases")).toHaveLength(2));
  await waitFor(async () => expect(await rows()).toBe(50));
  expect((screen.getByTestId("release-quality") as HTMLSelectElement).value).toBe("");
  expect((screen.getByTestId("release-language") as HTMLSelectElement).value).toBe("");
  expect(screen.queryByTestId("release-best-match")).toBeNull();
});

it("opens a row's matched formats with each one's score from its score", async () => {
  const formats = [{ name: "HDR", score: 100 }, { name: "Repack", score: 20 }, { name: "x265", score: 0 }];
  mockFetch([ME, TITLE, CURRENT, CURRENT, { method: "GET", path: "/titles/7/releases",
    reply: answer("movie-web", { candidates: [{ ...accepted, formats }, rejected] }) }]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  const t = await table();
  expect(within(t).queryByTestId("release-formats-detail")).toBeNull();
  const score = within(t).getAllByTestId("release-score")[0];
  fireEvent.click(score);
  const detail = await within(t).findByTestId("release-formats-detail");
  expect(detail.textContent).toBe("Matched formats HDR+100Repack+20x2650 Score +120");
  expect(score.getAttribute("aria-expanded")).toBe("true");
  // A release with no matched format has no button.
  expect(within(t).getAllByTestId("release-score")[1].tagName).toBe("SPAN");
  fireEvent.click(score);
  expect(within(t).queryByTestId("release-formats-detail")).toBeNull();
});

const outcomesRoute = (reply: Route["reply"]): Route => ({ method: "GET", path: "/titles/7/release-outcomes", reply });
const rowOf = async (hash: string) => within(await table()).getAllByRole("row").find((r) => r.dataset.releaseHash === hash)!;

it("shows what came of each release now over the search's own outcome: progress, a blocklisted failure with Try again", async () => {
  const api = mockFetch([
    ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: answer() },
    outcomesRoute({ outcomes: {
      aaa: { outcome: "grabbed", provider: "ad", download: 4, state: "downloading", words: "Downloading (43%)" },
      bbb: { outcome: "failed", provider: "rd", download: 2, reason: "Stalled", blocklisted: true },
    } }),
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  await waitFor(async () => expect(within(await rowOf("aaa")).queryByTestId("release-progress")).toBeTruthy());
  expect(api.calls("GET", "/titles/7/release-outcomes").map((g) => g.search)).toEqual(["?version=movie-web"]);
  const a = await rowOf("aaa");
  expect(within(a).getByTestId("release-progress").textContent).toContain("Downloading (43%)");
  expect(within(a).queryByTestId("release-grab")).toBeNull();
  expect(within(a).getByTestId("release-outcome").textContent).toContain("Downloading (43%)");
  const b = await rowOf("bbb");
  expect(within(b).getByTestId("release-failed").getAttribute("title")).toContain("On the blocklist: a grab by hand takes it off");
  expect(within(b).getByTestId("release-grab").textContent).toContain("Try again");
});

it("a failed release the library blocklisted asks once before the grab and sends unblock with it", async () => {
  const api = mockFetch([
    ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: answer() },
    outcomesRoute({ outcomes: {
      aaa: { outcome: "failed", provider: "rd", download: 2, reason: "Download stalled", blocklisted: true, blocked_reason: "Removed in Activity by admin" },
      bbb: { outcome: "failed", provider: "rd", download: 3, reason: "Download stalled", blocklisted: true, blocked_reason: "Stalled at RD" },
    } }),
    { method: "POST", path: "/titles/7/releases/grab", reply: sent },
    { method: "POST", path: "/titles/7/releases/grab", reply: { ...sent, info_hash: "bbb", accepted: false, overridden: true } },
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  await waitFor(async () => expect(within(await rowOf("aaa")).getByTestId("release-grab").textContent).toContain("Try again"));
  fireEvent.click(within(await rowOf("aaa")).getByTestId("release-grab"));
  let dialog = await screen.findByTestId("grab-dialog");
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(0);
  // The block's own reason (dismissed with blocklist), not the download's failure.
  expect(dialog.textContent).toContain("This release is on WEB's blocklist: Removed in Activity by admin. Grab it anyway? It is taken off the blocklist.");
  expect(dialog.textContent).not.toContain("Download stalled");
  fireEvent.click(within(dialog).getByText("Grab anyway"));
  await screen.findByTestId("confirm-done");
  fireEvent.click(screen.getByTestId("confirm-cancel"));
  await waitFor(() => expect(screen.queryByTestId("grab-dialog")).toBeNull());
  // Rejected too: the one question names both, and the one request overrides and unblocks.
  fireEvent.click(within(await rowOf("bbb")).getByTestId("release-grab"));
  dialog = await screen.findByTestId("grab-dialog");
  expect(dialog.textContent).toContain("WEB's blocklist: Stalled at RD");
  expect(dialog.textContent).toContain("WEBDL-720p is not wanted");
  fireEvent.click(within(dialog).getByText("Grab anyway"));
  await screen.findByTestId("confirm-done");
  expect(api.calls("POST", "/titles/7/releases/grab").map((g) => g.body)).toEqual([
    { version: "movie-web", info_hash: "aaa", override: false, unblock: true },
    { version: "movie-web", info_hash: "bbb", override: true, unblock: true },
  ]);
});

it("a grab the server refuses for the library's blocklist asks once, then sends it with unblock", async () => {
  const api = mockFetch([
    ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: answer() },
    outcomesRoute({ outcomes: { aaa: { outcome: "failed", provider: "rd", download: 2, reason: "Stalled" } } }),
    { method: "POST", path: "/titles/7/releases/grab", reply: { status: 409, body: { error: "This release is on WEB's blocklist: Stalled at RD", blocked: { library: "WEB", reason: "Stalled at RD." } } } },
    { method: "POST", path: "/titles/7/releases/grab", reply: sent },
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  await waitFor(async () => expect(within(await rowOf("aaa")).getByTestId("release-grab").textContent).toContain("Try again"));
  // Nothing asks before the server says it is blocked.
  expect(screen.queryByTestId("grab-dialog")).toBeNull();
  fireEvent.click(within(await rowOf("aaa")).getByTestId("release-grab"));
  const dialog = await screen.findByTestId("grab-dialog");
  expect(dialog.textContent).toContain("This release is on WEB's blocklist: Stalled at RD. Grab it anyway? It is taken off the blocklist.");
  expect(screen.queryByTestId("confirm-error")).toBeNull();
  fireEvent.click(within(dialog).getByText("Grab anyway"));
  await screen.findByTestId("confirm-done");
  expect(api.calls("POST", "/titles/7/releases/grab").map((g) => g.body)).toEqual([
    { version: "movie-web", info_hash: "aaa", override: false },
    { version: "movie-web", info_hash: "aaa", override: false, unblock: true },
  ]);
});

it("a failed release says what each provider answered and offers Try again at once", async () => {
  mockFetch([
    ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: answer() },
    outcomesRoute({ outcomes: {
      aaa: { outcome: "failed", provider: "rd", download: 3, reason: "RD status error" },
      bbb: { outcome: "failed", download: 2, reason: "Not cached on AD and TB", attempts: [
        { provider: "RD", outcome: "skipped", code: "rd_name_rule", message: "Real-Debrid refuses this name" },
        { provider: "AD", outcome: "refused", code: "uncached_deleted" },
        { provider: "TB", outcome: "skipped", code: "known_uncached" },
      ] },
    } }),
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  await waitFor(async () => expect(within(await rowOf("bbb")).queryByTestId("release-failed")).toBeTruthy());
  const a = await rowOf("aaa");
  expect(within(a).getByTestId("release-failed").textContent).toBe("Failed");
  expect(within(a).queryByTestId("release-answers")).toBeNull();
  expect(within(a).getByTestId("release-grab").textContent).toContain("Try again");
  const b = await rowOf("bbb");
  const line = within(b).getByTestId("release-answers");
  expect(line.textContent).toBe("RD: refused by Real-Debrid's name rule · AD: not cached, our add deleted · TB: known not cached");
  expect(line.getAttribute("title")).toContain("RD: refused by Real-Debrid's name rule (Real-Debrid refuses this name)");
  expect(within(b).getByTestId("release-failed").getAttribute("title")).toContain("Not cached on AD and TB");
  expect(within(b).getByTestId("release-outcome").getAttribute("title")).toContain("AD: not cached, our add deleted");
  expect(within(b).getByTestId("release-grab").textContent).toContain("Try again");
});

it("after Try again says Sent until a new download answers, then its progress; an imported release says Imported", async () => {
  let reads = 0;
  const failedA = { outcome: "failed", provider: "rd", download: 3, reason: "RD status error" };
  const importedB = { outcome: "imported", provider: "rd", download: 1 };
  mockFetch([
    ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: (r: { search: string }) => answer(new URLSearchParams(r.search).get("version") ?? "") },
    outcomesRoute((r: { search: string }) => {
      if (!r.search.includes("movie-web")) return { outcomes: {} };
      reads++;
      return { outcomes: { aaa: reads <= 2 ? failedA : { outcome: "grabbed", download: 4, state: "queued", words: "Queued" }, bbb: importedB } };
    }),
    { method: "POST", path: "/titles/7/releases/grab", reply: sent },
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  await waitFor(async () => expect(within(await rowOf("bbb")).queryByTestId("release-imported")).toBeTruthy());
  expect(within(await rowOf("bbb")).queryByTestId("release-grab")).toBeNull();
  fireEvent.click(within(await rowOf("aaa")).getByTestId("release-grab"));
  // The read after the grab still shows the earlier failure: the release was sent, nothing new answered yet.
  await waitFor(() => expect(reads).toBe(2));
  expect(await within(await rowOf("aaa")).findByTestId("release-sent")).toBeTruthy();
  // The next read (here: back on the library) has the new download.
  fireEvent.click(screen.getByRole("tab", { name: "Remux" }));
  await waitFor(async () => expect(within(await table()).getAllByTestId("release-grab")).toHaveLength(2));
  fireEvent.click(screen.getByRole("tab", { name: "WEB" }));
  await waitFor(async () => expect(within(await rowOf("aaa")).getByTestId("release-progress").textContent).toContain("Queued"));
});

it("keeps Show more open when the outcomes refresh after a grab further down", async () => {
  const many = Array.from({ length: 60 }, (_, i) => ({ ...accepted, rank: i + 1, release: `Alpha.2020.2160p.WEB-DL-G${i}`, info_hash: `h${String(i).padStart(2, "0")}` }));
  let grabbed = false;
  mockFetch([
    ME, TITLE, CURRENT,
    { method: "GET", path: "/titles/7/releases", reply: answer("movie-web", { candidates: many, results: 60, accepted: 60, outcomes: {} }) },
    outcomesRoute(() => ({ outcomes: grabbed ? { h55: { outcome: "grabbed", download: 9, state: "queued", words: "Queued" } } : {} })),
    { method: "POST", path: "/titles/7/releases/grab", reply: () => { grabbed = true; return { ...sent, info_hash: "h55" }; } },
  ]);
  renderPage(<InteractiveSearchButton titleId={7} version="movie-web" />);
  open();
  fireEvent.click(await screen.findByTestId("release-more"));
  fireEvent.click(within(await rowOf("h55")).getByTestId("release-grab"));
  await waitFor(async () => expect(within(await rowOf("h55")).getByTestId("release-progress").textContent).toContain("Queued"));
  expect(within(await table()).getAllByRole("row").filter((r) => r.dataset.releaseHash)).toHaveLength(60);
});

