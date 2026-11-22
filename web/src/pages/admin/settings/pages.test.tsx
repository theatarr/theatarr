// @vitest-environment jsdom
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { useNavigate } from "react-router";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { overview } from "@/components/settings/fixtures";
import schema from "@/components/settings/testdata/schema.json";
import { AdvancedSettingsPage, SettingsPage, SettingsPicker } from "./pages";
import { SettingsAuthority } from "@/components/settings/authority";
import { legacyTarget } from "@/lib/legacy-routes";
it("composes the library file panels and shared advanced toggle", async () => {
 mockFetch([
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "library", config: null, secrets: [] }] } },
  { method: "GET", path: "/policy", reply: { versions: [] } },
 ]);
 renderPage(<SettingsPage />, "/admin/settings/libraries/files");
 await screen.findByRole("heading", { name: "Mount and folders" });
 expect(screen.getByRole("heading", { name: "Broken link checks" })).toBeTruthy();
 expect(screen.getByRole("heading", { name: "Trash" })).toBeTruthy();
 expect(screen.queryByRole("heading", { name: "Plex server" })).toBeNull();
 expect(screen.queryByRole("heading", { name: "Media inspection" })).toBeNull();
 // Pause, in the header, is the only switch: no switch card on the page.
 expect(screen.queryByRole("switch", { name: "Write library links" })).toBeNull();
 expect(screen.queryByTestId("switches-advanced")).toBeNull();
 expect(screen.getByRole("tab", { name: "Naming" }).getAttribute("href")).toBe("/admin/settings/libraries/naming");
 // One toggle in the header from md up, one under the page picker on phones (CSS picks which shows).
 const toggles = screen.getAllByRole("button", { name: "Show advanced settings" });
 expect(toggles.map(t => t.className.includes("max-md:hidden") || t.className.includes("md:hidden"))).toEqual([true, true]);
 fireEvent.click(toggles[0]);
 expect(screen.getByRole("heading", { name: "Media inspection" })).toBeTruthy();
 fireEvent.click(toggles[0]);
});
it("keeps healthy authority details folded and fallback reload available", async () => {
 const v = renderPage(<SettingsAuthority overview={overview()} compact />);
 expect(screen.getByText("Settings are saved in Theatarr and apply at once.")).toBeTruthy();
 expect(screen.getByText("Details").closest("details")?.open).toBe(false);
 v.unmount();
 const fetch = mockFetch([{ method: "POST", path: "/settings/reload", reply: {} }]);
 renderPage(<SettingsAuthority overview={overview({ active_generation: 3 })} compact />);
 const details = screen.getByRole("button", { name: "Details" });
 expect(details.getAttribute("aria-expanded")).toBe("false");
 fireEvent.click(details);
 expect(details.getAttribute("aria-expanded")).toBe("true");
 fireEvent.click(screen.getByRole("button", { name: "Load generation 4 again" }));
 expect(fetch.calls("POST", "/settings/reload")[0].body).toEqual({ generation: 4 });
});

it("links request limits to VPN status", async () => {
 mockFetch([{ method: "GET", path: "/settings", reply: overview() }, { method: "GET", path: "/settings/schema", reply: schema }, { method: "GET", path: "/gateway", reply: {} }]);
 renderPage(<AdvancedSettingsPage />, "/admin/advanced/limits");
 expect(await screen.findByRole("link", { name: "VPN status: Indexers › VPN" })).toHaveProperty("href", expect.stringContaining("/admin/settings/indexers/vpn"));
});

