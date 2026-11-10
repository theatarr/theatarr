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
  it("adds no key for a choice left at its default, so the body is the one sent before the choices existed", () => {
    const series = { kind: "series" as const, name: "Kaiju", year: 2023, tmdb_id: 4321 };
    expect(JSON.stringify(addRequest(series, { kind: "series" }, { ...choice, monitorNew: true, monitor: undefined }))).toBe(
      '{"kind":"series","tmdb_id":4321,"title":"Kaiju","year":2023,"anime":false,"versions":["tv-2160p"],"seasons":[{"season":1,"monitored":true}],"search":false}',
    );
    const movie = { kind: "movie" as const, name: "Alpha", year: 2024, tmdb_id: 101 };
    expect(JSON.stringify(addRequest(movie, { kind: "movie" }, { versions: ["movie-web"], search: false, anime: false, monitorNew: true }))).toBe(
      '{"kind":"movie","tmdb_id":101,"title":"Alpha","year":2024,"anime":false,"versions":["movie-web"],"search":false}',
    );
  });
  it("sends each choice as its field, a preset without the seasons", () => {
    const series = { kind: "series" as const, name: "Kaiju", year: 2023, tmdb_id: 4321 };
    expect(addRequest(series, { kind: "series" }, { ...choice, monitor: "pilot", monitorNew: false })).toEqual({
      kind: "series", tmdb_id: 4321, title: "Kaiju", year: 2023, anime: false, versions: ["tv-2160p"], seasons: undefined, search: false,
      monitor: "pilot", monitor_new: "none",
    });
    // A movie takes neither.
    const body = addRequest({ kind: "movie" as const, name: "Alpha", tmdb_id: 101 }, { kind: "movie" }, { versions: ["movie-web"], search: false, anime: false, monitor: "pilot", monitorNew: false });
    expect(body.monitor ?? body.monitor_new).toBeUndefined();
  });
  it("sends monitor all with later seasons off unless a preset or season picks decide the first seasons", () => {
    const series = { kind: "series" as const, name: "Kaiju", year: 2023, tmdb_id: 4321 };
    // No episodes known yet (the usual new series): no season list.
    expect(addRequest(series, { kind: "series" }, { ...choice, seasons: undefined, monitorNew: false })).toEqual({
      kind: "series", tmdb_id: 4321, title: "Kaiju", year: 2023, anime: false, versions: ["tv-2160p"], seasons: undefined, search: false,
      monitor: "all", monitor_new: "none",
    });
    const picks = addRequest(series, { kind: "series" }, { ...choice, monitorNew: false });
    expect(picks).toMatchObject({ seasons: [{ season: 1, monitored: true }], monitor_new: "none" });
    expect("monitor" in picks).toBe(false);
    expect(addRequest(series, { kind: "series" }, { ...choice, seasons: undefined, monitor: "none", monitorNew: false })).toMatchObject({ monitor: "none", monitor_new: "none" });
    // Later seasons on: no monitor of its own.
    expect("monitor" in addRequest(series, { kind: "series" }, { ...choice, seasons: undefined, monitorNew: true })).toBe(false);
  });
  it("sends a new series' type and a movie's minimum availability only when set", () => {
    const series = { kind: "series" as const, name: "Kaiju", year: 2023, tmdb_id: 4321 };
    expect(addRequest(series, { kind: "series" }, { ...choice, seriesType: "daily", minimumAvailability: "announced" })).toEqual({
      kind: "series", tmdb_id: 4321, title: "Kaiju", year: 2023, anime: false, versions: ["tv-2160p"], seasons: [{ season: 1, monitored: true }], search: false, series_type: "daily",
    });
    const movie = { kind: "movie" as const, name: "Alpha", year: 2024, tmdb_id: 101 };
    const base = { versions: ["movie-web"], search: false, anime: false };
    // "Library default" is no choice: the body is the one of a plain add. Released is a choice like the others.
    expect(JSON.stringify(addRequest(movie, { kind: "movie" }, { ...base, minimumAvailability: undefined, seriesType: "daily" }))).toBe(
      '{"kind":"movie","tmdb_id":101,"title":"Alpha","year":2024,"anime":false,"versions":["movie-web"],"search":false}',
    );
    expect(addRequest(movie, { kind: "movie" }, { ...base, minimumAvailability: "released" })).toMatchObject({ minimum_availability: "released" });
    // A movie already in the library takes the choice for the libraries added now; a series there takes no type.
    expect(addRequest({ kind: "movie" as const, name: "Alpha", title_id: 1 }, { kind: "movie" }, { ...base, minimumAvailability: "announced" })).toEqual({
      title_id: 1, kind: "movie", versions: ["movie-web"], seasons: undefined, search: false, minimum_availability: "announced",
    });
    expect(addRequest({ kind: "series" as const, name: "The Show", title_id: 10 }, { kind: "series" }, { ...choice, seriesType: "daily", minimumAvailability: "announced" })).toEqual({
      title_id: 10, kind: "series", versions: ["tv-2160p"], seasons: [{ season: 1, monitored: true }], search: false,
    });
  });
  it("sends neither choice for a title already in the library", () => {
    expect(addRequest({ kind: "series" as const, name: "The Show", title_id: 10 }, { kind: "series" }, { ...choice, monitor: "pilot", monitorNew: false })).toEqual({
      title_id: 10, kind: "series", versions: ["tv-2160p"], seasons: [{ season: 1, monitored: true }], search: false,
    });
  });
});
