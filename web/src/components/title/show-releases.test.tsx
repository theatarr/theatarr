// @vitest-environment jsdom
import { fireEvent, screen, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { ReleaseGrid } from "./release-grid";
import { searchIncomplete, ShowReleases, Summary } from "./show-releases";
import { LibraryNamesProvider } from "@/lib/library-names";

const cand = (rank: number, hash: string, accepted = true) => ({ rank, release: `Alpha.2020.2160p.WEB-${rank}`, info_hash: hash, accepted, score: 100 - rank, quality: "WEBDL-2160p" });

const ME = { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse", "see_files", "manage"] } };

it("badges each row by what came of its release", () => {
  mockFetch([ME]);
  renderPage(<ReleaseGrid
    kind="movie"
    items={[cand(1, "AAA"), cand(2, "bbb"), cand(3, "ccc"), cand(4, "ddd"), cand(5, "eee"), cand(6, "fff")]}
    outcomes={{
      aaa: { outcome: "grabbed", provider: "rd" },
      bbb: { outcome: "imported", provider: "rd" },
      ccc: { outcome: "current", provider: "tb" },
      ddd: { outcome: "failed", provider: "rd", reason: "RD status error" },
      eee: { outcome: "replaced", provider: "rd", replaced_by: "Alpha.2020.2160p.REMUX-X" },
    }}
  />);
  const table = screen.getByTestId("releases-table");
  expect(within(table).getAllByTestId("release-outcome").map((b) => b.textContent)).toEqual(["Grabbed · RD", "Imported", "Current file", "Failed · RD status error", "Replaced"]);
  expect(within(table).getByText("by Alpha.2020.2160p.REMUX-X")).toBeTruthy();
});

it("shows one row per torrent with every indexer, each copy in the tooltip, and filters by any of them", () => {
  mockFetch([ME]);
  const merged = { ...cand(1, "aaa"), indexer: "prowlarr-tpb", indexers: ["prowlarr-tpb", "therarbg", "dmm"], seeders: 90, leechers: 10,
    copies: [{ indexer: "therarbg", release: "Alpha 2020 2160p WEB 1" }, { indexer: "dmm", release: "alpha.2020.2160p.web-1" }] };
  renderPage(<ReleaseGrid kind="movie" items={[merged, { ...cand(2, "bbb"), indexer: "nyaa", indexers: ["nyaa"] }]} />);
  const table = screen.getByTestId("releases-table");
  const cells = within(table).getAllByTestId("release-indexers");
  expect(cells.map((c) => c.textContent)).toEqual(["tpb, therarbg +1", "nyaa"]);
  expect(cells[0].getAttribute("title")).toBe("prowlarr-tpb: Alpha.2020.2160p.WEB-1\ntherarbg: Alpha 2020 2160p WEB 1\ndmm: alpha.2020.2160p.web-1");
  expect(within(table).getAllByTestId("release-peers")[0].textContent).toBe("90 / 10");
  expect(within(screen.getByTestId("releases-list")).getAllByRole("listitem")[0].textContent).toContain("tpb, therarbg, dmm");
  fireEvent.change(screen.getByTestId("release-indexer"), { target: { value: "dmm" } });
  expect(within(table).getAllByTestId("release-indexers").map((c) => c.textContent)).toEqual(["tpb, therarbg +1"]);
});

it("opens each copy's name by a tap on the indexers, in the table and on a card, and groups the score's thousands", () => {
  mockFetch([ME]);
  const merged = { ...cand(1, "aaa"), score: 4450, formats: [{ name: "DV", score: 4450 }], indexer: "prowlarr-tpb", indexers: ["prowlarr-tpb", "therarbg"],
    copies: [{ indexer: "therarbg", release: "Alpha 2020 2160p WEB 1" }] };
  renderPage(<ReleaseGrid kind="movie" items={[merged, { ...cand(2, "bbb"), indexer: "nyaa", indexers: ["nyaa"] }]} />);
  const table = screen.getByTestId("releases-table");
  const [withCopies, single] = within(table).getAllByTestId("release-indexers");
  // A row with copies opens them by its indexers (a button, so a tap or Enter does it); one without stays text.
  expect(withCopies.tagName).toBe("BUTTON");
  expect(single.tagName).toBe("SPAN");
  expect(screen.queryByTestId("release-copies")).toBeNull();
  fireEvent.click(withCopies);
  expect(withCopies.getAttribute("aria-expanded")).toBe("true");
  expect(within(screen.getByTestId("release-copies-detail")).getByTestId("release-copies").textContent).toBe("tpb: Alpha.2020.2160p.WEB-1therarbg: Alpha 2020 2160p WEB 1");
  fireEvent.click(withCopies);
  expect(screen.queryByTestId("release-copies-detail")).toBeNull();
  // The card: its indexers open the same list.
  const card = within(screen.getByTestId("releases-list")).getAllByRole("listitem")[0];
  fireEvent.click(within(card).getByTestId("release-card-indexers"));
  expect(within(card).getByTestId("release-copies").textContent).toContain("therarbg: Alpha 2020 2160p WEB 1");
  expect(within(table).getAllByTestId("release-score")[0].textContent).toBe("+4,450");
});

it("says what the shown search did and picks out the followed release", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: {
      title_id: 7, version: "movie-web", versions: ["movie-web"], live_allowed: false, source: "journal", results: 12, accepted: 2,
      candidates: [cand(1, "aaa"), cand(2, "bbb")], rejections: {}, outcomes: { aaa: { outcome: "failed", provider: "rd", reason: "RD status error" }, bbb: { outcome: "current", provider: "rd" } },
      grabbed: "Grabbed #2 via rd → imported (the current file)", grab: false,
      notes: "seadex: dropped 1 entries: private tracker; dropped 1 single-file entries; dropped 1 recent entries not filed under one of our titles; dropped 2 entries with an excluded tag",
    } },
  ]);
  renderPage(<LibraryNamesProvider keys><ShowReleases titleId={7} defaultOpen initialVersion="movie-web" focus="BBB" labels={{ "movie-web": "Movies WEB" }} /></LibraryNamesProvider>);
  expect((await screen.findByTestId("releases-grabbed")).textContent).toBe("Grabbed #2 via rd → imported (the current file)");
  const tab = screen.getByRole("tab", { name: "Movies WEB" });
  expect(tab.querySelector("[data-vkey]")?.getAttribute("title")).toBe("movie-web");
  const rows = within(screen.getByTestId("releases-table")).getAllByRole("row").filter((r) => r.dataset.releaseHash);
  expect(rows.map((r) => r.dataset.focus ?? "")).toEqual(["", "true"]);
  expect(screen.queryByTestId("releases-focus-missing")).toBeNull();
  // A library whose acquirer is down says so; the facts are one text run.
  expect(screen.getByTestId("releases-no-live").textContent).toBe("The acquirer is not running");
  expect(screen.getByTestId("releases-facts").textContent).toBe("12 releases returned by the indexers, 2 accepted");
  expect(screen.getByTestId("releases-notes").textContent).toBe("Indexers: seadex: dropped 1 entry: private tracker; dropped 1 single-file entry; dropped 1 recent entry not filed under one of our titles; dropped 2 entries with an excluded tag");
  expect(screen.queryByText("the last search Theatarr decided for each library")).toBeNull();
});

