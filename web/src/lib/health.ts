// The health checks (GET /health, internal/health) on System › Status: tones per type, and which failing
// checks join Needs attention. The VPN and failed-job checks are left out there: that list already names the VPN
// state and each failed job on its own.
import type { Schemas } from "@/api/client";
import { fmtDay } from "@/lib/format";

export type HealthCheck = Schemas["HealthResult"];
export type HealthType = NonNullable<HealthCheck["type"]>;

export const healthBadgeTone = (t?: string) =>
  t === "error" ? "negative" : t === "warning" ? "warning" : t === "notice" ? "info" : "positive";

export const healthStatTone = (t?: string): "ok" | "warn" | "bad" | undefined =>
  t === "error" ? "bad" : t === "warning" ? "warn" : t === "ok" ? "ok" : undefined;

/** Where a failing check is looked into. */
export function healthLink(source?: string): string {
  switch (source) {
    case "JobBacklogCheck":
    case "FailedJobsCheck":
      return "/admin/system/tasks";
    case "IndexerStatusCheck":
      return "/admin/settings/indexers";
    case "ProviderSyncCheck":
    case "ProviderAccountCheck":
      return "/admin/settings/downloads";
    case "VpnEgressCheck":
      return "/admin/settings/indexers/vpn";
    case "SetupCheck":
      return "/admin/setup";
    case "SettingsFallbackCheck":
      return "/admin/settings";
    case "BackupCheck":
      return "/admin/system/backup#backup-check";
    case "DiskSpaceCheck":
      return "/admin/system/backup";
    case "MetadataRefreshCheck":
      return "/admin/system/logs";
    default:
      return "/admin";
  }
}

/** What a failing check means for the household, and what to do next. */
export function healthEffect(source?: string): { effect: string; next: string } {
  switch (source) {
    case "MountCheck":
      return { effect: "Finished downloads cannot be linked into the library.", next: "Check the debrid mount on the host." };
    case "DanglingLinksCheck":
      return {
        effect: "Some library files point at debrid content that is no longer there and will not play.",
        next: "Open the list below; the repair sweep repoints renamed ones.",
      };
    case "JobBacklogCheck":
      return { effect: "Background work is running late.", next: "Look at the queue on Tasks." };
    case "IndexerStatusCheck":
      return { effect: "Searches skip the indexers that are unavailable.", next: "See which indexers are backed off or out of budget." };
    case "ProviderSyncCheck":
      return { effect: "What the debrid providers hold may be out of date.", next: "Check the provider's last sync." };
    case "ProviderAccountCheck":
      return { effect: "Downloads through that debrid account may fail.", next: "Check the account and its API key under Settings › Download clients." };
    case "VpnEgressCheck":
      return { effect: "See the configured VPN requirements and egress observations under Settings › Indexers › VPN.", next: "Check the VPN egress paths." };
    case "SetupCheck":
      return { effect: "Features whose settings are missing stay off.", next: "Finish setup and run the preflight." };
    case "SettingsFallbackCheck":
      return {
        effect: "Theatarr runs an older settings generation: the saved settings could not be loaded at start and are not in force.",
        next: "Read why in the log, then load the stored generation again in Settings.",
      };
    case "BackupCheck":
      return { effect: "A recent, complete backup set may be missing.", next: "Open the backup check: what the set holds and the fix." };
    case "DiskSpaceCheck":
      return { effect: "The database may stop accepting writes.", next: "Free space next to the database." };
    case "MetadataRefreshCheck":
      return { effect: "Titles may show stale or missing details and artwork.", next: "Check the metadata refresh in the logs." };
    default:
      return { effect: "", next: "Open the check." };
  }
}

const PROVIDER_NAMES: Record<string, string> = { rd: "Real-Debrid", ad: "AllDebrid", tb: "TorBox" };

/** "in 43 h" under two days, else "in 3 d". */
function inWords(ms: number): string {
  const h = Math.max(1, Math.round(ms / 3_600_000));
  return h < 48 ? `in ${h} h` : `in ${Math.round(h / 24)} d`;
}

/**
 * The debrid-account check (internal/health Accounts) as Status's notice: each account that needs attention by its
 * provider's name, and what to do. A premium plan that ends or ended says to renew it at the provider; a refused key
 * or an account that could not be read keeps the advice to check the key. code gives an account key's cache code (rd,
 * ad, tb: useProviderCode); null when the check's detail names no account that needs attention. next holds one sentence
 * per line.
 */
