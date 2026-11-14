// The Errors page's codes in words (internal/errinbox): a group's status and source, and a component that is a
// health check's type name (VpnEgressCheck) by the name System › Status gives the check (cmd/theatarr/servehealth.go).
import { wordsFor } from "./code-words";

const STATUS: Record<string, string> = { new: "New", seen: "Seen", resolved: "Resolved", ignored: "Ignored" };

/** A group's status: "seen" → "Seen". */
export const errorStatusWords = (s: string | null | undefined) => wordsFor(STATUS, s);

const SOURCES: Record<string, string> = { log: "Log", panic: "Crash", http: "Web request", job: "Task", health: "Health check" };

/** Where an error came from: "http" → "Web request". */
export const errorSourceWords = (s: string | null | undefined) => wordsFor(SOURCES, s);

const CHECKS: Record<string, string> = {
  SetupCheck: "Setup",
  SettingsFallbackCheck: "Settings generation",
  MountCheck: "Mount",
  MountHelperCheck: "Mount helpers",
  DanglingLinksCheck: "Dangling links",
  VanishFailsafeCheck: "Library links missing at once",
  MetadataRefreshCheck: "Metadata",
  JobBacklogCheck: "Job backlog",
  FailedJobsCheck: "Failed jobs",
  BackupCheck: "Backups",
  ProviderSyncCheck: "Debrid providers",
  ProviderAccountCheck: "Debrid accounts",
  UnsettledAddsCheck: "Provider adds",
  CachedOnlyCheck: "Cached-only checks",
  TorrentLimitCheck: "Torrent limits",
  IndexerStatusCheck: "Indexers",
  VpnEgressCheck: "VPN egress",
  DiskSpaceCheck: "Disk space",
};

/** A component as people read it: a health check by its name ("VpnEgressCheck" → "VPN egress"), anything else
 *  (a route, a package) as stored. */
export const errorComponentWords = (c: string | null | undefined) => (c ? CHECKS[c] ?? (/^[A-Z][A-Za-z]+Check$/.test(c) ? wordsFor({}, c.replace(/Check$/, "")) : c) : "");
