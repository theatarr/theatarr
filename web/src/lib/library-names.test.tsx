// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { VersionTag } from "@/components/page";
import { LibraryNamesProvider, libraryMarks, markRules, MARKS } from "./library-names";

const LIBS = [
  { key: "tv-2160p-mm", label: "4K" },
  { key: "movie-web-mm", label: "4K WEB" },
  { key: "anime-tv-mm", label: "Anime series" },
  { key: "anime-movie-mm", label: "Anime movies" },
  { key: "movie-remux-mm", label: "4K Remux" },
];

afterEach(cleanup);

describe("library colour marks", () => {
  it("gives five libraries five different marks, the same on every load", () => {
    const keys = LIBS.map((l) => l.key);
    const marks = libraryMarks(keys);
    expect(new Set(marks.values()).size).toBe(5);
    // Another order (a reload, a differently sorted response) gives the same marks.
    expect(libraryMarks([...keys].reverse())).toEqual(marks);
    expect([...marks.values()].every((m) => m >= 0 && m < MARKS)).toBe(true);
    expect(markRules(["movie-web-mm"])).toBe('[data-vkey="movie-web-mm"]{--vc:var(--v-0)}');
  });
});

describe("the library tag", () => {
  it("shows the library's name, with the key as tooltip and data-vkey for an account that sees files", () => {
    render(<LibraryNamesProvider libraries={LIBS} keys><VersionTag k="anime-tv-mm" /></LibraryNamesProvider>);
    const tag = screen.getByText("Anime series");
    expect(tag.getAttribute("data-vkey")).toBe("anime-tv-mm");
    expect(tag.getAttribute("title")).toBe("anime-tv-mm");
    expect(screen.queryByText(/anime-tv-mm/)).toBeNull();
  });

  it("never shows a household account the key, not even as tooltip", () => {
    render(<LibraryNamesProvider libraries={LIBS}><VersionTag k="anime-movie-mm" /></LibraryNamesProvider>);
    const tag = screen.getByText("Anime movies");
    expect(tag.getAttribute("title")).toBeNull();
  });

  it("names a key the server does not list by the default its key suggests", () => {
    render(<LibraryNamesProvider libraries={LIBS}><VersionTag k="movie-remux" /></LibraryNamesProvider>);
    expect(screen.getByText("Remux")).toBeTruthy();
  });

  it("puts the marks for the listed libraries on the page", () => {
    const { container } = render(<LibraryNamesProvider libraries={LIBS}><span /></LibraryNamesProvider>);
    expect(container.querySelector("style")?.textContent?.split("\n")).toHaveLength(5);
  });
});
