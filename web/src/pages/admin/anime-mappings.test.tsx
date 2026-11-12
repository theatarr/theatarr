// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { expect, it, vi } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { AnimeMappings, MetadataFranchise } from "./anime-mappings";

const day = 24 * 3600 * 1000;
const iso = (offset: number) => new Date(Date.now() + offset).toISOString();
const franchise = (job: unknown) => ({ slug: "mushoku-tensei", name: "Mushoku Tensei", source: "auto", entries: 4, statuses: { auto: 4 },
  open_review: 0, pending: false, built_at: iso(-2 * day), next_refresh: iso(5 * day), job });
const on = { ready: true, auto_build: true, refresh_interval: "168h0m0s" };
const uncovered = (job: unknown) => ({ builds: on, items: [
  { title_id: 10, name: "Frieren", tvdb_id: 424536, outcome: "no_mapping", detail: "no AniList id for TVDB 424536", tried_at: iso(-3600 * 1000), job },
  { title_id: 11, name: "Dandadan", tvdb_id: 433530, tried_at: null, job: null },
] });

it("rebuilds a franchise and builds an uncovered anime series, showing the job until it ends", async () => {
  // the lists show each job once its POST has queued it
  let rebuilt = false, built = false;
  const api = mockFetch([
    { method: "GET", path: "/anime/review", reply: { items: [] } },
    { method: "GET", path: "/anime/franchises", reply: () => ({ items: [franchise(rebuilt ? { id: 7, state: "queued" } : null)] }) },
    { method: "GET", path: "/anime/uncovered", reply: () => uncovered(built ? { id: 8, state: "running" } : null) },
    { method: "POST", path: "/anime/franchises/mushoku-tensei/build", reply: () => { rebuilt = true; return json({ job_id: 7, slug: "mushoku-tensei" }, 202); } },
    { method: "POST", path: "/anime/build", reply: () => { built = true; return json({ job_id: 8 }, 202); } },
  ]);
  renderPage(<AnimeMappings />);
  const row = (await screen.findByText("Mushoku Tensei")).closest("tr")!;
  expect(within(row).getByText("next refresh in 5 d")).toBeTruthy();
  fireEvent.click(within(row).getByRole("button", { name: "Rebuild" }));
  await waitFor(() => expect(api.calls("POST", "/anime/franchises/mushoku-tensei/build")).toHaveLength(1));
  const queued = (await screen.findByText("build queued")).closest("tr")!;
  expect(within(queued).getByRole("button", { name: "Rebuild" })).toHaveProperty("disabled", true);

  expect(screen.getByText(/Built automatically, one at a time; franchises are rebuilt every 7 d/)).toBeTruthy();
  const frieren = screen.getByText("Frieren").closest("tr")!;
  expect(within(frieren).getByText("not in the mapping lists")).toBeTruthy();
  expect(within(frieren).getByText("no AniList id for TVDB 424536")).toBeTruthy();
  expect(within(screen.getByText("Dandadan").closest("tr")!).getByText("not tried yet")).toBeTruthy();
  fireEvent.click(within(frieren).getByRole("button", { name: "Build" }));
  await waitFor(() => expect(api.calls("POST", "/anime/build")).toHaveLength(1));
  expect(api.calls("POST", "/anime/build")[0].body).toEqual({ tvdb_id: 424536 });
  const running = (await screen.findByText("building")).closest("tr")!;
  expect(within(running).getByRole("button", { name: "Build" })).toHaveProperty("disabled", true);
});

it("says why builds cannot run and shows a failed build and a refused request", async () => {
  mockFetch([
    { method: "GET", path: "/anime/review", reply: { items: [] } },
    { method: "GET", path: "/anime/franchises", reply: { items: [{ ...franchise({ id: 3, state: "failed", error: "anilist: down" }), next_refresh: null }] } },
    { method: "GET", path: "/anime/uncovered", reply: { builds: { ready: false, auto_build: true, refresh_interval: "168h0m0s", off: "metadata.sources lacks anilist" }, items: [] } },
    { method: "POST", path: "/anime/franchises/mushoku-tensei/build", reply: json({ error: "anime builds need metadata.sources" }, 503) },
  ]);
  renderPage(<AnimeMappings />);
  expect(await screen.findByText(/Builds cannot run: metadata.sources lacks anilist\./)).toBeTruthy();
  expect(screen.getByText("Every anime series has a franchise.")).toBeTruthy();
  const row = screen.getByText("Mushoku Tensei").closest("tr")!;
  expect(within(row).getByText("build failed").getAttribute("title")).toBe("anilist: down");
  expect(within(row).queryByText(/next refresh/)).toBeNull();
  fireEvent.click(within(row).getByRole("button", { name: "Rebuild" }));
  expect(await within(row).findByText("anime builds need metadata.sources")).toBeTruthy();
});

