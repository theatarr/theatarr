import { readFileSync } from "node:fs";
import { findSettings, settingsIndex } from "./settings-search";
import { describe, expect, it } from "vitest";
import schema from "@/components/settings/testdata/schema.json";
import { SETTINGS_PAGES, ADVANCED_SETTINGS_PAGES, SETTINGS_PANELS, settingHref } from "./settings-map";
describe("settings map", () => {
 it("gives every schema field exactly one home", () => {
  const actual = SETTINGS_PANELS.flatMap(p => p.keys.map(k => `${p.section}.${k}`)).sort();
  const expected = Object.entries(schema.sections).flatMap(([s, fields]) => fields.map(f => `${s}.${f.name}`)).sort();
  expect(actual).toEqual(expected);
 });
 it("has unique anchors and valid destinations", () => {
  expect(new Set(SETTINGS_PANELS.map(p => p.id)).size).toBe(SETTINGS_PANELS.length);
  for (const panel of SETTINGS_PANELS) {
   const page = [...SETTINGS_PAGES, ...ADVANCED_SETTINGS_PAGES].find(p => p.id === panel.page);
   expect(page).toBeDefined();
   if (panel.tab) expect(page?.tabs.some(t => t.id === panel.tab)).toBe(true);
  }
 });
 it("links Plex to its home", () => expect(settingHref("library", "plex")).toBe("/admin/settings/plex#plex"));
});

it("links wanted enable and Send notifications to their owners", () => {
 expect(settingHref("router", "wanted.enabled")).toBe("/admin/settings/downloads/behaviour#searches");
 expect(settingHref("notify", "enabled")).toBe("/admin/settings/notifications#targets");
 // Send notifications is the first field of the notify Destinations panel.
 expect(SETTINGS_PANELS.find(p => p.id === "targets")?.keys[0]).toBe("enabled");
});

it("finds upstream page names from the arr map, including non-settings destinations", () => {
 const index = settingsIndex();
 for (const name of ["Media Management", "Quality", "Metadata Source", "Import Lists", "Tags", "UI", "Apps", "Development", "Queue", "History", "Blocklist", "Missing", "Cutoff Unmet", "Status", "Tasks", "Backups", "Updates", "Events", "Log Files", "Update Log Files", "Config create / list templates", "State repair / migrate"]) {
  expect(findSettings(index, name).length, name).toBeGreaterThan(0);
 }
 expect(findSettings(index, "Cutoff Unmet")[0].href).toBe("/wanted/cutoff");
 expect(findSettings(index, "System › Tasks")[0].href).toBe("/admin/system/tasks");
});

it("indexes every page name documented in the arr map", () => {
 const map = readFileSync(new URL("../../../docs/arr-map.md", import.meta.url), "utf8");
 const index = settingsIndex();
 for (const line of map.split("\n").filter(l => l.startsWith("| ") && !l.startsWith("| Arr page"))) {
  const name = line.split("|")[1].trim();
  expect(findSettings(index, name).length, name).toBeGreaterThan(0);
 }
});

it("sends Prowlarr's History to the Decision journal under Activity and finds Manual import under Wanted", () => {
 const index = settingsIndex();
 expect(findSettings(index, "History").some(r => r.href === "/activity/journal" && r.label === "Activity › Decision journal")).toBe(true);
 expect(findSettings(index, "manual import").some(r => r.href === "/wanted/manual-import" && r.label === "Wanted › Manual import")).toBe(true);
 expect([...findSettings(index, "History"), ...findSettings(index, "manual import")].some(r => /advanced\/(journal|manual-import)/.test(r.href))).toBe(false);
});

it("keeps titled aliases distinct", () => {
 const index = settingsIndex();
 for (const name of ["Tags", "Statistics", "UI", "Updates", "Development"]) expect(findSettings(index, name).some(r => r.label === `${name} — no separate page`), name).toBe(true);
 expect(findSettings(index, "Metadata")[0].href).toBe("/admin/settings/metadata");
});

it("sends quality sizes to the profiles' Size limits", () => {
 expect(findSettings(settingsIndex(), "sonarr quality")[0].href).toBe("/admin/settings/profiles");
 expect(findSettings(settingsIndex(), "size limits")[0].href).toBe("/admin/settings/profiles");
});

it("finds Settings › General's panels on their new pages", () => {
 const hrefs = findSettings(settingsIndex(), "general").map(r => r.href);
 expect(hrefs).toEqual(expect.arrayContaining(["/admin/settings/downloads#provider-sync", "/admin/settings/metadata#artwork-cache", "/admin/torrents#scan-roots"]));
 expect(hrefs.some(h => h.startsWith("/admin/settings/general"))).toBe(false);
 expect(settingHref("torrents", "scan_roots")).toBe("/admin/torrents#scan-roots");
 expect(settingHref("provider_sync", "interval")).toBe("/admin/settings/downloads#provider-sync");
 expect(settingHref("ui", "images")).toBe("/admin/settings/metadata#artwork-cache");
});

it("sends Prowlarr's Apps to Connected apps, not to a no-page alias", () => {
 const found = findSettings(settingsIndex(), "apps");
 expect(found.some(r => r.label.includes("no separate page"))).toBe(false);
 expect(findSettings(settingsIndex(), "prowlarr apps")[0].href).toBe("/admin/settings/apps");
});

it("finds a named record's fields by its name, such as the tmdb source's API key", () => {
 const sections = { source: "db" as const, generation: 1, sections: [{ key: "metadata", live: true, yaml: "", secrets: [], config: { sources: { tmdb: { url: "https://api.themoviedb.org/3" }, tvdb: {} } } }] };
 const found = findSettings(settingsIndex(schema as never, sections), "tmdb key");
 expect(found[0]).toMatchObject({ href: "/admin/settings/metadata?open=metadata.sources.tmdb#sources", field: "Sources › tmdb", path: "metadata.sources.tmdb" });
 expect(found.some(r => r.path === "metadata.sources.tvdb")).toBe(false);
 expect(findSettings(settingsIndex(schema as never), "tmdb key")).toEqual([]);
});
