// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { expect, it, vi } from "vitest";
import { qk } from "@/api/query-keys";
import { mockFetch, renderPage } from "@/test/page";
import { ReleaseTable, ShowReleases } from "./show-releases";

const cand = (rank: number, hash: string, accepted = true) => ({ rank, release: `Alpha.2020.2160p.WEB-${rank}`, info_hash: hash, accepted, score: 100 - rank, quality: "WEBDL-2160p" });

it("badges each row by what came of its release", () => {
  renderPage(<ReleaseTable
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
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" focus="BBB" />);
  expect((await screen.findByTestId("releases-grabbed")).textContent).toBe("Grabbed #2 via rd → imported (the current file)");
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
const fresh = (cands: ReturnType<typeof cand>[]) => ({ ...stored(cands), source: "live", grabbed: undefined, grab: true });

async function grabStoredRow(n: number) {
  const rows = await within(await screen.findByTestId("releases-table")).findAllByTestId("release-grab");
  fireEvent.click(rows[n]);
  fireEvent.click(within(await screen.findByTestId("grab-dialog")).getByTestId("confirm-ok"));
}

it("a stored row's Grab searches first, then grabs that hash from the fresh answer", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa"), cand(2, "BBB", false)]) },
    { method: "GET", path: "/titles/7/releases", reply: fresh([cand(1, "bbb"), cand(2, "aaa")]) },
    { method: "POST", path: "/titles/7/releases/grab", reply: { version: "movie-web", release: "Alpha.2020.2160p.WEB-2", info_hash: "bbb", accepted: true, overridden: false, message: "Sent to the router" } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  await grabStoredRow(1);
  expect((await screen.findByTestId("confirm-done")).textContent).toContain("Sent to the router");
  const gets = api.calls("GET", "/titles/7/releases");
  expect(gets.map((g) => g.search)).toEqual(["?version=movie-web", "?version=movie-web&live=1"]);
  const grabs = api.calls("POST", "/titles/7/releases/grab");
  // Accepted by the fresh search: no override, although the stored list rejected it.
  expect(grabs.map((g) => g.body)).toEqual([{ version: "movie-web", info_hash: "BBB", override: false }]);
  expect(api.requests.indexOf(grabs[0])).toBeGreaterThan(api.requests.indexOf(gets[1]));
  fireEvent.click(screen.getByTestId("confirm-cancel"));
  await waitFor(() => expect(screen.getByTestId("releases-source").textContent).toBe("Live search"));
});

it("a stored row the fresh answer lacks is not grabbed and is said to be gone", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
    { method: "GET", path: "/titles/7/releases", reply: fresh([cand(1, "ccc")]) },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  await grabStoredRow(0);
  expect((await screen.findByTestId("releases-gone")).textContent).toContain("No longer offered by the indexer: Alpha.2020.2160p.WEB-1");
  expect(screen.getByTestId("releases-source").textContent).toBe("Live search");
  expect(screen.queryByTestId("grab-dialog")).toBeNull();
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(0);
});

it("a blocked search shows the search's own message and grabs nothing", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
    { method: "GET", path: "/titles/7/releases", reply: { ...stored([cand(1, "aaa")]), message: "Live search failed: no indexer answered; showing the last stored decision" } },
    { method: "GET", path: "/titles/7/releases", reply: fresh([cand(1, "aaa")]) },
    { method: "POST", path: "/titles/7/releases/grab", reply: { version: "movie-web", release: "Alpha.2020.2160p.WEB-1", info_hash: "aaa", accepted: true, overridden: false, message: "Sent to the router" } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  await grabStoredRow(0);
  expect((await screen.findByTestId("releases-message")).textContent).toBe("Live search failed: no indexer answered; showing the last stored decision");
  await waitFor(() => expect(screen.queryByTestId("grab-dialog")).toBeNull());
  expect(api.calls("GET", "/titles/7/releases")).toHaveLength(2);
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(0);
  // Grab again: a new search, whose answer this time offers the release.
  await grabStoredRow(0);
  await screen.findByTestId("confirm-done");
  expect(api.calls("GET", "/titles/7/releases").map((g) => g.search)).toEqual(["?version=movie-web", "?version=movie-web&live=1", "?version=movie-web&live=1"]);
  expect(api.calls("POST", "/titles/7/releases/grab").map((g) => g.body)).toEqual([{ version: "movie-web", info_hash: "aaa", override: false }]);
});

it("a stored row has no Grab when the version cannot search", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: { ...stored([cand(1, "aaa")]), live_allowed: false, mode: "shadow" } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-no-live");
  expect(screen.queryByTestId("release-grab")).toBeNull();
});

it("scrolls the followed release's card on a phone", () => {
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }));
  const scrolled: Element[] = [];
  const saved = Element.prototype.scrollIntoView;
  Element.prototype.scrollIntoView = function (this: Element) { scrolled.push(this); };
  renderPage(<ReleaseTable items={[cand(1, "aaa"), cand(2, "bbb")]} focus="BBB" />);
  expect(scrolled).toHaveLength(1);
  expect(scrolled[0].tagName).toBe("LI");
  expect(scrolled[0].getAttribute("data-release-hash")).toBe("bbb");
  Element.prototype.scrollIntoView = saved;
});

