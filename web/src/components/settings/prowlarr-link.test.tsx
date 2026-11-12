// @vitest-environment jsdom
// Link Prowlarr: the address and key entered once, Prowlarr's indexers listed, and the ticked ones linked as feeds in
// one write, on and in the ticked libraries. Feeds already linked, off in Prowlarr or Usenet cannot be ticked.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { ProwlarrFeed } from "@/api/prowlarr";
import type { VersionRecord } from "@/api/settings-versions";
import { SettingsTabView } from "@/pages/admin/settings";
import { dmm, overview, schema } from "./fixtures";
import { expectNamedControls, json, mockFetch, renderPage, type RequestRecord } from "@/test/page";

const lib = (key: string, kind: string) => ({ id: 1, key, created_at: "", updated_at: "", roots: [], config: { kind, anime: null, mode: "active" } }) as unknown as VersionRecord;
const feed = (o: Partial<ProwlarrFeed>): ProwlarrFeed => ({ id: 1, name: "Site", privacy: "public", protocol: "torrent", enabled: true, url: "http://10.0.0.5:9696/1/api", status: "linkable", ...o });
const feeds: ProwlarrFeed[] = [
  feed({ id: 1, name: "The Pirate Bay", key: "prowlarr-the-pirate-bay" }),
  feed({ id: 2, name: "Nyaa", status: "linked", linked: "prowlarr-nyaa", url: "http://10.0.0.5:9696/2/api" }),
  feed({ id: 3, name: "Old Site", status: "disabled", enabled: false, url: "http://10.0.0.5:9696/3/api" }),
  feed({ id: 4, name: "Usenet One", status: "usenet", protocol: "usenet", url: "http://10.0.0.5:9696/4/api" }),
  feed({ id: 5, name: "Private Site", privacy: "private", key: "prowlarr-private-site", url: "http://10.0.0.5:9696/5/api" }),
];

describe("Link Prowlarr", () => {
  it("connects Prowlarr, lists its indexers and links the ticked ones in one write", async () => {
    let connected = false;
    // The settings generation as the server has it: connecting saves one, and a link at an older one is stale.
    let generation = 4;
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: () => json(overview({ indexers: [dmm], generation })) },
      { method: "GET", path: "/settings/versions", reply: json({ source: "db", generation: 4, versions: [lib("movies", "movie"), lib("tv", "series")] }) },
      { method: "GET", path: "/cardigann/definitions", reply: json({ status: {}, total: 0, definitions: [], unavailable: "off" }) },
      { method: "GET", path: "/settings/prowlarr/feeds", reply: () => json(connected ? { configured: true, url: "http://10.0.0.5:9696", feeds } : { configured: false }) },
      { method: "GET", path: "/settings/sections", reply: json({ source: "db", generation: 4, sections: [{ key: "cardigann", live: true, config: null, yaml: "", secrets: [] }] }) },
      { method: "PUT", path: "/settings/sections/cardigann", reply: (r: RequestRecord) => {
        if ((r.body as { generation: number }).generation !== generation) return json({ error: "stale generation" }, 409);
        connected = true;
        return json({ generation: ++generation });
      } },
      { method: "POST", path: "/settings/prowlarr/feeds", reply: (r: RequestRecord) => (r.body as { generation: number }).generation !== generation
        ? json({ error: "stale generation" }, 409)
        : json({ generation: ++generation }) },
    ]);
    const user = userEvent.setup();
    renderPage(<SettingsTabView tab="indexers" overview={overview({ indexers: [dmm] })} schema={schema} versions={[]} />, "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Add indexer" }));
    await user.click(screen.getByRole("tab", { name: "Link Prowlarr" }));
    const tab = await screen.findByTestId("link-tab");
    expect(tab.textContent).toContain("the trackers, their logins and their VPN stay in Prowlarr");

    // Not connected yet: the address and key, saved in the catalog section with the key write-only.
    await user.type(await within(tab).findByRole("textbox", { name: "Prowlarr address" }), "http://10.0.0.5:9696");
    await user.type(within(tab).getByLabelText(/Prowlarr API key/), "invented-prowlarr-key");
    expectNamedControls();
    await user.click(within(tab).getByRole("button", { name: "Connect" }));
    await within(tab).findByTestId("prowlarr-connected");
    expect(fetch.calls("PUT", "/settings/sections/cardigann")[0].body).toEqual({
      generation: 4, config: { prowlarr: { url: "http://10.0.0.5:9696" } }, secrets: { "prowlarr.api_key": "invented-prowlarr-key" },
    });

    // Only the linkable feeds can be ticked, and all of them start ticked.
    await within(tab).findByRole("table");
    const here = Array.from(tab.querySelectorAll("[data-testid=feed-here]")).map((e) => e.textContent);
    expect(here).toEqual(["added as prowlarr-the-pirate-bay", "linked as prowlarr-nyaa", "off in Prowlarr", "Usenet: Theatarr searches torrents only", "added as prowlarr-private-site"]);
    const ticks = within(tab).getAllByRole("checkbox", { name: /^Link / });
    expect(ticks.map((c) => c.getAttribute("aria-label"))).toEqual(["Link The Pirate Bay as prowlarr-the-pirate-bay", "Link Private Site as prowlarr-private-site"]);
    expect(ticks.map((c) => (c as HTMLInputElement).checked)).toEqual([true, true]);
    await user.click(ticks[1]);
    // Every library is ticked for a feed; untick one.
    const libraries = within(tab).getByRole("group", { name: "Libraries" });
    await user.click(within(libraries).getByRole("checkbox", { name: /^tv/ }));
    await user.click(within(tab).getByRole("button", { name: "Link 1 feeds" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/prowlarr/feeds")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/prowlarr/feeds")[0].body).toEqual({ generation: 5, ids: [1], versions: ["movies"] });
    await waitFor(() => expect(screen.getByTestId("link-announce").textContent).toBe("1 feeds linked."));
  });
});

