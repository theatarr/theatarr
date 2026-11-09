// Failed-job management: retry, dismiss, dismiss all resolved (POST /jobs/{id}/retry, /jobs/{id}/dismiss,
// /jobs/dismiss-resolved). Each refreshes the job queries; the event stream also carries the new counts.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useLive } from "./events";
import { toast } from "@/components/toaster";
import { api, qs, type Schemas } from "./client";
import type { JobList } from "./hooks";
import { qk } from "./query-keys";

type JobFilter = { kind?: string; state?: string; attention?: boolean; dismissed?: boolean; limit?: number };

/** GET /jobs with the attention and dismissed filters. */
export const useJobsFiltered = (f: JobFilter) =>
  useQuery({ queryKey: qk.jobs.filtered(f), queryFn: () => api<JobList>("/jobs" + qs(f)) });

export function useJobActions() {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: qk.jobs.all });
  const failed = (what: string) => (e: Error) => toast({ tone: "bad", title: `${what} failed`, body: e.message });
  const retry = useMutation({
    mutationFn: (id: number) => api<Schemas["JobRetry"]>(`/jobs/${id}/retry`, { method: "POST" }),
    onSuccess: (r) => {
      toast({ tone: "ok", title: r.created ? `Job #${r.retry_of} queued again as #${r.job.id}` : `Job #${r.retry_of} is already being retried as #${r.job.id}` });
      refresh();
    },
    onError: failed("Retry"),
  });
  const dismiss = useMutation({
    mutationFn: (id: number) => api<Schemas["Job"]>(`/jobs/${id}/dismiss`, { method: "POST" }),
    onSuccess: (j) => {
      toast({ tone: "info", title: `Job #${j.id} dismissed` });
      refresh();
    },
    onError: failed("Dismiss"),
  });
  const dismissResolved = useMutation({
    mutationFn: () => api<{ dismissed: number }>("/jobs/dismiss-resolved", { method: "POST" }),
    onSuccess: (r) => {
      toast({ tone: "info", title: `Dismissed ${r.dismissed} resolved job${r.dismissed === 1 ? "" : "s"}` });
      refresh();
    },
    onError: failed("Dismiss"),
  });
  return { retry, dismiss, dismissResolved };
}

/** The job counts: the live stream's when it has spoken, else the query's. */
export function useJobCounts(jobs?: JobList): Record<string, number> {
  const live = useLive();
  return live.jobCounts ?? jobs?.counts ?? {};
}
