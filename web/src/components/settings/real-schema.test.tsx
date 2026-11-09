// @vitest-environment jsdom
import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { SchemaField } from "@/api/settings";
import { emptySecrets } from "./section-draft";
import { SectionFields } from "./field-input";
import schema from "./testdata/schema.json";
afterEach(cleanup);
function seeded(fields: SchemaField[]): Record<string, unknown> {
 const config: Record<string, unknown> = {};
 for (const f of fields) {
  let value: unknown;
  switch (f.type) {
   case "secret": continue;
   case "object": value = seeded(f.fields ?? []); break;
   case "keyed": value = { example: seeded(f.fields ?? []) }; break;
   case "object_list": value = [seeded(f.fields ?? [])]; break;
   case "boolean": value = null; break;
   case "duration": value = f.zero_allowed ? "0s" : (f.min ?? 0) <= 21600 && (f.max ?? Infinity) >= 21600 ? "6h0m0s" : null; break;
   case "bytes": value = "2GB"; break;
   case "integer": case "number": value = f.nullable ? null : 4; break;
   case "list": value = f.enum ? [f.enum[0]] : ["example"]; break;
   case "enum": value = f.enum?.[0]; break;
   default: value = "example";
  }
  if (f.name) config[f.name] = value; else Object.assign(config, value);
 }
 return config;
}
it.each(Object.entries(schema.sections))("renders real %s schema with seeded durations, null pointers and stored credentials", (key, fields) => {
 const secrets: {path: string; set: boolean}[] = [];
 const collect = (fs: SchemaField[], path: string) => { for (const f of fs) { const next = f.name ? `${path}.${f.name}` : path; if (f.type === "secret") secrets.push({ path: next.slice(key.length + 1), set: true }); collect(f.fields ?? [], next + (f.type === "keyed" ? ".example" : "")); } };
 collect(fields, key);
 render(<SectionFields fields={fields} config={seeded(fields)} path={key} advanced={false} onChange={() => {}} secrets={secrets.map(s => ({ ...s, path: `${key}.${s.path}` }))} secretDraft={emptySecrets()} onSecrets={() => {}} />);
 for (const select of screen.queryAllByRole("combobox")) expect((select as HTMLSelectElement).value).not.toBe("custom");
 for (const input of document.querySelectorAll('input[type="password"]')) expect((input as HTMLInputElement).value).toBe("");
});
