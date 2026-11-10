import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";

export type Collection = Schemas["Collection"];
export type CollectionsPage = Schemas["CollectionsPage"];
export type CollectionUpdate = Schemas["CollectionUpdate"];
export type CollectionHit = Schemas["CollectionHit"];
export type CollectionAdd = Schemas["CollectionAdd"];
export type CollectionRefreshed = Schemas["CollectionRefreshed"];

const KEY = ["collections"] as const;

/** GET /collections: every collection movie intake knows, and the movie libraries one may target. */
export const useCollections = (enabled = true) =>
  useQuery({ queryKey: KEY, queryFn: ({ signal }) => api<CollectionsPage>("/collections", { signal }), enabled });

/** PUT /collections/{id}: the stored collection replaces the listed one. */
export function useSetCollection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: CollectionUpdate & { id: number }) => api<Collection>(`/collections/${id}`, { method: "PUT", body }),
    onSuccess: (c) => qc.setQueryData<CollectionsPage>(KEY, old => old && { ...old, collections: old.collections.map(x => x.tmdb_id === c.tmdb_id ? c : x) }),
  });
}

/** GET /collections/search: TMDb collections by name (cached server side), each marked when already added. */
export function useCollectionSearch(q: string) {
  const query = q.trim();
  return useQuery({
    queryKey: [...KEY, "search", query],
    queryFn: ({ signal }) => api<Schemas["CollectionSearch"]>("/collections/search" + qs({ q: query }), { signal }),
    enabled: query.length >= 2,
    placeholderData: keepPreviousData,
  });
}

/** POST /collections: add a TMDb collection; a monitored one's missing movies are added in the same request. */
export function useAddCollection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CollectionAdd) => api<CollectionRefreshed>("/collections", { method: "POST", body }),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

/** POST /collections/{id}/refresh: reread one collection and add a monitored one's missing movies now. */
export function useRefreshCollection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api<CollectionRefreshed>(`/collections/${id}/refresh`, { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

/** What an add or a refresh did, in words. */
export function refreshWords(r: CollectionRefreshed): string {
  const name = r.collection.name || `Collection ${r.collection.tmdb_id}`;
  const movies = (n: number) => `${n} ${n === 1 ? "movie" : "movies"}`;
  const did = r.collection.monitored ? `${movies(r.added)} added, ${r.skipped} already in the library or excluded` : "not monitored, so no movies were added";
  return `${name}: ${did}.${r.refresh_error ? ` ${r.refresh_error}` : ""}`;
}
