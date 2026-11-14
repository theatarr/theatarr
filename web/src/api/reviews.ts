import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "@/components/toaster";
import { api, type Schemas } from "./client";
import { permsOf } from "./library";
import { useAuth } from "./auth";
import { qk } from "./query-keys";
import { useCardigannHolds } from "./cardigann";
import type { JournalPage, Review } from "./hooks";

export const useFlaggedDecisions = (enabled = true) => useQuery({
  queryKey: qk.journal.list({ suspect: true }),
  queryFn: () => api<JournalPage & { total?: number }>("/journal?suspect=1&limit=50"), enabled, refetchInterval: 60_000,
});

/** The open dead-link flags that are resolved or superseded (GET /journal/resolved): what Acknowledge resolved covers. */
export const useResolvedFlags = (enabled = true) => useQuery({
  queryKey: [...qk.journal.all, "resolved"] as const,
  queryFn: () => api<Schemas["JournalResolved"]>("/journal/resolved"), enabled, refetchInterval: 60_000,
});

/** Acknowledge them all: an OK verdict on each (the records stay; nothing is blocked, searched or relinked). */
export function useAcknowledgeResolved() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api<{ acknowledged: number }>("/journal/resolved/acknowledge", { method: "POST" }),
    onSuccess: (r) => {
      toast({ tone: "ok", title: `Acknowledged ${r.acknowledged} flag${r.acknowledged === 1 ? "" : "s"}` });
      void qc.invalidateQueries({ queryKey: qk.journal.all });
    },
    onError: (e: Error) => toast({ tone: "bad", title: "Acknowledge failed", body: e.message }),
  });
}

/** The journal currently returns a page, not a total. A full page or an unfinished scan is a lower bound. */
export function suspectCount(page?: JournalPage & { total?: number }) {
  const count = page?.total ?? page?.items?.length ?? 0;
  return { count, more: page?.total === undefined && (count >= 50 || !!page?.next_cursor) };
}
export function useReviewCounts() {
  const auth = useAuth();
  const enabled = permsOf(auth.data?.permissions).admin;
  const anime = useQuery({ queryKey: qk.anime.review("open"), queryFn: () => api<Review>("/anime/review?state=open"), enabled, refetchInterval: 60_000 });
  const indexers = useCardigannHolds(enabled);
  const decisions = useFlaggedDecisions(enabled);
  const suspect = suspectCount(decisions.data);
  const counts = { anime: anime.data?.items?.filter(i => i.state === "open").length ?? 0, indexers: indexers.data?.length ?? 0, decisions: suspect.count };
  return { ...counts, total: Object.values(counts).reduce((a, b) => a + b, 0), decisionsMore: suspect.more,
    isPending: auth.isPending || enabled && [anime, indexers, decisions].some(q => q.isPending),
    failures: [{ name: "anime mappings", q: anime }, { name: "indexer definitions", q: indexers }, { name: "flagged decisions", q: decisions }].filter(({ q }) => q.error).map(({ name }) => name),
    error: auth.error ?? [anime, indexers, decisions].find(q => q.error)?.error,
  };
}
