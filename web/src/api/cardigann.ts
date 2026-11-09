// The cardigann definition catalog (docs/design/cardigann.md): the definitions Theatarr runs itself, the updates held for
// review, and the one-off Prowlarr migration proposals. Every route answers 503 while cardigann.definitions is unset.
// The catalog never carries a configured value; the proposals write nothing.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./client";
import { qk } from "./query-keys";

export type CardigannSetting = {
  name: string;
  label: string;
  /** text, password, checkbox, select, or a predefined info_* note (its default is the text). */
  type: string;
  default?: string | boolean;
  options?: { value: string; label: string }[];
  /** Goes in the indexer's credential (a JSON object), never in its settings. */
  secret?: boolean;
  required?: boolean;
};

export type CardigannStatus = "runs" | "needs_account" | "needs_flaresolverr" | "unsupported";

export type CardigannDefinition = {
  id: string;
  name: string;
  description: string;
  /** The definition's own privacy; privacy is the label Theatarr gives it (private unless type is public). */
  type: string;
  privacy: string;
  language: string;
  protocol: string;
  links: string[];
  legacy_links?: string[];
  replaces?: string[];
  settings: CardigannSetting[];
  categories: number[];
  login?: string;
  /** Hosts besides the site that searches reach; the indexer must list them. */
  hosts?: string[];
  supported: boolean;
  problems?: string[];
  status: CardigannStatus;
  local?: boolean;
  /** The operator's own definition (custom/ in the catalog folder), which wins over a synced one with the same id. */
  custom?: boolean;
  /** The RSS sync can read the site's latest releases (its search without keywords); rss_problem says why not. */
  rss?: boolean;
  rss_problem?: string;
};

export type CardigannSync = {
  commit?: string;
  schema?: number;
  updated_at?: string;
  last_check?: string;
  last_error?: string;
  definitions: number;
  holds: number;
};

/** unavailable: set, with an empty list, when the catalog is off (why, and where to turn it on). */
export type CardigannList = { status: CardigannSync; total: number; definitions: CardigannDefinition[]; unavailable?: string };

export type CardigannHold = { id: string; reasons: string[]; detail: string[]; commit: string; since: string; removed?: boolean; hash?: string };

export type ProwlarrProposal = {
  prowlarr: { id: number; name: string; definition_name: string; privacy: string; enabled: boolean };
  status: "proposed" | "no_definition" | "unsupported" | "usenet";
  key?: string;
  indexer?: { kind: string; definition: string; url: string; privacy: string; budget: string; settings?: Record<string, string>; hosts?: string[] };
  budget?: Record<string, string>;
  secrets?: string[];
  notes?: string[];
};

/** The whole catalog; filtering is done in the page so typing never waits on a request. */
export const useCardigannCatalog = (enabled = true) =>
  useQuery({ queryKey: qk.settings.cardigann.catalog, queryFn: () => api<CardigannList>("/cardigann/definitions"), enabled, retry: false });

export const useCardigannHolds = (enabled = true) =>
  useQuery({ queryKey: qk.settings.cardigann.holds, queryFn: async () => {
    try { return await api<CardigannHold[]>("/cardigann/holds"); }
    catch (error) { if (error instanceof ApiError && error.status === 503) return []; throw error; }
  }, enabled, retry: false, refetchInterval: 60_000 });

/** Reads the configured Prowlarr through the gateway; only when asked. */
export const prowlarrProposals = () => api<ProwlarrProposal[]>("/cardigann/prowlarr");

/** Applies a held definition update (or a held removal). */
export function useApproveHold() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api<{ ok: boolean }>(`/cardigann/holds/${encodeURIComponent(id)}/approve`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.settings.cardigann.all }),
  });
}
