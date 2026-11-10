// Upgrade approval (docs/design/router.md "Upgrade approval", docs/design/manage.md "Upgrades awaiting approval"): GET upgrades,
// POST upgrades/{id}/approve|decline. Managers and admins only (the server answers 403 to anyone else), so callers
// pass enabled = perms.manage and family accounts never ask.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { qk } from "./query-keys";

export type UpgradeHold = Schemas["UpgradeHold"];
export type UpgradeHolds = Schemas["UpgradeHolds"];

export type HoldFilter = { state?: string; title?: number; limit?: number };

export const useUpgradeHolds = (f: HoldFilter, enabled = true) =>
  useQuery({
    queryKey: qk.upgrades.list(f),
    queryFn: () => api<UpgradeHolds>("/upgrades" + qs(f)),
    enabled,
    refetchInterval: 60_000,
  });

function useDecide<V>(fn: (v: V) => Promise<UpgradeHold>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (h) => {
      void qc.invalidateQueries({ queryKey: qk.upgrades.all });
      void qc.invalidateQueries({ queryKey: qk.activity.all });
      void qc.invalidateQueries({ queryKey: qk.title(h.title.id) });
    },
  });
}

export const useApproveUpgrade = () =>
  useDecide((id: number) => api<UpgradeHold>(`/upgrades/${id}/approve`, { method: "POST" }));

export const useDeclineUpgrade = () =>
  useDecide(({ id, note }: { id: number; note: string }) =>
    api<UpgradeHold>(`/upgrades/${id}/decline`, { method: "POST", body: { note } }));
