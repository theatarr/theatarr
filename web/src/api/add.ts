// Search and add (docs/design/ui.md): GET /search, GET /add/options, POST /titles, PUT /titles/{id}/versions/
// {key}, POST /titles/{id}/search and GET /titles/{id}/releases (stored, and live for interactive search).
import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, qs, type Schemas } from "./client";
import { collectionsKey } from "./collections";
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

/** The library group with the TMDb answer's library part merged in: titles a TMDb hit is marked In library for that
 *  the name search does not find (the server sends only those), once each, counted in the total. */
export function withInLibrary<T extends { id?: number }>(items: T[], total: number | undefined, tmdb: { library?: T[] } | undefined): { items: T[]; total: number | undefined } {
  const ids = new Set(items.map((t) => t.id));
  const extra = (tmdb?.library ?? []).filter((t) => !ids.has(t.id));
  if (extra.length === 0) return { items, total };
  return { items: [...items, ...extra], total: (total ?? items.length) + extra.length };
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

/** The add options for t. anime marks the title anime (anime=1); explicit (the Add sheet's own Anime choice) also
 *  sends anime=0, so a title every copy of which was removed is offered by that choice rather than its stored flag.
 *  Without it the options follow the stored flag, as a request does. */
export function useAddOptions(t: AddTarget | null, anime: boolean, explicit = false) {
  const param = anime ? 1 : explicit ? 0 : undefined;
  return useQuery({
    queryKey: qk.addOptions.one(t?.kind, t?.tmdb_id, t?.title_id, param),
    queryFn: () =>
      api<AddOptions>(
        "/add/options" + qs({ kind: t?.kind, tmdb_id: t?.tmdb_id, title_id: t?.title_id, anime: param }),
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
    onSuccess: (r) => {
      refreshTitle(qc, r.title.id, Boolean(r.search?.started));
      // A collection's counts and its page's films follow the add.
      void qc.invalidateQueries({ queryKey: collectionsKey });
    },
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

/** An interactive search: a live search of the indexers for one library, of the whole title, one season or one
 *  episode. session is new each time the window opens, so every opening searches afresh while a library already
 *  answered in this opening is not searched again. The request is cancelled when nothing shows it any more (the
 *  window closed or another library chosen), which ends the search on the server. */
export function useLiveSearch({ titleId, version, season, episode, session, enabled = true }: { titleId: number; version: string; season?: number; episode?: number; session: number; enabled?: boolean }) {
  const scope = episode !== undefined ? `episode:${episode}` : season !== undefined ? `season:${season}` : "";
  return useQuery({
    queryKey: qk.liveSearch(titleId, version, session, scope),
    queryFn: ({ signal }) =>
      api<Releases>(
        episode !== undefined
          ? `/titles/${titleId}/episodes/${episode}/releases` + qs({ version, live: 1 })
          : `/titles/${titleId}/releases` + qs({ version, live: 1, season }),
        { signal },
      ),
    enabled: enabled && !!version,
    staleTime: Infinity,
    // Kept while the window is open (it drops them on closing), so a library answered earlier is not searched again.
    gcTime: 30 * 60_000,
    retry: false,
    refetchOnWindowFocus: false,
  });
}