it("redirects the old torrent schedule bookmark with its query and hash", () => {
 // Settings › General is gone; its bookmarks redirect through lib/legacy-routes.ts (tested there too).
 expect(legacyTarget("/admin/settings/general", "?x=1", "#torrent-backups")).toBe("/admin/settings/downloads/torrents?x=1#torrent-backups");
});
it("shows General's former panels on Download clients and Metadata & artwork", async () => {
 const routes = [
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [] } },
  { method: "GET", path: "/policy", reply: { versions: [] } },
  { method: "GET", path: "/sources", reply: { providers: [], indexers: [] } },
  { method: "GET", path: "/cardigann/holds", reply: { holds: [] } },
 ];
 mockFetch(routes);
 const v = renderPage(<SettingsPage />, "/admin/settings/downloads");
 const sync = await screen.findByRole("heading", { name: "Provider account sync" });
 // The panel comes before provider status, which loads later and would push a #provider-sync target away.
 const status = await screen.findByText("No providers configured.");
 expect(sync.compareDocumentPosition(status) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
 v.unmount();
 mockFetch(routes);
 renderPage(<SettingsPage />, "/admin/settings/metadata");
 expect(await screen.findByRole("heading", { name: "Artwork cache" })).toBeTruthy();
 expect(screen.getByRole("heading", { level: 1, name: "Metadata & artwork" })).toBeTruthy();
});
it("offers a labelled settings page select for phones that moves between pages", async () => {
 const { useLocation } = await import("react-router");
 const Where = () => <output data-testid="where">{useLocation().pathname}</output>;
 renderPage(<><SettingsPicker current="/admin/settings/libraries" /><Where /></>, "/admin/settings/libraries/files");
 const picker = screen.getByTestId("settings-picker");
 // Phones only: the side list replaces it from md up.
 expect(picker.className).toContain("md:hidden");
 const select = screen.getByRole("combobox", { name: "Settings page" }) as HTMLSelectElement;
 expect(select.value).toBe("/admin/settings/libraries");
 // One select for pages and their tabs, grouped by page (no second "Section" select on a phone).
 expect([...select.options].map(o => o.textContent)).toContain("Download clients › Debrid providers");
 // A tab named like its page is not said twice ("Libraries › Libraries").
 expect([...select.options].map(o => o.textContent)).not.toContain("Libraries › Libraries");
 expect([...select.options].map(o => o.textContent)).toContain("Libraries › Naming");
 expect([...select.options].map(o => o.textContent)).toContain("Notifications");
 expect([...select.options].map(o => o.textContent)).not.toContain("Backup");
 expect(select.querySelectorAll("optgroup").length).toBeGreaterThan(2);
 fireEvent.change(select, { target: { value: "/admin/settings/downloads" } });
 expect(screen.getByTestId("where").textContent).toBe("/admin/settings/downloads");
});

it("shows no advanced toggle and no repeated banner on a page without advanced settings", async () => {
 mockFetch([
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [] } },
  { method: "GET", path: "/policy", reply: { versions: [] } },
 ]);
 const v = renderPage(<SettingsPage />, "/admin/settings/indexers/rss");
 await screen.findByRole("heading", { name: "RSS feed checks" });
 expect(screen.queryByRole("button", { name: "Show advanced settings" })).toBeNull();
 expect(screen.queryByText("Settings are saved in Theatarr and apply at once.")).toBeNull();
 v.unmount();
 renderPage(<SettingsPage />, "/admin/settings/downloads/clients");
 await screen.findByRole("heading", { name: "Other download clients" });
 // The page's help is its description, said once.
 expect(screen.getAllByText(/Debrid providers are tried in the order shown/)).toHaveLength(1);
});
it("shows no advanced toggle on the Advanced pages", async () => {
 mockFetch([{ method: "GET", path: "/settings", reply: overview() }, { method: "GET", path: "/settings/schema", reply: schema }, { method: "GET", path: "/settings/audit", reply: { items: [] } }]);
 renderPage(<AdvancedSettingsPage />, "/admin/advanced/history");
 await screen.findByRole("heading", { level: 1, name: "Settings history" });
 expect(screen.queryByRole("button", { name: "Show advanced settings" })).toBeNull();
});

it("names the format set page for what it is, with a way back to the profiles", async () => {
 mockFetch([
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/policy", reply: { versions: [] } },
  { method: "GET", path: "/settings/format_sets", reply: { generation: 4, records: [] } },
  { method: "GET", path: "/settings/formats/schema", reply: [] },
 ]);
 renderPage(<SettingsPage />, "/admin/settings/profiles/format_sets");
 expect((await screen.findByRole("heading", { level: 1 })).textContent).toBe("Format sets");
 expect(screen.getByTestId("back-to-profiles").getAttribute("href")).toBe("/admin/settings/profiles");
 const select = screen.getByRole("combobox", { name: "Settings page" }) as HTMLSelectElement;
 expect(select.value).toBe("/admin/settings/profiles/format_sets");
 expect(select.selectedOptions[0].textContent).toBe("Profiles › Format sets");
});


