import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";

export type Collection = Schemas["Collection"];
export type CollectionsPage = Schemas["CollectionsPage"];
export type CollectionUpdate = Schemas["CollectionUpdate"];

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
