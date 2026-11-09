import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { qk, titleChanged } from "./query-keys";

describe("query keys", () => {
  it("builds the same title key from a number and a string", () => {
    expect(qk.title(5)).toEqual(qk.title("5"));
    expect(qk.episodes(5, 1)).toEqual(qk.episodes("5", 1));
  });
  it("lets an invalidation by numeric id reach the title page, its episodes and the home rows", () => {
    const qc = new QueryClient();
    // Cached as the screens key them: the title page from its route param (a string).
    const hit = [qk.title("5"), qk.episodes("5"), qk.discover.home, qk.discover.row("missing"), qk.titles.grid({ kind: "movie" })];
    const miss = [qk.title("50"), qk.releases(5, "movie-web", true)];
    for (const k of [...hit, ...miss]) qc.setQueryData(k, {});
    // Invalidated as a mutation or a live event does it: by the number in the API answer.
    const reached = new Set(titleChanged(5).flatMap((queryKey) => qc.getQueryCache().findAll({ queryKey }).map((q) => q.queryHash)));
    for (const k of hit) expect(reached.has(JSON.stringify(k)), JSON.stringify(k)).toBe(true);
    // Never another title, and never a live releases search.
    for (const k of miss) expect(reached.has(JSON.stringify(k)), JSON.stringify(k)).toBe(false);
  });
});
