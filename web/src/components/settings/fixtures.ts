// Fixtures for the settings tests (lib/settings.test.ts, components/settings/settings.test.ts): an overview, the
// provider schema as GET /settings/schema serves it, and saved records. Test data only; no credential values exist
// anywhere in the UI to put here.
import type { SchemaEntity, SettingsOverview, SettingsRecord, SettingsSchema } from "@/api/settings";

export const overview = (o: Partial<SettingsOverview> = {}): SettingsOverview => ({
  source: "db",
  generation: 4,
  active_generation: 4,
  provider_epoch: 1,
  writable: true,
  policy_dir: "/config/policy",
  yaml_owned: ["the database path, policy directory and listen address (command line or environment)", "credentials supplied as environment variables (a stored credential wins)"],
  switches: { router_enabled: false, library_enabled: true, notify_enabled: false, versions: [{ key: "movie-web", mode: "shadow" }, { key: "tv-web", mode: "active" }] },
  indexers: [],
  providers: [],
  budgets: [],
  ...o,
});

export const providerSchema: SchemaEntity = {
  kinds: ["realdebrid", "alldebrid", "torbox"],
  tests: true,
  record: [],
  config: [
    { name: "kind", type: "enum", required: true, enum: ["realdebrid", "alldebrid", "torbox"], help: "" },
    { name: "order", type: "integer", required: true, help: "" },
    { name: "url", type: "string", required: true, help: "" },
    { name: "budget", type: "string", help: "" },
    { name: "pacing", type: "object", help: "", fields: [{ name: "adds_per_minute", type: "integer", help: "" }, { name: "cooldown", type: "duration", help: "" }] },
  ],
};

export const rd: SettingsRecord = {
  id: 7,
  key: "rd",
  kind: "realdebrid",
  enabled: true,
  order: 1,
  secret_set: true,
  config: { kind: "realdebrid", order: 1, url: "https://api.real-debrid.com/rest/1.0", budget: "rd-api", pacing: { adds_per_minute: 10, cooldown: "10m" } },
};

export const indexerSchema: SchemaEntity = {
  kinds: ["torznab", "dmm", "torrentio", "zilean", "seadex"],
  tests: true,
  record: [],
  config: [
    { name: "kind", type: "enum", required: true, enum: ["torznab", "dmm", "torrentio", "zilean", "seadex"], help: "" },
    { name: "url", type: "string", required: true, help: "Endpoint URL." },
    { name: "privacy", type: "enum", enum: ["public", "private", "semi-private"], help: "" },
    { name: "budget", type: "string", help: "" },
    { name: "page_cap", type: "integer", help: "" },
    { name: "seadex", type: "object", kinds: ["seadex"], help: "", fields: [{ name: "best", type: "integer", help: "" }] },
  ],
};

export const budgetSchema: SchemaEntity = {
  record: [],
  tests: false,
  config: [
    { name: "requests", type: "integer", required: true, help: "" },
    { name: "per", type: "duration", required: true, help: "" },
    { name: "daily", type: "integer", help: "" },
  ],
};

export const schema: SettingsSchema = { indexers: indexerSchema, providers: providerSchema, budgets: budgetSchema, rules: [] };

export const dmm: SettingsRecord = {
  id: 3,
  key: "dmm",
  kind: "dmm",
  enabled: true,
  priority: 0,
  secret_set: true,
  config: { kind: "dmm", url: "https://debridmediamanager.com/api/torznab/rd/cached", privacy: "public", budget: "dmm-key", page_cap: 200 },
  last_test: { key: "dmm", entity: "indexers", status: "ok", latency_ms: 120, results: 40, tested_at: "2026-10-27T11:00:00Z" },
};

export const zilean: SettingsRecord = {
  id: 4,
  key: "zilean",
  kind: "zilean",
  enabled: false,
  priority: 10,
  secret_set: false,
  config: { kind: "zilean", url: "http://zilean:8181", privacy: "public" },
  backoff: { subject: "indexer:zilean", active: true, until: "2026-10-27T13:00:00Z", failures: 3, reason: "connectivity", updated_at: "2026-10-27T11:30:00Z" },
};

export const budget: SettingsRecord = {
  id: 1,
  key: "dmm-key",
  config: { requests: 16, per: "1m0s", burst: 0, hourly: 0, daily: 0 },
  usage: { key: "dmm-key", requests: 16, per: "1m0s", used_hour: 12, used_day: 140, used_by: ["dmm"] },
};
