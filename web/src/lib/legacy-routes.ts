// Old admin URLs and where they live since the Admin regrouping (docs/design/ui.md): bookmarks and links from other
// screens keep working. The Decision journal and Manual import left Admin › Advanced for Activity and Wanted; the journal
// then moved on to System, and Collections › Lists to Settings › Import lists (phase J). Each old address goes straight to
// where the page lives now. The query string and hash are kept; path parameters carry over by name, exactly as they
// appear in the old address (already URL-encoded there, so not encoded again).
import { matchPath } from "react-router";

export const LEGACY_ROUTES: [from: string, to: string][] = [
  ["/activity/now", "/activity"],
  ["/activity/blocked", "/activity/blocklist"],
  ["/activity/wanted", "/wanted"],
  ["/admin/setup/migrate", "/admin/arr-import"],
  ["/admin/advanced/arr", "/admin/arr-import"],
  ["/admin/advanced/arr/migrate", "/admin/arr-import"],
  ["/admin/advanced/imports", "/admin/arr-import"],
  ["/admin/advanced/arr/imports", "/admin/arr-import"],
  ["/admin/advanced/inventory", "/admin/torrents/inventory"],
  ["/admin/advanced/inventory/:hash", "/admin/torrents/inventory/:hash"],
  ["/admin/metadata", "/admin/reviews/anime"],
  ["/admin/metadata/:slug", "/admin/reviews/anime/:slug"],
  ["/admin/jobs", "/admin/system/tasks"],
  ["/admin/errors", "/admin/system/errors"],
  ["/admin/advanced/logs", "/admin/system/logs"],
  ["/admin/advanced/database", "/admin/system/backup"],
  ["/admin/rules", "/admin/settings"],
  ["/admin/rules/files", "/admin/advanced/config"],
  ["/admin/rules/indexers", "/admin/settings/indexers"],
  ["/admin/rules/providers", "/admin/settings/downloads"],
  ["/admin/rules/budgets", "/admin/advanced/limits"],
  ["/admin/rules/profiles", "/admin/settings/profiles"],
  ["/admin/rules/formats", "/admin/settings/formats"],
  ["/admin/rules/format_sets", "/admin/settings/profiles/format_sets"],
  ["/admin/rules/naming", "/admin/settings/libraries/naming"],
  ["/admin/rules/sync_sources", "/admin/settings/quality-sync"],
  ["/admin/rules/import", "/admin/advanced/config/import"],
  ["/admin/rules/audit", "/admin/advanced/history"],
  ["/admin/settings/versions", "/admin/settings/libraries"],
  ["/admin/settings/general", "/admin/settings"],
  ["/admin/connections", "/admin/settings/downloads"],
  ["/admin/connections/sources", "/admin/settings/downloads"],
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
  ["/admin/registry", "/admin/torrents/inventory"],
  ["/admin/registry/:hash", "/admin/torrents/inventory/:hash"],
  ["/admin/journal", "/admin/system/journal"],
  ["/admin/journal/:id", "/admin/system/journal/:id"],
  ["/admin/advanced/journal", "/admin/system/journal"],
  ["/admin/advanced/journal/:id", "/admin/system/journal/:id"],
  ["/activity/journal", "/admin/system/journal"],
  ["/activity/journal/:id", "/admin/system/journal/:id"],
  ["/collections/lists", "/admin/settings/import-lists"],
  ["/admin/advanced/manual-import", "/wanted/manual-import"],
  ["/admin/logs", "/admin/system/logs"],
];

/** Settings › General was removed (audit 14): its panel anchors go to the pages that own those panels now. */
const LEGACY_ANCHORS: Record<string, Record<string, string>> = {
  "/admin/settings/general": {
    "#provider-sync": "/admin/settings/downloads",
    "#artwork-cache": "/admin/settings/metadata",
    "#scan-roots": "/admin/settings/downloads/torrents",
    "#torrent-backups": "/admin/settings/downloads/torrents",
  },
};

/** Settings panels that left a page which still exists (phase J): an old link to the panel's anchor goes on to its new
 *  page, the anchor kept; the page without that anchor stays. */
const MOVED_ANCHORS: Record<string, Record<string, string>> = {
  "/admin/settings/downloads/behaviour": { "#searches": "/admin/settings/indexers/rss" },
  "/admin/torrents": { "#scan-roots": "/admin/settings/downloads/torrents" },
  "/admin/system/backup": { "#torrent-backups": "/admin/settings/downloads/torrents" },
};

/** Where a panel anchor on a current page lives now, with the query and hash; null when it has not moved. */
export function movedAnchor(pathname: string, search = "", hash = ""): string | null {
  const to = MOVED_ANCHORS[pathname]?.[hash];
  return to ? to + search + hash : null;
}

/** Where an old URL lives now, with its query and hash; null when the path is not an old one. */
export function legacyTarget(pathname: string, search = "", hash = ""): string | null {
  const anchored = LEGACY_ANCHORS[pathname]?.[hash];
  if (anchored) return anchored + search + hash;
  for (const [from, to] of LEGACY_ROUTES) {
    if (!matchPath({ path: from, end: true }, pathname)) continue;
    // matchPath decodes some escapes (%2F becomes "/"), so each value is the old address's own segment.
    const raw = pathname.replace(/\/+$/, "").split("/");
    const params: Record<string, string> = {};
    from.split("/").forEach((seg, i) => { if (seg.startsWith(":")) params[seg.slice(1)] = raw[i] ?? ""; });
    return to.replace(/:(\w+)/g, (_, k: string) => params[k] ?? "") + search + hash;
  }
  return null;
}
