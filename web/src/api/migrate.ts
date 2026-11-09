// Migration reads use the native API only; source credentials are sent only to inspect, never cached or persisted.
import { api, type Schemas } from "./client";

export type Sources = Schemas["MigrateSources"];
export type PlanOptions = Schemas["MigratePlanOptions"];
export type Step = Schemas["MigrateStep"];
export type Gap = Schemas["MigrateGap"];
export type Plan = Omit<Schemas["MigratePlan"], "libraries" | "indexers"> & {
  libraries: { name: string; kind: string; versions: { key: string; instance: string; library: string; profile: string }[] | null }[] | null;
  indexers: { key: string; name: string; choice: string; notes?: string[] | null }[] | null;
};
export type Summary = Schemas["MigratePlanSummary"];
export type VerifyReport = Schemas["MigrateVerifyReport"];

// The inventory's open object schemas are described by internal/migration/inventory.go. Only the display fields
// are read here; raw arr bodies and source key values never reach the browser.
export type Inventory = Omit<Schemas["MigrateInventory"], "arr" | "prowlarr" | "seerr" | "recyclarr"> & {
  arr: {
    name: string; flavour: string; url: string; error?: string; app_version?: string;
    titles: { total: number; monitored: number; with_files: number; anime: number };
    root_folders: { path: string; titles: number }[] | null;
    quality_profiles: { id: number; name: string; titles: number }[] | null;
    indexers: { name: string; protocol: string }[] | null;
    download_clients: { name: string; protocol: string; implementation: string }[] | null;
  }[] | null;
  prowlarr?: { url: string; error?: string; indexers: { id: number; name: string; protocol: string }[] | null; applications: { name: string }[] | null };
  seerr?: { url: string; error?: string; radarr: { name: string }[] | null; sonarr: { name: string }[] | null };
  recyclarr?: { path: string; error?: string; instances: { name: string }[] | null };
};
export type PlanView = { plan: Plan; inventory: Inventory };
export type InspectResult = Inventory & { plan?: Plan };

export const inspect = (sources: Sources, keys: Record<string, string>, options: PlanOptions, save_plan = false) =>
  api<InspectResult>("/migrate/inspect", { method: "POST", body: { sources, keys, options, save_plan } });
export const apply = (id: number, steps: string[], confirm: string[] = [], token?: string) =>
  api<Plan>(`/migrate/plans/${id}/apply`, { method: "POST", body: { steps, confirm, confirmation_token: token } });
export const verify = (id: number) => api<VerifyReport>(`/migrate/plans/${id}/verify`, { method: "POST" });
