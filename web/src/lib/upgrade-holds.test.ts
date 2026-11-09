import { describe, expect, it } from "vitest";
import type { UpgradeHold } from "@/api/upgrades";
import { approveState, decisionLine, episodesText, holdLines, holdsFor } from "./upgrade-holds";

const hold = (o: Partial<UpgradeHold> = {}): UpgradeHold => ({
  id: 1,
  title: { id: 5, name: "Dune", kind: "movie", poster: "" },
  version: "movie-remux",
  version_label: "4K Remux",
  release: "Dune.2021.2160p.UHD.BluRay.REMUX-GRP",
  quality: "Remux-2160p",
  score: 3000,
  current: { quality: "WEBDL-2160p", score: 1400, files: 1 },
  state: "pending",
  created_at: "2026-10-26T10:00:00Z",
  seen_at: "2026-10-26T10:00:00Z",
  can_approve: true,
  ...o,
});

describe("upgrade holds", () => {
  it("compares the held release with the file it replaces", () => {
    expect(holdLines(hold())).toEqual({ held: "Remux-2160p · score 3,000", replaces: "WEBDL-2160p · score 1,400" });
    expect(holdLines(hold({ current: { quality: "WEBDL-1080p", files: 8 } })).replaces).toBe("8 episode files (WEBDL-1080p)");
  });
  it("names episode runs", () => {
    expect(episodesText(undefined)).toBe("");
    expect(episodesText(["S01E03"])).toBe("S01E03");
    expect(episodesText(["S01E01", "S01E02", "S01E10"])).toBe("S01E01–S01E10");
  });
  it("keeps a copy's pending holds", () => {
    const hs = [hold(), hold({ id: 2, version: "movie-web" }), hold({ id: 3, state: "declined" })];
    expect(holdsFor(hs, "movie-remux").map((h) => h.id)).toEqual([1]);
    expect(holdsFor(undefined, "movie-remux")).toEqual([]);
  });
  it("disables approve with the server's reason", () => {
    expect(approveState(hold())).toEqual({ approve: true, decline: true });
    expect(approveState(hold(), true)).toEqual({ approve: false, decline: false });
    const off = approveState(hold({ can_approve: false, approve_reason: "This copy is still in shadow mode" }));
    expect(off).toEqual({ approve: false, decline: true, reason: "This copy is still in shadow mode" });
    expect(approveState(hold({ state: "approved" }))).toMatchObject({ approve: false, decline: false, reason: "Approved" });
  });
  it("words the decision with who and how", () => {
    expect(decisionLine(hold())).toBe("");
    const by = { id: 7, username: "sam", display_name: "Sam" };
    expect(decisionLine(hold({ state: "approved", decided_by: by, decided_via: "session", route_outcome: "added" }))).toBe("Approved by Sam (session) · added");
    expect(decisionLine(hold({ state: "declined", decided_by: by, decided_via: "key phone", note: "too big" }))).toBe("Declined by Sam (key phone): too big");
    expect(decisionLine(hold({ state: "expired", note: "a newer file arrived" }))).toBe("Expired: a newer file arrived");
  });
});
