import { useEffect, useRef, useState } from "react";
import { Link, useLocation } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { sectionsQuery, useSettings, useSettingsSections, useSettingsSchema } from "@/api/settings";
import { ErrorState, Loading } from "@/components/page";
import { useVersions } from "@/api/settings-versions";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { useShowAdvanced } from "@/lib/settings-map";
import { SectionFields, objectValue, type Config } from "./field-input";
import { changedPaths, emptySecrets, sectionSecrets, sectionErrors, withSecretParents } from "./section-draft";
import { UnsavedBar } from "./unsaved-bar";
import { useSectionPatch } from "./use-section-patch";

const plainFields = (fields: import("@/api/settings").SchemaField[], prefix: string, notifyForced = false): import("@/api/settings").SchemaField[] => fields.map(field => {
 const path = `${prefix}.${field.name}`;
 const wording: Record<string, { label?: string; help: string }> = {
  "library.versions": { label: "Libraries", help: "Choose the Plex library that shows each Theatarr library." },
  "library.versions.plex_section": { help: "The Plex library ID that shows this library’s folder. 0: no scans." },
  "notify.enabled": { label: "Send notifications", help: "Off: events are recorded, nothing is sent." + (notifyForced ? " Locked off by THEATARR_FORCE_OFF. Remove it and restart to send notifications." : "") },
  "notify.targets": { help: "Webhook destinations, by name." },
  "notify.targets.versions": { label: "Libraries", help: "Send events only for these libraries, by key. Empty: every library." },
  "cardigann.definitions": { help: "Where the catalog's indexer definitions come from. Empty repository and branch: prowlarr/indexers, master." },
 };
 return { ...field, ...wording[path], ...(field.fields ? { fields: plainFields(field.fields, path) } : {}) };
});
type Props = { id: string; title: string; help?: string; section: string; keys: string[]; advanced?: boolean; onSaved?: () => void };
export function SectionPanel(props: Props) {
 const [advanced, setAdvanced] = useShowAdvanced();
 const { hash } = useLocation();
 useEffect(() => {
  if (props.advanced && hash === `#${props.id}`) setAdvanced(true);
 }, [hash, props.id, props.advanced, setAdvanced]);
 return props.advanced && !advanced ? null : <PanelEditor {...props} />;
}
function PanelEditor({ id, title, help, section: key, keys, onSaved }: Props) {
 const sections = useSettingsSections();
 const schema = useSettingsSchema();
 const qc = useQueryClient();
 const [advanced] = useShowAdvanced();
 const [draft, setDraft] = useState<Config>();
 const [secrets, setSecrets] = useState(emptySecrets);
 const [saved, setSaved] = useState(false);
 const [highlight, setHighlight] = useState(false);
 const ref = useRef<HTMLDivElement>(null);
 const { hash } = useLocation();
 // The panel exists once both reads are in, whichever lands last, so the anchor scroll waits for both.
 const ready = !sections.isPending && !schema.isPending;
 useEffect(() => {
  if (hash !== `#${id}` || !ready) return;
  ref.current?.scrollIntoView?.({ behavior: "smooth", block: "start" });
  setHighlight(true);
  const timer = setTimeout(() => setHighlight(false), 1800);
  return () => clearTimeout(timer);
 }, [hash, id, ready]);
 const perLibrary = key === "library" && keys.includes("versions");
 const libraries = useVersions(perLibrary);
 // THEATARR_FORCE_OFF=notify keeps notifications off whatever the stored tick says; the tick stays editable.
 const notifyForced = useSettings(key === "notify").data?.switches.notify_forced === true;
 const editor = useSectionPatch(key, () => {
  setSaved(true); setDraft(undefined); setSecrets(emptySecrets());
  onSaved?.();
  void qc.invalidateQueries({ queryKey: sectionsQuery.queryKey });
 });
 if (sections.isPending || schema.isPending) return <Loading />;
 if (sections.error || schema.error) return <ErrorState error={sections.error ?? schema.error} />;
 const section = sections.data.sections.find(s => s.key === key);
 const config = objectValue(section?.config);
 const select = (value: Config) => keys.includes("") ? structuredClone(value) : Object.fromEntries(keys.filter(k => k in value).map(k => [k, structuredClone(value[k])]));
 // Plex library for each Theatarr library: the stored section names a library only once something was set for it,
 // so a new library had no row. Every library gets one; a row left empty is not written.
 const libraryKeys = (libraries.data?.versions ?? []).map(v => v.key);
 const storedRows = objectValue(config.versions);
 const withRows = (value: Config): Config => perLibrary && libraryKeys.length ? { ...value, versions: { ...Object.fromEntries(libraryKeys.map(k => [k, {}])), ...objectValue(value.versions) } } : value;
 const withoutEmptyRows = (versions: unknown) => {
  const rows = Object.entries(objectValue(versions)).filter(([k, row]) => k in storedRows || Object.keys(objectValue(row)).length > 0);
  return rows.length || config.versions !== undefined ? Object.fromEntries(rows) : undefined;
 };
 // A library ID needs the Plex server (the server refuses one while library.plex is unset), so the rows wait for it.
 const plexMissing = perLibrary && config.plex == null;
 const loaded = withRows(select(config));
 const value = draft ?? loaded;
 const changes = changedPaths(value, loaded).length + Object.values(secrets.actions).filter(a => a !== "keep").length;
 const dirty = changes > 0;
 const readOnly = sections.data.source !== "db";
 const missingSecret = Object.entries(secrets.actions).some(([path, action]) => action === "replace" && !secrets.values[path]);
 const reset = () => { setDraft(undefined); setSecrets(emptySecrets()); setSaved(false); };
 const save = () => {
  const scoped = { actions: Object.fromEntries(Object.entries(secrets.actions).filter(([path]) => keys.includes("") || keys.some(k => path === `${key}.${k}` || path.startsWith(`${key}.${k}.`)))), values: secrets.values };
  const credentials = sectionSecrets(key, scoped);
  void editor.save(latest => {
   if (keys.includes("")) { for (const k of Object.keys(latest)) delete latest[k]; Object.assign(latest, value); }
   else for (const k of keys) {
    const next = perLibrary && k === "versions" ? withoutEmptyRows(value[k]) : value[k];
    if (k in value && next !== undefined) latest[k] = next; else delete latest[k];
   }
   Object.assign(latest, withSecretParents(latest, key, scoped));
  }, credentials.secrets, credentials.clear_secrets);
 };
 return <div ref={ref}><Card id={id} className={`mb-4 scroll-mt-6 ${highlight ? "ring-2 ring-primary" : ""}`}>
  <h2 className="text-lg font-semibold">{title}</h2>
  {help ? <p className="mb-4 text-sm text-muted-foreground">{help}</p> : null}
  {readOnly ? <p>Read-only: these settings cannot be edited here.</p> : null}
  {plexMissing ? <p className="mb-4 text-sm" data-testid="plex-server-first">Set the Plex address and token above first.</p> : null}
  {key === "cardigann" && keys.includes("definitions") ? <CatalogState saved={loaded.definitions} draft={value.definitions} disabled={readOnly || editor.busy} onTurnOn={() => { setDraft({ ...value, definitions: { repo: CATALOG_REPO } }); setSaved(false); }} /> : null}
  <SectionFields fields={plainFields((schema.data.sections?.[key] ?? []).filter(f => keys.includes(f.name)), key, notifyForced)} config={value} saved={loaded} path={key} advanced={advanced} disabled={readOnly || editor.busy || plexMissing} onChange={v => { setDraft(v); setSaved(false); }} secrets={(section?.secrets ?? []).map(s => ({ ...s, path: `${key}.${s.path}` }))} secretDraft={secrets} onSecrets={v => { setSecrets(v); setSaved(false); }} errors={sectionErrors(editor.failure)} />
  {perLibrary && libraries.data && !Object.keys(objectValue(value.versions)).length ? <p className="text-sm" data-testid="plex-libraries-empty">No libraries yet. Create one under <Link className="underline" to="/admin/settings/libraries">Settings › Libraries</Link>; each library then gets a row here for the ID of the Plex library that shows its folder.</p> : null}
  <UnsavedBar changes={changes} label={title}>
   <Button variant="outline" disabled={readOnly || editor.busy || !dirty} onClick={reset}>Discard</Button>
   <Button disabled={readOnly || editor.busy || !dirty || missingSecret} onClick={save}>Save</Button>
  </UnsavedBar>
  {saved ? <p role="status">Saved.</p> : null}
  {editor.feedback}
 </Card></div>;
}

const CATALOG_REPO = "prowlarr/indexers";
/** The indexer catalog is on exactly when cardigann.definitions is set (docs/design/cardigann.md "The definition catalog"): say
 * which, and turn it on by filling in the usual repository, saved with the panel's Save like any other edit. */
function CatalogState({ saved, draft, disabled, onTurnOn }: { saved: unknown; draft: unknown; disabled: boolean; onTurnOn: () => void }) {
 if (saved != null) {
  const d = objectValue(saved);
  return <p className="mb-4 text-sm" data-testid="catalog-state">The catalog is on: it follows {String(d.repo || CATALOG_REPO)}, branch {String(d.branch || "master")}.</p>;
 }
 if (draft != null) return <p className="mb-4 text-sm" data-testid="catalog-state">The catalog is off. Save to turn it on.</p>;
 return <div className="mb-4 flex flex-wrap items-center gap-3 text-sm" data-testid="catalog-state"><span>The catalog is off: no indexer definitions are fetched.</span><Button type="button" variant="outline" size="sm" disabled={disabled} onClick={onTurnOn}>Turn the catalog on</Button></div>;
}
