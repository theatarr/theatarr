// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { expect, it } from "vitest";
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
