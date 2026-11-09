// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
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
