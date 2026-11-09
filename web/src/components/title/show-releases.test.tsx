// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it } from "vitest";
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
    { method: "GET", path: "/titles/7/releases", reply: fresh([cand(1, "bbb", false), cand(2, "aaa")]) },
    { method: "POST", path: "/titles/7/releases/grab", reply: { version: "movie-web", release: "Alpha.2020.2160p.WEB-2", info_hash: "bbb", accepted: false, overridden: true, message: "Sent to the router" } },
  ]);
  renderPage(<ShowReleases titleId={7} defaultOpen initialVersion="movie-web" />);
  await screen.findByTestId("releases-grabbed");
  await grabStoredRow(1);
  expect((await screen.findByTestId("confirm-done")).textContent).toContain("Sent to the router");
  const gets = api.calls("GET", "/titles/7/releases");
  expect(gets.map((g) => g.search)).toEqual(["?version=movie-web", "?version=movie-web&live=1"]);
  const grabs = api.calls("POST", "/titles/7/releases/grab");
  expect(grabs.map((g) => g.body)).toEqual([{ version: "movie-web", info_hash: "BBB", override: true }]);
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
