import { describe, expect, it } from "vitest";
import { outcomeWords, rangeWords, reviewQuestion, type ReviewItem } from "./anime-review";

describe("rangeWords", () => {
  it("says a season range, specials, one episode and two seasons", () => {
    expect(rangeWords(Array.from({ length: 12 }, (_, i) => [2, 13 + i]))).toBe("Season 2, episodes 13–24");
    expect(rangeWords([[0, 5], [0, 6]])).toBe("Specials 5–6");
    expect(rangeWords([[1, 3]])).toBe("Season 1, episode 3");
    expect(rangeWords([[2, 1], [1, 12], [1, 13], [2, 2]])).toBe("Season 1, episodes 12–13; Season 2, episodes 1–2");
  });
});

const mt = { tvdb: 371310, title_id: 9, name: "Mushoku Tensei" };
const entry = { key: "al:166873", title: "Mushoku Tensei II Part 2", episodes: 12 };
const opt = (eps: number[][]) => ({ proposed: true, series: mt, episodes: eps, count: eps.length });

describe("reviewQuestion", () => {
  it("asks one plain question per kind", () => {
    const base: ReviewItem = { id: 1, franchise: "mushoku-tensei", franchise_name: "Mushoku Tensei", entry_key: "al:166873", state: "open", entry };
    expect(reviewQuestion({ ...base, kind: "conflict", options: [opt([[2, 13], [2, 14]])] })).toBe(
      "Which Mushoku Tensei episodes are “Mushoku Tensei II Part 2”? The mapping lists disagree, so until you choose its episodes keep Sonarr's numbering.");
    expect(reviewQuestion({ ...base, kind: "heuristic", options: [opt([[2, 13], [2, 14]])] })).toBe(
      "Is “Mushoku Tensei II Part 2” Mushoku Tensei Season 2, episodes 13–14? No mapping list covers it; Theatarr matched it by air date and title and uses this match now.");
    expect(reviewQuestion({ ...base, kind: "refresh_shrink", entry: undefined, entry_key: "tvdb:371310", refresh: { series: mt, before: 48, after: 36, items: 1 } })).toBe(
      "Apply the refresh of Mushoku Tensei? It would map 36 of the 48 Mushoku Tensei episodes mapped now. Nothing changes until you choose.");
    expect(reviewQuestion({ ...base, kind: "refresh_moves_linked", refresh: { from: opt([[2, 13]]), to: opt([[0, 40]]), with_files: 1, items: 2 } })).toBe(
      "Apply the refresh of Mushoku Tensei? The refresh maps “Mushoku Tensei II Part 2” to Mushoku Tensei Special 40 instead of Mushoku Tensei Season 2, episode 13. 1 of these episodes have files; files already linked for them may be moved to match.");
  });

  it("does not promise a move when the refresh maps the entry to nothing", () => {
    const it: ReviewItem = { id: 2, franchise: "mushoku-tensei", franchise_name: "Mushoku Tensei", entry_key: "al:166873", kind: "refresh_moves_linked",
      state: "open", entry, refresh: { from: opt([[2, 13], [2, 14]]), with_files: 2, items: 1 } };
    expect(reviewQuestion(it)).toBe(
      "Apply the refresh of Mushoku Tensei? The refresh stops mapping “Mushoku Tensei II Part 2” (Mushoku Tensei Season 2, episodes 13–14), so new files take Sonarr's numbering. 2 of these episodes have files; files already linked for them may be moved to match.");
  });

  it("falls back to TVDB {id} and the English title", () => {
    const it: ReviewItem = { id: 3, franchise: "x", entry_key: "al:1", kind: "conflict", state: "open", entry: { key: "al:1", title_en: "Jobless Reincarnation" },
      options: [{ proposed: true, series: { tvdb: 5 }, episodes: [[1, 1]], count: 1 }] };
    expect(reviewQuestion(it)).toMatch(/^Which TVDB 5 episodes are “Jobless Reincarnation”\?/);
  });
});

describe("a resolved item", () => {
  it("says what was decided, not that the match is in use", () => {
    const it: ReviewItem = { id: 5, franchise: "mushoku-tensei", entry_key: "al:166873", kind: "heuristic", state: "rejected", entry,
      options: [opt([[2, 13], [2, 14]])] };
    expect(reviewQuestion(it)).toBe("Is “Mushoku Tensei II Part 2” Mushoku Tensei Season 2, episodes 13–14? No mapping list covered it; Theatarr matched it by air date and title.");
    expect(outcomeWords(it)).toBe("Rejected: the match is not used; new files follow Sonarr's numbering.");
    expect(outcomeWords({ ...it, state: "accepted", note: "checked" })).toBe("Accepted: the match or the mapping chosen is used. Note: checked");
  });
});
