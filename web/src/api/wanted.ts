import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";

export type WantedRow = Schemas["WantedRow"];
export type WantedQuery = { only: "missing" | "cutoff"; version?: string; waiting?: string; limit: number; offset: number };
export const useWanted = (q: WantedQuery) => useQuery({
  queryKey: ["wanted", q],
  queryFn: ({ signal }) => api<Schemas["WantedPage"]>("/wanted" + qs(q), { signal }),
  // The previous page stays up while paging or filtering one tab; switching tab shows none of the other tab's rows
  // (they would sit under this tab's Search all).
  placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] as WantedQuery | undefined)?.only === q.only ? keepPreviousData(prev) : undefined,
  refetchInterval: 60_000,
});

export type WantedSearchBody = Schemas["WantedSearchBody"];
export type WantedSearchResult = Schemas["WantedSearchResult"];

/** POST /wanted/search: queue a search of the named rows, or of every row of a filter, under the hourly limit. */
export function useWantedSearch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: WantedSearchBody) => api<WantedSearchResult>("/wanted/search", { method: "POST", body }),
    onSettled: () => qc.invalidateQueries({ queryKey: ["wanted"] }),
  });
}

/** DELETE /wanted/search: cancel the searches Search now has queued. */
export function useClearWantedSearch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api<{ cleared: number }>("/wanted/search", { method: "DELETE" }),
    onSettled: () => qc.invalidateQueries({ queryKey: ["wanted"] }),
  });
}
