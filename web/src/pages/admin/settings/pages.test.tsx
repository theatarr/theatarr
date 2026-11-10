// @vitest-environment jsdom
import { fireEvent, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { overview } from "@/components/settings/fixtures";
import schema from "@/components/settings/testdata/schema.json";
import { AdvancedSettingsPage, SettingsPage, SettingsPicker } from "./pages";
import { SettingsAuthority } from "@/components/settings/authority";
import { legacyTarget } from "@/lib/legacy-routes";
it("composes the library file panels and shared advanced toggle with the library switch", async () => {
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
 // The switch sits under an Advanced fold (the header's Pause sets it with Allow new downloads); closed while it is on.
 expect(screen.queryByRole("switch", { name: "Write library links" })).toBeNull();
 fireEvent.click(within(screen.getByTestId("switches-advanced")).getByText("Advanced"));
 expect(screen.getByText(/Pause, at the top of every admin page/)).toBeTruthy();
 expect(screen.getByRole("switch", { name: "Write library links" })).toBeTruthy();
 expect(screen.getByRole("tab", { name: "Naming" }).getAttribute("href")).toBe("/admin/settings/libraries/naming");
 fireEvent.click(screen.getByRole("button", { name: "Show advanced settings" }));
 expect(screen.getByRole("heading", { name: "Media inspection" })).toBeTruthy();
 fireEvent.click(screen.getByRole("button", { name: "Show advanced settings" }));
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
 expect(legacyTarget("/admin/settings/general", "?x=1", "#torrent-backups")).toBe("/admin/system/backup?x=1#torrent-backups");
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
 expect([...select.options].map(o => o.textContent)).toContain("Download clients");
 expect([...select.options].map(o => o.textContent)).not.toContain("Backup");
 fireEvent.change(select, { target: { value: "/admin/settings/downloads" } });
 expect(screen.getByTestId("where").textContent).toBe("/admin/settings/downloads");
});
