import { describe, expect, it } from "vitest";
import { LEGACY_ROUTES, legacyTarget } from "./legacy-routes";

describe("old admin URLs", () => {
  it("redirect to their new place, keeping parameters, query and hash", () => {
    const cases: [string, string][] = [
      ["/activity/now", "/activity"], ["/activity/blocked", "/activity/blocklist"], ["/activity/wanted", "/wanted"],
 ["/admin/connections/network", "/admin/advanced/limits"],
      ["/admin/system", "/admin"],
      ["/admin/system/health", "/admin"],
      ["/admin/system/jobs", "/admin/system/tasks"],
      ["/admin/system/network", "/admin/settings/indexers/vpn"],
      ["/admin/system/imports", "/admin/advanced/arr/imports"],
      ["/admin/system/database", "/admin/system/backup"],
      ["/admin/sources", "/admin/settings/downloads"],
      ["/admin/policy", "/admin/settings"],
      ["/admin/keys", "/admin/users"],
      ["/admin/shadow", "/admin/advanced/arr/comparison"],
      ["/admin/shadow/12", "/admin/advanced/arr/comparison/12"],
      ["/admin/registry", "/admin/torrents/inventory"],
      ["/admin/registry/abcdef", "/admin/torrents/inventory/abcdef"],
      ["/admin/journal", "/admin/advanced/journal"],
      ["/admin/journal/dl:7", "/admin/advanced/journal/dl:7"],
      ["/admin/journal/dl%3A3", "/admin/advanced/journal/dl%3A3"],
      ["/admin/journal/a%2Fb", "/admin/advanced/journal/a%2Fb"],
      ["/admin/registry/a%2Fb%3Ac", "/admin/torrents/inventory/a%2Fb%3Ac"],
      ["/admin/shadow/a%2Fb/", "/admin/advanced/arr/comparison/a%2Fb"],
      ["/admin/logs", "/admin/system/logs"],
    ];
    for (const [from, to] of cases) expect(legacyTarget(from)).toBe(to);
    expect(legacyTarget("/admin/journal", "?title=4", "#x")).toBe("/admin/advanced/journal?title=4#x");
    expect(legacyTarget("/admin/shadow", "?bucket=agree")).toBe("/admin/advanced/arr/comparison?bucket=agree");
  });
  it("leaves current URLs alone", () => {
    for (const p of ["/admin", "/admin/system/tasks", "/admin/users", "/admin/reviews/anime/x", "/admin/advanced/journal", "/movies"]) {
      expect(legacyTarget(p)).toBeNull();
    }
  });
});

it("preserves edit, raw and anchors for all moved settings routes", () => {
 for (const [from, to] of LEGACY_ROUTES.filter(([from]) => !from.includes(":"))) {
  expect(legacyTarget(from, "?edit=movie-web&raw=1", "#mount")).toBe(to + "?edit=movie-web&raw=1#mount");
 }
});

it("preserves query and hash for every legacy route including details", () => {
 for (const [from, to] of LEGACY_ROUTES) {
  const path = from.replace(/:\w+/g, "example");
  expect(legacyTarget(path, "?x=1", "#y")).toBe(to.replace(/:\w+/g, "example") + "?x=1#y");
 }
});
it.each(["tasks", "backup", "errors", "logs"])("system parent aliases do not shadow /admin/system/%s", page => {
 expect(legacyTarget(`/admin/system/${page}`, "?x=1", "#y")).toBeNull();
});

it.each([
 ["/admin/jobs", "/admin/system/tasks"],
 ["/admin/system/jobs", "/admin/system/tasks"],
 ["/admin/errors", "/admin/system/errors"],
 ["/admin/advanced/logs", "/admin/system/logs"],
 ["/admin/logs", "/admin/system/logs"],
 ["/admin/advanced/database", "/admin/system/backup"],
 ["/admin/system/database", "/admin/system/backup"],
 ["/admin/metadata", "/admin/reviews/anime"],
 ["/admin/metadata/example", "/admin/reviews/anime/example"],
 ["/admin/advanced/inventory", "/admin/torrents/inventory"],
 ["/admin/advanced/inventory/abc", "/admin/torrents/inventory/abc"],
 ["/admin/registry", "/admin/torrents/inventory"],
 ["/admin/registry/abc", "/admin/torrents/inventory/abc"],
 ["/admin/setup/migrate", "/admin/advanced/arr/migrate"],
 ["/admin/advanced/migration", "/admin/advanced/arr/comparison"],
 ["/admin/advanced/migration/3", "/admin/advanced/arr/comparison/3"],
 ["/admin/shadow", "/admin/advanced/arr/comparison"],
 ["/admin/shadow/3", "/admin/advanced/arr/comparison/3"],
 ["/admin/advanced/imports", "/admin/advanced/arr/imports"],
 ["/admin/system/imports", "/admin/advanced/arr/imports"],
])("ia2 redirects %s with query and hash intact", (from, to) => {
 expect(legacyTarget(from, "?x=1", "#y")).toBe(to + "?x=1#y");
});

it("sends Settings › General bookmarks to the pages that own its panels now", () => {
 expect(legacyTarget("/admin/settings/general", "?x=1", "#provider-sync")).toBe("/admin/settings/downloads?x=1#provider-sync");
 expect(legacyTarget("/admin/settings/general", "", "#artwork-cache")).toBe("/admin/settings/metadata#artwork-cache");
 expect(legacyTarget("/admin/settings/general", "", "#scan-roots")).toBe("/admin/torrents#scan-roots");
 expect(legacyTarget("/admin/settings/general", "?x=1", "#torrent-backups")).toBe("/admin/system/backup?x=1#torrent-backups");
 expect(legacyTarget("/admin/settings/general")).toBe("/admin/settings");
});
