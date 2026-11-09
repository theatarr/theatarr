import { describe, expect, it } from "vitest";
import { addRequest, libraryTitleId } from "./add-request";

const choice = { versions: ["tv-2160p"], seasons: [{ season: 1, monitored: true }], search: false, anime: false };

describe("add sheet request", () => {
  it("adds to a title from its page by title_id, even a series with only a TVDB id", () => {
    // The title page's target: no TMDb id at all.
    const target = { kind: "series" as const, name: "The Show", year: 2019, title_id: 10 };
    const body = addRequest(target, { kind: "series" }, choice);
    expect(body).toEqual({ title_id: 10, kind: "series", versions: ["tv-2160p"], seasons: [{ season: 1, monitored: true }], search: false });
    expect(body.tmdb_id).toBeUndefined();
  });
  it("uses the title add/options found by TMDb id", () => {
    const target = { kind: "movie" as const, name: "Alpha", tmdb_id: 101 };
    expect(libraryTitleId(target, { title_id: 1 })).toBe(1);
    expect(addRequest(target, { kind: "movie", title_id: 1 }, { ...choice, versions: ["movie-web"], seasons: undefined })).toMatchObject({
      title_id: 1,
      versions: ["movie-web"],
    });
  });
  it("sends a new title by kind and TMDb id with its name, year and anime flag", () => {
    const target = { kind: "series" as const, name: "Kaiju", year: 2023, tmdb_id: 4321, anime: true };
    expect(libraryTitleId(target, {})).toBeUndefined();
    expect(addRequest(target, { kind: "series" }, { ...choice, anime: true, search: true })).toEqual({
      kind: "series",
      tmdb_id: 4321,
      title: "Kaiju",
      year: 2023,
      anime: true,
      versions: ["tv-2160p"],
      seasons: [{ season: 1, monitored: true }],
      search: true,
    });
  });
});
