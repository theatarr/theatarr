// Activity › Now triage (docs/design/ui.md): the stage filters (Needs attention, Downloading: real transfers only,
// Waiting, All; ?show= in the URL), the short stage name each row shows, and how long a download has waited. The
// server sends waiting_since and next_retry when it knows them (internal/web/activity.go waitTimes).
import type { ActivityDownload } from "@/api/activity";

export type ActivityShow = "attention" | "downloading" | "waiting" | "all";

export const SHOWS: { id: ActivityShow; label: string; userLabel?: string }[] = [
  { id: "attention", label: "Needs attention", userLabel: "Delayed" },
  { id: "downloading", label: "Downloading" },
  { id: "waiting", label: "Waiting" },
  { id: "all", label: "All" },
];

/** The filter a ?show= value names; All for anything else. */
export function parseShow(v: string | null | undefined): ActivityShow {
  return SHOWS.some((s) => s.id === v) ? (v as ActivityShow) : "all";
}

type D = Pick<
  ActivityDownload,
  "state" | "steps" | "import_state" | "updated_at" | "waiting_since" | "next_retry" | "not_started"
>;

const MIN = 60_000;
const HOUR = 60 * MIN;

/** How long a wait may last in each state before it needs a look. */
export const WAIT_LIMIT: Record<string, number> = { queued: 6 * HOUR, added: 2 * HOUR, ready: HOUR, importing: HOUR };
/** An active download that has not moved for this long needs a look too. */
export const STILL_LIMIT = 6 * HOUR;

const ms = (iso?: string) => (iso ? new Date(iso).getTime() : NaN);

/** A wait with a time limit (WAIT_LIMIT): queued, added (the provider has not started), or finished and waiting to
 * be linked. */
function waitsForTurn(d: D): boolean {
  return d.state === "queued" || d.state === "added" || d.state === "ready" || (d.state === "importing" && !!d.waiting_since);
}

/** Waiting: in hand and not transferring: queued, starting (the provider add), finished, or being linked. The library
 * views' stage links for those stages open this filter (lib/copies.ts activityHref). */
export function isWaiting(d: D): boolean {
  return waitsForTurn(d) || d.state === "adding" || d.state === "importing";
}

/** Downloading: only a provider transfer in progress. A failure (a failed step, a refused link) is not one: it shows
 * under Needs attention; a linked download only under All. */
export function isTransfer(d: D): boolean {
  if (d.state !== "downloading") return false;
  if (d.steps?.some((s) => s.state === "failed")) return false;
  return d.import_state !== "rejected" && d.import_state !== "failed";
}

/** Needs attention: a failed step, a refused or partial link, a wait past its limit, or no progress for hours. A
 * download not started because downloads were off (not_started) does not: it never failed. */
export function needsAttention(d: D, now: number = Date.now()): boolean {
  if (d.not_started) return false;
  if (d.state === "failed" || d.steps?.some((s) => s.state === "failed")) return true;
  if (d.import_state === "rejected" || d.import_state === "failed" || d.import_state === "partial") return true;
  if (waitsForTurn(d)) {
    const since = ms(d.waiting_since);
    const limit = WAIT_LIMIT[d.state];
    return limit !== undefined && !Number.isNaN(since) && now - since > limit;
  }
  const upd = ms(d.updated_at);
  return !Number.isNaN(upd) && now - upd > STILL_LIMIT;
}

export function matchesShow(d: D, show: ActivityShow, now: number = Date.now()): boolean {
  switch (show) {
    case "attention":
      return needsAttention(d, now);
    case "waiting":
      return isWaiting(d);
    case "downloading":
      return isTransfer(d);
    default:
      return true;
  }
}

export function filterDownloads<T extends D>(items: T[], show: ActivityShow, now: number = Date.now()): T[] {
  return items.filter((d) => matchesShow(d, show, now));
}

export function showCounts(items: D[], now: number = Date.now()): Record<ActivityShow, number> {
  const out: Record<ActivityShow, number> = { attention: 0, downloading: 0, waiting: 0, all: items.length };
  for (const d of items) {
    if (needsAttention(d, now)) out.attention++;
    if (isWaiting(d)) out.waiting++;
    else if (isTransfer(d)) out.downloading++;
  }
  return out;
}

/**
 * The stage in a few words. A provider's finished download is "Download complete", never "ready": it still has to be
 * linked, and "ready to watch" is kept for the library.
 */
export function stageLabel(d: D): string {
  switch (d.state) {
    case "queued":
      return d.next_retry ? "Waiting for a provider" : "Queued";
    case "adding":
      return "Starting";
    case "added":
      return "Waiting to start";
    case "downloading":
      return "Downloading";
    case "ready":
      switch (d.import_state) {
        case "waiting_mount":
          return "Waiting for the mount";
        case "rejected":
          return "Rejected by the library";
        case "failed":
          return "Linking failed";
        case "partial":
          return "Partly linked";
      }
      return "Download complete";
    case "importing":
      return d.import_state === "partial" ? "Partly linked" : "Linking";
    case "imported":
      return "Linked";
    case "failed":
      return d.not_started ? "Not started" : "Failed";
  }
  return d.state;
}

/** A span of time: "45 s", "12 min", "8 h", "3 d". */
export function fmtSpan(msSpan: number): string {
  const s = Math.max(0, Math.round(msSpan / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) return `${Math.round(s / 3600)} h`;
  return `${Math.round(s / 86400)} d`;
}

/** The timing line: how long it has waited, when it was last updated, when it is tried next (each when known). */
export function timing(d: D, now: number = Date.now()): { waited?: string; updated?: string; retry?: string } {
  const out: { waited?: string; updated?: string; retry?: string } = {};
  const since = ms(d.waiting_since);
  if (!Number.isNaN(since)) out.waited = fmtSpan(now - since);
  const upd = ms(d.updated_at);
  if (!Number.isNaN(upd)) out.updated = `${fmtSpan(now - upd)} ago`;
  const retry = ms(d.next_retry);
  if (!Number.isNaN(retry) && retry > now) out.retry = `in ${fmtSpan(retry - now)}`;
  return out;
}
