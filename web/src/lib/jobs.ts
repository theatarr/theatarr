// What became of a job, for the Jobs tab and Needs attention (internal/jobs manage.go): a failed job needs attention
// until a later done job of the same kind and payload resolves it, it is retried, or someone dismisses it.
import type { Schemas } from "@/api/client";

export type Job = Schemas["Job"];

export type JobNote = { key: string; label: string; tone: "neutral" | "info" };

/** The small badges after a job's state: retry links, resolved, dismissed. All muted. */
export function jobNotes(j: Job): JobNote[] {
  const out: JobNote[] = [];
  if (j.retry_of) out.push({ key: "retry-of", label: `retry of #${j.retry_of}`, tone: "neutral" });
  if (j.state === "failed") {
    if (j.resolved_by) out.push({ key: "resolved", label: `resolved by #${j.resolved_by}`, tone: "neutral" });
    if (j.retried_as) out.push({ key: "retried", label: `retried as #${j.retried_as}`, tone: "info" });
    if (j.dismissed_at) out.push({ key: "dismissed", label: "dismissed", tone: "neutral" });
  }
  return out;
}

/** Retry makes sense for a failed job nothing has resolved or retried yet. */
export const canRetry = (j: Job) => j.state === "failed" && !j.resolved_by && !j.retried_as;

/** Dismiss is for a failed job not yet dismissed. */
export const canDismiss = (j: Job) => j.state === "failed" && !j.dismissed_at;

/** The failed-job count that needs attention: the server's attention count, else (an older server) failed. */
export function attentionCount(counts?: Record<string, number>): number {
  if (!counts) return 0;
  return counts.attention ?? counts.failed ?? 0;
}

// Friendly names for the job kinds (the one place they live); the raw kind stays visible as secondary text. A kind
// not listed shows as itself.
const JOB_KINDS: Record<string, { name: string; effect: string }> = {
  "acquire.search": { name: "Search for a title", effect: "A wanted title was not searched." },
  "acquire.wanted": { name: "Search everything wanted", effect: "The periodic search for wanted titles did not run." },
  "acquire.research": { name: "Search again after a failed download", effect: "A failed download was not replaced." },
  "router.route": { name: "Send a release to a debrid provider", effect: "A chosen release was not added to a provider." },
  "router.offer": { name: "Offer a download to other libraries", effect: "Another library for the title may miss a release it could reuse." },
  "provider.sync": { name: "Sync debrid holdings", effect: "What the providers hold may be out of date." },
  "meta.art": { name: "Refresh details and artwork", effect: "Titles may show stale details or artwork." },
  "meta.series": { name: "Refresh series episodes", effect: "New episodes may be missing from series." },
  "ceiling.compute": { name: "Work out the best available quality", effect: "Upgrade status may be out of date." },
  "plex.scan": { name: "Ask Plex to scan", effect: "New files may not show in Plex yet." },
  "shadow.rss": { name: "Check RSS feeds", effect: "The indexers' RSS feeds were not read: new releases for Live libraries may be missed." },
  "arrview.maintenance": { name: "Arr view housekeeping", effect: "Old arr-view records were not tidied." },
  "db.nightly": { name: "Nightly database backup", effect: "Last night's backup may be missing." },
};

/** A job kind in plain words. */
export const jobName = (kind?: string) => (kind ? JOB_KINDS[kind]?.name ?? kind : "Job");

/** What a failed job of this kind means, in plain words; empty for a kind not listed. */
export const jobEffect = (kind?: string) => (kind ? JOB_KINDS[kind]?.effect ?? "" : "");

export type JobGroups = { attention: Job[]; active: Job[]; settled: Job[]; done: Job[] };

/**
 * The Jobs screen's sections: failed jobs that need attention first, then running and queued, then folded away the
 * failures already resolved, retried or dismissed, and the finished ones. Duplicates (a job in both lists) appear once.
 */
export function groupJobs(failed: Job[], latest: Job[]): JobGroups {
  const seen = new Set<number>();
  const out: JobGroups = { attention: [], active: [], settled: [], done: [] };
  for (const j of [...failed, ...latest]) {
    if (j.id === undefined || seen.has(j.id)) continue;
    seen.add(j.id);
    if (j.state === "failed") (needsAttention(j) ? out.attention : out.settled).push(j);
    else if (j.state === "running" || j.state === "queued") out.active.push(j);
    else out.done.push(j);
  }
  // Running before queued, then by run time.
  out.active.sort((a, b) => (a.state === b.state ? (a.run_at ?? "").localeCompare(b.run_at ?? "") : a.state === "running" ? -1 : 1));
  return out;
}

/** A failed job nothing has resolved, retried or dismissed (the server's attention flag when it sends one). */
export function needsAttention(j: Job): boolean {
  if (j.state !== "failed") return false;
  if (j.attention !== undefined) return j.attention;
  return !j.resolved_by && !j.retried_as && !j.dismissed_at;
}
