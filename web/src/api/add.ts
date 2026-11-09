// Search and add (docs/design/ui.md): GET /search, GET /add/options, POST /titles, PUT /titles/{id}/versions/
// {key}, POST /titles/{id}/search and GET /titles/{id}/releases.
import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, qs, type Schemas } from "./client";
import { qk, titleChanged } from "./query-keys";

export type SearchAnswer = Schemas["SearchAnswer"];
export type SearchHit = Schemas["SearchHit"];
export type LibraryHit = Schemas["TitleRow"];
export type AddOptions = Schemas["AddOptions"];
export type VersionOption = Schemas["VersionOption"];
export type AddRequest = Schemas["AddRequest"];
export type AddResult = Schemas["AddResult"];
export type AddedTitle = Schemas["AddedTitle"];
export type SearchOutcome = Schemas["SearchOutcome"];
export type Releases = Schemas["Releases"];
export type Candidate = Schemas["JournalCandidate"];
export type ReleaseOutcome = Schemas["ReleaseOutcome"];

/** value, once it has stopped changing for ms. */
export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setV(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return v;
}

export type SearchKind = "" | "movie" | "series";

/** The library part answers at once; the TMDb part (cached a day server side) follows. The kind is applied by the
 * server before the limit, so a filtered answer is never a filtered first page. */
export function useSearch(q: string, scope: "library" | "tmdb", limit = 12, kind: SearchKind = "") {
  const query = q.trim();
  return useQuery({
    queryKey: qk.search.part(scope, query, kind, limit),
    queryFn: ({ signal }) => api<SearchAnswer>("/search" + qs({ q: query, scope, limit, kind }), { signal }),
    enabled: query.length > 0 && (scope === "library" || query.length >= 2),
    placeholderData: keepPreviousData,
    staleTime: scope === "tmdb" ? 10 * 60_000 : 15_000,
    retry: 1,
  });
}

/** Every library match of a kind, a page at a time (the /search page's "Show more"); library_total counts them all. */
export function useLibrarySearchPages(q: string, kind: SearchKind, page = 50) {
  const query = q.trim();
  return useInfiniteQuery({
    queryKey: qk.search.pages(query, kind, page),
    queryFn: ({ pageParam, signal }) =>
      api<SearchAnswer>("/search" + qs({ q: query, scope: "library", kind, limit: page, offset: pageParam || undefined }), { signal }),
    initialPageParam: 0,
    getNextPageParam: (last, all) => {
      const seen = all.reduce((n, p) => n + p.library.length, 0);
      return last.library.length > 0 && seen < last.library_total ? seen : undefined;
    },
    enabled: query.length > 0,
    placeholderData: keepPreviousData,
    staleTime: 15_000,
    retry: 1,
  });
}

export type AddTarget = {
  kind: "movie" | "series";
  name: string;
  year?: number;
  poster?: string;
  /** The TMDb poster path (a request captures it, so the request shows art before the title exists). */
  poster_path?: string;
  overview?: string;
  tmdb_id?: number;
  title_id?: number;
  anime?: boolean;
};

export function useAddOptions(t: AddTarget | null, anime: boolean) {
  return useQuery({
    queryKey: qk.addOptions.one(t?.kind, t?.tmdb_id, t?.title_id, anime),
    queryFn: () =>
      api<AddOptions>(
        "/add/options" + qs({ kind: t?.kind, tmdb_id: t?.tmdb_id, title_id: t?.title_id, anime: anime ? 1 : undefined }),
      ),
    enabled: t !== null,
  });
}

/** Refresh every view of a title after its versions or seasons changed (and the activity, when a search started). */
export function refreshTitle(qc: QueryClient, titleId?: number | string, searched = false) {
  for (const queryKey of titleChanged(titleId)) void qc.invalidateQueries({ queryKey });
  if (searched) void qc.invalidateQueries({ queryKey: qk.activity.all });
}

export function useAddTitle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: AddRequest) => api<AddResult>("/titles", { method: "POST", body }),
    onSuccess: (r) => refreshTitle(qc, r.title.id, Boolean(r.search?.started)),
  });
}

export function useSetVersion(titleId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ key, monitored }: { key: string; monitored: boolean }) =>
      api<AddedTitle>(`/titles/${titleId}/versions/${encodeURIComponent(key)}`, { method: "PUT", body: { monitored } }),
    onSuccess: () => refreshTitle(qc, titleId),
  });
}

export function useSearchNow(titleId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (versions?: string[]) =>
      api<SearchOutcome>(`/titles/${titleId}/search`, { method: "POST", body: versions ? { versions } : {} }),
    onSuccess: (o) => {
      if (o.started) void qc.invalidateQueries({ queryKey: qk.activity.all });
    },
  });
}

export function useReleases(titleId: number, version: string | undefined, live: boolean) {
  return useQuery({
    queryKey: qk.releases(titleId, version ?? "", live),
    queryFn: () => api<Releases>(`/titles/${titleId}/releases` + qs({ version, live: live ? 1 : undefined })),
    // A live answer is a fresh indexer search: never refetched or retried on its own.
    staleTime: live ? Infinity : 30_000,
    refetchOnWindowFocus: !live,
    ...(live ? { retry: false } : {}),
  });
}
