// The setup screen (docs/quickstart.md, docs/design/web.md "Setup"): what a fresh install is missing (GET /setup) and the
// preflight (POST /preflight), which checks the mount, roots, providers, indexers, metadata keys, arr views, Plex
// and the VPN egress, each ok, warn or fail with the next step.
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "./client";

export type SetupState = Schemas["SetupState"];
export type PreflightReport = Schemas["PreflightReport"];
export type PreflightItem = Schemas["PreflightItem"];

export const setupKey = ["setup"] as const;
export const useSetup = (enabled = true) => useQuery({ queryKey: setupKey, queryFn: () => api<SetupState>("/setup"), enabled });

/** sources false skips the provider, indexer and Plex calls: only what needs no outbound request. */
export const usePreflight = (generation?: number) =>
  useMutation({ mutationKey: ["setup-preflight", generation], gcTime: Infinity, mutationFn: (sources: boolean) => api<PreflightReport>("/preflight", { method: "POST", body: { sources } }) });

export const setupUnfinished = (state?: SetupState) => !!state && (state.first_run || (state.missing ?? []).length > 0);
