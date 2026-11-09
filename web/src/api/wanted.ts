import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";

export type WantedRow = Schemas["WantedRow"];
export type WantedQuery = { only: "missing" | "cutoff"; version?: string; waiting?: string; limit: number; offset: number };
export const useWanted = (q: WantedQuery) => useQuery({
  queryKey: ["wanted", q],
  queryFn: ({ signal }) => api<Schemas["WantedPage"]>("/wanted" + qs(q), { signal }),
  placeholderData: keepPreviousData,
  refetchInterval: 60_000,
});