it("says when the followed release is not in the stored list", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: {
      title_id: 7, version: "movie-web", versions: ["movie-web"], live_allowed: false, source: "journal", results: 4, accepted: 0,
      candidates: [cand(1, "aaa", false)], rejections: {}, outcomes: {}, grabbed: "Nothing grabbed: no accepted release among 4 results", grab: false,
    } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" focus="zzz" />);
  expect((await screen.findByTestId("releases-grabbed")).textContent).toBe("Nothing grabbed: no accepted release among 4 results");
  expect(screen.getByTestId("releases-focus-missing")).toBeTruthy();
});

const stored = (cands: ReturnType<typeof cand>[]) => ({
  title_id: 7, version: "movie-web", versions: ["movie-web"], live_allowed: true, source: "journal", results: cands.length, accepted: 1,
  candidates: cands, rejections: {}, outcomes: {}, grabbed: "Nothing grabbed: blocklisted", grab: false,
});

it("a stored list has no Grab buttons and offers Interactive search", async () => {
  mockFetch([
    ME,
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa"), cand(2, "bbb", false)]) },
    { method: "GET", path: "/titles/7", reply: { id: 7, name: "Alpha", year: 2020, kind: "movie", versions: [{ id: 1, key: "movie-web", label: "WEB", control: { can_edit: true, can_search: true, live_releases: true } }] } },
    { method: "GET", path: "/titles/7/releases", reply: () => new Promise(() => {}) },
    { method: "GET", path: "/titles/7/removal", reply: { title_id: 7, name: "x", title: true, leftover: [], copies: [{ key: "movie-web", label: "movie-web", links: 0, downloads: 0 }] } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-table");
  expect(screen.queryByTestId("release-grab")).toBeNull();
  fireEvent.click(screen.getByTestId("interactive-search-open"));
  expect(await screen.findByTestId("interactive-waiting")).toBeTruthy();
});
it("scrolls the followed release's card on a phone", () => {
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }));
  const scrolled: Element[] = [];
  const saved = Element.prototype.scrollIntoView;
  Element.prototype.scrollIntoView = function (this: Element) { scrolled.push(this); };
  mockFetch([ME]);
  renderPage(<ReleaseGrid kind="movie" items={[cand(1, "aaa"), cand(2, "bbb")]} focus="BBB" />);
  expect(scrolled).toHaveLength(1);
  expect(scrolled[0].tagName).toBe("LI");
  expect(scrolled[0].getAttribute("data-release-hash")).toBe("bbb");
  Element.prototype.scrollIntoView = saved;
});

