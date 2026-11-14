// @vitest-environment jsdom
// The request sheet's add options follow the title's stored anime flag, as the request it creates does: it never
// sends anime=0 (only the Add sheet's own Anime choice does). Invented keys and names.
import { waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
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
