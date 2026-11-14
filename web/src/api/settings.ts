// The settings admin API (docs/design/settings.md): indexers, debrid providers and budgets in the settings
// database. Every write carries the generation it was made against; a stale one answers 409. Secrets are write-only:
// records carry secret_set and nothing else. With --settings-source yaml every write answers 409.
import { setupKey } from "@/api/setup";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs, type Schemas } from "./client";
import { qk } from "./query-keys";

export type SettingsOverview = Schemas["SettingsOverview"];
export type SettingsRecord = Schemas["SettingsRecord"];
export type SettingsTestResult = Schemas["SettingsTestResult"];
export type SettingsWrite = Schemas["SettingsWrite"];
export type SettingsAuditPage = Schemas["SettingsAuditPage"];
export type SettingsAuditRow = SettingsAuditPage["records"][number];
export type Entity = "indexers" | "providers" | "budgets";
export type SourceEntity = "indexers" | "providers";

/** One form field from GET /settings/schema (internal/web/settingsschema.go). */
export type SchemaField = {
  name: string;
  path?: string;
  min?: number;
  max?: number;
  default?: string;
  unit?: string;
  label?: string;
  group?: string;
  advanced?: boolean;
  zero_allowed?: boolean;
  /** string, integer, boolean, duration, enum, list, map (to integers), string_map (to strings), object or secret. */
  type: string;
  required?: boolean;
  enum?: string[];
  /** The kinds the field applies to; empty means every kind. */
  kinds?: string[];
  help: string;
  /** Clearing the field sends null, which takes its default (an integer's 0 means something else). */
  nullable?: boolean;
  fields?: SchemaField[];
};
export type SchemaEntity = { kinds?: string[]; record: SchemaField[]; config: SchemaField[]; tests: boolean };
export type SettingsSchema = {
  section_nullable?: Record<string, boolean>; sections?: Record<string, SchemaField[]>; indexers: SchemaEntity; providers: SchemaEntity; budgets: SchemaEntity; rules: string[] };

/** One record an import would create, update or delete (POST /settings/import/preview). */
export type ImportChange = {
  entity: Entity | "profiles" | "formats" | "format_sets" | "naming" | "versions" | "views";
  key: string;
  operation: "create" | "update" | "delete";
  /** The import replaces the record's credential (a flag only: never a value). */
  credential_changed?: boolean;
  /** What an update changes: kind, enabled, priority, order, config. An import keeps enabled and priority. */
  fields?: ("kind" | "enabled" | "priority" | "order" | "config" | "specification" | "specifications")[];
};
export type ImportPreview = {
  generation: number;
  changes: ImportChange[];
  before: Record<Entity, SettingsRecord[]>;
  after: Record<Entity, SettingsRecord[]>;
  /** Review YAML of the imported result, credentials and *_env names stripped. */
  yaml: string;
  files?: Record<string, unknown>;
  /** One line: settings sections are kept as stored, and which the files fill. */
  sections_note: string;
  /** The settings sections the database lacks, which the import takes from the files. */
  sections_from_files: string[];
};
export type TestAll = { generation: number; results: SettingsTestResult[] };

export const settingsOverview = () => api<SettingsOverview>("/settings");

export const useSettings = (enabled = true) => useQuery({ queryKey: qk.settings.overview, queryFn: settingsOverview, enabled });

export const useSettingsSchema = () =>
  useQuery({ queryKey: qk.settings.schema, queryFn: () => api<SettingsSchema>("/settings/schema"), staleTime: Infinity });

/** The audit newest first: before is the previous page's next (0: start at the newest record). */
export const useSettingsAudit = (before: number, limit: number, enabled = true) =>
  useQuery({
    queryKey: qk.settings.audit(before, limit),
    queryFn: () => api<SettingsAuditPage>("/settings/audit" + qs({ order: "desc", before: before || undefined, limit })),
    enabled,
  });

/** token: a confirmation token from a 422 that listed write-enabling effects (sent as X-Confirmation-Token). */
export type SettingsCall = { method: "POST" | "PUT" | "DELETE"; path: string; body?: unknown; token?: string };

/** The header that carries a confirmation token; tokens never go in a URL. */
export const confirmationHeaders = (token?: string): Record<string, string> | undefined =>
  token ? { "X-Confirmation-Token": token } : undefined;

/** Any settings write (create, update, delete, priority, order, import, reload). Success refreshes every settings
 * read and the Connections pages; a refusal leaves the cache as it is so the form keeps what was typed. */
export function useSettingsWrite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (c: SettingsCall) => api<SettingsWrite>("/settings" + c.path, { method: c.method, body: c.body, headers: confirmationHeaders(c.token) }),
    onSuccess: (_data, c) => {
      void qc.invalidateQueries({ queryKey: setupKey });
      void qc.invalidateQueries({ queryKey: qk.settings.all });
      void qc.invalidateQueries({ queryKey: qk.sources });
      void qc.invalidateQueries({ queryKey: qk.gateway });
      // A provider account's tag (auth/me providers) follows an added, changed or removed account.
      if (c.path.includes("/providers")) void qc.invalidateQueries({ queryKey: qk.me });
    },
  });
}

/** Reload the settings reads now (after a 409: settings changed elsewhere). */
export function useReloadSettings() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: qk.settings.all });
}

export const importPreview = (generation: number) =>
  api<ImportPreview>("/settings/import/preview", { method: "POST", body: { generation } });

export const importConfirm = (generation: number, orphan: boolean) =>
  api<Schemas["SettingsConfirmation"]>("/settings/import/confirm", { method: "POST", body: { generation, orphan_active_downloads: orphan } });

export const importApply = (generation: number, orphan: boolean, confirmation_token?: string) =>
  api<SettingsWrite>("/settings/import", { method: "POST", body: { generation, orphan_active_downloads: orphan, confirmation_token } });

/** Test a saved source. A 429 (cooldown, no slot) is an ApiError whose body is the test result. */
export const testSaved = (entity: SourceEntity, id: number) =>
  api<SettingsTestResult>(`/settings/${entity}/${id}/test`, { method: "POST" });

/** Test an unsaved candidate (a new source, or changes to a saved one by id); nothing is stored. */
export const testCandidate = (entity: SourceEntity, body: Record<string, unknown>) =>
  api<SettingsTestResult>(`/settings/${entity}/test`, { method: "POST", body });

export const testAll = (entity: SourceEntity, includeDisabled: boolean) =>
  api<TestAll>(`/settings/${entity}/testall`, { method: "POST", body: includeDisabled ? { include_disabled: true } : {} });

/** The database-owned policy sections (GET /settings/sections): each as config and YAML, live or restart, and its
 * credential paths (write-only: set, plus the environment fallback name). */
export type SettingsSections = Schemas["SettingsSections"];
export type SettingsSection = SettingsSections["sections"][number];
export const sectionsQuery = { queryKey: [...qk.settings.all, "sections"], queryFn: () => api<SettingsSections>("/settings/sections") };
export const useSettingsSections = () => useQuery(sectionsQuery);

/** The arr-compatible views (GET /settings/view-bindings): identity, version sets and whether a key is stored. */
export type SettingsViews = Schemas["SettingsViewBindings"];
export type SettingsView = SettingsViews["views"][number];
export const viewsQuery = { queryKey: ["settings", "view-bindings"], queryFn: () => api<SettingsViews>("/settings/view-bindings") };
export const useSettingsViews = () => useQuery(viewsQuery);
