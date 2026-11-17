import { describe, expect, it } from "vitest";
import {
  columnSort,
  defaultColumns,
  defaultDesc,
  indexColumns,
  languageWords,
  listColumns,
  perLibrary,
  sortColumn,
  visibleIndexColumns,
  type IndexColumnId,
} from "./index-columns";
import { normalizeColumnPrefs } from "./prefs";
import { titleStatusWords } from "./tmdb-words";

const ids = (cs: { id: string }[]) => cs.map((c) => c.id);
const label = (list: "movies" | "series" | "anime", id: string, seeFiles = true) => indexColumns(list, seeFiles).find((c) => c.id === id)?.label;

describe("the lists' columns", () => {
  it("offers series-only and movie-only columns to the right list, under that list's label", () => {
    for (const id of ["seasons", "previous", "series_type"]) {
      expect(ids(listColumns("series"))).toContain(id);
      expect(ids(listColumns("anime"))).toContain(id);
      expect(ids(listColumns("movies"))).not.toContain(id);
    }
    for (const id of ["in_cinemas", "digital", "physical", "min_availability"]) {
      expect(ids(listColumns("movies"))).toContain(id);
      expect(ids(listColumns("series"))).not.toContain(id);
    }
    expect(label("movies", "network")).toBe("Studio");
    expect(label("series", "network")).toBe("Network");
    expect(label("movies", "next")).toBe("Next release");
    expect(label("anime", "next")).toBe("Next episode / release"); // Anime mixes movies and series
    expect(label("movies", "release")).toBe("Release date");
    expect(label("series", "release")).toBe("First aired");
    expect(label("series", "title_status")).toBe("Series status");
    expect(listColumns("movies")).toHaveLength(24);
    expect(listColumns("series")).toHaveLength(23);
  });
  it("gives Anime's Movies segment the movie columns and words and its Series segment the series words", () => {
    const movie = indexColumns("anime", true, "movie");
    for (const id of ["seasons", "previous", "series_type"]) expect(ids(movie)).not.toContain(id);
    expect(movie.find((c) => c.id === "network")?.label).toBe("Studio");
    expect(movie.find((c) => c.id === "next")?.label).toBe("Next release");
    expect(movie.find((c) => c.id === "release")?.label).toBe("Release date");
    for (const c of movie) expect(c.hint).not.toMatch(/season|episode|series|air(ed|ing)|continuing|ended/i);
    const series = indexColumns("anime", true, "series");
    expect(ids(series)).toEqual(ids(indexColumns("anime", true)));
    expect(series.find((c) => c.id === "network")?.label).toBe("Network");
    expect(series.find((c) => c.id === "release")?.label).toBe("First aired");
    // All keeps the mixed words; a stored choice with Seasons shows it again on Series and All.
    expect(label("anime", "network")).toBe("Network / studio");
    const p = { order: ["status", "seasons", "network"] as IndexColumnId[], shown: ["status", "seasons", "network"] as IndexColumnId[] };
    expect(visibleIndexColumns(p, "anime", true, "movie")).toEqual(["status", "network"]);
    expect(visibleIndexColumns(p, "anime", true, "series")).toEqual(["status", "seasons", "network"]);
    expect(visibleIndexColumns(p, "anime", true)).toEqual(["status", "seasons", "network"]);
  });
  it("explains Movies' columns without seasons, episodes or series words (Opus C25)", () => {
    for (const c of [...listColumns("movies"), ...indexColumns("movies", false)]) expect(c.hint).not.toMatch(/season|episode|series|air(ed|ing)|continuing|ended/i);
    const hint = (list: "movies" | "series" | "anime", id: string) => listColumns(list).find((c) => c.id === id)?.hint;
    expect(hint("series", "next")).toBe("The next episode's air date");
    expect(hint("series", "release")).toBe("The first episode's air date");
    expect(hint("anime", "next")).toBe("The next episode or coming release");
  });

  it("does not offer the file columns without see files, and heads Status Availability", () => {
    const plain = ids(indexColumns("movies", false));
    for (const id of ["libraries", "quality", "profile", "size", "min_availability", "provider", "folder"]) expect(plain).not.toContain(id);
    expect(label("movies", "status", false)).toBe("Availability");
    expect(label("movies", "status")).toBe("Status");
    // A stored choice that shows a file column still hides it for this account.
    const p = { order: ["status", "size", "libraries", "year"] as const, shown: ["status", "size", "libraries", "year"] as const };
    expect(visibleIndexColumns({ order: [...p.order], shown: [...p.shown] }, "movies", false)).toEqual(["status", "year"]);
    expect(visibleIndexColumns({ order: [...p.order], shown: [...p.shown] }, "movies", true)).toEqual(["status", "size", "libraries", "year"]);
  });

  it("starts each list with its default columns", () => {
    // Movies: the title carries its year; Studio is offered, not on by default.
    expect(defaultColumns("movies")).toEqual(["status", "libraries", "size", "added"]);
    expect(defaultColumns("series")).toEqual(["status", "libraries", "network", "next", "seasons", "size"]);
    expect(defaultColumns("anime")).toEqual(defaultColumns("series"));
    // Nothing stored: the defaults; the old shared five-column choice is a different key and never read.
    expect(normalizeColumnPrefs(listColumns("movies"), defaultColumns("movies"))(undefined).shown).toEqual(defaultColumns("movies"));
  });

  it("sorts Added by created and Latest file by added; the other headers by their own sort", () => {
    expect(columnSort("added")).toBe("created");
    expect(columnSort("latest_file")).toBe("added");
    expect(sortColumn("created")).toBe("added");
    expect(sortColumn("added")).toBe("latest_file");
    expect(columnSort("title")).toBe("name");
    expect(columnSort("network")).toBe("network");
    expect(columnSort("seasons")).toBeUndefined();
    expect(columnSort("libraries")).toBeUndefined();
    expect(sortColumn("completion")).toBeUndefined();
    expect(defaultDesc("network")).toBe(false);
    expect(defaultDesc("certification")).toBe(false);
    expect(defaultDesc("created")).toBe(true);
  });

  it("words statuses, languages and per-library values", () => {
    expect(titleStatusWords("Returning Series")).toBe("Continuing");
    expect(titleStatusWords("Canceled")).toBe("Cancelled");
    expect(titleStatusWords("Planned")).toBe("Upcoming");
    expect(titleStatusWords("Something")).toBe("Something");
    expect(titleStatusWords("inCinemas")).toBe("In cinemas");
    expect(languageWords("ja")).toBe("Japanese");
    const vs = [{ label: "4K" }, { label: "WEB" }];
    expect(perLibrary(vs, () => "HD")).toBe("HD");
    expect(perLibrary(vs, (i) => (i ? "Any" : "UHD"))).toBe("4K: UHD · WEB: Any");
    expect(perLibrary(vs, (i) => (i ? "Any" : ""))).toBe("WEB: Any");
  });
});

it("words the Anime list's mixed columns for movies and series alike", () => {
  const label = (id: string) => listColumns("anime").find((c) => c.id === id)?.label;
  expect([label("network"), label("next"), label("release")]).toEqual(["Network / studio", "Next episode / release", "Released / first aired"]);
  expect(listColumns("series").find((c) => c.id === "network")?.label).toBe("Network");
});