it("says when automatic builds are off", async () => {
  mockFetch([
    { method: "GET", path: "/anime/review", reply: { items: [] } },
    { method: "GET", path: "/anime/franchises", reply: { items: [] } },
    { method: "GET", path: "/anime/uncovered", reply: { builds: { ...on, auto_build: false }, items: [] } },
  ]);
  renderPage(<AnimeMappings />);
  expect(await screen.findByText(/Automatic builds are off \(metadata.anime.auto_build\)/)).toBeTruthy();
});

it("rebuilds from the franchise page and shows the next refresh", async () => {
  const api = mockFetch([
    { method: "GET", path: "/anime/franchises/mushoku-tensei", reply: { ...franchise(null), orders: [], numbering: [] } },
    { method: "GET", path: "/anime/franchises/mushoku-tensei", reply: { ...franchise({ id: 9, state: "running" }), orders: [], numbering: [] } },
    { method: "POST", path: "/anime/franchises/mushoku-tensei/build", reply: json({ job_id: 9, slug: "mushoku-tensei" }, 202) },
  ]);
  renderPage(<Routes><Route path="/anime/:slug" element={<MetadataFranchise />} /></Routes>, "/anime/mushoku-tensei");
  expect(await screen.findByText(/next refresh in 5 d/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Rebuild" }));
  expect(await screen.findByText("building")).toBeTruthy();
  expect(api.calls("POST", "/anime/franchises/mushoku-tensei/build")).toHaveLength(1);
  expect(screen.getByRole("button", { name: "Rebuild" })).toHaveProperty("disabled", true);
});

it("refetches the franchises and review items when a build ends, and the lists every 30 s", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    let done = false; // the discovery build of Frieren is running, then done with a new franchise
    const api = mockFetch([
      { method: "GET", path: "/anime/review", reply: { items: [] } },
      { method: "GET", path: "/anime/franchises", reply: () => ({ items: done ? [franchise(null)] : [] }) },
      { method: "GET", path: "/anime/uncovered", reply: () => uncovered(done ? { id: 8, state: "done" } : { id: 8, state: "running" }) },
    ]);
    renderPage(<AnimeMappings />);
    expect(await screen.findByText("building")).toBeTruthy();
    const reviews = api.calls("GET", "/anime/review").length;
    done = true;
    await vi.advanceTimersByTimeAsync(5000); // the uncovered list polls while its job runs, and sees it end
    expect(await screen.findByText("Mushoku Tensei")).toBeTruthy();
    expect(api.calls("GET", "/anime/review").length).toBeGreaterThan(reviews);

    // nothing runs: the lists still refetch every 30 s, so a build the loop starts shows up
    const franchises = api.calls("GET", "/anime/franchises").length;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(api.calls("GET", "/anime/franchises").length).toBe(franchises);
    await vi.advanceTimersByTimeAsync(20_000);
    await waitFor(() => expect(api.calls("GET", "/anime/franchises").length).toBeGreaterThan(franchises));
  } finally {
    vi.useRealTimers();
  }
});

it("refetches an idle franchise detail every 30 s, so a build the loop starts shows up", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    let running = false;
    const api = mockFetch([
      {
        method: "GET",
        path: "/anime/franchises/mushoku-tensei",
        reply: () => ({ ...franchise(running ? { id: 9, state: "running" } : null), orders: [], numbering: [] }),
      },
    ]);
    renderPage(<Routes><Route path="/anime/:slug" element={<MetadataFranchise />} /></Routes>, "/anime/mushoku-tensei");
    expect(await screen.findByText(/next refresh in 5 d/)).toBeTruthy();
    const reads = api.calls("GET", "/anime/franchises/mushoku-tensei").length;
    running = true;
    await vi.advanceTimersByTimeAsync(30_000);
    await waitFor(() => expect(api.calls("GET", "/anime/franchises/mushoku-tensei").length).toBeGreaterThan(reads));
    expect(await screen.findByText("building")).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
});

