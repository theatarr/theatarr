import { createElement, useSyncExternalStore } from "react";
import { Button } from "@/components/ui/button";
export type SettingsPage = { id: string; system?: boolean; label: string; route: string; arr: string; description: string; tabs: { id: string; label: string; route: string }[] };
export type SettingsPanel = { id: string; page: string; tab?: string; title: string; help?: string; section: string; keys: string[]; advanced?: boolean };
export const SETTINGS_PAGES: SettingsPage[] = [
  { id: "backup", system: true, label: "Backup", route: "/admin/system/backup", arr: "System", description: "Theatarr backups and saved torrent lists.", tabs: [] },
  { id: "torrents", system: true, label: "Torrents", route: "/admin/torrents", arr: "", description: "Provider torrents and what uses them.", tabs: [] },
  {
    "id": "libraries",
    "label": "Libraries",
    "route": "/admin/settings/libraries",
    "arr": "Media Management",
    "description": "Library folders, naming and file management.",
    "tabs": [
      {
        "id": "libraries",
        "label": "Libraries",
        "route": "/admin/settings/libraries"
      },
      {
        "id": "naming",
        "label": "Naming",
        "route": "/admin/settings/libraries/naming"
      },
      {
        "id": "files",
        "label": "File management",
        "route": "/admin/settings/libraries/files"
      }
    ]
  },
  {
    "id": "profiles",
    "label": "Profiles",
    "route": "/admin/settings/profiles",
    "arr": "Profiles",
    "description": "Quality profiles and preferences.",
    "tabs": [
      {
        "id": "profiles",
        "label": "Profiles",
        "route": "/admin/settings/profiles"
      },
      {
        "id": "check",
        "label": "Check a release",
        "route": "/admin/settings/profiles/check"
      }
    ]
  },
  {
    "id": "formats",
    "label": "Custom formats",
    "route": "/admin/settings/formats",
    "arr": "Custom Formats",
    "description": "Rules that match releases and influence quality scores.",
    "tabs": []
  },
  {
    "id": "quality-sync",
    "label": "Quality sync",
    "route": "/admin/settings/quality-sync",
    "arr": "Recyclarr",
    "description": "Sync quality presets: sources, preview, run and the update log.",
    "tabs": []
  },
  {
    "id": "indexers",
    "label": "Indexers",
    "route": "/admin/settings/indexers",
    "arr": "Prowlarr Indexers",
    "description": "Search sources, VPN and feed checks.",
    "tabs": [
      {
        "id": "indexers",
        "label": "Indexers",
        "route": "/admin/settings/indexers"
      },
      {
        "id": "vpn",
        "label": "VPN",
        "route": "/admin/settings/indexers/vpn"
      },
      {
        "id": "catalog",
        "label": "Catalog",
        "route": "/admin/settings/indexers/catalog"
      },
      {
        "id": "rss",
        "label": "RSS",
        "route": "/admin/settings/indexers/rss"
      }
    ]
  },
  {
    "id": "downloads",
    "label": "Download clients",
    "route": "/admin/settings/downloads",
    "arr": "Download Clients",
    "description": "Debrid providers and other download clients.",
    "tabs": [
      {
        "id": "providers",
        "label": "Debrid providers",
        "route": "/admin/settings/downloads"
      },
      {
        "id": "clients",
        "label": "Other clients",
        "route": "/admin/settings/downloads/clients"
      },
      {
        "id": "behaviour",
        "label": "Behaviour",
        "route": "/admin/settings/downloads/behaviour"
      }
    ]
  },
  {
    "id": "notifications",
    "label": "Notifications",
    "route": "/admin/settings/notifications",
    "arr": "Connect",
    "description": "Where and when Theatarr sends messages.",
    "tabs": []
  },
  {
    "id": "plex",
    "label": "Plex & playback",
    "route": "/admin/settings/plex",
    "arr": "Connect \u203a Plex",
    "description": "Plex connections, playback and playlists.",
    "tabs": []
  },
  {
    "id": "metadata",
    "label": "Metadata & artwork",
    "route": "/admin/settings/metadata",
    "arr": "Metadata Source",
    "description": "Metadata services, refresh schedules and the artwork cache.",
    "tabs": []
  },
  {
    "id": "apps",
    "label": "Connected apps",
    "route": "/admin/settings/apps",
    "arr": "Applications",
    "description": "Radarr- and Sonarr-compatible endpoints for Seerr and other apps: their libraries and API keys.",
    "tabs": []
  }
];
export const ADVANCED_SETTINGS_PAGES: SettingsPage[] = [
 { id: "config", label: "YAML settings", route: "/admin/advanced/config", arr: "", description: "Edit settings sections as YAML, inspect policy files and export settings.", tabs: [{ id: "files", label: "Policy files", route: "/admin/advanced/config" }, { id: "sections", label: "Sections", route: "/admin/advanced/config/sections" }, { id: "import", label: "Import & export", route: "/admin/advanced/config/import" }] },
 { id: "history", label: "Settings history", route: "/admin/advanced/history", arr: "", description: "Changes to settings and who made them.", tabs: [] },
 { id: "limits", label: "Request limits", route: "/admin/advanced/limits", arr: "", description: "Request budgets and back-offs.", tabs: [] },
];
export const SETTINGS_PANELS: SettingsPanel[] = [
  {
    "id": "mount",
    "page": "libraries",
    "title": "Mount and folders",
    "section": "library",
    "keys": [
      "mount"
    ],
    "tab": "files"
  },
  {
    "id": "links",
    "page": "libraries",
    "title": "Broken link checks",
    "section": "library",
    "keys": [
      "relink_interval",
      "dangling"
    ],
    "tab": "files"
  },
  {
    "id": "trash",
    "page": "libraries",
    "title": "Trash",
    "section": "library",
    "keys": [
      "trash"
    ],
    "tab": "files"
  },
  {
    "id": "inspection",
    "page": "libraries",
    "title": "Media inspection",
    "section": "library",
    "keys": [
      "probe",
      "sample"
    ],
    "tab": "files",
    "advanced": true
  },
  {
    "id": "vpn",
    "page": "indexers",
    "title": "VPN guard",
    "section": "egress",
    "keys": [
      "check_url",
      "vpn_ips",
      "vpn_cidrs",
      "interval",
      "max_age",
      "timeout",
      "require_vpn_for"
    ],
    "tab": "vpn"
  },
  {
    "id": "catalog",
    "page": "indexers",
    "title": "Indexer catalog and Prowlarr",
    "section": "cardigann",
    "keys": [
      "definitions",
      "flaresolverr_url",
      "prowlarr"
    ],
    "tab": "catalog"
  },
  {
    "id": "rss",
    "page": "indexers",
    "title": "RSS feed checks",
    "section": "shadow",
    "keys": [
      "rss",
      "rss_interval"
    ],
    "tab": "rss"
  },
  {
    "id": "clients",
    "page": "downloads",
    "title": "Other download clients",
    "section": "clients",
    "keys": [
      ""
    ],
    "tab": "clients"
  },
  {
    "id": "grabbing",
    "page": "downloads",
    "title": "What to grab",
    "section": "router",
    "keys": [
      "cached_only",
      "alldebrid_delete_uncached"
    ],
    "tab": "behaviour"
  },
  {
    "id": "searches",
    "page": "downloads",
    "title": "Automatic search",
    "section": "router",
    "keys": [
      "wanted"
    ],
    "tab": "behaviour"
  },
  {
    "id": "cache-checks",
    "page": "downloads",
    "title": "Cache checks",
    "section": "router",
    "keys": [
      "cache_evidence"
    ],
    "tab": "behaviour"
  },
  {
    "id": "timing",
    "page": "downloads",
    "title": "Retries and timing",
    "section": "router",
    "keys": [
      "cached_grace",
      "stalled_after",
      "downloading_timeout",
      "ready_timeout",
      "miss_ttl",
      "poll_interval",
      "max_candidates",
      "trackers"
    ],
    "tab": "behaviour",
    "advanced": true
  },
  {
    "id": "targets",
    "page": "notifications",
    "title": "Destinations",
    "section": "notify",
    "keys": [
      "enabled",
      "targets",
      "digest_at"
    ]
  },
  {
    "id": "delivery",
    "page": "notifications",
    "title": "Delivery",
    "section": "notify",
    "keys": [
      "allow_hosts",
      "budget",
      "retries",
      "poll"
    ],
    "advanced": true
  },
  {
    "id": "plex",
    "page": "plex",
    "title": "Plex server",
    "section": "library",
    "keys": [
      "plex"
    ]
  },
  {
    "id": "plex-libraries",
    "page": "plex",
    "title": "Plex library for each Theatarr library",
    "section": "library",
    "keys": [
      "versions"
    ]
  },
  {
    "id": "playlists",
    "page": "plex",
    "title": "Playlists",
    "section": "playlists",
    "keys": [
      "enabled",
      "orders",
      "routes",
      "skip_optional",
      "name",
      "franchises",
      "interval"
    ]
  },
  {
    "id": "sources",
    "page": "metadata",
    "title": "Metadata services",
    "section": "metadata",
    "keys": [
      "sources"
    ]
  },
  {
    "id": "refresh",
    "page": "metadata",
    "title": "Refresh schedules",
    "section": "metadata",
    "keys": [
      "art",
      "series",
      "lists",
      "anime"
    ]
  },
  {
    "id": "overrides",
    "page": "metadata",
    "title": "Overrides folder",
    "section": "metadata",
    "keys": [
      "overrides_dir"
    ],
    "advanced": true
  },
  {
    "id": "provider-sync",
    "page": "downloads",
    "tab": "providers",
    "title": "Provider account sync",
    "section": "provider_sync",
    "keys": [
      "interval"
    ]
  },
  {
    "id": "artwork-cache",
    "page": "metadata",
    "title": "Artwork cache",
    "section": "ui",
    "keys": [
      "images"
    ]
  },
  {
    "id": "torrent-backups",
    "page": "backup",
    "title": "Torrent-list backups",
    "section": "torrents",
    "keys": [
      "backup"
    ]
  },
  {
    "id": "scan-roots",
    "page": "torrents",
    "title": "Other libraries\u2019 folders",
    "section": "torrents",
    "keys": [
      "scan_roots"
    ],
    "help": "Folders of other libraries to check before calling a provider torrent unused."
  }
];
export function panelHref(panel: SettingsPanel) {
 const page = [...SETTINGS_PAGES, ...ADVANCED_SETTINGS_PAGES].find(p => p.id === panel.page)!;
 return (page.tabs.find(t => t.id === panel.tab)?.route ?? page.route) + "#" + panel.id;
}
export function settingHref(section: string, key: string) {
 key = key.split(".")[0];
 const panel = SETTINGS_PANELS.find(p => p.section === section && p.keys.includes(key));
 return panel ? panelHref(panel) : undefined;
}
const storageKey = "theatarr.settings.advanced";
const listeners = new Set<() => void>();
function readAdvanced() { try { return localStorage.getItem(storageKey) === "true"; } catch { return false; } }
let advanced = readAdvanced();
function subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener); }; }
function setAdvanced(next: boolean) {
 advanced = next;
 try { localStorage.setItem(storageKey, String(next)); } catch { /* Storage can be unavailable. */ }
 listeners.forEach(l => l());
}
export function useShowAdvanced() {
 const value = useSyncExternalStore(subscribe, () => advanced);
 return [value, setAdvanced] as const;
}
export function ShowAdvancedToggle() {
 const [value, set] = useShowAdvanced();
 return createElement(Button, { variant: "outline", "aria-pressed": value, onClick: () => set(!value) }, "Show advanced settings");
}

