import { SettingsAuthority } from "@/components/settings/authority";
import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, API_BASE, ApiError } from "@/api/client";
import { confirmationHeaders, useSettings } from "@/api/settings";
import { useWriteConfirmation } from "@/components/settings/confirm-write";
import { Announcer, ExplainBox, useAnnouncement, ManagedInSettings } from "@/components/settings/parts";
import { ErrorState, Loading } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Sheet, SheetLayout, useEditParam } from "@/components/ui/sheet";
import { explainError } from "@/lib/settings";
import { qk } from "@/api/query-keys";
import { FormatFields, FormGrid, JSONField, ProfileFields, Select, TextField, Toggle, object, pretty, text, type Config, type FormatJSON, type Implementation } from "./fields";
import { SyncSources } from "./sync";

export const QUALITY_TABS = ["profiles", "formats", "format_sets", "naming", "sync_sources"] as const;
export type QualityEntity = typeof QUALITY_TABS[number];
export type QualityRecord = { id: number; key: string; config: Config; specification?: FormatJSON; specifications?: Record<string, FormatJSON> };
/** The naming fields in form order, with their labels. */
const NAMING_LABELS: Record<string, string> = {
  folder: "Title folder",
  file: "File name",
  season: "Season folder",
  specials: "Specials folder",
  daily: "Daily episode file name",
  anime: "Anime episode file name",
  from_arr: "Naming from arr",
  from_version: "Naming from library",
};

const NOUN: Record<QualityEntity, string> = { profiles: "profile", formats: "format", format_sets: "format set", naming: "library naming", sync_sources: "sync source" };
type Session = { entity: QualityEntity; id?: number; generation: number; draft: QualityRecord };
const blankFormat = (): FormatJSON => ({ name: "", includeCustomFormatWhenRenaming: false, specifications: [] });
const defaults = (entity: QualityEntity): Config => entity === "profiles" ? { qualities: ["WEBDL-1080p"], cutoff: "WEBDL-1080p", formats: {}, min_score: 0, cutoff_score: 0, min_upgrade_step: 1, upgrades: true } : entity === "format_sets" ? { flavour: "radarr", formats: {} } : entity === "sync_sources" ? { kind: "trash", flavour: "radarr", schedule: "24h", enabled: false, trash_ids: [], profiles: {} } : {};
// Tracking a branch defaults to a weekly run; a pinned archive does not apply to it.
export const trackOrigin = (c: Config, origin: string): Config => {
  const next: Config = { ...c, origin };
  if (origin === "github") {
    delete next.archive_url; delete next.commit;
    if (!c.schedule || c.schedule === "24h") next.schedule = "168h";
  } else {
    delete next.repo; delete next.branch;
    if (c.schedule === "168h") next.schedule = "24h";
  }
  return next;
};
const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const draftRecord = (record: QualityRecord): QualityRecord => {
  const draft = clone(record);
  if (draft.specification) {
    if (typeof draft.config.include_when_renaming === "boolean") draft.specification.includeCustomFormatWhenRenaming = draft.config.include_when_renaming;
    if (typeof draft.config.trash_id === "string" && draft.config.trash_id) draft.specification.trash_id = draft.config.trash_id;
  }
  return draft;
};

const NAMING_TEMPLATES = ["folder", "file", "season", "specials", "daily", "anime", "colon", "multi_episode"] as const;

/** A library that takes its naming from the library it follows (from_version F) can copy F's templates into its own
 * draft, so it keeps them once it stops following. A template F leaves empty is copied as the built-in one F uses
 * (defaults, from the server), so the copy names files exactly as F does and passes the follower's validation. F named
 * by an arr instance (from_arr) cannot be copied here. */
function CopyNaming({ from, source, defaults, onCopy }: { from: string; source?: QualityRecord; defaults: Record<string, string>; onCopy: (config: Config) => void }) {
  const arr = source ? text(source.config.from_arr) : "";
  if (!source || arr) return <p className="text-sm" data-testid="copy-naming">{from} takes its naming from the {arr || "arr"} instance, so it cannot be copied here. Type the templates in by hand.</p>;
  const copy = () => {
    const config: Config = { from_version: "", from_arr: "" };
    for (const key of NAMING_TEMPLATES) config[key] = text(source.config[key]) || defaults[key];
    onCopy(config);
  };
  return <div className="space-y-2 text-sm" data-testid="copy-naming"><p>This library uses the naming of <strong>{from}</strong>. Copy it here so this library keeps its own naming when it stops following.</p><Button type="button" variant="outline" size="sm" onClick={copy}>Copy {from}'s naming</Button></div>;
}