// Review cards: Mushoku Tensei II Part 2 against TVDB 371310 (title 9 in Theatarr).
const mt = { tvdb: 371310, title_id: 9, name: "Mushoku Tensei" };
const range = (s: number, from: number, n: number) => Array.from({ length: n }, (_, i) => [s, from + i]);
const sample = (s: number, n: number, name: string, air: string) => ({ season: s, number: n, name, air_date: air });
const entry = { key: "al:166873", title: "Mushoku Tensei II Part 2", title_en: "Jobless Reincarnation Season 2 Part 2", format: "TV",
  start: "2024-04-08", end: "2024-06-24", episodes: 12, anilist_id: 166873, anidb_id: 18085 };
const conflict = {
  id: 1, franchise: "mushoku-tensei", franchise_name: "Mushoku Tensei", entry_key: "al:166873", kind: "conflict", state: "open",
  summary: "lists disagree for al:166873", title: "Mushoku Tensei II Part 2", entry,
  snippet: "mappings:\n  al:166873: {tvdb: 371310, episodes: S02E13-S02E24}",
  mapping: { winner: "anibridge", tvdb: 371310, episodes: range(2, 13, 12), candidates: [
    { source: "anibridge", series_id: 371310, range: "S02E13-S02E24", score: 0.8 },
    { source: "scudlee", series_id: 371310, range: "S03E01-S03E12", score: 0.7 },
  ] },
  options: [
    { proposed: true, source: "anibridge", series: mt, episodes: range(2, 13, 12), count: 12,
      samples: [sample(2, 13, "Pride", "2024-04-08"), sample(2, 14, "Respite", "2024-04-15"), sample(2, 24, "Ending", "2024-06-24")] },
    { candidate: 1, proposed: false, source: "scudlee", series: mt, episodes: range(3, 1, 12), count: 12,
      samples: [sample(3, 1, "New Start", "2024-04-08"), sample(3, 2, "Second", "2024-04-15"), sample(3, 12, "Last", "2024-06-24")] },
  ],
};
const lists = (items: unknown[]) => [
  { method: "GET", path: "/anime/review", reply: { items } },
  { method: "GET", path: "/anime/franchises", reply: { items: [] } },
  { method: "GET", path: "/anime/uncovered", reply: { builds: on, items: [] } },
];