it("a stored row the fresh search now rejects asks again with the fresh reasons before grabbing", async () => {
  const now = { ...cand(1, "aaa", false), rejections: [{ reason: "season", message: "not a season pack" }] };
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
    { method: "GET", path: "/titles/7/releases", reply: fresh([now]) },
    { method: "POST", path: "/titles/7/releases/grab", reply: { version: "movie-web", release: "Alpha.2020.2160p.WEB-1", info_hash: "aaa", accepted: false, overridden: true, message: "Sent to the router" } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  await grabStoredRow(0);
  const dialog = await screen.findByTestId("grab-dialog");
  await waitFor(() => expect(within(dialog).getByTestId("confirm-ok").textContent).toBe("Grab anyway"));
  expect(dialog.textContent).toContain("not a season pack");
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(0);
  fireEvent.click(within(dialog).getByTestId("confirm-ok"));
  await screen.findByTestId("confirm-done");
  expect(api.calls("POST", "/titles/7/releases/grab").map((g) => g.body)).toEqual([{ version: "movie-web", info_hash: "aaa", override: true }]);
  expect(api.calls("GET", "/titles/7/releases")).toHaveLength(2);
});

it("the grab dialog stays until its grab settles", async () => {
  let settle: (v: unknown) => void = () => {};
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
    { method: "GET", path: "/titles/7/releases", reply: fresh([cand(1, "aaa")]) },
    { method: "POST", path: "/titles/7/releases/grab", reply: () => new Promise((r) => { settle = r; }) },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  await grabStoredRow(0);
  await waitFor(() => expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(1));
  fireEvent.click(screen.getByTestId("confirm-cancel"));
  expect(screen.getByTestId("grab-dialog")).toBeTruthy();
  settle({ version: "movie-web", release: "Alpha.2020.2160p.WEB-1", info_hash: "aaa", accepted: true, overridden: false, message: "Sent to the router" });
  await screen.findByTestId("confirm-done");
  fireEvent.click(screen.getByTestId("confirm-cancel"));
  await waitFor(() => expect(screen.queryByTestId("grab-dialog")).toBeNull());
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(1);
});

/** Render with a client that keeps unused answers and retries like the app's. */
function renderKept(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity, retry: (n) => n < 2, retryDelay: 0 }, mutations: { retry: false } } });
  render(<MemoryRouter><QueryClientProvider client={client}>{ui}</QueryClientProvider></MemoryRouter>);
  return client;
}

it("a stored row's grab waits for the new search, never an older live answer", async () => {
  let answer: (v: unknown) => void = () => {};
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
    { method: "GET", path: "/titles/7/releases", reply: () => new Promise((r) => { answer = r; }) },
  ]);
  const client = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity, retry: false } } });
  // An older live answer that still offered the release.
  client.setQueryData(qk.releases(7, "movie-web", true), fresh([cand(1, "aaa")]));
  render(<MemoryRouter><QueryClientProvider client={client}><ShowReleases titleId={7} defaultOpen initialVersion="movie-web" /></QueryClientProvider></MemoryRouter>);
  await screen.findByTestId("releases-grabbed");
  await grabStoredRow(0);
  await waitFor(() => expect(api.calls("GET", "/titles/7/releases")).toHaveLength(2));
  await new Promise((r) => setTimeout(r, 20));
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(0);
  answer(fresh([cand(1, "ccc")]));
  await screen.findByTestId("releases-gone");
  expect(api.calls("POST", "/titles/7/releases/grab")).toHaveLength(0);
});

it("a live search is not retried on its own", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
    { method: "GET", path: "/titles/7/releases", reply: { status: 502, body: { error: "bad gateway" } } },
  ]);
  renderKept(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  fireEvent.click(await screen.findByTestId("releases-live"));
  await screen.findByText("bad gateway");
  await new Promise((r) => setTimeout(r, 20));
  expect(api.calls("GET", "/titles/7/releases").map((g) => g.search)).toEqual(["?version=movie-web", "?version=movie-web&live=1"]);
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

it("a manager has no Add by magnet or hash", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "GET", path: "/titles/7/releases", reply: stored([cand(1, "aaa")]) },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  expect(screen.queryByTestId("add-by-hash")).toBeNull();
});
