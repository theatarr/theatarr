// @vitest-environment jsdom
// The request sheet's add options follow the title's stored anime flag, as the request it creates does: it never
// sends anime=0 (only the Add sheet's own Anime choice does). A new series' own seasons show without episode counts.
// Invented keys and names.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { RequestSheet } from "./request-sheet";

const options = { kind: "movie", anime: true, title_id: 5, in_library: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions: [] };

it("asks for a removed title's options without overriding its stored anime flag", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: options },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
  ]);
  renderPage(<RequestSheet target={{ kind: "movie", name: "Alpha", title_id: 5, anime: false }} onClose={() => {}} />);
  await waitFor(() => expect(fetch.calls("GET", "/add/options")).toHaveLength(1));
  expect(fetch.calls("GET", "/add/options")[0].search).not.toContain("anime=");
});

it("shows a new series' own seasons without episode counts and asks for the picked ones", async () => {
  const series = { ...options, kind: "series", anime: false, title_id: undefined, own_seasons: true, seasons: [{ season: 0 }, { season: 1 }, { season: 2 }],
    versions: [{ key: "tv-web", label: "TV", kind: "series", enable: "", fits: true, default: true, on: false, searchable: true, grab_state: { paused: false, pause_forced: false, library_writes: true, will_grab: true } }] };
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 3, permissions: ["request"] } },
    { method: "GET", path: "/add/options", reply: series },
    { method: "GET", path: "/requests", reply: { items: [], total: 0, counts: {} } },
    { method: "POST", path: "/requests", reply: json({ approved: false, merged: false, request: { id: 4, name: "Kappa", status: "pending", versions: [{ key: "tv-web", label: "TV" }], seasons: [1] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<RequestSheet target={{ kind: "series", name: "Kappa", tmdb_id: 8 }} onClose={() => {}} />);
  const list = await screen.findByTestId("request-seasons");
  expect(within(list).getAllByRole("checkbox")).toHaveLength(2);
  expect(list.textContent).not.toMatch(/ep\./);
  await user.click(within(list).getAllByRole("checkbox")[1]);
  await user.click(screen.getByTestId("request-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/requests")).toHaveLength(1));
  expect(fetch.calls("POST", "/requests")[0].body).toMatchObject({ kind: "series", tmdb_id: 8, seasons: [1] });
});
