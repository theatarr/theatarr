// "Add to other providers" (docs/design/router.md "Copies to other providers"): POST /titles/{id}/copies for a
// library's file (managers), POST /torrents/copy for a Torrents row (admins). The answer is the copies; the page's
// own query then shows them and asks again while one is unfinished.
import { useMutation, useQueryClient } from "@tanstack/react-query";
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

export function useCopyTorrent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: Schemas["CopyTorrentRequest"]) => api<CopiesResult>("/torrents/copy", { method: "POST", body: req }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.torrents.report }),
  });
}
