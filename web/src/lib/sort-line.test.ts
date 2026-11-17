import { describe, expect, it } from "vitest";
import type { TitleRow } from "@/api/library";
import { sortLineFor } from "./sort-line";

const film = { id: 1, kind: "movie", name: "Akira", rating: 7.94, size_bytes: 52e9, runtime_minutes: 124, released: "1988-07-16", title_added_at: "2025-10-01T12:00:00Z", added_at: new Date(Date.now() - 3 * 86400e3).toISOString(), versions: [] } as unknown as TitleRow;
const line = (sort: string, t: TitleRow = film, seeFiles = true) => sortLineFor(sort, seeFiles)?.(t);

describe("the posters' sort line", () => {
  it("shows the active sort's value as the table says it", () => {
    expect(line("rating")).toBe("Rating 7.9");
    expect(line("size")).toBe("Size 48.4 GiB");
    expect(line("runtime")).toBe("Runtime 2 h 4 min");
    expect(line("created")).toBe("Added Oct 1, 2025");
    expect(line("added")).toBe("Latest file 3 d ago");
    expect(line("release")).toBe("Released Jul 16, 1988");
    expect(line("release", { ...film, kind: "series", released: "2019-03-01" } as TitleRow)).toBe("First aired Mar 1, 2019");
  });
  it("has none for title and year, and an empty line for a missing value", () => {
    for (const s of ["name", "year", "next", "network"]) expect(sortLineFor(s, true)).toBeUndefined();
    expect(line("rating", { ...film, rating: undefined } as TitleRow)).toBe("");
    expect(line("release", { ...film, released: undefined } as TitleRow)).toBe("");
  });
  it("shows no size or latest file to a household account", () => {
    expect(sortLineFor("size", false)).toBeUndefined();
    expect(sortLineFor("added", false)).toBeUndefined();
    expect(line("rating", film, false)).toBe("Rating 7.9");
  });
});
