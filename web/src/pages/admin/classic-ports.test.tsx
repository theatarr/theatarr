// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { ShadowDiffPage } from "./shadow";
import { MetadataFranchise } from "./anime-mappings";

it("shows every candidate's formats, score and rejection reasons without a classic link", async () => {
  const candidate = (release: string, score: number) => ({ release, quality: "WEB", score, accepted: false,
    formats: [{ name: `${release} format`, score }], rejections: [1, 2, 3].map(n => ({ message: `${release} reason ${n}` })) });
  mockFetch([{ method: "GET", path: "/shadow/diffs/7", reply: { id: 7, title: "Film", title_id: 1,
    grab: { arr_decision: candidate("Arr", -100), pick: candidate("Pick", 200), results: [candidate("First", 10), candidate("Last", -20)] },
    rss_pick: { decision: candidate("RSS", 300) } } }]);
  renderPage(<Routes><Route path="/comparison/:id" element={<ShadowDiffPage />} /></Routes>, "/comparison/7");
  for (const name of ["Arr", "Pick", "First", "Last", "RSS"]) {
    expect(await screen.findByText(`${name} format`)).toBeTruthy();
    expect(screen.getByText(`${name} reason 3`)).toBeTruthy();
  }
  const row = screen.getByText("Last format").closest("tr")!;
  expect(within(row).getAllByText("-20").length).toBeGreaterThan(0);
  // Long release names wrap so the table fits a desktop page instead of hiding the Decision column.
  expect(row.querySelector("td")!.className).toContain("[overflow-wrap:anywhere]");
  expect(screen.queryByRole("link", { name: /classic/i })).toBeNull();
});

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

it("shows existing file evidence and both stored router plans", async () => {
  mockFetch([{ method: "GET", path: "/shadow/diffs/8", reply: {
    id: 8, title: "Film", detail: "Long.Release.Without.Spaces",
    grab: { pick: { release: "Pick", existing: { quality: "Bluray-1080p", score: 75, formats: [{ name: "Existing HDR", score: 50 }] }, upgrade: true },
      router_plan: { summary: "Search refused", steps: [{ release: "First.Release", provider: "rd", outcome: "refused", code: "not_cached", message: "No cached copy" }, { release: "Second.Release", provider: "rd", outcome: "accepted", code: "cached", message: "Cached copy" }] } },
    rss_pick: { router_plan: { summary: "RSS refused", steps: [{ provider: "tb", outcome: "skipped", code: "disabled", message: "Provider disabled" }] } },
  } }]);
  renderPage(<Routes><Route path="/comparison/:id" element={<ShadowDiffPage />} /></Routes>, "/comparison/8");
  expect(await screen.findByText("Bluray-1080p · score 75")).toBeTruthy();
  expect(screen.getByText("Existing HDR")).toBeTruthy();
  expect(screen.getByText("Upgrade: yes")).toBeTruthy();
  expect(screen.getByText("Long.Release.Without.Spaces").className).toContain("[overflow-wrap:anywhere]");
  expect(screen.getByRole("row", { name: "First.Release rd refused not_cached No cached copy" })).toBeTruthy();
  expect(screen.getByRole("row", { name: "Second.Release rd accepted cached Cached copy" })).toBeTruthy();
  expect(screen.getByRole("row", { name: "tb skipped disabled Provider disabled" })).toBeTruthy();
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


it("shows candidate evidence and history/search context with phone quality in a scrolling table", async () => {
  mockFetch([{ method: "GET", path: "/shadow/diffs/9", reply: { id: 9, title: "Film",
    grab: { instance: "sonarr_anime", history_id: 42, grabbed_at: "2026-11-01T10:00:00Z", release_source: "torrent", arr_indexer: "Nyaa", arr_quality: "HDTV-720p", arr_score: 25, existing_source: "previous file",
      searched: true, searched_at: "2026-11-01T10:05:00Z", found_arr: true, result_count: 1, accepted_count: 1,
      results: [{ release: "Candidate.Release", quality: "WEB-1080p", info_hash: "0123456789abcdef0123456789abcdef01234567", size: 1073741824, seeders: 12, languages: ["Japanese", "English"], published: "2026-10-31T09:00:00Z" }] },
  } }]);
  renderPage(<Routes><Route path="/comparison/:id" element={<ShadowDiffPage />} /></Routes>, "/comparison/9");
  const row = (await screen.findByText("Candidate.Release")).closest("tr")!;
  for (const text of ["0123456789abcdef0123456789abcdef01234567", "1.0 GiB", "12 seeders", "Japanese, English"]) expect(row.textContent).toContain(text);
  expect(within(row).getByText("WEB-1080p").className).not.toContain("hidden");
  expect(screen.getByRole("columnheader", { name: "Quality" }).className).not.toContain("hidden");
  expect(row.querySelector("time")?.dateTime).toBe("2026-10-31T09:00:00Z");
  expect(screen.getByText(/^Grabbed:/).querySelector("time")?.dateTime).toBe("2026-11-01T10:00:00Z");
  expect(screen.getByText(/^Searched:/).querySelector("time")?.dateTime).toBe("2026-11-01T10:05:00Z");
  expect(row.closest("table")?.parentElement?.className).toContain("overflow-x-auto");
  // Phone width: each text column keeps a readable minimum and the table scrolls in its card (min-width cells, as add-indexer).
  expect([...row.querySelectorAll("td")].map((cell) => cell.className.split(" ").find((c) => c.startsWith("min-w-")))).toEqual(
    ["min-w-[16rem]", "min-w-[7rem]", "min-w-[5rem]", "min-w-[14rem]", "min-w-[11rem]", "min-w-[9rem]"]);
  // Scores and their heading stay on one line instead of wrapping one character per line.
  expect(screen.getByRole("columnheader", { name: "Score" }).className.split(" ")).toEqual(expect.arrayContaining(["min-w-[5rem]", "whitespace-nowrap"]));
  expect(row.querySelectorAll("td")[2].className.split(" ")).toContain("whitespace-nowrap");
  for (const text of ["sonarr_anime history #42", "torrent · Nyaa", "HDTV-720p · score 25", "File then: previous file", "The arr's release was among the results."]) expect(screen.getByText(text)).toBeTruthy();
});
