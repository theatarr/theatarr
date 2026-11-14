import { expect, it } from "vitest";
import type { SchemaField } from "@/api/settings";
import schema from "./testdata/schema.json";
import { SETTINGS_PANELS } from "@/lib/settings-map";

// Help under the ordinary (not advanced) Download behaviour and Notifications controls says what the control does in
// plain words: no settings keys (cached_only), outcome names (uncached_kept) or Go type names (CutoffMode).
it("keeps settings keys out of the ordinary Download behaviour and Notifications help", () => {
  const sections = schema.sections as Record<string, SchemaField[]>;
  const helps: string[] = [];
  const walk = (fields: SchemaField[], path: string) => {
    for (const f of fields) {
      if (f.advanced) continue;
      helps.push(`${path}.${f.name}: ${f.help}`);
      walk(f.fields ?? [], `${path}.${f.name}`);
    }
  };
  for (const p of SETTINGS_PANELS.filter((p) => !p.advanced && (p.page === "notifications" || (p.page === "downloads" && p.tab === "behaviour")))) {
    walk(sections[p.section].filter((f) => p.keys.includes(f.name)), p.section);
  }
  expect(helps.length).toBeGreaterThan(10);
  const names = /\b(TorBox|AllDebrid|FlareSolverr|GitHub|YouTube)\b/g;
  for (const h of helps) expect(h.slice(h.indexOf(": ")).replace(names, ""), h).not.toMatch(/\b[a-z]+_[a-z_]+\b|\b[A-Z][a-z]+[A-Z][A-Za-z]*\b/);
});

// No help names a source file, a design document, a job name or another field's YAML path (verified D39).
it("keeps source files, design documents, job names and YAML paths out of every settings help", () => {
  const helps: string[] = [];
  const walk = (fields: SchemaField[], path: string) => {
    for (const f of fields) { helps.push(`${path}.${f.name}: ${f.help}`); walk(f.fields ?? [], `${path}.${f.name}`); }
  };
  for (const [key, fields] of Object.entries(schema.sections as Record<string, SchemaField[]>)) walk(fields, key);
  for (const h of helps) expect(h.slice(h.indexOf(": ")), h).not.toMatch(/\.go\b|docs\/|shadow\.rss|meta\.art|library\.(enabled|mount)|backup\.every|ShouldRefreshSeries|theatarr serve|\b\d+[hms]\b/);
});
