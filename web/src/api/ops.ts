// Recovery actions (docs/design/ui.md "Recovery actions"): grab a release from a live search, remove or retry a
// download, unblock a release, remove a copy or a title, unlink a library file. Managers and admins only (the server
// answers 403 to anyone else); every action is recorded in the journal with who did it. Removal never touches a
// debrid torrent (they are shared) and an unlink moves only Theatarr's own symlink to the trash.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { qk, titleChanged } from "./query-keys";
import { toast } from "@/components/toaster";
import { operationOutcomeToast } from "@/lib/ops";

export type GrabRequest = Schemas["GrabRequest"];
export type GrabResult = Schemas["GrabResult"];
export type AddReleaseRequest = Schemas["AddReleaseRequest"];
export type RemovalScope = Schemas["RemovalScope"];
export type RemovalCopy = Schemas["RemovalCopy"];
export type RemoveRequest = Schemas["RemoveRequest"];
export type RemoveResult = Schemas["RemoveResult"];
export type UnlinkResult = Schemas["UnlinkResult"];
export type ReassignRequest = Schemas["ReassignRequest"];
export type ReassignResult = Schemas["ReassignResult"];
export type RemoveDownloadRequest = Schemas["RemoveDownloadRequest"];
export type RemoveDownloadResult = Schemas["RemoveDownloadResult"];
export type RetryResult = Schemas["RetryResult"];
export type UnblockRequest = Schemas["UnblockRequest"];
export type UnblockResult = Schemas["UnblockResult"];

/** After an action: the title's views, the activity and the journal. */
function useRefresh() {
  const qc = useQueryClient();
  return (titleId?: number) => {
    for (const queryKey of [...titleChanged(titleId || undefined), qk.activity.all, qk.journal.all]) {
      void qc.invalidateQueries({ queryKey });
    }
  };
}

export function useGrab(titleId: number) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: (body: GrabRequest) => api<GrabResult>(`/titles/${titleId}/releases/grab`, { method: "POST", body }),
    onSuccess: () => refresh(titleId),
  });
}

/** Add a release by magnet or hash (admins): one no search found. Routed like a grab; the answer is a grab's. */
export function useAddRelease(titleId: number) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: (body: AddReleaseRequest) => api<GrabResult>(`/titles/${titleId}/releases/add`, { method: "POST", body }),
    onSuccess: () => refresh(titleId),
  });
}

export function useRemovalScope(titleId: number, versions: string[] | undefined, enabled: boolean) {
  const v = versions?.join(",") ?? "";
  return useQuery({
    queryKey: [...qk.title(titleId), "removal", v],
    queryFn: () => api<RemovalScope>(`/titles/${titleId}/removal` + qs({ versions: v })),
    enabled,
    staleTime: 0,
  });
}

export function useRemove(titleId: number) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: (body: RemoveRequest) => api<RemoveResult>(`/titles/${titleId}/remove`, { method: "POST", body }),
    onSuccess: () => refresh(titleId),
  });
}

export function useUnlink(titleId: number) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: (linkId: number) => api<UnlinkResult>(`/links/${linkId}/unlink`, { method: "POST" }),
    onSuccess: () => refresh(titleId),
  });
}

/** Change the episodes a library link holds (admins): the link is renamed for them, and the choice is kept. */
export function useReassignEpisodes(titleId: number) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: ({ linkId, ...body }: ReassignRequest & { linkId: number }) =>
      api<ReassignResult>(`/links/${linkId}/episodes`, { method: "POST", body }),
    onSuccess: () => refresh(titleId),
  });
}

export function useRemoveDownload() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: ({ id, ...body }: RemoveDownloadRequest & { id: number; titleId?: number }) =>
      api<RemoveDownloadResult>(`/activity/downloads/${id}/remove`, {
        method: "POST",
        body: { blocklist: body.blocklist, search: body.search },
      }),
    onSuccess: (r, v) => {
      toast(operationOutcomeToast("Download removed", r.message, r.search));
      refresh(v.titleId);
    },
  });
}

export function useRetryDownload() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: ({ id }: { id: number; titleId?: number }) =>
      api<RetryResult>(`/activity/downloads/${id}/retry`, { method: "POST" }),
    onSuccess: (_r, v) => refresh(v.titleId),
  });
}

export function useUnblock() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: ({ titleId: _t, ...body }: UnblockRequest & { titleId?: number }) =>
      api<UnblockResult>("/activity/blocked/unblock", { method: "POST", body }),
    onSuccess: (r, v) => {
      toast(operationOutcomeToast("Release unblocked", r.message, r.search));
      refresh(v.titleId);
    },
  });
}
