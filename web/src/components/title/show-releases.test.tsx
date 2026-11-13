// @vitest-environment jsdom
import { fireEvent, screen, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { ReleaseGrid } from "./release-grid";
import { ShowReleases } from "./show-releases";

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
  expect(within(table).getAllByTestId("release-outcome").map((b) => b.textContent)).toEqual(["Grabbed · rd", "Imported", "Current file", "Failed · RD status error", "Replaced"]);
  expect(within(table).getByText("by Alpha.2020.2160p.REMUX-X")).toBeTruthy();
});

it("says what the shown search did and picks out the followed release", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: {
      title_id: 7, version: "movie-web", versions: ["movie-web"], mode: "active", live_allowed: false, source: "journal", results: 12, accepted: 2,
      candidates: [cand(1, "aaa"), cand(2, "bbb")], rejections: {}, outcomes: { aaa: { outcome: "failed", provider: "rd", reason: "RD status error" }, bbb: { outcome: "current", provider: "rd" } },
      grabbed: "Grabbed #2 via rd → imported (the current file)", grab: false,
    } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" focus="BBB" labels={{ "movie-web": "Movies WEB" }} />);
  expect((await screen.findByTestId("releases-grabbed")).textContent).toBe("Grabbed #2 via rd → imported (the current file)");
  const tab = screen.getByRole("tab", { name: "Movies WEB" });
  expect(tab.querySelector("[data-vkey]")?.getAttribute("title")).toBe("movie-web");
  const rows = within(screen.getByTestId("releases-table")).getAllByRole("row").filter((r) => r.dataset.releaseHash);
  expect(rows.map((r) => r.dataset.focus ?? "")).toEqual(["", "true"]);
  expect(screen.queryByTestId("releases-focus-missing")).toBeNull();
});

it("says when the followed release is not in the stored list", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: {
      title_id: 7, version: "movie-web", versions: ["movie-web"], mode: "active", live_allowed: false, source: "journal", results: 4, accepted: 0,
      candidates: [cand(1, "aaa", false)], rejections: {}, outcomes: {}, grabbed: "Nothing grabbed: no accepted release among 4 results", grab: false,
    } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" focus="zzz" />);
  expect((await screen.findByTestId("releases-grabbed")).textContent).toBe("Nothing grabbed: no accepted release among 4 results");
  expect(screen.getByTestId("releases-focus-missing")).toBeTruthy();
});

const stored = (cands: ReturnType<typeof cand>[]) => ({
  title_id: 7, version: "movie-web", versions: ["movie-web"], mode: "active", live_allowed: true, source: "journal", results: cands.length, accepted: 1,
  candidates: cands, rejections: {}, outcomes: {}, grabbed: "Nothing grabbed: blocklisted", grab: false,
});

it("a stored list has no Grab buttons and offers Interactive search", async () => {
  mockFetch([
    ME,
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa"), cand(2, "bbb", false)]) },
    { method: "GET", path: "/titles/7", reply: { id: 7, name: "Alpha", year: 2020, kind: "movie", versions: [{ id: 1, key: "movie-web", label: "WEB", control: { mode: "active", can_edit: true, can_search: true, live_releases: true } }] } },
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
