// The Lists tab of Collections (docs/design/arrview.md, Lists): GET/POST /lists, GET /lists/lookup, PUT/DELETE
// /lists/{id} and POST /lists/{id}/sync.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";

export type MovieList = Schemas["MovieList"];
export type ListsPage = Schemas["ListsPage"];
export type ListAdd = Schemas["ListAdd"];
export type ListUpdate = Schemas["ListUpdate"];
export type ListLookup = Schemas["ListLookup"];
export type ListSynced = Schemas["ListSynced"];

const KEY = ["lists"] as const;

/** GET /lists: every import list, and the movie libraries a list may add to. */
export const useLists = (enabled = true) =>
  useQuery({ queryKey: KEY, queryFn: ({ signal }) => api<ListsPage>("/lists", { signal }), enabled });

/** GET /lists/lookup: the TMDb list behind an id (0: nothing to look up). */
export const useListLookup = (tmdbID: number) =>
  useQuery({
    queryKey: [...KEY, "lookup", tmdbID],
    queryFn: ({ signal }) => api<ListLookup>("/lists/lookup" + qs({ tmdb_id: tmdbID }), { signal }),
    enabled: tmdbID > 0,
    retry: false,
  });

/** The mutations of the tab; each rereads the lists (a sync also changes what Collections counts as held). */
export function useListMutations() {
  const qc = useQueryClient();
  const done = () => { void qc.invalidateQueries({ queryKey: KEY }); void qc.invalidateQueries({ queryKey: ["collections"] }); };
  return {
    add: useMutation({ mutationFn: (body: ListAdd) => api<MovieList>("/lists", { method: "POST", body }), onSuccess: done }),
    set: useMutation({ mutationFn: ({ id, ...body }: ListUpdate & { id: number }) => api<MovieList>(`/lists/${id}`, { method: "PUT", body }), onSuccess: done }),
    remove: useMutation({ mutationFn: (id: number) => api<void>(`/lists/${id}`, { method: "DELETE" }), onSuccess: done }),
    sync: useMutation({ mutationFn: (id: number) => api<ListSynced>(`/lists/${id}/sync`, { method: "POST" }), onSuccess: done }),
  };
}

/** The TMDb list id in what was typed: digits, or a themoviedb.org list URL (".../list/8136-name"). 0 when none. */
export function listIDFrom(text: string): number {
  const t = text.trim();
  const m = /^\d+$/.test(t) ? t : /\/list\/(\d+)/.exec(t)?.[1];
  return m ? Number(m) : 0;
}
