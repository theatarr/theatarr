import { describe, expect, it } from "vitest";
import { LEGACY_ROUTES, legacyTarget, movedAnchor } from "./legacy-routes";

describe("old admin URLs", () => {
  it("redirect to their new place, keeping parameters, query and hash", () => {
    const cases: [string, string][] = [
      ["/activity/now", "/activity"], ["/activity/blocked", "/activity/blocklist"], ["/activity/wanted", "/wanted"],
 ["/admin/connections/network", "/admin/advanced/limits"],
      ["/admin/system", "/admin"],
      ["/admin/system/health", "/admin"],
      ["/admin/system/jobs", "/admin/system/tasks"],
      ["/admin/system/network", "/admin/settings/indexers/vpn"],
      ["/admin/system/imports", "/admin/arr-import"],
      ["/admin/system/database", "/admin/system/backup"],
      ["/admin/sources", "/admin/settings/downloads"],
      ["/admin/policy", "/admin/settings"],
      ["/admin/keys", "/admin/users"],
      ["/admin/registry", "/admin/torrents"],
      ["/admin/registry/abcdef", "/admin/torrents?q=abcdef"],
      ["/admin/journal", "/admin/system/journal"],
      ["/admin/journal/dl:7", "/admin/system/journal/dl:7"],
      ["/admin/journal/dl%3A3", "/admin/system/journal/dl%3A3"],
      ["/admin/journal/a%2Fb", "/admin/system/journal/a%2Fb"],
      ["/admin/advanced/journal", "/admin/system/journal"],
      ["/admin/advanced/journal/dl:7", "/admin/system/journal/dl:7"],
      ["/admin/advanced/journal/dl%3A3", "/admin/system/journal/dl%3A3"],
      ["/admin/advanced/journal/a%2Fb", "/admin/system/journal/a%2Fb"],
      ["/admin/advanced/manual-import", "/wanted/manual-import"],
      ["/admin/registry/a%2Fb%3Ac", "/admin/torrents?q=a%2Fb%3Ac"],
      // K9: Saved inventory merged into Torrents; an entry opens Torrents filtered to its hash.
      ["/admin/torrents/inventory", "/admin/torrents"],
      ["/admin/torrents/inventory/abcdef", "/admin/torrents?q=abcdef"],
      ["/admin/logs", "/admin/system/logs"],
      // Phase J: the journal left Activity for System, the import lists Collections for Settings.
      ["/activity/journal", "/admin/system/journal"],
      ["/activity/journal/dl:7", "/admin/system/journal/dl:7"],
      ["/activity/journal/dl%3A3", "/admin/system/journal/dl%3A3"],
      ["/collections/lists", "/admin/settings/import-lists"],
    ];
    for (const [from, to] of cases) expect(legacyTarget(from)).toBe(to);
    expect(legacyTarget("/admin/journal", "?title=4", "#x")).toBe("/admin/system/journal?title=4#x");
    expect(legacyTarget("/admin/advanced/journal", "?suspect=1", "#x")).toBe("/admin/system/journal?suspect=1#x");
    expect(legacyTarget("/admin/torrents/inventory/abcdef", "?x=1", "#y")).toBe("/admin/torrents?q=abcdef&x=1#y");
  });
  it("leaves current URLs alone", () => {
    for (const p of ["/admin", "/admin/system/tasks", "/admin/users", "/admin/settings/anime-mappings/x", "/admin/system/journal", "/admin/system/journal/dl:7", "/admin/settings/import-lists", "/collections", "/collections/12", "/wanted/manual-import", "/movies"]) {
      expect(legacyTarget(p)).toBeNull();
    }
  });
});

it("preserves edit, raw and anchors for all moved settings routes", () => {
 for (const [from, to] of LEGACY_ROUTES.filter(([from]) => !from.includes(":"))) {
  expect(legacyTarget(from, "?edit=movie-web&raw=1", "#mount")).toBe(to + (to.includes("?") ? "&" : "?") + "edit=movie-web&raw=1#mount");
 }
});