export function accountNotice(c: HealthCheck, code: (key: string) => string = (k) => k, now: Date = new Date()): { effect: string; next: string } | null {
  // advice: ProviderAccountCheck's own sentence for an account it could not read (internal/health, detail[k].advice:
  // network, VPN or key), so Needs attention and the Checks list agree; without it the key advice stands.
  const detail = (c.detail ?? {}) as Record<string, { expires?: string; premium?: boolean; error?: string; refused?: boolean; advice?: string } | undefined>;
  const lines: string[] = [];
  const renew: string[] = [];
  const advice: string[] = [];
  let key = false;
  for (const k of Object.keys(detail).sort()) {
    const d = detail[k];
    if (!d || typeof d !== "object") continue;
    const name = PROVIDER_NAMES[code(k).toLowerCase()] ?? k;
    const at = d.expires ? new Date(d.expires) : null;
    const left = at && !Number.isNaN(at.getTime()) ? at.getTime() - now.getTime() : null;
    if (d.refused || d.error) {
      lines.push(d.refused ? `${name} refused the API key.` : `${name}'s account could not be read.`);
      if (d.advice) {
        // The check's advice has no full stop (its Checks row joins the lines with semicolons): one here, so each
        // account's advice reads as a sentence on its own line.
        const a = /[.!?]$/.test(d.advice) ? d.advice : `${d.advice}.`;
        if (!advice.includes(a)) advice.push(a);
      } else key = true;
    } else if (left !== null && left <= 0) {
      lines.push(`${name} premium ended ${fmtDay(d.expires!, now)}.`);
      renew.push(name);
    } else if (d.premium === false) {
      lines.push(`${name} account is not premium.`);
      renew.push(name);
    } else if (left !== null && left < 7 * 86_400_000) {
      lines.push(`${name} premium ends ${fmtDay(d.expires!, now)} (${inWords(left)}).`);
      renew.push(name);
    }
  }
  if (lines.length === 0) return null;
  const next = [
    ...renew.map((n) => `Renew at ${n} to keep premium access.`),
    ...advice,
    key ? "Check the account and its API key under Settings › Download clients." : "",
  ].filter(Boolean).join("\n");
  return { effect: lines.join(" "), next };
}

const listedElsewhere = new Set(["VpnEgressCheck", "FailedJobsCheck"]);

/** The checks that join Needs attention: warnings and errors, worst first, not those it lists already. */
export function healthAttention(checks: HealthCheck[] | undefined): HealthCheck[] {
  const rank = (t?: string) => (t === "error" ? 2 : t === "warning" ? 1 : 0);
  return (checks ?? [])
    .filter((c) => rank(c.type) > 0 && !listedElsewhere.has(c.source ?? ""))
    .sort((a, b) => rank(b.type) - rank(a.type));
}

/** One file of a backup set and whether the newest set has it. */
export type BackupPart = { label: string; present: boolean };

/**
 * The backup check (internal/health Backup) in words for System › Backup: the newest set's files, and
 * the fix when the check fails. The check's detail holds dir, newest, age_s, count, and policy and manifest (whether
 * the policy archive and the manifest sit beside the newest database copy).
 */
export function backupCheck(c: HealthCheck | undefined): { parts: BackupPart[]; newest?: string; dir?: string; count?: number; fix?: string } {
  const d = (c?.detail ?? {}) as { dir?: string; newest?: string; age_s?: number; count?: number; policy?: boolean; manifest?: boolean };
  const parts: BackupPart[] = d.newest
    ? [
        { label: "Database copy", present: true },
        { label: "Policy archive", present: d.policy === true },
        { label: "Manifest", present: d.manifest === true },
      ]
    : [];
  const out = { parts, newest: d.newest, dir: d.dir, count: d.count };
  if (!c || c.type === "ok" || c.type === "notice") return out;
  let fix: string;
  if (!d.dir) fix = "Backups are off: start Theatarr with a backup directory (--backup-dir), then run theatarr backup now.";
  else if (!d.newest) fix = "No set in the backup directory: run theatarr backup now in the container (docker exec theatarr theatarr backup now), then theatarr backup list.";
  else if (parts.some((p) => !p.present) && (d.age_s ?? 0) < 36 * 3600)
    fix = "The newest set is incomplete: run theatarr backup now in the container (docker exec theatarr theatarr backup now), then theatarr backup list. A complete set has the database copy, the policy archive and the manifest; theatarr backup restore refuses a set without its manifest.";
  else fix = "The nightly backup task (db.nightly) has not written a set lately: check it on Tasks, then run theatarr backup now in the container (docker exec theatarr theatarr backup now).";
  return { ...out, fix };
}

/**
 * What happens next to a broken link, from the state of the link Theatarr recorded for its path (GET
 * /health/dangling link_state): a hint, not a repair log.
 */
export function danglingNext(linkState?: string): string {
  switch (linkState) {
    case "dead":
      return "Link marked missing";
    case "active":
      return "Waiting for the next repair sweep";
    case "unlinked":
      return "A link Theatarr let go of";
    default:
      return "Not a link Theatarr made";
  }
}

/** A debrid account's premium end for its provider card (Settings › Download clients), from the debrid-accounts
 *  check's detail (internal/health Accounts: detail[key].expires): within 7 days "Premium ends Nov 17 (3 days)" as a
 *  warning, after it "Premium ended Nov 10" as an error; null otherwise or when not known. */
export function premiumEnd(checks: HealthCheck[] | undefined, key: string, now: Date = new Date()): { text: string; tone: "warn" | "bad" } | null {
  const c = checks?.find((x) => x.source === "ProviderAccountCheck");
  const d = (c?.detail as Record<string, { expires?: string } | undefined> | undefined)?.[key];
  const at = d?.expires ? new Date(d.expires) : null;
  if (!at || Number.isNaN(at.getTime())) return null;
  const day = fmtDay(d!.expires!, now);
  const left = at.getTime() - now.getTime();
  if (left <= 0) return { text: `Premium ended ${day}`, tone: "bad" };
  if (left >= 7 * 86_400_000) return null;
  const days = Math.max(1, Math.round(left / 86_400_000));
  return { text: `Premium ends ${day} (${days} day${days === 1 ? "" : "s"})`, tone: "warn" };
}
