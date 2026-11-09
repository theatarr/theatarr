import { objectValue, type Config, type SecretDraft } from "./field-input";
import type { Explained } from "@/lib/settings";

export function sectionExplanation(error: Explained): Explained {
  return error.kind === "stale" ? { ...error, message: "Settings changed since this edit began. Reload the saved settings before saving again.", server: undefined } : error;
}
export const emptySecrets = (): SecretDraft => ({ actions: {}, values: {} });
// The API has always accepted section-relative credential paths. The schema and
// field errors use full paths so controls can be matched across all sections.
export function sectionSecrets(key: string, draft: SecretDraft) {
  const secrets: Record<string, string> = {};
  const clear_secrets: string[] = [];
  for (const [path, action] of Object.entries(draft.actions)) {
    const relative = path.startsWith(key + ".") ? path.slice(key.length + 1) : path;
    if (action === "replace") secrets[relative] = draft.values[path] ?? "";
    if (action === "clear") clear_secrets.push(relative);
  }
  return { secrets, clear_secrets };
}
export function sectionErrors(error: unknown): Record<string, string> {
  const fields = (error as { body?: { fields?: { path: string; message: string }[] } } | undefined)?.body?.fields;
  return Object.fromEntries((fields ?? []).map((f) => [f.path.replace(/\[([^\]]+)\]/g, ".$1"), f.message]));
}
// Creating a credential creates its parent in the same PUT, without requiring
// an environment fallback name. No unrelated defaults are materialized.
export function withSecretParents(config: unknown, key: string, draft: SecretDraft): unknown {
  let next = config;
  for (const [path, action] of Object.entries(draft.actions)) {
    if (action !== "replace") continue;
    next = structuredClone(objectValue(next));
    let parent = next as Config;
    for (const part of path.slice(key.length + 1).split(".").slice(0, -1)) {
      parent[part] = { ...objectValue(parent[part]) };
      parent = parent[part] as Config;
    }
  }
  return next;
}
// The leaf paths where a draft differs from the saved value, for the unsaved-changes count. Plain objects are
// compared key by key; lists and scalars as a whole. A key that is absent and one that is null both read as unset.
export function changedPaths(draft: unknown, saved: unknown, prefix = ""): string[] {
  const plain = (v: unknown) => !!v && typeof v === "object" && !Array.isArray(v);
  if (plain(draft) && plain(saved)) {
    const a = draft as Config, b = saved as Config;
    return [...new Set([...Object.keys(a), ...Object.keys(b)])].flatMap((k) => changedPaths(a[k], b[k], prefix ? `${prefix}.${k}` : k));
  }
  return JSON.stringify(draft ?? null) === JSON.stringify(saved ?? null) ? [] : [prefix];
}