it("asks for the key again when the address changes, and sends nothing", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/settings/versions", reply: json({ source: "db", generation: 4, versions: [] }) },
    { method: "GET", path: "/cardigann/definitions", reply: json({ status: {}, total: 0, definitions: [], unavailable: "off" }) },
    { method: "GET", path: "/settings/prowlarr/feeds", reply: json({ configured: true, url: "http://10.0.0.5:9696", feeds: [] }) },
  ]);
  const user = userEvent.setup();
  renderPage(<SettingsTabView tab="indexers" overview={overview({ indexers: [dmm] })} schema={schema} versions={[]} />, "/admin/settings/indexers");
  await user.click(screen.getByRole("button", { name: "Add indexer" }));
  await user.click(screen.getByRole("tab", { name: "Link Prowlarr" }));
  await user.click(await screen.findByRole("button", { name: "Change" }));
  const address = screen.getByRole("textbox", { name: "Prowlarr address" });
  await user.clear(address);
  await user.type(address, "http://10.0.0.6:9696");
  await user.click(screen.getByRole("button", { name: "Connect" }));
  expect(screen.getByText("Enter the API key again for the new address")).toBeTruthy();
  expect(fetch.requests.filter((r) => r.method !== "GET")).toHaveLength(0);
});

it("reports a saved connection that cannot reach Prowlarr apart from the save, and reads again only on Try again", async () => {
  let connected = false;
  const fetch = mockFetch([
    { method: "GET", path: "/settings", reply: json(overview({ indexers: [dmm] })) },
    { method: "GET", path: "/settings/versions", reply: json({ source: "db", generation: 4, versions: [] }) },
    { method: "GET", path: "/cardigann/definitions", reply: json({ status: {}, total: 0, definitions: [], unavailable: "off" }) },
    { method: "GET", path: "/settings/prowlarr/feeds", reply: () => connected
      ? json({ error: "could not read Prowlarr's indexers: connection refused" }, 502)
      : json({ configured: false }) },
    { method: "GET", path: "/settings/sections", reply: json({ source: "db", generation: 4, sections: [{ key: "cardigann", live: true, config: null, yaml: "", secrets: [] }] }) },
    { method: "PUT", path: "/settings/sections/cardigann", reply: () => { connected = true; return json({ generation: 5 }); } },
  ]);
  const user = userEvent.setup();
  renderPage(<SettingsTabView tab="indexers" overview={overview({ indexers: [dmm] })} schema={schema} versions={[]} />, "/admin/settings/indexers");
  await user.click(screen.getByRole("button", { name: "Add indexer" }));
  await user.click(screen.getByRole("tab", { name: "Link Prowlarr" }));
  const tab = await screen.findByTestId("link-tab");
  await user.type(await within(tab).findByRole("textbox", { name: "Prowlarr address" }), "http://127.0.0.1:9");
  await user.type(within(tab).getByLabelText(/Prowlarr API key/), "invented-prowlarr-key");
  await user.click(within(tab).getByRole("button", { name: "Connect" }));
  const box = await within(tab).findByTestId("prowlarr-unreachable");
  expect(box.textContent).toContain("The connection was saved, but Prowlarr could not be reached at that address.");
  expect(box.textContent).toContain("connection refused");
  expect(tab.textContent).not.toContain("Could not save");
  const reads = () => fetch.calls("GET", "/settings/prowlarr/feeds").length;
  const failed = reads();
  await new Promise((r) => setTimeout(r, 50));
  expect(reads()).toBe(failed);
  await user.click(within(box).getByRole("button", { name: "Try again" }));
  await waitFor(() => expect(reads()).toBe(failed + 1));
});
