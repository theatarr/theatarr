import { describe, expect, it } from "vitest";
import { ApiError } from "@/api/client";
import { facetOf, hitTarget, searchProblem, tmdbNote } from "./search-results";

describe("search results", () => {
  it("picks the facet", () => {
    expect(facetOf("movie", false)).toBe("movie");
    expect(facetOf("series", false)).toBe("series");
    expect(facetOf("series", true)).toBe("anime");
  });
  it("turns a TMDb hit into an add target", () => {
    const t = hitTarget({ media_type: "tv", kind: "series", tmdb_id: 5, name: "Kaiju", anime: true, in_library: false, requested: false, year: 2023 });
    expect(t).toMatchObject({ kind: "series", tmdb_id: 5, name: "Kaiju", anime: true, year: 2023 });
    expect(t.title_id).toBeUndefined();
  });
  it("explains a missing TMDb part", () => {
    expect(tmdbNote("ok")).toBeNull();
    expect(tmdbNote("off")).toMatch(/no TMDb key/);
    expect(tmdbNote("error")).toBeNull();
  });
  it("tells a failed search from an empty one", () => {
    expect(searchProblem(null, { tmdb_state: "ok" }, "tmdb")).toBeNull();
    expect(searchProblem(null, { tmdb_state: "skipped" })).toBeNull();
    expect(searchProblem(new TypeError("Failed to fetch"))?.kind).toBe("transport");
    expect(searchProblem(new ApiError(500, "boom"))).toMatchObject({ kind: "api", detail: "The server answered 500: boom" });
    expect(searchProblem(null, { tmdb_state: "error", tmdb_error: "TMDb search failed" }, "tmdb")).toMatchObject({ kind: "tmdb", detail: "TMDb search failed" });
    // The library part ignores the TMDb state.
    expect(searchProblem(null, { tmdb_state: "error" })).toBeNull();
  });
});
