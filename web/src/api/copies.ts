// The library's copy-targeted bulk monitor (POST /titles/monitor, docs/design/ui.md "Library views"): a preview
// (dry_run) first, then the apply with the preview's values as expect. It only sets monitored on existing copies, and
// nothing is searched.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";
import { titleChanged } from "./query-keys";

export type BulkMonitorRequest = Schemas["BulkMonitorRequest"];
export type BulkMonitorResult = Schemas["BulkMonitorResult"];
export type BulkItem = BulkMonitorResult["items"][number];

export const bulkMonitor = (body: BulkMonitorRequest) => api<BulkMonitorResult>("/titles/monitor", { method: "POST", body });

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
