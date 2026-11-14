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
    expect(listColumns("movies")).toHaveLength(23);
    expect(listColumns("series")).toHaveLength(22);
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
    expect(defaultColumns("movies")).toEqual(["status", "libraries", "network", "size", "added"]);
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
