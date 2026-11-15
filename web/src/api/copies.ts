// The library's copy-targeted bulk monitor (POST /titles/monitor, docs/design/ui.md "Library views"): a preview
// (dry_run) first, then the apply with the preview's values as expect. It only sets monitored on existing copies, and
// nothing is searched.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";
import { titleChanged } from "./query-keys";

export type BulkMonitorRequest = Schemas["BulkMonitorRequest"];
export type BulkMonitorResult = Schemas["BulkMonitorResult"];
export type BulkItem = BulkMonitorResult["items"][number];

/** POST /titles/monitor takes at most this many titles (internal/web maxBulkTitles). */
const MONITOR_GROUP = 500;

/** The bulk monitor in groups of 500 titles, one after another; the answers are joined in the selection's order and
 *  the message is said again for the sums, as the server words it (internal/web bulkWords). */
export async function bulkMonitor(body: BulkMonitorRequest): Promise<BulkMonitorResult> {
  const ids = body.title_ids;
  if (ids.length <= MONITOR_GROUP) return api<BulkMonitorResult>("/titles/monitor", { method: "POST", body });
  const out: BulkMonitorResult = { dry_run: !!body.dry_run, items: [], changed: 0, unchanged: 0, missing: 0, stale: 0, message: "" };
  for (let i = 0; i < ids.length; i += MONITOR_GROUP) {
    const group = new Set(ids.slice(i, i + MONITOR_GROUP));
    const expect = body.expect?.filter((e) => group.has(e.title_id));
    const r = await api<BulkMonitorResult>("/titles/monitor", { method: "POST", body: { ...body, title_ids: [...group], ...(expect ? { expect } : {}) } });
    out.dry_run = r.dry_run;
    out.items.push(...r.items);
    out.changed += r.changed;
    out.unchanged += r.unchanged;
    out.missing += r.missing;
    out.stale += r.stale;
  }
  const to = body.monitored;
  const verb = out.dry_run ? (to ? "Would monitor" : "Would unmonitor") : to ? "Monitored" : "Unmonitored";
  const parts = [`${verb} ${out.changed === 1 ? "1 library" : `${out.changed} libraries`}`];
  if (out.unchanged > 0) parts.push(`${out.unchanged} already ${to ? "monitored" : "not monitored"}`);
  if (out.missing > 0) parts.push(`${out.missing} missing`);
  if (out.stale > 0) parts.push(`${out.stale} changed since the preview`);
  out.message = parts.join(" · ");
  return out;
}

/** The bulk monitor; an apply (not a preview) refreshes every title list and the titles it named. */
export function useBulkMonitor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: bulkMonitor,
    onSuccess: (r, req) => {
      if (r.dry_run) return;
      for (const queryKey of titleChanged()) void qc.invalidateQueries({ queryKey });
      for (const id of req.title_ids) void qc.invalidateQueries({ queryKey: ["title", String(id)] });
    },
  });
}
