// "Add to other providers" (docs/design/router.md "Copies to other providers"): POST /titles/{id}/copies for a
// library's file (managers), POST /torrents/copy for a Torrents row (admins). The answer is the copies; the page's
// own query then shows them and asks again while one is unfinished.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { COPY_POLL_MS, copiesUnfinished } from "@/lib/provider-copies";
import { api, type Schemas } from "./client";
import { qk } from "./query-keys";

export type CopiesResult = Schemas["CopiesResult"];

export function useCopyFile(titleId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: Schemas["CopyTitleRequest"]) => api<CopiesResult>(`/titles/${titleId}/copies`, { method: "POST", body: req }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.title(titleId) });
      void qc.invalidateQueries({ queryKey: ["title", titleId] });
    },
  });
}

/** A Torrents row's action. The answer seeds the row's own copies query; the report is not read again. */
export function useCopyTorrent(hash: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: Schemas["CopyTorrentRequest"]) => api<CopiesResult>("/torrents/copy", { method: "POST", body: req }),
    onSuccess: (r) => qc.setQueryData(["torrents", "copies", hash], r),
  });
}

/** One torrent's copies (GET /torrents/copies), read only while one is unfinished: the whole report reads every
 *  account, so it is not polled. initial: the copies the report gave. */
export function useTorrentCopies(hash: string, initial?: Schemas["ProviderCopy"][]) {
  return useQuery({
    queryKey: ["torrents", "copies", hash],
    queryFn: () => api<CopiesResult>(`/torrents/copies?hash=${encodeURIComponent(hash)}`),
    initialData: { copies: initial ?? [] },
    staleTime: Infinity,
    refetchInterval: (q) => (copiesUnfinished(q.state.data?.copies) ? COPY_POLL_MS : false),
  });
}
