// The error inbox (docs/design/errors.md; admins): GET /errors, GET /errors/{id}, POST /errors/{id}, POST /errors/bulk.
// Every change refreshes the errors queries; the nav badge polls the count of new rows.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "@/components/toaster";
import { api, qs, type Schemas } from "./client";
import { qk } from "./query-keys";

export type ErrorEvent = Schemas["ErrorEvent"];
export type ErrorPage = Schemas["ErrorPage"];
export type ErrorFilter = { status?: string; source?: string; since?: string; limit?: number; offset?: number };

/** GET /errors: rows newest sighting first, the matching total, counts per status and drops. */
export const useErrors = (f: ErrorFilter) =>
  useQuery({ queryKey: qk.errors.list(f), queryFn: () => api<ErrorPage>("/errors" + qs(f)) });

/** GET /errors/{id} with its context and stack, when id is set. */
export const useErrorEvent = (id?: number) =>
  useQuery({ queryKey: qk.errors.entry(id ?? 0), queryFn: () => api<ErrorEvent>(`/errors/${id}`), enabled: id !== undefined });

/** The new-row count for the nav badge, every minute. */
export const useNewErrorCount = (enabled = true) =>
  useQuery({
    queryKey: qk.errors.newCount,
    queryFn: () => api<ErrorPage>("/errors" + qs({ status: "new", limit: 1 })),
    select: (p) => p.counts.new ?? 0,
    refetchInterval: 60_000,
    enabled,
  });

export function useErrorActions() {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: qk.errors.all });
  const failed = (e: Error) => toast({ tone: "bad", title: "Update failed", body: e.message });
  const update = useMutation({
    mutationFn: ({ id, status, note }: { id: number; status?: string; note?: string }) =>
      api<ErrorEvent>(`/errors/${id}`, { method: "POST", body: { status, note } }),
    onSuccess: (r) => {
      toast({ tone: "info", title: `Error #${r.id} ${r.status}` });
      refresh();
    },
    onError: failed,
  });
  const bulk = useMutation({
    mutationFn: (body: { ids?: number[]; where?: { status?: string; source?: string; since?: string }; status: string }) =>
      api<{ changed: number }>("/errors/bulk", { method: "POST", body }),
    onSuccess: (r, v) => {
      toast({ tone: "info", title: `${r.changed} error${r.changed === 1 ? "" : "s"} ${v.status}` });
      refresh();
    },
    onError: failed,
  });
  return { update, bulk };
}

/** Job failures already contribute through attention jobs in the System badge. */
export const useNewNonJobErrorCount = () => useQuery({
  queryKey: [...qk.errors.all, "new-non-job-count"],
  queryFn: async () => {
    const p = await api<ErrorPage>("/errors" + qs({ status: "new", source: "job", limit: 1 }));
    return Math.max(0, (p.counts.new ?? 0) - p.total);
  },
  refetchInterval: 60_000,
});
