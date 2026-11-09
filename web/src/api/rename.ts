import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";

export type RenameFile = { link_id: number; version: string; existing_path: string; new_path: string };
export type RenamePreview = { files: RenameFile[]; skipped: number };

export function useRenamePreview(titleId: number, enabled = true) {
  return useQuery({ queryKey: ["title-rename", titleId], queryFn: () => api<RenamePreview>(`/titles/${titleId}/rename`), enabled });
}

export function useRename(titleId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api<RenamePreview>(`/titles/${titleId}/rename`, { method: "POST", body: {} }),
    // A failed batch may have moved some links before it stopped: read the preview again either way.
    onSettled: () => qc.invalidateQueries({ queryKey: ["title-rename", titleId] }),
  });
}
