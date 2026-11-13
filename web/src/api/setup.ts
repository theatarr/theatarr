// The setup screen (docs/quickstart.md, docs/design/web.md "Setup"): what a fresh install is missing (GET /setup) and the
// preflight (POST /preflight), which checks the mount, roots, providers, indexers, metadata keys, arr views, Plex
// and the VPN egress, each ok, warn or fail with the next step.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";
import { qk } from "./query-keys";

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
  // A full run also tests every enabled source as saved, so the settings list's last tests are refetched with it.
  return useMutation({ mutationKey: ["setup-preflight", generation], gcTime: Infinity, mutationFn: (sources: boolean) => api<PreflightReport>("/preflight", { method: "POST", body: { sources } }), onSettled: () => { void client.invalidateQueries({ queryKey: qk.settings.overview }); return client.invalidateQueries({ queryKey: setupKey }); } });
};

export const setupUnfinished = (state?: SetupState) => !!state && (state.first_run || (state.missing ?? []).length > 0);

/** Whether Home and the sidebar lead to Setup. Something the install needs is missing (setupUnfinished), or it has
 * not gone live and its readiness is open: no library is Active and no full preflight passed without failures for
 * the current settings. An install with an Active library never gets the link back for readiness alone: the
 * preflight summary is kept in memory, so its readiness step reopens with every restart. live is undefined until
 * the libraries have loaded, which shows nothing new. */
export const setupLinkShown = (state: SetupState | undefined, live: boolean | undefined, readiness: boolean) =>
  setupUnfinished(state) || (!!state && live === false && !readiness);

/** The one-step start of a new install: the library active, the optional preset as
 * its profile, and Pause off, in one save. A 422 lists the effects with a token. */
export const startSetup = (body: SetupStartBody) => api<SetupStarted>("/setup/start", { method: "POST", body });
