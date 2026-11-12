// One-time import from Sonarr/Radarr (internal/arrimport, internal/web/arrimport.go): Check reads the instance's
// profiles and folders, a POST starts a Preview (dry_run) or an Import, and the run is read back while it goes. The
// API key goes in the request body only; it is never answered, cached or kept in the browser.
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "./client";

export type ArrImportCheck = Schemas["ArrImportCheckResult"];
export type ArrImportRequest = Schemas["ArrImportRequest"];
export type ArrImportRun = Schemas["ArrImportRun"];
export type ArrImportReport = Schemas["ArrImportReport"];
export type ArrImportState = Schemas["ArrImportState"];

export const arrImportKey = ["arr-import"] as const;

export const useArrImportCheck = () =>
  useMutation({
    mutationFn: (body: Schemas["ArrImportCheckRequest"]) =>
      api<ArrImportCheck>("/arr-import/check", { method: "POST", body }),
  });

export const startArrImport = (body: ArrImportRequest) =>
  api<ArrImportState>("/arr-import", { method: "POST", body });

/** The current or last run; read every 2 s while it runs. */
export const useArrImportRun = () =>
  useQuery({
    queryKey: arrImportKey,
    queryFn: () => api<ArrImportState>("/arr-import"),
    refetchInterval: (q) => (q.state.data?.run?.state === "running" ? 2000 : false),
  });
