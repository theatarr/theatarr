import { useQuery } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { confirmationHeaders } from "./settings";

export type VersionRecord = Schemas["SettingsVersionRecord"];
export type VersionConfig = Schemas["SettingsVersionConfig"];
export type VersionBody = Schemas["SettingsVersionBody"];
export type VersionList = Schemas["SettingsVersions"];
export type Confirmation = Schemas["SettingsConfirmation"];

export const useVersions = (enabled = true) => useQuery({ enabled, queryKey: ["settings", "versions"], queryFn: () => api<VersionList>("/settings/versions") });
export const confirmVersion = (body: VersionBody) => api<Confirmation>("/settings/versions/confirm", { method: "POST", body });
export const saveVersion = (body: VersionBody, create: boolean) => body.delete
  ? api<Schemas["SettingsWrite"]>(`/settings/versions/${encodeURIComponent(body.key ?? "")}` + qs({ generation: body.generation }), { method: "DELETE", headers: confirmationHeaders(body.confirmation_token) })
  : api<Schemas["SettingsWrite"]>(create ? "/settings/versions" : `/settings/versions/${encodeURIComponent(body.key ?? "")}`, { method: create ? "POST" : "PUT", body });
export const checkRoot = (generation: number, key: string, root: string, signal: AbortSignal) => api<Schemas["SettingsRootCheck"]>("/settings/versions/validate-root", { method: "POST", body: { generation, key, root }, signal });
