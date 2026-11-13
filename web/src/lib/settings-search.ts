import type { SchemaField, SettingsSchema, SettingsSections } from "@/api/settings";
import { ADVANCED_SETTINGS_PAGES, SETTINGS_PAGES, SETTINGS_PANELS, SETTINGS_ALIASES, panelHref } from "./settings-map";
import { wordMatcher } from "@/lib/words";
export type SettingResult = { href: string; label: string; field?: string; path?: string; terms: string };
/** Every page, tab, panel and field, plus each named record the saved settings hold (a metadata source such as tmdb,
 * a client, a notification target) with its fields: "tmdb key" finds the tmdb source's API key. */
export function settingsIndex(schema?: SettingsSchema, sections?: SettingsSections): SettingResult[] {
 const results: SettingResult[] = [];
 for (const p of [...SETTINGS_PAGES, ...ADVANCED_SETTINGS_PAGES]) {
  results.push({ href: p.route, label: p.label, terms: `${p.label} ${p.arr} ${p.description}` });
  for (const t of p.tabs) results.push({ href: t.route, label: `${p.label} › ${t.label}`, terms: `${p.label} ${t.label} ${p.arr} ${p.description}` });
 }
 for (const panel of SETTINGS_PANELS) {
  const p = [...SETTINGS_PAGES, ...ADVANCED_SETTINGS_PAGES].find(p => p.id === panel.page)!;
  const tab = p.tabs.find(t => t.id === panel.tab);
  const label = [p.label, tab?.label, panel.title].filter((s, i, all) => s && all.indexOf(s) === i).join(" › ");
  const href = panelHref(panel);
  const terms = `${label} ${panel.help ?? ""} ${panel.section} ${panel.keys.join(" ")}`;
  results.push({ href, label, terms });
  const walk = (fields: SchemaField[], prefix: string, config: unknown) => {
   for (const f of fields) {
    const path = f.name ? `${prefix}.${f.name}` : prefix;
    const value = f.name ? record(config)[f.name] : config;
    results.push({ href, label, field: f.label ?? f.name, path, terms: `${terms} ${f.label ?? ""} ${f.help ?? ""} ${path}` });
    if (f.type === "keyed") for (const name of Object.keys(record(value))) {
     const children = (f.fields ?? []).map(c => `${name} ${c.label ?? c.name}${c.type === "secret" ? ` ${name} key` : ""}`).join(" ");
     // ?open= unfolds the record on arrival (field-input.tsx KeyedRecord).
     const [route, anchor] = href.split("#");
     results.push({ href: `${route}?open=${encodeURIComponent(`${path}.${name}`)}#${anchor}`, label, field: `${f.label ?? f.name} › ${name}`, path: `${path}.${name}`, terms: `${terms} ${f.label ?? ""} ${name} ${children}` });
    }
    if (f.fields) walk(f.fields, path, f.type === "keyed" ? undefined : value);
   }
  };
  const config = sections?.sections.find(s => s.key === panel.section)?.config;
  walk((schema?.sections?.[panel.section] ?? []).filter(f => panel.keys.includes(f.name)), panel.section, config);
 }
 for (const alias of SETTINGS_ALIASES) {
  const found = alias.title ? undefined : results.find(r => r.href === alias.href);
  if (!found && alias.title) results.push({ href: alias.href, label: alias.title, terms: `${alias.title} ${alias.terms.join(" ")}` });
  if (found) found.terms += ` ${alias.terms.join(" ")}`;
 }
 return results;
}
const record = (v: unknown): Record<string, unknown> => v && typeof v === "object" && !Array.isArray(v) ? v as Record<string, unknown> : {};
/** The first 20 rows with every word typed: rows holding the whole typed phrase first, then the other matches,
 * each in index order. */
export function findSettings(index: SettingResult[], query: string) {
 const q = query.trim().toLowerCase();
 if (!q) return [];
 const match = wordMatcher(q);
 const phrase: SettingResult[] = [], words: SettingResult[] = [];
 for (const r of index) {
  if (r.terms.toLowerCase().includes(q)) phrase.push(r);
  else if (match(r.terms)) words.push(r);
 }
 return [...phrase, ...words].slice(0, 20);
}
