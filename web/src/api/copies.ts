// The library's copy-targeted bulk monitor (POST /titles/monitor, docs/design/ui.md "Library views"): a preview
// (dry_run) first, then the apply with the preview's values as expect. It only sets monitored on existing copies, and
// nothing is searched.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";
import { titleChanged } from "./query-keys";
import { plural } from "@/lib/format";

export type BulkMonitorRequest = Schemas["BulkMonitorRequest"];
export type BulkMonitorResult = Schemas["BulkMonitorResult"];
export type BulkItem = BulkMonitorResult["items"][number];

/** POST /titles/monitor takes at most this many titles (internal/web maxBulkTitles). */
const MONITOR_GROUP = 500;

/** The copies an answer changes (or would) and the libraries they are in: "4 copies in 2 libraries", as the server
 *  counts them (internal/web bulkWords). */
export function changedCopies(out: Pick<BulkMonitorResult, "items" | "changed">): string {
  const libs = new Set(out.items.filter((i) => i.outcome === "changed").map((i) => i.version)).size;
  return `${plural(out.changed, "copy", "copies")}${libs ? ` in ${plural(libs, "library", "libraries")}` : ""}`;
}

/** The sums of joined answers, worded as the server words one answer (internal/web bulkWords). */
function sumWords(out: BulkMonitorResult, to: boolean): string {
  const verb = out.dry_run ? (to ? "Would monitor" : "Would unmonitor") : to ? "Monitored" : "Unmonitored";
  const parts = [`${verb} ${changedCopies(out)}`];
  if (out.unchanged > 0) parts.push(`${out.unchanged} already ${to ? "monitored" : "not monitored"}`);
  if (out.missing > 0) parts.push(`${out.missing} missing`);
  if (out.stale > 0) parts.push(`${out.stale} changed since the preview`);
  return parts.join(" · ");
}

/** The bulk monitor in groups of 500 titles, one after another; the answers are joined in the selection's order and
 *  the message is said again for the sums. An apply that fails after a group went through says what was done and
 *  that the rest was not changed. */
export async function bulkMonitor(body: BulkMonitorRequest): Promise<BulkMonitorResult> {
  const ids = body.title_ids;
  if (ids.length <= MONITOR_GROUP) return api<BulkMonitorResult>("/titles/monitor", { method: "POST", body });
  const out: BulkMonitorResult = { dry_run: !!body.dry_run, items: [], changed: 0, unchanged: 0, missing: 0, stale: 0, message: "" };
  for (let i = 0; i < ids.length; i += MONITOR_GROUP) {
    const group = new Set(ids.slice(i, i + MONITOR_GROUP));
    const expect = body.expect?.filter((e) => group.has(e.title_id));
    let r: BulkMonitorResult;
    try {
      r = await api<BulkMonitorResult>("/titles/monitor", { method: "POST", body: { ...body, title_ids: [...group], ...(expect ? { expect } : {}) } });
    } catch (e) {
      if (body.dry_run || i === 0) throw e;
      const why = e instanceof Error ? e.message : String(e);
      throw new Error(`${sumWords(out, body.monitored)} for the first ${i} of ${ids.length} titles; the other ${ids.length - i} were not changed: ${why}`);
    }
    out.dry_run = r.dry_run;
    out.items.push(...r.items);
    out.changed += r.changed;
    out.unchanged += r.unchanged;
    out.missing += r.missing;
    out.stale += r.stale;
  }
  out.message = sumWords(out, body.monitored);
  return out;
}

/** The bulk monitor; an apply (not a preview) refreshes every title list and every title page. */
export function useBulkMonitor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: bulkMonitor,
    // Also after a failed apply: a group before the failure may have gone through.
    onSettled: (_r, _e, body) => {
      if (body.dry_run) return;
      for (const queryKey of titleChanged()) void qc.invalidateQueries({ queryKey });
      void qc.invalidateQueries({ queryKey: ["title"] });
    },
  });
}