export const SETTINGS_ALIASES: { terms: string[]; href: string; title?: string }[] = [
 { terms: ["root folder", "media management"], href: "/admin/settings/libraries" },
 { terms: ["quality profile"], href: "/admin/settings/profiles" },
 { terms: ["recyclarr", "trash"], href: "/admin/settings/quality-sync" },
 { terms: ["prowlarr", "jackett", "torznab", "newznab"], href: "/admin/settings/indexers" },
 { terms: ["proxy", "vpn"], href: "/admin/settings/indexers/vpn" },
 { terms: ["debrid", "real-debrid", "alldebrid", "torbox", "router", "grabbing"], href: "/admin/settings/downloads" },
 { terms: ["qbittorrent", "seedbox", "decypharr", "download client"], href: "/admin/settings/downloads/clients" },
 { terms: ["connect", "webhook", "discord", "ntfy"], href: "/admin/settings/notifications" },
 { terms: ["rclone"], href: "/admin/settings/libraries/files#mount" },
 { terms: ["seerr", "overseerr", "api key for seerr", "view bindings", "arr views", "applications"], href: "/admin/settings/apps" },
 { terms: ["backup", "restore", "theatarr backup"], href: "/admin/system/backup", title: "Theatarr backups and restore" },
 { terms: ["backup"], href: "/admin/system/backup#torrent-backups" },
 // Page names from docs/arr-map.md, including routes with no separate settings editor.
 {"terms": ["series", "sonarr series"], "href": "/series", "title": "Library › Series"},
 {"terms": ["add new", "movies › add new", "prowlarr search", "radarr add new", "search", "series › add new", "sonarr add new"], "href": "/search", "title": "Search"},
 {"terms": ["import library", "library import", "movies › import library", "radarr import library", "series › library import", "sonarr library import", "import from sonarr", "import from radarr"], "href": "/admin/arr-import", "title": "Admin › Import from Sonarr/Radarr"},
 {"terms": ["calendar", "radarr calendar", "sonarr calendar"], "href": "/calendar", "title": "Calendar"},
 {"terms": ["sonarr statistics", "statistics"], "href": "/admin/settings", "title": "Statistics — no separate page"},
 {"terms": ["prowlarr settings", "radarr settings", "settings", "sonarr settings"], "href": "/admin/settings", "title": "Admin › Settings"},
 {"terms": ["media management", "radarr media management", "settings › media management", "sonarr media management"], "href": "/admin/settings/libraries", "title": "Admin › Settings › Libraries"},
 {"terms": ["config create / list templates", "list quality profiles / qualities / score sets", "profiles", "radarr profiles", "recyclarr config create / list templates", "recyclarr list quality profiles / qualities / score sets", "settings › profiles", "sonarr profiles"], "href": "/admin/settings/profiles", "title": "Admin › Settings › Profiles"},
 {"terms": ["custom formats", "delete custom formats", "list custom formats / custom format groups", "radarr custom formats", "recyclarr delete custom formats", "recyclarr list custom formats / custom format groups", "settings › custom formats", "sonarr custom formats"], "href": "/admin/settings/formats", "title": "Admin › Settings › Custom formats"},
 {"terms": ["indexers", "indexers › stats", "prowlarr indexers", "prowlarr stats", "radarr indexers", "settings › indexers", "sonarr indexers", "stats"], "href": "/admin/settings/indexers", "title": "Admin › Settings › Indexers"},
 {"terms": ["download clients", "prowlarr download clients", "radarr download clients", "settings › download clients", "sonarr download clients"], "href": "/admin/settings/downloads", "title": "Admin › Settings › Download clients"},
 {"terms": ["import lists", "radarr import lists", "settings › import lists", "sonarr import lists", "lists", "tmdb list"], "href": "/collections/lists", "title": "Library › Collections › Lists"},
 {"terms": ["connect", "prowlarr connect", "radarr connect", "settings › connect", "sonarr connect"], "href": "/admin/settings/notifications", "title": "Admin › Settings › Notifications"},
 {"terms": ["metadata", "radarr metadata", "settings › metadata", "sonarr metadata"], "href": "/admin/settings", "title": "Metadata — no separate page"},
 {"terms": ["prowlarr tags", "radarr tags", "settings › tags", "sonarr tags", "tags"], "href": "/admin/settings", "title": "Tags — no separate page"},
 // Settings › General's panels moved to their task pages (audit 14).
 {"terms": ["general", "prowlarr general", "radarr general", "settings › general", "sonarr general", "account sync"], "href": "/admin/settings/downloads#provider-sync", "title": "Download clients › Provider account sync (was General)"},
 {"terms": ["general", "settings › general", "artwork", "image cache"], "href": "/admin/settings/metadata#artwork-cache", "title": "Metadata & artwork › Artwork cache (was General)"},
 {"terms": ["general", "settings › general", "other libraries", "usage folders"], "href": "/admin/torrents#scan-roots", "title": "Torrents › Other libraries’ folders (was General)"},
 {"terms": ["prowlarr ui", "radarr ui", "settings › ui", "sonarr ui", "ui"], "href": "/admin/settings", "title": "UI — no separate page"},
 {"terms": ["metadata source", "settings › metadata source", "sonarr metadata source"], "href": "/admin/settings/metadata", "title": "Admin › Settings › Metadata & artwork"},
 {"terms": ["prowlarr status", "radarr status", "sonarr status", "status", "system › status"], "href": "/admin", "title": "Admin › System › Status"},
 {"terms": ["prowlarr tasks", "radarr tasks", "sonarr tasks", "system › tasks", "tasks"], "href": "/admin/system/tasks", "title": "Admin › System › Tasks"},
 {"terms": ["backup", "backups", "prowlarr backup", "radarr backup", "sonarr backups", "system › backup", "system › backups"], "href": "/admin/system/backup", "title": "Admin › System › Backup"},
 {"terms": ["prowlarr updates", "radarr updates", "sonarr updates", "system › updates", "updates"], "href": "/admin/settings", "title": "Updates — no separate page"},
 {"terms": ["events", "prowlarr events", "radarr events", "sonarr events", "system › events"], "href": "/admin/system/errors", "title": "Admin › System › Errors"},
 {"terms": ["log files", "prowlarr log files", "radarr log files", "sonarr log files", "system › log files"], "href": "/admin/system/logs", "title": "Admin › System › Logs"},
 {"terms": ["sonarr update log files", "system › update log files", "update log files"], "href": "/admin/settings", "title": "Update Log Files — no separate page"},
 {"terms": ["activity › queue", "queue", "radarr queue", "sonarr queue"], "href": "/activity", "title": "Activity › Queue"},
 {"terms": ["activity › history", "history", "radarr history", "sonarr history"], "href": "/activity/history", "title": "Activity › History"},
 {"terms": ["activity › blocklist", "blocklist", "radarr blocklist", "sonarr blocklist"], "href": "/activity/blocklist", "title": "Activity › Blocklist"},
 {"terms": ["missing", "radarr missing", "sonarr missing", "wanted › missing"], "href": "/wanted", "title": "Wanted › Missing"},
 {"terms": ["cutoff unmet", "radarr cutoff unmet", "sonarr cutoff unmet", "wanted › cutoff unmet"], "href": "/wanted/cutoff", "title": "Wanted › Cutoff unmet"},
 {"terms": ["movies", "radarr movies"], "href": "/movies", "title": "Library › Movies"},
 {"terms": ["collections", "movies › collections", "radarr collections", "add collection"], "href": "/collections", "title": "Library › Collections"},
 {"terms": ["discover", "movies › discover", "radarr discover"], "href": "/", "title": "Home"},
 {"terms": ["history", "prowlarr history"], "href": "/activity/journal", "title": "Activity › Decision journal"},
 {"terms": ["manual import"], "href": "/wanted/manual-import", "title": "Wanted › Manual import"},
 {"terms": ["indexers", "prowlarr indexers", "settings › indexers"], "href": "/admin/settings/indexers/vpn", "title": "Admin › Settings › Indexers › VPN"},
 {"terms": ["prowlarr apps", "settings › apps"], "href": "/admin/settings/apps"},
 {"terms": ["development", "prowlarr development", "settings › development"], "href": "/admin/settings", "title": "Development — no separate page"},
 {"terms": ["config list local", "recyclarr config list local", "recyclarr sync", "sync"], "href": "/admin/settings/quality-sync", "title": "Admin › Settings › Quality sync"},
 {"terms": ["list media naming", "recyclarr list media naming"], "href": "/admin/settings/libraries/naming", "title": "Admin › Settings › Libraries › Naming"},
 {"terms": ["recyclarr state repair / migrate", "state repair / migrate"], "href": "/admin/settings", "title": "State repair / migrate — no separate page"},

 { terms: ["quality", "quality sizes", "quality definitions", "size limits", "radarr quality", "sonarr quality", "settings › quality"], href: "/admin/settings/profiles", title: "Profiles › Size limits (in each profile's editor)" },
];