it("shows a conflict as a plain question with links and sample episodes, and the raw rule only on request", async () => {
  mockFetch(lists([conflict]));
  renderPage(<AnimeMappings />);
  expect(await screen.findByText(/^Which Mushoku Tensei episodes are “Mushoku Tensei II Part 2”\?/)).toBeTruthy();
  const hrefs = screen.getAllByRole("link").map((a) => a.getAttribute("href"));
  expect(hrefs).toContain("/title/9");
  expect(hrefs).toContain("https://anilist.co/anime/166873");
  expect(hrefs).toContain("https://anidb.net/anime/18085");
  expect(hrefs).toContain("https://www.thetvdb.com/dereferrer/series/371310");
  expect(screen.getByText("Lists disagree")).toBeTruthy();
  expect(screen.getByText("Jobless Reincarnation Season 2 Part 2")).toBeTruthy(); // the AniList side
  expect(screen.getByText(/“Pride”/)).toBeTruthy(); // TVDB names of both options
  expect(screen.getByText(/“New Start”/)).toBeTruthy();
  expect(screen.getByText("Theatarr's pick")).toBeTruthy();
  expect(screen.getByText("TVDB · in use now: Sonarr's numbering")).toBeTruthy();
  expect(screen.queryByText(/mappings:/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Show the raw rule" }));
  expect(screen.getByText(/mappings:/)).toBeTruthy();
  expect(screen.getByText("lists disagree for al:166873")).toBeTruthy();
});

it("uses another candidate and keeps Sonarr's numbering without a browser confirm", async () => {
  const confirm = vi.spyOn(window, "confirm");
  const api = mockFetch([
    ...lists([conflict]),
    { method: "POST", path: "/anime/review/1/accept", reply: {} },
    { method: "POST", path: "/anime/review/1/reject", reply: {} },
  ]);
  renderPage(<AnimeMappings />);
  const other = (await screen.findByText(/“New Start”/)).closest("div.rounded-lg") as HTMLElement;
  fireEvent.click(within(other).getByRole("button", { name: "Use this mapping" }));
  await waitFor(() => expect(api.calls("POST", "/anime/review/1/accept")).toHaveLength(1));
  expect(api.calls("POST", "/anime/review/1/accept")[0].body).toEqual({ candidate: 1 });
  fireEvent.click(screen.getByRole("button", { name: "Keep Sonarr's numbering" }));
  await waitFor(() => expect(api.calls("POST", "/anime/review/1/reject")).toHaveLength(1));
  expect(api.calls("POST", "/anime/review/1/reject")[0].body).toEqual({});
  expect(confirm).not.toHaveBeenCalled();
  confirm.mockRestore();
});

it("sets a heuristic match by hand with Edit season/offset… and shows a refusal's sentence", async () => {
  const heuristic = { ...conflict, id: 4, kind: "heuristic", snippet: undefined,
    mapping: { ...conflict.mapping, winner: "heuristic", candidates: [] }, options: [{ ...conflict.options[0], source: "heuristic" }] };
  const api = mockFetch([
    ...lists([heuristic]),
    { method: "POST", path: "/anime/review/4/accept", reply: json({ error: "Season 2 has no episode 24 in Theatarr's episode list for Mushoku Tensei" }, 400) },
  ]);
  renderPage(<AnimeMappings />);
  expect(await screen.findByText(/^Is “Mushoku Tensei II Part 2” Mushoku Tensei Season 2, episodes 13–24\?/)).toBeTruthy();
  expect(screen.getByText("Don't use this match")).toBeTruthy();
  expect(screen.getByText("In use now")).toBeTruthy();
  expect(screen.queryByText("Theatarr's pick")).toBeNull();
  expect(screen.getByText(/^Don't use this match: new files take Sonarr's numbering/)).toBeTruthy();
  expect(screen.queryByText(/\(12\)/)).toBeNull();
  expect(screen.getByText(/12 episodes/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Edit season/offset…" }));
  fireEvent.change(screen.getByLabelText("Season"), { target: { value: "2" } });
  fireEvent.change(screen.getByLabelText("First episode"), { target: { value: "13" } });
  fireEvent.change(screen.getByLabelText("Episodes"), { target: { value: "12" } });
  const form = screen.getByLabelText("Series").closest("div.rounded-lg") as HTMLElement;
  fireEvent.click(within(form).getByRole("button", { name: "Use this mapping" }));
  await waitFor(() => expect(api.calls("POST", "/anime/review/4/accept")).toHaveLength(1));
  expect(api.calls("POST", "/anime/review/4/accept")[0].body).toEqual({ mapping: { tvdb: 371310, season: 2, first: 13, count: 12 } });
  expect(await screen.findByText("Season 2 has no episode 24 in Theatarr's episode list for Mushoku Tensei")).toBeTruthy();
});

it("asks to apply a held refresh, says it answers all its changes, and applies it", async () => {
  const held = { id: 7, franchise: "mushoku-tensei", franchise_name: "Mushoku Tensei", entry_key: "al:166873", kind: "refresh_moves_linked",
    state: "open", summary: "move al:166873", entry,
    refresh: { from: { ...conflict.options[0], proposed: false }, to: { series: mt, episodes: [[0, 40]], count: 1, samples: [sample(0, 40, "OVA", "2024-07-01")] },
      with_files: 12, items: 2 } };
  const api = mockFetch([...lists([held]), { method: "POST", path: "/anime/review/7/accept", reply: {} }]);
  renderPage(<AnimeMappings />);
  expect(await screen.findByText(/^Apply the refresh of Mushoku Tensei\? The refresh maps .*files already linked for them may be moved to match\.$/)).toBeTruthy();
  expect(screen.getByText("This answers all 2 changes of this refresh.")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Edit season/offset…" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Apply the refresh" }));
  await waitFor(() => expect(api.calls("POST", "/anime/review/7/accept")).toHaveLength(1));
  expect(api.calls("POST", "/anime/review/7/accept")[0].body).toEqual({});
});
