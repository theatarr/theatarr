// Query hooks, one per API read, typed from the generated schema (openapi.json).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs } from "./client";
import { qk } from "./query-keys";
import type { paths } from "./schema";

/** The 200 JSON body of GET <P>. */
export type Get<P extends keyof paths> = paths[P] extends {
  get: { responses: { 200: { content: { "application/json": infer T } } } };
}
  ? T
  : never;

export type Me = Get<"/auth/me">;
export type Status = Get<"/status">;
export type Health = Get<"/health">;
export type JobList = Get<"/jobs">;
export type Gateway = Get<"/gateway">;
export type Imports = Get<"/imports">;
export type Policy = Get<"/policy">;
export type Validate = Get<"/policy/validate">;
export type Keys = Get<"/keys">;
export type ShadowSummary = Get<"/shadow/summary">;
export type ShadowDiffs = Get<"/shadow/diffs">;
export type ShadowDiff = Get<"/shadow/diffs/{id}">;
export type Franchises = Get<"/anime/franchises">;
export type Franchise = Get<"/anime/franchises/{slug}">;
export type Review = Get<"/anime/review">;
export type Playlists = Get<"/anime/playlists">;
export type Uncovered = Get<"/anime/uncovered">;
export type Registry = Get<"/registry">;
export type Content = Get<"/registry/{hash}">;
export type Sources = Get<"/sources">;
export type Logs = Get<"/logs"> & { skippedTotal?: number; restartTotal?: number };
export type Titles = Get<"/titles">;

export function useMe() {
  return useQuery({
    queryKey: qk.me,
    queryFn: () => api<Me>("/auth/me", { redirectOn401: false }),
    retry: false,
    staleTime: 60_000,
  });
}

export const useStatus = (enabled = true) =>
  useQuery({ queryKey: qk.status, queryFn: () => api<Status>("/status"), enabled });

/** The health checks (GET /health): cached a minute on the server, so polling every minute costs nothing. */
export const useHealth = () =>
  useQuery({ queryKey: qk.health, queryFn: () => api<Health>("/health"), refetchInterval: 60_000 });

export const useJobs = (state?: string, limit = 100) =>
  useQuery({ queryKey: qk.jobs.state(state, limit), queryFn: () => api<JobList>("/jobs" + qs({ state, limit })) });

export const useGateway = () => useQuery({ queryKey: qk.gateway, queryFn: () => api<Gateway>("/gateway") });

export const useImports = (limit = 20) =>
  useQuery({ queryKey: qk.imports.list(limit), queryFn: () => api<Imports>("/imports" + qs({ limit })) });

export const usePolicy = () => useQuery({ queryKey: qk.policy.all, queryFn: () => api<Policy>("/policy") });

export const useValidate = (enabled: boolean) =>
  useQuery({
    queryKey: qk.policy.validate,
    queryFn: () => api<Validate>("/policy/validate"),
    enabled,
    staleTime: 0,
  });

export const useKeys = () => useQuery({ queryKey: qk.keys, queryFn: () => api<Keys>("/keys") });

export function useCreateKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      api<{ key: string; info: unknown }>("/keys", { method: "POST", body: { name } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.keys }),
  });
}

