// The torrent manager (docs/design/torrents.md): GET /torrents/report (from the record), /torrents/backups and
// /torrents/journal; Refresh (POST /torrents/refresh: the provider sync now, answering with the new report), a backup
// now, an import, restore and delete previews and their confirmed runs. Writes refresh the torrent queries.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { qk } from "./query-keys";
import type { DeletePlan, DeleteSelection, RestorePlan } from "@/lib/torrents";

export type TorrentBackups = Schemas["TorrentBackupList"];
export type TorrentJournal = { entries: Schemas["TorrentJournalEntry"][] };
export type RestoreRequest = Schemas["TorrentRestoreRequest"];

export const useTorrentReport = () =>
  useQuery({ queryKey: qk.torrents.report, queryFn: () => api<Schemas["TorrentReport"]>("/torrents/report"), staleTime: 60_000 });

export const useTorrentBackups = () => useQuery({ queryKey: qk.torrents.backups, queryFn: () => api<TorrentBackups>("/torrents/backups") });

export const useTorrentJournal = (limit = 50) =>
  useQuery({ queryKey: qk.torrents.journal(limit), queryFn: () => api<TorrentJournal>("/torrents/journal" + qs({ limit })) });

/** The download link of a backup: Theatarr's format, or Debrid Media Manager's. */
export const backupURL = (id: string, format: "theatarr" | "dmm" = "theatarr") =>
  `/api/theatarr/v1/torrents/backups/${encodeURIComponent(id)}${format === "dmm" ? "?format=dmm" : ""}`;

export function useTorrentActions() {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: qk.torrents.all });
  const refreshRecord = useMutation({
    mutationFn: () => api<Schemas["TorrentReport"]>("/torrents/refresh", { method: "POST" }),
    onSuccess: (r) => {
      qc.setQueryData(qk.torrents.report, r);
      return refresh();
    },
  });
  const backupNow = useMutation({
    mutationFn: () => api<{ job: number }>("/torrents/backups", { method: "POST" }),
    onSuccess: refresh,
  });
  const importBackup = useMutation({
    mutationFn: ({ data, provider }: { data: unknown; provider: string }) =>
      api<Schemas["TorrentBackupInfo"]>("/torrents/backups/import" + qs({ provider }), { method: "POST", body: data }),
    onSuccess: refresh,
  });
  const restorePreview = useMutation({
    mutationFn: (req: RestoreRequest) => api<RestorePlan>("/torrents/restore/preview", { method: "POST", body: req }),
  });
  const restore = useMutation({
    mutationFn: (req: RestoreRequest) => api<{ job: number; plan: RestorePlan }>("/torrents/restore", { method: "POST", body: req }),
    onSuccess: refresh,
  });
  const deletePreview = useMutation({
    mutationFn: (items: DeleteSelection[]) => api<DeletePlan>("/torrents/delete/preview", { method: "POST", body: { items } }),
  });
  const del = useMutation({
    mutationFn: ({ items, token }: { items: DeleteSelection[]; token: string }) =>
      api<DeletePlan>("/torrents/delete", { method: "POST", body: { items, confirmation_token: token } }),
    onSuccess: refresh,
  });
  return { refreshRecord, backupNow, importBackup, restorePreview, restore, deletePreview, del };
}
