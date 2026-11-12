// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { MetadataFranchise } from "./anime-mappings";

it("shows episode numbering and alternate cuts in the franchise", async () => {
  mockFetch([{ method: "GET", path: "/anime/franchises/example", reply: { slug: "example", name: "Example",
    orders: [{ kind: "watch", items: [], alternates: [{ key: "al:2", title: "Alternate story", alternate_of: "al:1", source: "Override route" }] }],
    alt_cuts: [{ name: "Director's Cut", tvdb: 123, entry: "al:3", episodes: { 1: [[1, 1], [1, 2]] } }],
    numbering: [{ tvdb: 123, episode: "S01E02", absolute: 2, entry: "al:1", entry_title: "Main story", entry_number: "2", status: "held", source: "scudlee", importable: false }] } }]);
  renderPage(<Routes><Route path="/anime/:slug" element={<MetadataFranchise />} /></Routes>, "/anime/example");
  expect(await screen.findByText("Alternate story")).toBeTruthy();
  expect(screen.getByText("Override route")).toBeTruthy();
  expect(screen.getByText("Director's Cut")).toBeTruthy();
  expect(screen.getByText("S01E01, S01E02")).toBeTruthy();
  expect(screen.getByText("Main story")).toBeTruthy();
  expect(screen.getByText("scudlee")).toBeTruthy();
  expect(screen.getByText("Sonarr's numbering")).toBeTruthy();
  expect(screen.queryByRole("link", { name: /classic/i })).toBeNull();
});

it("shows where to stop and resume around an interleaved special and each entry's IDs", async () => {
  mockFetch([{ method: "GET", path: "/anime/franchises/split", reply: { slug: "split", name: "Split story", orders: [{ kind: "watch", items: [
    { n: 1, key: "al:1", title: "Main series", part: "up to before S01E07", tvdb: "123", tmdb: [456] },
    { n: 2, key: "al:2", title: "Interleaved special", tvdb: "789", tmdb: [101, 102] },
    { n: 3, key: "al:1", title: "Main series", part: "from S01E07", tvdb: "123", tmdb: [456] },
  ] }] } }]);
  renderPage(<Routes><Route path="/anime/:slug" element={<MetadataFranchise />} /></Routes>, "/anime/split");
  const stop = (await screen.findByText("(up to before S01E07)")).closest("tr")!;
  expect(stop.textContent).toContain("Main series");
  expect(stop.textContent).toContain("TVDB 123");
  expect(stop.textContent).toContain("TMDb 456");
  const resume = screen.getByText("(from S01E07)").closest("tr")!;
  expect(resume.textContent).toContain("Main series");
  const special = screen.getByText("Interleaved special").closest("tr")!;
  expect(special.textContent).toContain("TVDB 789");
  expect(special.textContent).toContain("TMDb 101, 102");
  expect(screen.getAllByRole("row").slice(1, 4)).toEqual([stop, special, resume]);
});