/** skip: record keys listed elsewhere, left out of the record list (Custom formats lists its formats itself and opens
 * this editor through ?edit=; a stored definition its merged list does not show keeps its row here). match: only
 * records it accepts are listed (Custom formats' filter). */
export function QualityPanel({ entity, compact = false, skip = [], match }: { entity: QualityEntity; compact?: boolean; skip?: readonly string[]; match?: (r: QualityRecord) => boolean }) {
  const overview = useSettings();
  const qc = useQueryClient();
  const records = useQuery({ queryKey: [...qk.settings.all, "quality", entity], queryFn: () => api<{ generation: number; records: QualityRecord[]; defaults?: Record<string, Record<string, string>> }>(`/settings/${entity}`), enabled: overview.data?.source === "db" });
  const schema = useQuery({ queryKey: [...qk.settings.all, "format-schema"], queryFn: () => api<Implementation[]>("/settings/formats/schema") });
  const [session, setSession] = useState<Session>();
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [formRevision, setFormRevision] = useState(0);
  const [preview, setPreview] = useState<{ file: string; folder: string }>();
  const [announcement, announce] = useAnnouncement();
  const confirm = useWriteConfirmation();
  const heading = useRef<HTMLHeadingElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const deleted = useRef(false);
  const errorBox = useRef<HTMLDivElement>(null);
  const formId = useId();
  useEffect(() => { if (error) errorBox.current?.querySelector<HTMLElement>("[role=alert]")?.focus(); }, [error]);
  // Each request captures the draft and its original generation. An edit elsewhere never rebases this session.
  useEffect(() => {
    if (!session || session.entity !== "naming" || !session.draft.key) { setPreview(undefined); return; }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      void api<{ file: string; folder: string }>("/settings/naming/preview", { method: "POST", signal: controller.signal, body: { generation: session.generation, version: session.draft.key, naming: session.draft.config } }).then(setPreview).catch((e: unknown) => { if (!controller.signal.aborted) setError(e); });
    }, 400);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [session]);
  const start = (record?: QualityRecord) => {
    deleted.current = false;
    setError(undefined); setConfirmDelete(false); setFormRevision((n) => n + 1);
    setSession({ entity, id: record?.id, generation: records.data?.generation ?? overview.data?.generation ?? 0, draft: record ? draftRecord(record) : { id: 0, key: "", config: defaults(entity), ...(entity === "formats" ? { specification: blankFormat() } : {}) } });
  };
  const setEdit = useEditParam(!!records.data, !!session, (key) => {
    const record = records.data?.records.find((r) => r.key === key);
    if (key === "new" || record) start(record);
    else setEdit(null);
  });
  if (overview.isPending) return <Loading />;
  if (overview.error) return <ErrorState error={overview.error} />;
  const o = overview.data;
  const readOnly = o.source !== "db" || !o.writable;
  const close = () => { setSession(undefined); setError(undefined); setConfirmDelete(false); setEdit(null); };
  const refresh = async () => { await qc.invalidateQueries({ queryKey: qk.settings.all }); };
  const open = (record?: QualityRecord) => { start(record); setEdit(record ? record.key : "new"); };
  const rebase = async (keep: boolean) => {
    const current = await records.refetch(); await overview.refetch();
    if (!session || !current.data) return;
    const saved = current.data.records.find((r) => r.id === session.id);
    if (session.id && !saved) { setError(new Error("This record was deleted. Close this editor and create a new record.")); return; }
    setSession({ ...session, generation: current.data.generation, draft: keep ? session.draft : draftRecord(saved ?? { id: 0, key: "", config: defaults(entity), ...(entity === "formats" ? { specification: blankFormat() } : {}) }) }); setError(undefined);
    if (!keep) setFormRevision((n) => n + 1);
  };
  const write = async (remove = false, token?: string) => {
    if (!session || session.entity !== entity) return;
    setBusy(true); setError(undefined);
    try {
      await api(`/settings/${entity}${session.id ? `/${session.id}` : ""}`, { method: remove ? "DELETE" : session.id ? "PUT" : "POST", headers: confirmationHeaders(token), body: { generation: session.generation, key: session.draft.key, ...(!remove ? { config: session.draft.config, specification: session.draft.specification, specifications: session.draft.specifications } : {}) } });
      await refresh(); announce(`${session.draft.key} ${remove ? "deleted" : "saved"}.`); deleted.current = remove; close();
    } catch (e) {
      // A profile that widens an active version is confirmed in a dialog, then saved again with the token.
      if (confirm.ask(e, (next) => { void write(remove, next); })) announce("This change needs confirmation.");
      // A refused delete closes its dialog: the sheet explains it (and lists the uses).
      else { setConfirmDelete(false); setError(e); announce("Settings were not saved."); }
    } finally { setBusy(false); }
  };
  const patch = (patch: Partial<QualityRecord>) => { if (session) { setSession({ ...session, draft: { ...session.draft, ...patch } }); setPreview(undefined); } };
  const c = session?.draft.config ?? {};
  const set = (key: string, value: unknown) => patch({ config: { ...c, [key]: value } });
  return <div ref={panel} tabIndex={-1} className="space-y-4 outline-none">
    <SettingsAuthority compact={compact} overview={o} />{!compact ? <ManagedInSettings /> : null}
    {confirm.dialog}
    <Announcer announcement={announcement} testid="quality-announcer" />
    {records.error ? <ErrorState error={records.error} /> : null}
    <Button ref={addButton} disabled={readOnly} onClick={() => open()} data-testid="add">Add {entity === "naming" ? "library naming" : entity.replaceAll("_", " ")}</Button>
    {(records.data?.records ?? []).filter((r) => !skip.includes(r.key) && (!match || match(r))).map((r) => <Card key={r.id} data-record={r.key} data-selected={session?.id === r.id ? "true" : undefined} className="flex flex-wrap items-center justify-between gap-2 p-3"><span className="min-w-0 font-mono text-[13.5px] [overflow-wrap:anywhere]">{r.key}</span><div className="flex flex-wrap items-center gap-2">
      {entity === "formats" ? <a className="text-sm text-[var(--accent-ink)] underline" href={`${API_BASE}/settings/formats/${r.id}/export`} download={`${r.key}.json`}>Export arr JSON</a> : null}
      <Button variant="outline" size="sm" disabled={readOnly} onClick={() => open(r)} data-testid="edit">Edit {r.key}</Button>
    </div></Card>)}
    {session ? <Sheet label={`${session.id ? "Edit" : "Add"} ${NOUN[entity]}${session.id ? ` ${session.draft.key}` : ""}`} onClose={close} initialFocus={heading} returnFocus={() => (deleted.current ? addButton.current ?? panel.current : undefined)}>
      <SheetLayout testid="quality-form" headingRef={heading} onClose={close}
        title={`${session.id ? "Edit" : "Add"} ${entity.replaceAll("_", " ")}`}
        subtitle={session.id ? session.draft.key : undefined}
        footer={<><Button type="submit" form={formId} disabled={busy || readOnly}>{busy ? "Saving…" : "Save"}</Button><Button type="button" variant="outline" onClick={close}>Cancel</Button>{session.id ? <Button type="button" variant="destructive" className="ml-auto" disabled={busy || readOnly} onClick={() => setConfirmDelete(true)}>Delete</Button> : null}</>}>
      <div ref={errorBox}>{error ? <div className="mb-4"><ExplainBox error={explainError(error)} onReload={() => { void rebase(false); }} onReloadKeep={() => { void rebase(true); }} />{error instanceof ApiError && object(error.body).uses ? <pre className="whitespace-pre-wrap text-sm">{pretty(object(error.body).uses)}</pre> : null}</div> : null}</div>
      <form id={formId} key={formRevision} className="space-y-4" onSubmit={(e) => { e.preventDefault(); void write(); }}>
        <fieldset disabled={busy || readOnly} className="space-y-4">
          <label className="flex flex-col gap-1 text-sm">{entity === "naming" ? "Library key" : "Key"}<Input className="h-9" required disabled={!!session.id} value={session.draft.key} onChange={(e) => patch({ key: e.target.value })} /></label>
          {entity === "profiles" ? <ProfileFields config={c} onChange={(config) => patch({ config })} /> : null}
          {entity === "formats" ? <><Select label="Flavour" value={text(c.flavour)} values={["", "radarr", "sonarr"]} onChange={(v) => set("flavour", v)} />{schema.isPending ? <Loading /> : schema.error ? <ErrorState error={schema.error} /> : <FormatFields spec={session.draft.specification ?? blankFormat()} schema={schema.data} flavour={text(c.flavour)} onChange={(specification) => patch({ specification, config: { ...c, trash_id: specification.trash_id ?? "", include_when_renaming: specification.includeCustomFormatWhenRenaming } })} />}</> : null}
          {entity === "format_sets" ? <><Select label="Flavour" value={text(c.flavour)} values={["radarr", "sonarr"]} onChange={(v) => set("flavour", v)} /><JSONField label={'Formats by display name: {"WEB Tier 01": {"ref": "global-key"}}; inline entries use trash_id and a specification below'} value={c.formats ?? {}} onChange={(v) => set("formats", v)} /><JSONField label="Full inline arr JSON by display name" value={session.draft.specifications ?? {}} onChange={(v) => patch({ specifications: v as Record<string, FormatJSON> })} /></> : null}
          {entity === "naming" ? <>{text(c.from_version) ? <CopyNaming from={text(c.from_version)} source={records.data?.records.find((r) => r.key === text(c.from_version))} defaults={records.data?.defaults?.[text(c.from_version)] ?? {}} onCopy={(config) => { patch({ config: { ...c, ...config } }); announce(`Copied ${text(c.from_version)}'s naming. Save to keep it.`); }} /> : null}<FormGrid>{Object.entries(NAMING_LABELS).map(([key, label]) => <TextField key={key} label={label} value={c[key]} onChange={(v) => set(key, v)} />)}<Select label="Colon replacement" value={text(c.colon)} values={["", "delete", "dash", "space_dash", "space_dash_space", "smart"]} onChange={(v) => set("colon", v)} /><Select label="Multi episode style" value={text(c.multi_episode)} values={["", "extend", "duplicate", "repeat", "scene", "range", "prefixed_range"]} onChange={(v) => set("multi_episode", v)} /></FormGrid><p className="text-sm">Changes name new imports. Use the explicit rename preview to rename existing links.</p><div role="status" aria-live="polite">{preview ? <><p>Folder: {preview.folder}</p><p>File: {preview.file}</p></> : "Naming preview pending"}</div></> : null}
          {entity === "sync_sources" ? <><FormGrid><Select label="Source" value={text(c.kind)} values={["trash", "recyclarr"]} onChange={(v) => set("kind", v)} /><Select label="Flavour" value={text(c.flavour)} values={["radarr", "sonarr"]} onChange={(v) => set("flavour", v)} /><Select label="TRaSH origin (Default: local TRaSH or a pinned archive; github: track a branch)" value={text(c.origin)} values={["", "github"]} onChange={(v) => patch({ config: trackOrigin(c, v) })} /><TextField label={c.origin === "github" ? "Schedule (default 168h, weekly)" : "Schedule (default 24h)"} value={c.schedule} onChange={(v) => set("schedule", v)} />{c.origin === "github" ? <><TextField label="Tracked repository (default TRaSH-Guides/Guides)" value={c.repo} onChange={(v) => set("repo", v)} /><TextField label="Tracked branch (default master)" value={c.branch} onChange={(v) => set("branch", v)} /></> : <><TextField label="Pinned git archive URL (empty uses local TRaSH)" value={c.archive_url} onChange={(v) => set("archive_url", v)} /><TextField label="Archive commit (full 40 hex digits)" value={c.commit} onChange={(v) => set("commit", v)} /></>}<Toggle label="Scheduled sync enabled" checked={c.enabled === true} onChange={(v) => set("enabled", v)} /></FormGrid><JSONField label="TRaSH IDs to add" value={c.trash_ids ?? []} onChange={(v) => set("trash_ids", v)} /><JSONField label="Local profile key to TRaSH score set" value={c.profiles ?? {}} onChange={(v) => set("profiles", v)} />{c.kind === "recyclarr" ? <Toggle label="Apply quality sizes from quality_definition (TRaSH sizes; off lists it as skipped)" checked={c.quality_sizes !== false} onChange={(v) => set("quality_sizes", v)} /> : null}{c.kind === "recyclarr" ? <label className="flex flex-col gap-1">Recyclarr YAML<textarea className="min-h-48 rounded border p-2 font-mono text-xs" value={text(c.recyclarr)} onChange={(e) => set("recyclarr", e.target.value)} /></label> : null}</> : null}
        </fieldset>
      </form>
      </SheetLayout>
      {confirmDelete ? <ConfirmDialog testid="delete-confirm" subject={`${NOUN[entity]} ${session.draft.key}`} scope={{ title: `Delete ${NOUN[entity]} ${session.draft.key}?`, does: [`Delete the ${NOUN[entity]} ${session.draft.key}.`], keeps: ["Records that use it must drop it first, or the delete is refused and the sheet lists them."], confirm: "Confirm delete" }} busy={busy} onClose={() => setConfirmDelete(false)} onConfirm={() => { void write(true); }} /> : null}
    </Sheet> : error ? <ExplainBox error={explainError(error)} onReload={() => { void refresh(); }} /> : null}
    {entity === "sync_sources" && !readOnly ? <SyncSources records={records.data?.records ?? []} generation={records.data?.generation ?? o.generation} draft={session?.draft} baseGeneration={session?.generation} onChanged={refresh} /> : null}
    <a className="block w-fit text-sm underline" href={`${API_BASE}/settings/export/files`} download="policy-files.json">Export policy YAML and custom format files</a>
  </div>;
}
