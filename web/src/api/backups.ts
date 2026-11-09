import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";

export type AppBackupSet = Schemas["AppBackupSet"];
export type AppBackupList = Schemas["AppBackupList"];
const key = ["system", "backups"] as const;
export function useAppBackups() {
  return useQuery({ queryKey: key, queryFn: () => api<AppBackupList>("/system/backups") });
}
export function useAppBackupActions() {
  const qc = useQueryClient();
  const backupNow = useMutation({
    mutationFn: () => api<{ name: string }>("/system/backups", { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const restore = useMutation({
    mutationFn: ({ name, confirm }: { name: string; confirm: string }) =>
      api<Schemas["AppRestoreAccepted"]>(`/system/backups/${encodeURIComponent(name)}/restore`, { method: "POST", body: { confirm } }),
  });
  return { backupNow, restore };
}