export function useRevokeKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api<void>(`/keys/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.keys }),
  });
}

export const useShadowSummary = () =>
  useQuery({ queryKey: qk.shadow.summary, queryFn: () => api<ShadowSummary>("/shadow/summary") });

export type DiffFilter = { bucket?: string; reason?: string; version?: string; search_found_better?: string; offset?: number };
export const useShadowDiffs = (f: DiffFilter) =>
  useQuery({
    queryKey: qk.shadow.diffs(f),
    queryFn: () => api<ShadowDiffs>("/shadow/diffs" + qs({ ...f, limit: 50 })),
  });

export const useShadowDiff = (id: string) =>
  useQuery({ queryKey: qk.shadow.diff(id), queryFn: () => api<ShadowDiff>(`/shadow/diffs/${encodeURIComponent(id)}`) });

// A build job still to run or running: the anime lists poll until it ends.
const buildActive = (j?: { state?: string } | null) => j?.state === "queued" || j?.state === "running";
const POLL_MS = 5000;

export const useFranchises = () =>
  useQuery({
    queryKey: qk.anime.franchises,
    queryFn: () => api<Franchises>("/anime/franchises"),
    refetchInterval: (q) => ((q.state.data?.items ?? []).some((f) => buildActive(f.job)) ? POLL_MS : false),
  });

export const useFranchise = (slug: string) =>
  useQuery({
    queryKey: qk.anime.franchise(slug),
    queryFn: () => api<Franchise>(`/anime/franchises/${encodeURIComponent(slug)}`),
    refetchInterval: (q) => (buildActive(q.state.data?.job) ? POLL_MS : false),
  });

export const useReview = (state: string, franchise?: string) =>
  useQuery({
    queryKey: qk.anime.review(state, franchise),
    queryFn: () => api<Review>("/anime/review" + qs({ state, franchise })),
  });

export function useResolveReview() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, accept, candidate, note }: { id: number; accept: boolean; candidate?: number; note?: string }) =>
      api<unknown>(`/anime/review/${id}/${accept ? "accept" : "reject"}`, {
        method: "POST",
        body: { candidate, note },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.anime.all }),
  });
}

export const useUncovered = () =>
  useQuery({
    queryKey: qk.anime.uncovered,
    queryFn: () => api<Uncovered>("/anime/uncovered"),
    refetchInterval: (q) => ((q.state.data?.items ?? []).some((u) => buildActive(u.job)) ? POLL_MS : false),
  });

/** Queues an anime build: a franchise rebuild (slug) or a build for an anime series (tvdb_id). */
export function useAnimeBuild() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (to: { slug: string } | { tvdb_id: number }) =>
      "slug" in to
        ? api<{ job_id: number; slug?: string }>(`/anime/franchises/${encodeURIComponent(to.slug)}/build`, { method: "POST" })
        : api<{ job_id: number; slug?: string }>("/anime/build", { method: "POST", body: { tvdb_id: to.tvdb_id } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.anime.all }),
  });
}

export const usePlaylists = () =>
  useQuery({ queryKey: qk.anime.playlists, queryFn: () => api<Playlists>("/anime/playlists") });

export const useRegistry = (q: string, state: string, offset: number) =>
  useQuery({
    queryKey: qk.registry.list(q, state, offset),
    queryFn: () => api<Registry>("/registry" + qs({ q, state, offset, limit: 50 })),
  });

export const useContent = (hash: string) =>
  useQuery({ queryKey: qk.registry.content(hash), queryFn: () => api<Content>(`/registry/${encodeURIComponent(hash)}`) });

export const useSources = () => useQuery({ queryKey: qk.sources, queryFn: () => api<Sources>("/sources") });

export function useLogs(level: string, component: string) {
  const qc = useQueryClient();
  const queryKey = qk.logs(level, component);
  return useQuery({
    queryKey,
    queryFn: async ({ signal }) => {
      let page = qc.getQueryData<Logs>(queryKey);
      // Drain at most the ring's capacity per query. A busy writer can keep More true, so bound each poll.
      for (let n = 0; n < 4; n++) {
        const next = await api<Logs>("/logs" + qs({ level, component, limit: 500, after: page?.after, boot_id: page?.boot_id, seen: page?.seen }), { signal });
        const sameBoot = page?.boot_id === next.boot_id;
        const items = sameBoot ? [...new Map([...page!.items, ...next.items].map((entry) => [entry.seq, entry])).values()] : next.items;
        page = { ...next, items: items.slice(-2000), skippedTotal: (sameBoot ? page?.skippedTotal ?? 0 : 0) + next.skipped, restartTotal: (page?.restartTotal ?? 0) + (page?.boot_id && !sameBoot ? 1 : 0) };
        if (!next.more) break;
      }
      return page!;
    },
    refetchInterval: 5000,
  });
}

export const useTitles = (params: { kind?: string; limit?: number; offset?: number }) =>
  useQuery({ queryKey: qk.titles.table(params), queryFn: () => api<Titles>("/titles" + qs(params)) });

export type JournalPage = Get<"/journal">;
export type JournalEntry = Get<"/journal/{id}">;
export type JournalFilter = { suspect?: boolean; kind?: string; version?: string; title?: string; cursor?: string };

export const useJournal = (f: JournalFilter) =>
  useQuery({
    queryKey: qk.journal.list(f),
    queryFn: () => api<JournalPage>("/journal" + qs({ ...f, suspect: f.suspect || undefined, limit: 50 })),
  });

export const useJournalEntry = (id: string) =>
  useQuery({ queryKey: qk.journal.entry(id), queryFn: () => api<JournalEntry>(`/journal/${encodeURIComponent(id)}`) });

export function useJournalVerdict() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, verdict, reason }: { id: string; verdict: "ok" | "wrong"; reason?: string }) =>
      api<unknown>(`/journal/${encodeURIComponent(id)}/verdict`, { method: "POST", body: { verdict, reason } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.journal.all }),
  });
}

export const useLogFiles = () =>
  useQuery({ queryKey: qk.logFiles, queryFn: () => api<Get<"/logs/files">>("/logs/files"), refetchInterval: 30_000 });

export const useLogLevel = () =>
  useQuery({ queryKey: qk.logLevel, queryFn: () => api<Get<"/logs/level">>("/logs/level") });

export function useSetLogLevel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (level: string) => api<Get<"/logs/level">>("/logs/level", { method: "PUT", body: { level } }),
    onSuccess: (data) => {
      qc.setQueryData(qk.logLevel, data);
      return qc.invalidateQueries({ queryKey: qk.logLevel });
    },
  });
}