it("preserves query and hash for every legacy route including details", () => {
 for (const [from, to] of LEGACY_ROUTES) {
  const path = from.replace(/:\w+/g, "example");
  const target = to.replace(/:\w+/g, "example");
  expect(legacyTarget(path, "?x=1", "#y")).toBe(target + (target.includes("?") ? "&" : "?") + "x=1#y");
 }
});
it.each(["tasks", "backup", "logs", "journal"])("system parent aliases do not shadow /admin/system/%s", page => {
 expect(legacyTarget(`/admin/system/${page}`, "?x=1", "#y")).toBeNull();
});

it.each([
 ["/admin/jobs", "/admin/system/tasks"],
 ["/admin/system/jobs", "/admin/system/tasks"],
 ["/admin/errors", "/admin/system/logs"],
 ["/admin/system/errors", "/admin/system/logs"],
 ["/admin/reviews", "/admin/system/journal?suspect=1"],
 ["/admin/reviews/decisions", "/admin/system/journal?suspect=1"],
 ["/admin/reviews/anime", "/admin/settings/anime-mappings"],
 ["/admin/reviews/anime/example", "/admin/settings/anime-mappings/example"],
 ["/admin/reviews/indexers", "/admin/settings/indexers"],
 ["/admin/reviews/quality", "/admin/settings/quality-sync"],
 ["/admin/advanced/logs", "/admin/system/logs"],
 ["/admin/logs", "/admin/system/logs"],
 ["/admin/advanced/database", "/admin/system/backup"],
 ["/admin/system/database", "/admin/system/backup"],
 ["/admin/metadata", "/admin/settings/anime-mappings"],
 ["/admin/metadata/example", "/admin/settings/anime-mappings/example"],
 ["/admin/advanced/inventory", "/admin/torrents"],
 ["/admin/registry", "/admin/torrents"],
 ["/admin/setup/migrate", "/admin/arr-import"],
 ["/admin/advanced/arr", "/admin/arr-import"],
 ["/admin/advanced/arr/migrate", "/admin/arr-import"],
 ["/admin/advanced/imports", "/admin/arr-import"],
 ["/admin/advanced/arr/imports", "/admin/arr-import"],
 ["/admin/system/imports", "/admin/arr-import"],
])("ia2 redirects %s with query and hash intact", (from, to) => {
 expect(legacyTarget(from, "?x=1", "#y")).toBe(to + (to.includes("?") ? "&" : "?") + "x=1#y");
});

it("sends Settings › General bookmarks to the pages that own its panels now", () => {
 expect(legacyTarget("/admin/settings/general", "?x=1", "#provider-sync")).toBe("/admin/settings/downloads?x=1#provider-sync");
 expect(legacyTarget("/admin/settings/general", "", "#artwork-cache")).toBe("/admin/settings/metadata#artwork-cache");
 expect(legacyTarget("/admin/settings/general", "", "#scan-roots")).toBe("/admin/settings/downloads/torrents#scan-roots");
 expect(legacyTarget("/admin/settings/general", "?x=1", "#torrent-backups")).toBe("/admin/settings/downloads/torrents?x=1#torrent-backups");
 expect(legacyTarget("/admin/settings/general")).toBe("/admin/settings");
});

it("sends a moved panel's anchor on to its new page and leaves the old page alone", () => {
 expect(movedAnchor("/admin/settings/downloads/behaviour", "?x=1", "#searches")).toBe("/admin/settings/indexers/rss?x=1#searches");
 expect(movedAnchor("/admin/torrents", "", "#scan-roots")).toBe("/admin/settings/downloads/torrents#scan-roots");
 expect(movedAnchor("/admin/system/backup", "", "#torrent-backups")).toBe("/admin/settings/downloads/torrents#torrent-backups");
 for (const [path, hash] of [["/admin/settings/downloads/behaviour", ""], ["/admin/settings/downloads/behaviour", "#grabbing"], ["/admin/torrents", ""], ["/admin/system/backup", ""]]) expect(movedAnchor(path, "", hash)).toBeNull();
});