const ADMIN_PERMS = { permissions: ["browse", "see_files", "manage", "admin"] };
const added = { version: "movie-web", release: "Alpha.2020.2160p.WEB-DL-GRP", info_hash: "a".repeat(40), accepted: true, overridden: false, message: "Sent to the router" };

it("an admin adds a release by magnet: the name comes from the magnet and a rejection needs Add anyway", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: ADMIN_PERMS },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
    { method: "POST", path: "/titles/7/releases/add", reply: { status: 409, body: { error: "rejected for movie-web: WEBDL-1080p is not wanted", detail: ["WEBDL-1080p is not wanted"] } } },
    { method: "POST", path: "/titles/7/releases/add", reply: { ...added, accepted: false, overridden: true, message: "Added although rejected; sent to the router" } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  const form = await screen.findByTestId("add-by-hash");
  const link = within(form).getByTestId("add-by-hash-link") as HTMLInputElement;
  const name = within(form).getByTestId("add-by-hash-name") as HTMLInputElement;
  const button = within(form).getByTestId("add-by-hash-add") as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  const magnet = `magnet:?xt=urn:btih:${"a".repeat(40)}&dn=Alpha.2020.1080p.WEB-DL+GRP&tr=udp%3A%2F%2Ftracker.example`;
  fireEvent.change(link, { target: { value: magnet } });
  expect(name.value).toBe("Alpha.2020.1080p.WEB-DL GRP");
  fireEvent.click(button);
  const rejected = await screen.findByTestId("add-by-hash-rejected");
  expect(rejected.textContent).toContain("WEBDL-1080p is not wanted");
  fireEvent.click(within(rejected).getByTestId("add-by-hash-anyway"));
  expect((await screen.findByTestId("add-by-hash-done")).textContent).toContain("Added although rejected");
  expect(api.calls("POST", "/titles/7/releases/add").map((c) => c.body)).toEqual([
    { version: "movie-web", name: "Alpha.2020.1080p.WEB-DL GRP", override: false, magnet },
    { version: "movie-web", name: "Alpha.2020.1080p.WEB-DL GRP", override: true, magnet },
  ]);
  expect(link.value).toBe("");
});

it("an add the server refuses for the library's blocklist offers Add anyway, which sends it with unblock", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: ADMIN_PERMS },
    { method: "GET", path: "/titles/7/releases", reply: stored([]) },
    { method: "POST", path: "/titles/7/releases/add", reply: { status: 409, body: { error: "This release is on WEB's blocklist: Failed the library check", blocked: { library: "WEB", reason: "Failed the library check" } } } },
    { method: "POST", path: "/titles/7/releases/add", reply: added },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  const form = await screen.findByTestId("add-by-hash");
  fireEvent.change(within(form).getByTestId("add-by-hash-link"), { target: { value: "c".repeat(40) } });
  fireEvent.change(within(form).getByTestId("add-by-hash-name"), { target: { value: "Alpha.2020.2160p.WEB-DL-GRP" } });
  fireEvent.click(within(form).getByTestId("add-by-hash-add"));
  const blocked = await screen.findByTestId("add-by-hash-blocked");
  expect(blocked.textContent).toContain("This release is on WEB's blocklist: Failed the library check. Add it anyway? It is taken off the blocklist.");
  expect(screen.queryByTestId("add-by-hash-error")).toBeNull();
  fireEvent.click(within(blocked).getByTestId("add-by-hash-unblock"));
  expect((await screen.findByTestId("add-by-hash-done")).textContent).toContain("Sent to the router");
  expect(api.calls("POST", "/titles/7/releases/add").map((c) => c.body)).toEqual([
    { version: "movie-web", name: "Alpha.2020.2160p.WEB-DL-GRP", override: false, info_hash: "c".repeat(40) },
    { version: "movie-web", name: "Alpha.2020.2160p.WEB-DL-GRP", override: false, unblock: true, info_hash: "c".repeat(40) },
  ]);
});