it("puts Automatic search with RSS under Indexers and leaves Behaviour to grabbing", async () => {
 const routes = [
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [] } },
  { method: "GET", path: "/policy", reply: { versions: [] } },
 ];
 mockFetch(routes);
 const v = renderPage(<SettingsPage />, "/admin/settings/indexers/rss");
 const search = await screen.findByRole("heading", { name: "Automatic search" });
 const rss = screen.getByRole("heading", { name: "RSS feed checks" });
 expect(search.compareDocumentPosition(rss) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
 expect(screen.getByRole("tab", { name: "Search & RSS" }).getAttribute("aria-selected")).toBe("true");
 v.unmount();
 mockFetch(routes);
 renderPage(<SettingsPage />, "/admin/settings/downloads/behaviour");
 await screen.findByRole("heading", { name: "What to grab" });
 expect(screen.queryByRole("heading", { name: "Automatic search" })).toBeNull();
});
it("shows indexer statistics under Status with a 7 / 30 / 90 days choice", async () => {
 const { IndexerStatus } = await import("@/pages/admin/sources");
 const fetch = mockFetch([
  { method: "GET", path: "/sources", reply: { providers: [], indexers: [] } },
  { method: "GET", path: "/indexers/stats", reply: (r: { search: string }) => ({ days: r.search.includes("30") ? 30 : 7, measured_since: "2026-11-16", indexers: [
   { key: "dmm", requests: 15, measured: 10, failed: 2, average_ms: 1250, grabbed: 2, imported: 1, failed_downloads: 1 },
   { key: "zilean", requests: 4, measured: 0, grabbed: 0, imported: 0, failed_downloads: 0 },
  ] }) },
 ]);
 renderPage(<IndexerStatus />);
 await screen.findByTestId("indexer-stats-help");
 const cells = (key: string) => [...document.querySelectorAll(`[data-testid="indexer-stats"] [data-indexer="${key}"] td`)].map(c => c.textContent);
 expect(cells("dmm")).toEqual(["dmm", "15", "2 (20%)", "1.3 s", "2", "1", "1"]);
 // No measured request (counts from before the upgrade): no failed count or time.
 expect(cells("zilean")).toEqual(["zilean", "4", "—", "—", "0", "0", "0"]);
 expect(screen.getByTestId("indexer-stats-help").textContent).toContain("Measured since Nov 16");
 expect(screen.queryByText(/not tracked yet/)).toBeNull();
 fireEvent.click(screen.getByRole("button", { name: "30 days" }));
 await screen.findByRole("button", { name: "30 days", pressed: true });
 await vi.waitFor(() => expect(fetch.calls("GET", "/indexers/stats").map(r => r.search)).toContain("?days=30"));
});
it("keeps the scroll when an editor opens on the same page (?edit=) (F22 review)", async () => {
 mockFetch([
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "library", config: null, secrets: [] }] } },
 ]);
 const scroll = vi.fn();
 vi.stubGlobal("scrollTo", scroll);
 function Edit() { const navigate = useNavigate(); return <button type="button" onClick={() => navigate("?edit=x")}>Open editor</button>; }
 renderPage(<><SettingsPage /><Edit /></>, "/admin/settings/libraries/files");
 await screen.findByRole("heading", { name: "Mount and folders" });
 fireEvent.click(screen.getByRole("button", { name: "Open editor" }));
 await screen.findByRole("heading", { name: "Mount and folders" });
 expect(scroll).not.toHaveBeenCalled();
});
it("opens another settings page at its top, and Back leaves the scroll to the browser (F22)", async () => {
 mockFetch([
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "library", config: null, secrets: [] }] } },
 ]);
 const scroll = vi.fn();
 vi.stubGlobal("scrollTo", scroll);
 function Back() { const navigate = useNavigate(); return <button type="button" onClick={() => navigate(-1)}>Go back</button>; }
 renderPage(<><SettingsPage /><Back /></>, "/admin/settings/libraries/files");
 await screen.findByRole("heading", { name: "Mount and folders" });
 expect(scroll).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole("tab", { name: "Naming" }));
 await waitFor(() => expect(scroll).toHaveBeenCalledWith(0, 0));
 scroll.mockClear();
 fireEvent.click(screen.getByRole("button", { name: "Go back" }));
 await screen.findByRole("heading", { name: "Mount and folders" });
 expect(scroll).not.toHaveBeenCalled();
});
