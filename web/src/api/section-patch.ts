import { api } from "./client";
import type { SettingsSections } from "./settings";
export type SectionConfig = Record<string, unknown>;
export type SectionMutation = (config: SectionConfig) => void;
export type SectionRequest = { path: string; method: "PUT"; body: { generation: number; config: SectionConfig; secrets?: Record<string, string>; clear_secrets?: string[] } };

/** Read and copy the latest section, then submit the exact patch through the
 * editor's shared confirmation/conflict lifecycle. No automatic stale retry. */
export async function saveSectionPatch(key: string, mutate: SectionMutation, send: (request: SectionRequest) => Promise<void>, secrets?: Record<string, string>, clearSecrets?: string[]) {
  const data = await api<SettingsSections>("/settings/sections");
  if (data.source !== "db") throw new Error("Read-only: these settings cannot be edited here.");
  const section = data.sections.find((s) => s.key === key);
  if (!section) throw new Error("Section not found. Reload settings.");
  const config = structuredClone(section.config ?? {}) as SectionConfig;
  mutate(config);
  const relative = secrets ? Object.fromEntries(Object.entries(secrets).map(([path, value]) => [path.startsWith(key + ".") ? path.slice(key.length + 1) : path, value])) : undefined;
  await send({ path: `/settings/sections/${encodeURIComponent(key)}`, method: "PUT", body: { generation: data.generation, config, ...(clearSecrets?.length ? { clear_secrets: clearSecrets.map((path) => path.startsWith(key + ".") ? path.slice(key.length + 1) : path) } : {}), ...(relative && Object.keys(relative).length ? { secrets: relative } : {}) } });
}