it("a bare hash needs a release name before Add", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: ADMIN_PERMS },
    { method: "GET", path: "/titles/7/releases", reply: stored([]) },
    { method: "POST", path: "/titles/7/releases/add", reply: added },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  const form = await screen.findByTestId("add-by-hash");
  const button = within(form).getByTestId("add-by-hash-add") as HTMLButtonElement;
  fireEvent.change(within(form).getByTestId("add-by-hash-link"), { target: { value: ` ${"A".repeat(40)} ` } });
  expect((within(form).getByTestId("add-by-hash-name") as HTMLInputElement).required).toBe(true);
  expect(button.disabled).toBe(true);
  fireEvent.change(within(form).getByTestId("add-by-hash-name"), { target: { value: "Alpha.2020.2160p.WEB-DL-GRP" } });
  expect(button.disabled).toBe(false);
  fireEvent.click(button);
  expect((await screen.findByTestId("add-by-hash-done")).textContent).toContain("Sent to the router");
  expect(api.calls("POST", "/titles/7/releases/add").map((c) => c.body)).toEqual([
    { version: "movie-web", name: "Alpha.2020.2160p.WEB-DL-GRP", override: false, info_hash: "A".repeat(40) },
  ]);
});

it("a refusal of Add anyway shows the server's reason and does not offer it again", async () => {
  const no = (error: string) => ({ status: 409, body: { error, detail: ["WEBDL-1080p is not wanted"] } });
  mockFetch([
    { method: "GET", path: "/auth/me", reply: ADMIN_PERMS },
    { method: "GET", path: "/titles/7/releases", reply: stored([]) },
    { method: "POST", path: "/titles/7/releases/add", reply: no("rejected for movie-web: WEBDL-1080p is not wanted") },
    { method: "POST", path: "/titles/7/releases/add", reply: no("the title has no movie-web copy") },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  const form = await screen.findByTestId("add-by-hash");
  fireEvent.change(within(form).getByTestId("add-by-hash-link"), { target: { value: "b".repeat(40) } });
  fireEvent.change(within(form).getByTestId("add-by-hash-name"), { target: { value: "Alpha 1080p" } });
  fireEvent.click(within(form).getByTestId("add-by-hash-add"));
  fireEvent.click(await screen.findByTestId("add-by-hash-anyway"));
  expect((await screen.findByTestId("add-by-hash-error")).textContent).toBe("the title has no movie-web copy");
  expect(screen.queryByTestId("add-by-hash-anyway")).toBeNull();
});

it("a name that matches no episode is refused at once with the server's reason, and Add anyway is not offered", async () => {
  // The server refuses it before any rejection is offered for an override: a 409 with no detail.
  const reason =
    "the release name matches no episode of the title. The season belongs in the release name: " +
    "name a season pack with its season (Show.S02.1080p...) and an episode with its number (S02E05)";
  mockFetch([
    { method: "GET", path: "/auth/me", reply: ADMIN_PERMS },
    { method: "GET", path: "/titles/7/releases", reply: stored([]) },
    { method: "POST", path: "/titles/7/releases/add", reply: { status: 409, body: { error: reason } } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  const form = await screen.findByTestId("add-by-hash");
  fireEvent.change(within(form).getByTestId("add-by-hash-link"), { target: { value: "c".repeat(40) } });
  fireEvent.change(within(form).getByTestId("add-by-hash-name"), { target: { value: "Show 1080p WEB-DL" } });
  fireEvent.click(within(form).getByTestId("add-by-hash-add"));
  expect((await screen.findByTestId("add-by-hash-error")).textContent).toBe(reason);
  expect(screen.queryByTestId("add-by-hash-anyway")).toBeNull();
});

it("a manager has no Add by magnet or hash", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  expect(screen.queryByTestId("add-by-hash")).toBeNull();
});

it("labels an accepted release that betters a held file an upgrade, and a pack that only fills episodes as such", () => {
  mockFetch([ME]);
  renderPage(<ReleaseGrid kind="series" items={[
    { ...cand(1, "aaa"), upgrade: true, upgrade_reason: "better quality: Bluray-2160p over WEBDL-2160p; fills 2 episode(s) with no file" },
    { ...cand(2, "bbb"), upgrade: true, upgrade_reason: "fills 3 episode(s) with no file" },
  ]} />);
  const table = screen.getByTestId("releases-table");
  const up = within(table).getByText("· upgrade");
  expect(up.getAttribute("title")).toBe("better quality: Bluray-2160p over WEBDL-2160p; fills 2 episode(s) with no file");
  expect(within(table).getByText("· fills missing episodes").getAttribute("title")).toBe("fills 3 episode(s) with no file");
});

it("shows the indexer without its prowlarr- prefix, the key as tooltip, and a short filter hint", () => {
  mockFetch([ME]);
  renderPage(<ReleaseGrid kind="movie" items={[{ ...cand(1, "aaa"), indexer: "prowlarr-therarbg" }]} />);
  const cell = within(screen.getByTestId("releases-table")).getByText("therarbg");
  expect(cell.getAttribute("title")).toBe("prowlarr-therarbg");
  expect(screen.getByTestId("release-filter").getAttribute("placeholder")).toBe("Filter releases");
});

// Each library tab has its own grid: a filter typed in one is not applied to the other.
it("does not carry a filter from one library tab to the other", async () => {
  const reply = (r: { search: string }) => {
    const version = new URLSearchParams(r.search).get("version") || "movie-web";
    return { title_id: 7, version, versions: ["movie-web", "movie-remux"], live_allowed: false, source: "journal", results: 2, accepted: 2,
      candidates: [cand(1, "aaa"), cand(2, "bbb")], rejections: {}, outcomes: {}, grab: false };
  };
  mockFetch([ME, { method: "GET", path: "/titles/7/releases", reply }]);
  renderPage(<LibraryNamesProvider keys><ShowReleases titleId={7} defaultOpen initialVersion="movie-web" labels={{ "movie-web": "WEB", "movie-remux": "Remux" }} /></LibraryNamesProvider>);
  const rows = async () => within(await screen.findByTestId("releases-table")).getAllByRole("row").length - 1;
  expect(await rows()).toBe(2);
  fireEvent.change(screen.getByTestId("release-filter"), { target: { value: "WEB-1" } });
  expect(await rows()).toBe(1);
  fireEvent.click(screen.getByRole("tab", { name: "Remux" }));
  await vi.waitFor(async () => expect(await rows()).toBe(2));
  fireEvent.change(screen.getByTestId("release-filter"), { target: { value: "WEB-2" } });
  // Back to a tab already answered (no loading in between): its grid starts afresh.
  fireEvent.click(screen.getByRole("tab", { name: "WEB" }));
  await vi.waitFor(async () => expect(await rows()).toBe(2));
  expect((screen.getByTestId("release-filter") as HTMLInputElement).value).toBe("");
});

it("names the indexers that did not answer fully in one line, the raw notes kept under Technical details", async () => {
  const notes = "therarbg: partial answer; indexer therarbg: therarbg: request failed: gateway: budget exhausted: indexer:therarbg: budget therarbg: next token in 5.786s (retry at 2026-11-14T03:22:08Z); nyaa: partial answer; indexer nyaa: nyaa: waited too long for the site's previous search to finish: context deadline exceeded; page_cap 200 reached after 2 pages; season search: kept 44 packs, dropped 42 single episodes; title search added 0, dropped 0 for another IMDb id";
  expect(searchIncomplete(notes)).toBe("Search incomplete: therarbg was rate-limited, nyaa did not answer in time.");
  expect(searchIncomplete("title search added 1, dropped 0 for another IMDb id")).toBe("");
  expect(searchIncomplete("seadex: dropped 1 entries: private tracker; dropped 2 entries with an excluded tag")).toBe("");
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: {
      title_id: 7, version: "movie-web", versions: ["movie-web"], live_allowed: false, source: "journal", results: 0, accepted: 0,
      candidates: [], rejections: {}, outcomes: {}, grabbed: "Nothing grabbed", grab: false, notes,
    } },
  ]);
  renderPage(<LibraryNamesProvider keys><ShowReleases titleId={7} defaultOpen initialVersion="movie-web" /></LibraryNamesProvider>);
  expect((await screen.findByTestId("releases-incomplete")).textContent).toBe("Search incomplete: therarbg was rate-limited, nyaa did not answer in time.");
  const raw = screen.getByTestId("releases-notes");
  expect(raw.closest("details")?.open).toBe(false);
  expect(raw.textContent).toBe(`Indexers: ${notes}`);
});

it("leaves no failure out of the line: HTTP errors, unprefixed fallbacks and gateway notes name their indexer", () => {
  // Real 401 and 503 answers, and Zilean's title search failing behind an IMDb answer that still gave releases.
  expect(searchIncomplete("prowlarr-tpb: torznab: HTTP 401 Unauthorized; dmm: request failed: HTTP 503 Service Unavailable; title search failed, IMDb results only: zilean: HTTP 500")).toBe(
    "Search incomplete: prowlarr-tpb refused the sign-in, dmm was unavailable, zilean was unavailable.");
  // A gateway note belongs to the indexer it names, not to "gateway".
  expect(searchIncomplete("one: partial answer; gateway: budget exhausted: indexer:one: budget one: next token in 2s")).toBe(
    "Search incomplete: one was rate-limited.");
  // Anything that is not a known piece of information is trouble.
  expect(searchIncomplete("nyaa: unexpected EOF")).toBe("Search incomplete: nyaa had an error.");
  // The known information stays out, the name-only fallback included.
  expect(searchIncomplete("zilean: only a plain keyword query; no episode criteria; page_cap 200 reached after 2 pages; season search: kept 4 packs, dropped 2 single episodes; no episode counts for this series; pack sizes are file sizes; dropped 3 releases with conflicting or unverifiable torrent identities; no AniList id maps to this title: nothing to ask SeaDex")).toBe("");
});

it("names a fallback note by the indexer it carries before its usual owner", () => {
  expect(searchIncomplete("title search failed, IMDb results only: indexer my-zilean: upstream answered HTTP 503 Service Unavailable")).toBe(
    "Search incomplete: my-zilean was unavailable.");
});

it("names a stored RSS read as such, and says nothing about counts when nothing is stored", () => {
  mockFetch([ME]);
  const base = { title_id: 7, version: "tv", versions: ["tv"], live_allowed: false, candidates: [], rejections: {}, outcomes: {}, grab: false, grab_state: { paused: false, pause_forced: false, will_grab: false } };
  const { unmount } = renderPage(<Summary r={{ ...base, source: "journal", target: "rss", results: 3, accepted: 1 }} />);
  expect(screen.getByTestId("releases-source").textContent).toBe("Last RSS match");
  expect(screen.getByTestId("releases-facts").textContent).toBe("3 releases returned by the indexers, 1 accepted");
  unmount();
  const none = renderPage(<Summary r={{ ...base, source: "none", results: 0, accepted: 0 }} />);
  expect(screen.getByTestId("releases-source").textContent).toBe("Nothing stored");
  expect(screen.queryByTestId("releases-facts")).toBeNull();
  none.unmount();
  // A stored search that really returned nothing keeps its zero.
  renderPage(<Summary r={{ ...base, source: "journal", target: "s01e02", results: 0, accepted: 0 }} />);
  expect(screen.getByTestId("releases-source").textContent).toBe("Last decided search");
  expect(screen.getByTestId("releases-facts").textContent).toBe("0 releases returned by the indexers, 0 accepted");
});

it("counts a season search's stored candidates for the episode in an episode's window, and an episode search's results as returned", () => {
  mockFetch([ME]);
  const base = { title_id: 7, version: "tv", versions: ["tv"], live_allowed: false, candidates: [], rejections: {}, outcomes: {}, grab: false, grab_state: { paused: false, pause_forced: false, will_grab: false } };
  const season = renderPage(<Summary episode r={{ ...base, source: "journal", target: "S02", results: 4, accepted: 0 }} />);
  expect(screen.getByTestId("releases-facts").textContent).toBe("4 stored candidates for this episode, 0 accepted");
  season.unmount();
  const own = renderPage(<Summary episode r={{ ...base, source: "journal", target: "s02e01", results: 30, accepted: 2 }} />);
  expect(screen.getByTestId("releases-facts").textContent).toBe("30 releases returned by the indexers, 2 accepted");
  own.unmount();
  // The season's own panel keeps the search's count.
  renderPage(<Summary r={{ ...base, source: "journal", target: "S02", results: 508, accepted: 0 }} />);
  expect(screen.getByTestId("releases-facts").textContent).toBe("508 releases returned by the indexers, 0 accepted");
});
