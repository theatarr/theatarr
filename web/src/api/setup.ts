// The setup screen (docs/quickstart.md, docs/design/web.md "Setup"): what a fresh install is missing (GET /setup) and the
// preflight (POST /preflight), which checks the mount, roots, providers, indexers, metadata keys, arr views, Plex
// and the VPN egress, each ok, warn or fail with the next step.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";

export type SetupState = Schemas["SetupState"];
export type PreflightReport = Schemas["PreflightReport"];
export type PreflightItem = Schemas["PreflightItem"];
export type LastPreflight = Schemas["LastPreflight"];
export type SetupStartBody = Schemas["SetupStartBody"];
export type SetupStarted = Schemas["SetupStarted"];

export const setupKey = ["setup"] as const;
export const useSetup = (enabled = true) => useQuery({ queryKey: setupKey, queryFn: () => api<SetupState>("/setup"), enabled });

/** sources false skips the provider, indexer and Plex calls: only what needs no outbound request. */
export const usePreflight = (generation?: number) => {
  const client = useQueryClient();
  // The server keeps the latest run's summary (GET /setup last_preflight); refetch it so a reload agrees with this session.
  return useMutation({ mutationKey: ["setup-preflight", generation], gcTime: Infinity, mutationFn: (sources: boolean) => api<PreflightReport>("/preflight", { method: "POST", body: { sources } }), onSettled: () => client.invalidateQueries({ queryKey: setupKey }) });
};

export const setupUnfinished = (state?: SetupState) => !!state && (state.first_run || (state.missing ?? []).length > 0);

/** The one-step start of an install no arr feeds: the library active with its downloads on, the optional preset as
 * its profile, and the global switches router and library on, in one save. A 422 lists the effects with a token. */
export const startSetup = (body: SetupStartBody) => api<SetupStarted>("/setup/start", { method: "POST", body });
