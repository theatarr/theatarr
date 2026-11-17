import { keyProblem } from "@/lib/field-checks";
import { useQueryClient } from "@tanstack/react-query";
import { setupKey } from "@/api/setup";
import { qk } from "@/api/query-keys";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { bringInSync, checkRoot, confirmVersion, saveVersion, useBringInSyncPreview, useVersions, type Confirmation, type VersionBody, type VersionConfig, type VersionList, type VersionRecord } from "@/api/settings-versions";
import { CATEGORY_LABEL, PRESET_PICK, categoryOf, ensurePresetProfile, originLabel, overviewKey, presetCopies, presetPick, usePresets, useQualityOverview, type Category, type Preset, type ProfileSummary } from "@/api/settings-profiles";
import { DownloadsOffNotice, pausedWords, VersionStatus } from "@/components/version-status";
import { PageHeader } from "@/components/page";
import { EffectsConfirmDialog, useWriteConfirmation } from "@/components/settings/confirm-write";
import { Announcer, ChangedMark, ExplainBox, SELECT_CLASS, useAnnouncement } from "@/components/settings/parts";
import { changedPaths } from "@/components/settings/section-draft";
import { changeCount } from "@/components/settings/unsaved-bar";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Sheet, SheetCancel, SheetLayout, useEditParam } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { explainError } from "@/lib/settings";
import { sectionsQuery } from "@/api/settings";
import { libraryLabel } from "@/lib/library-label";
import { useLibraryName } from "@/lib/library-names";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { toast } from "@/components/toaster";
import { readableCode } from "@/lib/code-words";

/** A library being edited: its record, and sync, the other libraries kept in sync with it (sorted). */
type Draft = VersionRecord & { sync?: string[] };
/** saved: the record as stored (a new library: its first draft), for the changed marks, the count and Discard. */
type Session = { record: Draft; saved: Draft; generation: number; create: boolean; draftID: number };
/** create: the body adds a library; inSheet: it came from the edit sheet. */
type Pending = { body: VersionBody; effects: Confirmation["effects"]; proof?: Confirmation; create: boolean; inSheet: boolean };
/** The arr flavour whose TRaSH defaults fit a library kind. */
const KIND_WORD: Record<string, string> = { movie: "Movies", series: "Series" };
const flavourOf = (kind: string) => kind === "series" ? "sonarr" : "radarr";
const split = (text: string) => text.split(",").map((part) => part.trim()).filter(Boolean);
const copy = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const message = (error: unknown) => error instanceof Error ? error.message : String(error);
const confirmationEffects = (error: unknown): Confirmation["effects"] | undefined => {
  if (!(error instanceof ApiError) || error.status !== 422 || !error.body || typeof error.body !== "object") return;
  const effects = (error.body as { effects?: unknown }).effects;
  return Array.isArray(effects) && effects.length ? effects as NonNullable<Confirmation["effects"]> : undefined;
};

/** A refusal's message for the first field path that matches (the 422 body's fields), for showing beside its input. */
export function fieldMessage(error: unknown, match: (path: string) => boolean): string | undefined {
  if (!(error instanceof ApiError) || !error.body || typeof error.body !== "object") return;
  const fields = (error.body as { fields?: { path?: string; message?: string }[] }).fields;
  return Array.isArray(fields) ? fields.find((f) => f.path && match(f.path))?.message : undefined;
}

/** help sits outside the label, so the control's accessible name stays the short label. */
function Field({ label, children, changed, onUndo, name, error, help }: { label: string; children: ReactNode; changed?: boolean; onUndo?: () => void; name?: string; error?: string; help?: string }) {
  return <div className="flex flex-col gap-1 text-sm" data-changed={changed || undefined}>
    <label className="flex flex-col gap-1"><span>{label}</span>{children}</label>
    {help ? <p className="text-xs text-muted-foreground">{help}</p> : null}
    {error ? <p role="alert" className="text-xs text-destructive">{error}</p> : null}
    {changed ? <ChangedMark name={name ?? label} onUndo={onUndo} /> : null}
  </div>;
}

/** The library draft's edits against the stored record (key, settings and write roots). */
/** A new library's key, checked as the server checks it (an empty one too): Save waits for it; the field says why once
 * the key has been typed in. */
const newKeyProblem = (session: Session) => session.create ? keyProblem(session.record.key) : undefined;

export function libraryChanges(record: Draft, saved: Draft): number {
  const view = (r: Draft) => ({ key: r.key, config: r.config, roots: r.roots ?? [], sync: (r.sync ?? []).join(",") });
  return changedPaths(view(record), view(saved)).length;
}

/** The other libraries in key's sync group, sorted; none when it is in no group. */
export function syncPartners(groups: string[][] | undefined, key: string): string[] {
  return (groups ?? []).find((g) => g.includes(key))?.filter((k) => k !== key).sort() ?? [];
}

const ANIME_RULE = (anime: boolean | null | undefined) => anime === true ? "anime only" : anime === false ? "not anime" : "anime and not";

/** Whether other can be kept in sync with a library of config: the same kind and the same anime rule. */
const sameRule = (a: VersionConfig, b: VersionConfig) => a.kind === b.kind && (a.anime ?? null) === (b.anime ?? null);

/** with a draft: its record with the stored sync group. */
const withSync = (record: VersionRecord, data: VersionList): Draft => ({ ...record, sync: syncPartners(data.sync_groups, record.key) });

function Pick({ value, options, onChange, disabled, labels }: { value: string; options: string[]; onChange: (value: string) => void; disabled?: boolean; labels?: Record<string, string> }) {
  return <select className={SELECT_CLASS} value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled}>{options.map((option) => <option key={option} value={option}>{labels?.[option] ?? (option || "None")}</option>)}</select>;
}

/** A picker's supported options, plus a not-yet-supported value only when the saved record or the draft already uses it,
 * so an existing config still shows and saves as it is (seedbox storage, hardlink and copy imports: docs/not-supported.md). */
function withSaved(supported: readonly string[], ...used: (string | undefined)[]): { options: string[]; labels: Record<string, string> } {
  const options = [...supported];
  const labels: Record<string, string> = {};
  for (const value of used) {
    if (value && !options.includes(value)) {
      options.push(value);
      labels[value] = `${value} (not supported yet)`;
    }
  }
  return { options, labels };
}

function Multi({ label, choices, values, onChange }: { label: string; choices: string[]; values: string[]; onChange: (values: string[]) => void }) {
  return <fieldset className="space-y-1"><legend className="text-sm">{label}</legend><div className="flex flex-wrap gap-3">{choices.map((key) => <label key={key} className="flex items-center gap-1 text-sm"><input type="checkbox" checked={values.includes(key)} onChange={(event) => onChange(choices.filter((choice) => choice === key ? event.target.checked : values.includes(choice)))} />{key}</label>)}</div></fieldset>;
}

// A new version starts with the default profile for movies (Quality profiles, "Defaults for new
// versions"), else the first profile.
function blank(data: VersionList, defaults: Record<string, string> = {}): VersionRecord {
  const profile = defaults.movie && Object.hasOwn(data.profiles, defaults.movie) ? defaults.movie : Object.keys(data.profiles)[0] ?? "";
  return { id: 0, key: "", created_at: "", updated_at: "", roots: [], config: { kind: "movie", anime: false, profile, library: "", storage: "debrid", indexers: [], providers: [], client: data.clients[0] ?? "", import: "symlink", upgrades: "auto" } };
}

const GROUPS: Category[] = ["movie", "series", "anime_movie", "anime_series"];

/** "from TRaSH WEB 2160p + 1080p fallbacks", "our own": where a profile came from. */
function profileOrigin(p: ProfileSummary | undefined): string {
  if (!p) return "";
  if (p.origin) return `from TRaSH ${originLabel(p.origin)}${p.guessed ? " (closest preset)" : ""}`;
  return "our own profile";
}

function VersionGroup({ title, versions, all, groups, switches, selected, readOnly, onOpen }: { title: string; versions: VersionRecord[]; all: VersionRecord[]; groups: string[][]; switches: VersionList["switches"]; selected?: string; readOnly: boolean; onOpen: (record: VersionRecord) => void }) {
  const nameOf = useLibraryName();
  const label = (key: string) => { const c = all.find((r) => r.key === key)?.config; return nameOf(key, libraryLabel(key, c?.profile ?? "", c?.name, c?.kind)); };
  return <section className="space-y-2" aria-label={title}>
    <h2 className="text-base font-semibold">{title}</h2>
    <Card className="p-0">
      {/* Fixed column widths, so the tables of the kinds line up under each other; below md each library stacks with
          its values labelled and no header. */}
      <Table className="md:table-fixed" data-testid="library-table">
        <TableHeader className="max-md:hidden"><TableRow>
          <TableHead className="md:w-[34%] lg:w-[28%]">Library</TableHead>
          <TableHead className="hidden md:table-cell md:w-[30%] lg:w-[25%]">Where</TableHead>
          <TableHead className="hidden md:table-cell md:w-[22%] lg:w-[18%]">Profile</TableHead>
          <TableHead className="hidden lg:table-cell lg:w-[20%]">Sources</TableHead>
          <TableHead className="md:w-[14%] lg:w-[9%]"><span className="sr-only">Actions</span></TableHead>
        </TableRow></TableHeader>
        <TableBody>{versions.map((record) => {
          const c = record.config;
          const where = <><div className="font-mono text-[12.5px] [overflow-wrap:anywhere]">{c.library || "no root"}</div><div className="text-xs text-muted-foreground" data-testid="library-where">{[c.storage, c.add_via === "client" ? c.client : "", c.import].filter(Boolean).join(" · ")}</div></>;
          const profile = <><div className="font-mono text-[12.5px] [overflow-wrap:anywhere]">{c.profile}</div></>;
          const sources = <div className="text-xs [overflow-wrap:anywhere]"><div>{(c.indexers ?? []).length ? (c.indexers ?? []).join(", ") : "no indexers"}</div><div className="text-muted-foreground">{c.add_via === "client" ? `via client ${c.client || "(none)"}` : (c.providers ?? []).length ? (c.providers ?? []).join(" › ") : "no providers"}</div></div>;
          return <TableRow key={record.key} data-record={record.key} data-selected={selected === record.key ? "true" : undefined}>
            <TableCell className="align-top">
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5"><span className="text-[13.5px] font-medium [overflow-wrap:anywhere]" data-testid="library-name">{nameOf(record.key, libraryLabel(record.key, c.profile, c.name, c.kind))}</span><span className="font-mono text-[12px] text-muted-foreground [overflow-wrap:anywhere]" data-testid="library-key">{record.key}</span></div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5" data-testid="library-state-row"><VersionStatus words settingsKey={record.key} canManage state={switches.versions.find((v) => v.key === record.key)?.grab_state} /></div>
              <div className="text-xs text-muted-foreground">{c.anime === null ? "anime and regular" : ""}</div>
              {syncPartners(groups, record.key).length ? <div className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="library-sync">In sync with {syncPartners(groups, record.key).map(label).join(", ")}</div> : null}
              <div className="mt-1.5 space-y-1.5 md:hidden"><div><div className="text-[11px] text-muted-foreground">Where</div>{where}</div><div><div className="text-[11px] text-muted-foreground">Profile</div>{profile}</div></div>
              <div className="mt-1.5 lg:hidden"><div className="text-[11px] text-muted-foreground">Sources</div>{sources}</div>
            </TableCell>
            <TableCell className="hidden align-top md:table-cell">{where}</TableCell>
            <TableCell className="hidden align-top md:table-cell">{profile}</TableCell>
            <TableCell className="hidden align-top lg:table-cell">{sources}</TableCell>
            <TableCell className="align-top text-right"><Button variant="outline" size="sm" onClick={() => onOpen(record)} data-testid="edit">{readOnly ? "View" : "Edit"}</Button></TableCell>
          </TableRow>;
        })}</TableBody>
      </Table>
    </Card>
  </section>;
}

export function VersionsPage({ embedded = false }: { embedded?: boolean }) {
  const qc = useQueryClient();
  const nameOf = useLibraryName();
  const query = useVersions();
  // Profile origins and the defaults for new versions; the page works without them.
  const quality = useQualityOverview();
  const defaults = quality.data?.defaults ?? {};
  const profiles = new Map((quality.data?.profiles ?? []).map((p) => [p.key, p]));
  const [session, setSession] = useState<Session>();
  const [error, setError] = useState<unknown>();
  const [pending, setPending] = useState<Pending>();
  const profileConfirm = useWriteConfirmation();
  const [busy, setBusy] = useState(false);
  // Discard remounts the form (its root text) without moving focus off the footer.
  const [discards, setDiscards] = useState(0);
  const [notice, setNotice] = useAnnouncement();
  const heading = useRef<HTMLHeadingElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const errorBox = useRef<HTMLDivElement>(null);
  const stopped = useRef(false);
  // The profile this editor created from a picked TRaSH default, said beside the form until the library is saved.
  const [made, setMade] = useState<{ key: string; name: string }>();
  const data = query.data;
  const readOnly = data?.source !== "db";
  // Focus moves to the heading when a session reloads (draftID); the sheet focuses it on open.
  useEffect(() => { if (session) heading.current?.focus(); }, [session?.draftID]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (error) errorBox.current?.focus(); }, [error]);

  function start(record: VersionRecord | undefined) {
    if (!data) return;
    stopped.current = false;
    const first: Draft = record ? withSync(record, data) : { ...blank(data, defaults), sync: [] };
    setSession({ record: copy(first), saved: copy(first), generation: data.generation, create: !record, draftID: Date.now() });
    setError(undefined);
    setPending(undefined);
    setMade(undefined);
  }
  const setEdit = useEditParam(!!data, !!session, (key) => {
    const record = data?.versions.find((version) => version.key === key);
    if (key === "new" || record) start(record);
    else setEdit(null);
  });
  function open(record: VersionRecord | undefined) {
    start(record);
    setEdit(record ? record.key : "new");
  }

  function close() {
    setSession(undefined);
    setPending(undefined);
    setError(undefined);
    setMade(undefined);
    setEdit(null);
  }

  async function reload(keep: boolean) {
    const refreshed = await query.refetch();
    if (!refreshed.data || !session) return;
    const record = refreshed.data.versions.find((version) => version.key === session.record.key);
    if (keep) setSession({ ...session, saved: record && !session.create ? copy(withSync(record, refreshed.data)) : session.saved, generation: refreshed.data.generation });
    else {
      if (!record && !session.create) { close(); return; }
      const first = copy<Draft>(record ? withSync(record, refreshed.data) : { ...blank(refreshed.data, defaults), sync: [] });
      setSession({ record: first, saved: copy(first), generation: refreshed.data.generation, create: session.create, draftID: Date.now() });
    }
    setError(undefined);
    setPending(undefined);
  }

  // inSheet: the save came from the edit sheet, which closes on success.
  async function apply(body: VersionBody, create: boolean, inSheet: boolean) {
    if (inSheet && !session) return;
    setBusy(true);
    try {
      await saveVersion(body, create);
      void qc.invalidateQueries({ queryKey: setupKey });
      // A library change shows on the lists' row actions.
      void qc.invalidateQueries({ queryKey: qk.titles.all });
      // The header Pause saves with the overview's generation, which this save moved on.
      void qc.invalidateQueries({ queryKey: qk.settings.overview });
      // The name as saved (the names from auth/me still hold the old one until they reload).
      const saved = body.delete || !body.config ? nameOf(body.key ?? "") : libraryLabel(body.key ?? "", body.config.profile, body.config.name, body.config.kind);
      setNotice(`${saved} ${body.delete ? "is no longer managed" : "saved"}.`);
      // Library names (auth/me) follow a renamed, added or removed library.
      void qc.invalidateQueries({ queryKey: qk.me });
      // A stopped version's row goes away: focus goes to Add library instead of its Edit button.
      stopped.current = !!body.delete;
      if (inSheet) close();
      else { setPending(undefined); setError(undefined); }
      await query.refetch();
      // Stopping a library drops it from the arr apps that served it (Settings › Connected apps).
      void qc.invalidateQueries({ queryKey: ["settings", "view-bindings"] });
      // Keep in sync, and a deleted library leaving its group, change the library section.
      void qc.invalidateQueries({ queryKey: sectionsQuery.queryKey });
    } catch (failure) {
      const effects = confirmationEffects(failure);
      if (effects) setPending({ body, effects, create, inSheet });
      else { setPending(undefined); setError(failure); }
    }
    finally { setBusy(false); }
  }

  // A picked TRaSH default ("preset:<trash_id>") becomes a stored profile first, named after the library; the draft
  // then names that profile, so a retry or a confirmation saves the library with it and never creates it twice.
  // When creating the profile itself needs a confirmation (after a startup fallback the save also applies stored
  // settings), the standard dialog asks; confirming repeats that same request with its token and then saves the
  // library. undefined says the dialog is open and nothing more happens until then.
  async function withPresetProfile(current: Session, confirmed?: { key: string; token: string }): Promise<Session | undefined> {
    const preset = presetPick(current.record.config.profile);
    if (!preset || !data) return current;
    if (!current.record.key.trim()) throw new Error("Give the library a key first: the profile made from the TRaSH default is named after it.");
    const flavour = flavourOf(current.record.config.kind);
    let tried = confirmed?.key ?? "";
    let profile: { generation: number; key: string };
    try {
      profile = await ensurePresetProfile({ generation: current.generation, base: current.record.key.trim(), flavour, preset, taken: [...Object.keys(data.profiles), ...data.format_sets], confirmed, onAttempt: (key) => { tried = key; } });
    } catch (failure) {
      if (tried && profileConfirm.ask(failure, (token) => void review(false, { key: tried, token }))) return undefined;
      throw failure;
    }
    const next = { ...current, generation: profile.generation, record: { ...current.record, config: { ...current.record.config, profile: profile.key } } };
    setSession(next);
    setMade({ key: profile.key, name: qc.getQueryData<{ presets: Preset[] }>(["settings", "presets", flavour])?.presets.find((p) => p.trash_id === preset)?.name ?? preset });
    // The pickers and the profiles page now list the new profile.
    void qc.invalidateQueries({ queryKey: overviewKey });
    await query.refetch();
    return next;
  }

  async function review(deleting = false, confirmed?: { key: string; token: string }) {
    if (!session) return;
    setBusy(true);
    setError(undefined);
    try {
      const current = deleting ? session : await withPresetProfile(session, confirmed);
      if (!current) return;
      // Keep in sync goes along only when it changed: a save of what was loaded sends the library alone.
      const sync = current.record.sync ?? [];
      const syncChanged = sync.join(",") !== (current.saved.sync ?? []).join(",");
      const body: VersionBody = { generation: current.generation, key: current.record.key, ...(deleting ? { delete: true } : { config: current.record.config, roots: current.record.roots ?? [], ...(syncChanged ? { sync_with: sync } : {}) }) };
      if (!deleting) {
        await apply(body, current.create, true);
        return;
      }
      const proof = await confirmVersion(body);
      setPending({ body, effects: proof.effects ?? [], proof, create: false, inSheet: true });
    } catch (failure) { setError(failure); }
    finally { setBusy(false); }
  }

  async function approvePending() {
    if (!pending) return;
    const current = pending;
    setBusy(true);
    try {
      const proof = current.proof ?? await confirmVersion(current.body);
      setPending(undefined);
      await apply({ ...current.body, confirmation_token: proof.token }, current.create, current.inSheet);
    } catch (failure) { setPending(undefined); setError(failure); }
    finally { setBusy(false); }
  }

  return <div className="space-y-4">
    {!embedded ? <PageHeader title="Libraries" crumb={[{ label: "Admin", to: "/admin" }, { label: "Settings", to: "/admin/settings" }]} description="Manage your libraries and the libraries exposed to apps." /> : null}
    <Announcer announcement={notice} testid="version-announce" />
    {query.isPending ? <p>Loading libraries…</p> : query.error ? <p role="alert">{message(query.error)}</p> : null}
    {data ? <>
      {readOnly ? <p>Read-only: these settings cannot be edited here. Pause {data.switches.paused ? "ON" : "OFF"}</p> : null}
      <DownloadsOffNotice off={data.switches.pause_forced ? "locked" : data.switches.paused ? "off" : undefined} canManage />
      {error && !session ? <div ref={errorBox} tabIndex={-1} data-testid="row-error"><ExplainBox error={explainError(error)} onReload={() => { setError(undefined); void query.refetch(); }} onReloadKeep={() => { setError(undefined); void query.refetch(); }} /></div> : null}
      <p className="text-sm">Coming from Sonarr/Radarr? <Link className="underline" to="/admin/arr-import">Import from Sonarr/Radarr</Link></p>
      <Button ref={addButton} onClick={() => open(undefined)} disabled={readOnly}>Add library</Button>
      {GROUPS.map((group) => {
        const list = data.versions.filter((record) => categoryOf(record.config.kind, record.config.anime) === group);
        return list.length ? <VersionGroup key={group} title={CATEGORY_LABEL[group]} versions={list} all={data.versions} groups={data.sync_groups ?? []} switches={data.switches} selected={session && !session.create ? session.record.key : undefined} readOnly={readOnly} onOpen={open} /> : null;
      })}
      {session ? <Sheet label={session.create ? "New library" : `Edit library ${nameOf(session.record.key, libraryLabel(session.record.key, session.saved.config.profile, session.saved.config.name, session.saved.config.kind))}`} onClose={close} initialFocus={heading} returnFocus={() => (stopped.current ? addButton.current : undefined)}>
        <SheetLayout key={session.draftID} testid="version-form" headingRef={heading} onClose={close} changes={readOnly ? 0 : libraryChanges(session.record, session.saved)}
          title={`${session.create ? "New library" : `Edit library ${nameOf(session.record.key, libraryLabel(session.record.key, session.saved.config.profile, session.saved.config.name, session.saved.config.kind))}`}`}
          subtitle={session.create ? "Downloads once saved, while Pause is off." : `${KIND_WORD[session.record.config.kind] ?? session.record.config.kind}${session.record.config.anime ? " · anime" : ""}`}
          footer={<><Button onClick={() => void review()} disabled={readOnly || busy || newKeyProblem(session) !== undefined}>Save</Button><SheetCancel variant="outline" onClick={close}>Cancel</SheetCancel>{libraryChanges(session.record, session.saved) ? <><span className="text-[13px] font-medium text-[var(--ink)]" data-testid="unsaved-count" aria-live="polite">{changeCount(libraryChanges(session.record, session.saved))}</span><Button variant="ghost" size="sm" disabled={busy} data-testid="version-discard" onClick={() => { setSession({ ...session, record: copy(session.saved) }); setError(undefined); setPending(undefined); setDiscards(discards + 1); }}>Discard</Button></> : null}<Link className="text-sm text-[var(--accent-ink)] underline" to="/admin/settings/libraries/naming">Edit naming</Link>{!session.create ? <Button className="ml-auto" variant="destructive" onClick={() => void review(true)} disabled={readOnly || busy}>Stop managing library</Button> : null}</>}>
          {made ? <p className="mb-4 rounded-md border border-[var(--line)] px-3 py-2 text-[13px]" role="status" data-testid="preset-profile-made">The profile <span className="font-mono">{made.key}</span> was created from the TRaSH default {made.name} and is selected. The library itself is saved when Save succeeds; the profile stays either way.</p> : null}
          {error ? <div ref={errorBox} tabIndex={-1} className="mb-4"><ExplainBox error={explainError(error)} onReload={() => void reload(false)} onReloadKeep={() => void reload(true)} /></div> : null}
          <fieldset disabled={readOnly || busy}>
            <VersionForm key={discards} session={session} data={data} defaults={defaults} profiles={profiles} error={error} onChange={(record) => { setSession({ ...session, record }); setPending(undefined); }} />
          </fieldset>
        </SheetLayout>
      </Sheet> : null}
      <p className="text-sm text-muted-foreground">Which arr apps (Seerr, Radarr- and Sonarr-compatible clients) see each library is set under <Link className="underline" to="/admin/settings/apps">Connected apps</Link>.</p>
    </> : null}
    {profileConfirm.dialog}
    {pending ? <EffectsConfirmDialog subject={pending.body.key} library={pending.body.key} effects={pending.effects ?? []} scope={{ title: "Confirm library change", does: [], keeps: ["Pause keeps its current setting; environment force-off overrides stay in force.", "Existing links, mount files and trash entries stay where they are."], confirm: "Confirm this change" }} busy={busy} onClose={() => setPending(undefined)} onConfirm={() => void approvePending()} /> : null}
  </div>;
}

function Part({ title, help, children }: { title: string; help: string; children: ReactNode }) {
  return <section className="space-y-3" aria-label={title}>
    <div><h3 className="font-semibold">{title}</h3><p className="text-xs text-muted-foreground">{help}</p></div>
    <div className="grid gap-4 md:grid-cols-2">{children}</div>
  </section>;
}

function VersionForm({ session, data, defaults, profiles, error, onChange }: { session: Session; data: VersionList; defaults: Record<string, string>; profiles: Map<string, ProfileSummary>; error?: unknown; onChange: (record: Draft) => void }) {
  const { record, generation, saved } = session;
  const config = record.config;
  const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
  // A field's changed mark and its Undo back to the stored value.
  const mark = <K extends keyof VersionConfig,>(key: K) => ({ changed: !same(config[key], saved.config[key]), onUndo: () => update(key, saved.config[key]) });
  // Root also rewrites the write roots, so neither has an Undo (one undo must not wipe the other's edit); their
  // Changed marks stay and Discard resets both.
  const rootsChanged = !same(record.roots ?? [], saved.roots ?? []);
  const [rootStatus, setRootStatus] = useState("");
  const [rootsText, setRootsText] = useState(record.roots?.join(", ") ?? "");
  const update = <K extends keyof VersionConfig,>(key: K, value: VersionConfig[K]) => onChange({ ...record, config: { ...config, [key]: value } });
  // A new version takes the default profile of its kind while nobody picked its profile here: a pick made in the
  // Profile field stays through a change of kind, also when it equals the previous kind's default.
  const pickedByHand = useRef(false);
  // The kind the library had when its current profile was chosen, once the kind moved between movies and series
  // and the profile stayed: the picker then says so, instead of keeping it without a word.
  const [profileFrom, setProfileFrom] = useState<VersionConfig["kind"]>();
  const recategorize = (next: VersionConfig) => {
    // A picked TRaSH default belongs to one flavour: a change of kind drops it (the new kind's default, if any, is taken below).
    if (presetPick(next.profile) && flavourOf(next.kind) !== flavourOf(config.kind)) next = { ...next, profile: "" };
    // Minimum availability and its delay are a movie library's: another kind drops them.
    if (next.kind !== "movie") {
      next = withAvailability(withAvailability(next, "minimum_availability", ""), "availability_delay", 0);
      setDelayText("");
    }
    if (session.create) {
      const was = defaults[categoryOf(config.kind, config.anime)] ?? "";
      const now = defaults[categoryOf(next.kind, next.anime)];
      const fits = now && Object.hasOwn(data.profiles, now) ? now : "";
      if (fits && (!next.profile || (!pickedByHand.current && config.profile === was))) next = { ...next, profile: fits };
      // Movies to series or back, with the profile the form opened with (nobody picked it): it was preselected for
      // the other kind, so it is not kept. The new kind's default takes its place, else the picker asks. A profile
      // picked by hand stays.
      else if (!pickedByHand.current && flavourOf(next.kind) !== flavourOf(config.kind)) next = { ...next, profile: fits };
    }
    if (flavourOf(next.kind) !== flavourOf(config.kind)) setProfileFrom(next.profile && next.profile === config.profile && !profileFrom ? config.kind : undefined);
    // Libraries of another kind or anime setting cannot stay in sync with it.
    const sync = (record.sync ?? []).filter((key) => { const other = data.versions.find((v) => v.key === key); return other && sameRule(other.config, next); });
    onChange({ ...record, config: next, sync });
  };
  useEffect(() => {
    if (data.source !== "db" || !record.key || !config.library) { setRootStatus(""); return; }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setRootStatus("Checking root…");
      void checkRoot(generation, record.key, config.library, controller.signal).then((result) => setRootStatus(`Root resolves to ${result.resolved} · writable`), (failure: unknown) => { if (!controller.signal.aborted) setRootStatus(`root check: ${fieldMessage(failure, (path) => path === "root") ?? message(failure).replace(/^settings refused:\s*(?:root:\s*)?/, "")}`); });
    }, 400);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [generation, record.key, config.library, data.source]);
  const summary = profiles.get(config.profile);
  const viaClient = config.add_via === "client";
  // The name is optional and stored only when set: an empty one is left out of the request unless it clears a stored name.
  const setName = (value: string) => {
    const next = { ...config };
    if (value.trim() || saved.config.name) next.name = value.trim() ? value : "";
    else delete next.name;
    onChange({ ...record, config: next });
  };
  // Minimum availability and its delay are optional and stored only when set: unset, each is left out of the
  // request unless that clears a stored value (sent empty, or 0), so a save that does not touch them stores the
  // library as it was.
  const withAvailability = <K extends "minimum_availability" | "availability_delay",>(from: VersionConfig, key: K, value: NonNullable<VersionConfig[K]>): VersionConfig => {
    const next = { ...from };
    if (value || saved.config[key]) next[key] = value;
    else delete next[key];
    return next;
  };
  const [delayText, setDelayText] = useState(config.availability_delay ? String(config.availability_delay) : "");
  // Whole days, negative for earlier; empty (or a lone minus sign while typing) is no delay.
  const setDelay = (text: string) => {
    if (!/^-?\d*$/.test(text)) return;
    setDelayText(text);
    onChange({ ...record, config: withAvailability(config, "availability_delay", /\d/.test(text) ? Number(text) : 0) });
  };
  const fieldError = (field: string) => fieldMessage(error, (path) => path === `versions[${record.key}].${field}`);
  // Storage and Import mode offer one supported value each; they show only when the library uses another one.
  const storage = withSaved(["debrid"], saved.config.storage, config.storage);
  const importMode = withSaved(["symlink"], saved.config.import, config.import);
  const rootsError = fieldMessage(error, (path) => path.startsWith(`library.versions[${record.key}].roots`));
  const advancedError = rootsError ?? fieldError("storage") ?? fieldError("add_via") ?? fieldError("client") ?? fieldError("import");
  // Advanced starts open when the library uses something in it: write roots other than the root, a download client
  // for its grabs, or a storage or import mode that is not the supported one. A refused field
  // inside keeps it open.
  const [opened, setAdvanced] = useState(() => (!!record.roots?.length && !same(record.roots, [config.library])) || viaClient || storage.options.length > 1 || importMode.options.length > 1);
  const advanced = opened || !!advancedError;
  // The TRaSH defaults of the library's kind (movies: Radarr's, series: Sonarr's), offered beside the stored profiles.
  const flavour = flavourOf(config.kind);
  const presets = usePresets(flavour, data.source === "db").data?.presets ?? [];
  // A stored profile whose format set is of the other kind (a Radarr set for a series library) is left out: saving
  // refuses it. The current profile stays listed; a profile without a format set may suit either kind.
  const stored = Object.keys(data.profiles).filter((key) => { const p = profiles.get(key); return key === config.profile || !p?.format_set || p.flavour === flavour; });
  const picked = presetPick(config.profile);
  const pickedPreset = picked ? presets.find((p) => p.trash_id === picked) : undefined;
  const copiesOf = (id: string) => presetCopies([...profiles.values()], flavour, id);
  const select = (value: string) => { pickedByHand.current = true; setProfileFrom(undefined); update("profile", value); };
  return <div className="space-y-6">
    <Part title="Where" help="What the library holds, where its folder lives and how files get there.">
      <Field label="Name" help="Optional. Shown in place of the guessed label, such as 4K WEB. Connected apps never see it." changed={(config.name ?? "") !== (saved.config.name ?? "")} onUndo={() => setName(saved.config.name ?? "")} error={fieldError("name")}><Input value={config.name ?? ""} maxLength={40} onChange={(event) => setName(event.target.value)} /></Field>
      <Field label="Key" help={`${session.create && !record.key ? "Enter a name. " : ""}Used by connected apps and in settings; fixed once the library exists. Lower case letters, digits, - and _.`} changed={record.key !== saved.key} onUndo={() => onChange({ ...record, key: saved.key })} error={(record.key ? newKeyProblem(session) : undefined) ?? fieldError("key")}><Input value={record.key} disabled={!session.create} onChange={(event) => onChange({ ...record, key: event.target.value })} /></Field>
      <Field label="Kind" {...mark("kind")}><Pick value={config.kind} options={["movie", "series"]} labels={{ movie: "Movies", series: "Series" }} onChange={(value) => recategorize({ ...config, kind: value as VersionConfig["kind"] })} /></Field>
      <Field label="Anime" {...mark("anime")}><Pick value={config.anime === null ? "both" : String(config.anime)} options={["false", "true", "both"]} labels={{ false: "Not anime", true: "Anime only", both: "Any" }} onChange={(value) => recategorize({ ...config, anime: value === "both" ? null : value === "true" })} /></Field>
      <Field label="Root" changed={!same(config.library, saved.config.library)} error={fieldError("library")}><Input value={config.library} onChange={(event) => { const root = event.target.value; const roots = record.roots?.length && record.roots.length > 1 ? record.roots.map((value) => value === config.library ? root : value) : [root]; setRootsText(roots.join(", ")); onChange({ ...record, config: { ...config, library: root }, roots }); }} /><span role="status" className="text-xs [overflow-wrap:anywhere]">{rootStatus}</span></Field>
      <details className="md:col-span-2" open={advanced} data-testid="library-advanced">
        {/* The click is handled here, so the open state has one owner. */}
        <summary className="cursor-pointer text-sm font-medium" onClick={(event) => { event.preventDefault(); setAdvanced(!opened); }}>Advanced</summary>
        {advanced ? <div className="mt-3 grid gap-4 md:grid-cols-2">
          <Field label="Write roots" help="Extra folders this library may write to, comma separated. Leave as the root." changed={rootsChanged} error={rootsError}><Input value={rootsText} onChange={(event) => { setRootsText(event.target.value); onChange({ ...record, roots: split(event.target.value) }); }} /></Field>
          {storage.options.length > 1 ? <Field label="Storage" {...mark("storage")} error={fieldError("storage")}><Pick value={config.storage} {...storage} onChange={(value) => update("storage", value as VersionConfig["storage"])} /></Field> : null}
          <Field label="Send grabs to" {...mark("add_via")} error={fieldError("add_via")}><Pick value={viaClient ? "client" : "providers"} options={["providers", "client"]} labels={{ providers: "Debrid providers", client: "Download client" }} onChange={(value) => update("add_via", value as VersionConfig["add_via"])} /></Field>
          <Field label={viaClient ? "Client (a decypharr client: it adds to its own debrid account)" : "Client (only when grabs go to a download client; debrid libraries need none)"} name="Client" {...mark("client")} error={fieldError("client")}><Pick value={config.client} options={["", ...data.clients]} onChange={(value) => update("client", value)} /></Field>
          {importMode.options.length > 1 ? <Field label="Import mode" {...mark("import")} error={fieldError("import")}><Pick value={config.import} {...importMode} onChange={(value) => update("import", value as VersionConfig["import"])} /></Field> : null}
        </div> : null}
      </details>
    </Part>
    <Part title="Which releases" help="The quality profile decides which releases it takes and when it upgrades.">
      <div className="flex flex-col gap-1"><Field label="Profile" {...mark("profile")} error={fieldError("profile")}><select className={SELECT_CLASS} value={config.profile} onChange={(event) => select(event.target.value)}>
          {/* One select element whether or not the TRaSH defaults have loaded, so a load never drops focus. */}
          {presets.length || picked ? <>
            {config.profile === "" || (!picked && !stored.includes(config.profile)) ? <option value={config.profile}>{readableCode(config.profile) || "Choose a profile"}</option> : null}
            {stored.length ? <optgroup label="Your profiles">{stored.map((key) => <option key={key} value={key}>{readableCode(key)}</option>)}</optgroup> : null}
            <optgroup label="TRaSH defaults">
              {presets.map((p) => { const copies = copiesOf(p.trash_id); return <option key={p.trash_id} value={PRESET_PICK + p.trash_id}>{p.name}{copies.length ? ` (copy: ${copies.join(", ")})` : ""}</option>; })}
              {picked && !pickedPreset ? <option value={config.profile}>TRaSH default {picked}</option> : null}
            </optgroup>
          </> : <>{config.profile === "" ? <option value="">Choose a profile</option> : null}{stored.map((key) => <option key={key} value={key}>{readableCode(key) || "None"}</option>)}</>}
        </select></Field>
        {picked
          ? <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="preset-pick-note">TRaSH default{pickedPreset ? ` ${pickedPreset.name}` : ""}: saving first creates a profile of your own from it, named after this library{record.key.trim() ? <> (<span className="font-mono">{record.key.trim()}</span>, or the next free key)</> : null}, with its format set. You can edit it afterwards under <Link className="underline" to="/admin/settings/profiles">Profiles</Link>.{copiesOf(picked).length ? ` To reuse the copy you already have, pick ${copiesOf(picked).join(" or ")} under Your profiles.` : ""}</span>
          : profileFrom && config.profile ? <span className="text-xs text-[var(--warn-ink)] [overflow-wrap:anywhere]" data-testid="profile-other-kind">This profile was chosen while the library was a {profileFrom} library. Check that it suits a {config.kind} library, or pick another; picking it again here confirms it.</span>
          : config.profile === "" ? <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="profile-needed">Choose a profile for this {config.kind} library: one of yours, or a TRaSH default for {config.kind === "series" ? "series" : "movies"}. The profile preselected for the other kind is not kept when the kind changes.</span>
          : <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]">{summary ? `${profileOrigin(summary)} · ` : ""}its format set comes with it{data.profiles[config.profile] ? `: ${data.profiles[config.profile]}` : ""}. {config.profile ? <Link className="underline" to={`/admin/settings/profiles?edit=${encodeURIComponent(config.profile)}`}>Open the profile</Link> : null}</span>}
      </div>
      <Field label="Upgrades" help="When a better release turns up for a file this library already has. Automatic replaces it, within the quality profile's upgrade setting and cutoff. Never keeps the file." {...mark("upgrades")}><Pick value={config.upgrades} options={["auto", "off"]} labels={{ auto: "Automatic", off: "Never" }} onChange={(value) => update("upgrades", value as VersionConfig["upgrades"])} /></Field>
      <Field label="Prefer SeaDex" help="SeaDex picks take priority among allowed qualities and can replace higher-resolution files. Actual quality remains shown; size limits, upgrades and the other checks still apply." {...mark("prefer_seadex")}><Switch className="self-start" checked={config.prefer_seadex ?? false} onCheckedChange={(on) => update("prefer_seadex", on)} /></Field>
      {config.kind === "movie" ? <>
        <Field label="Minimum availability" help="When a movie of this library counts as out and is searched, unless the movie has its own choice. Not set means Released." changed={(config.minimum_availability ?? "") !== (saved.config.minimum_availability ?? "")} onUndo={() => onChange({ ...record, config: withAvailability(config, "minimum_availability", saved.config.minimum_availability ?? "") })} error={fieldError("minimum_availability")}><Pick value={config.minimum_availability ?? ""} options={["", "announced", "inCinemas", "released"]} labels={{ "": "Not set", announced: "Announced", inCinemas: "In cinemas", released: "Released" }} onChange={(value) => onChange({ ...record, config: withAvailability(config, "minimum_availability", value as NonNullable<VersionConfig["minimum_availability"]>) })} /></Field>
        <Field label="Availability delay (days)" help="Days to wait after that date before searching; negative starts earlier. Empty means none." changed={(config.availability_delay ?? 0) !== (saved.config.availability_delay ?? 0)} onUndo={() => setDelay(saved.config.availability_delay ? String(saved.config.availability_delay) : "")} error={fieldError("availability_delay")}><Input value={delayText} onChange={(event) => setDelay(event.target.value)} /></Field>
      </> : null}
    </Part>
    <SyncPart record={record} saved={saved} data={data} onChange={onChange} />
    <Part title="Sources" help="Where it searches and which debrid providers it adds through, in fallback order.">
      <div className="space-y-1"><Multi label="Indexers" choices={data.indexers} values={config.indexers ?? []} onChange={(values) => update("indexers", values)} />{mark("indexers").changed ? <ChangedMark name="Indexers" onUndo={mark("indexers").onUndo} /> : null}</div>
      {viaClient ? <p className="text-sm text-muted-foreground">Grabs go to download client {config.client || "(none picked)"}, which adds them to its own debrid account and decides cached-only, pacing and stalled downloads itself; the providers below are not used.</p> : null}
      <div className="space-y-1"><Multi label="Providers (fallback order)" choices={data.providers} values={config.providers ?? []} onChange={(values) => update("providers", values)} />{mark("providers").changed ? <ChangedMark name="Providers" onUndo={mark("providers").onUndo} /> : null}</div>
      {viaClient ? null : <div className="flex flex-col gap-1"><Field label="Provider preference" {...mark("provider_preference")}><Pick value={config.provider_preference || "ordered"} options={["ordered", "none"]} labels={{ ordered: "Fallback order", none: "No preference" }} onChange={(value) => update("provider_preference", value as VersionConfig["provider_preference"])} /></Field><span className="text-xs text-muted-foreground">Among releases of equal quality and format score, the one cached on more providers wins. Fallback order first prefers the release cached on an earlier provider; No preference only counts providers. Providers are tried in fallback order either way.</span></div>}
    </Part>
  </div>;
}

/** Keep in sync: the libraries of the same kind and anime setting this one is kept in sync with. One in another
 * group, or of another anime setting, is not offered, and the list says why; libraries of the other kind are left
 * out. The group is saved with the library. */
function SyncPart({ record, saved, data, onChange }: { record: Draft; saved: Draft; data: VersionList; onChange: (record: Draft) => void }) {
  const nameOf = useLibraryName();
  const config = record.config;
  const label = (r: VersionRecord) => nameOf(r.key, libraryLabel(r.key, r.config.profile, r.config.name, r.config.kind));
  const others = data.versions.filter((r) => r.key !== record.key && r.config.kind === config.kind);
  const groups = data.sync_groups ?? [];
  // The libraries in another group than this library's stored one.
  const elsewhere = (key: string) => { const g = groups.find((group) => group.includes(key)); return g && !g.includes(record.key) ? g : undefined; };
  const offered = others.filter((r) => sameRule(r.config, config) && !elsewhere(r.key));
  const refused = others.filter((r) => !offered.includes(r)).map((r) => {
    const g = elsewhere(r.key);
    const why = !sameRule(r.config, config) ? ANIME_RULE(r.config.anime) : `in sync with ${(g ?? []).filter((k) => k !== r.key).map((k) => { const o = data.versions.find((v) => v.key === k); return o ? label(o) : k; }).join(", ")}`;
    return `${label(r)} (${why})`;
  });
  const sync = record.sync ?? [];
  const changed = sync.join(",") !== (saved.sync ?? []).join(",");
  const toggle = (key: string, on: boolean) => onChange({ ...record, sync: (on ? [...sync, key] : sync.filter((k) => k !== key)).sort() });
  return <section className="space-y-3" aria-label="Keep in sync" data-testid="library-sync-part">
    <div><h3 className="font-semibold">Keep in sync</h3><p className="text-xs text-muted-foreground">A title added to one of these libraries is added to the others too, unless it was removed from them. Monitoring, removals and files stay each library's own. Only libraries of the same kind and anime setting can be kept in sync.</p></div>
    {offered.length ? <fieldset className="space-y-1"><legend className="sr-only">Keep in sync with</legend><div className="flex flex-wrap gap-3">{offered.map((r) => <label key={r.key} className="flex items-center gap-1 text-sm"><input type="checkbox" checked={sync.includes(r.key)} onChange={(event) => toggle(r.key, event.target.checked)} />{label(r)}</label>)}</div></fieldset>
      : <p className="text-sm text-muted-foreground">No other library can be kept in sync with this one.</p>}
    {refused.length ? <p className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="library-sync-refused">Not offered: {refused.join("; ")}.</p> : null}
    {changed ? <ChangedMark name="Keep in sync" onUndo={() => onChange({ ...record, sync: saved.sync ?? [] })} /> : null}
    {!changed && (saved.sync ?? []).length > 0 ? <BringInSyncLine libraryKey={record.key} paused={data.switches.pause_forced ? "locked" : data.switches.paused ? "off" : undefined} /> : null}
  </section>;
}

/** Bring in sync, for a saved group: "N titles would gain a copy in this group" (hidden at 0) and the dialog that
 * adds those copies in one server action; searching for them is a choice, off by default, through the paced search
 * schedule, and not offered while Pause is on (nothing would be queued). */
function BringInSyncLine({ libraryKey, paused }: { libraryKey: string; paused?: "locked" | "off" }) {
  const preview = useBringInSyncPreview(libraryKey, true);
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const p = preview.data;
  if (!p || p.titles === 0) return null;
  const gains = p.libraries.filter((l) => l.count > 0);
  const titles = (n: number) => `${n} ${n === 1 ? "title" : "titles"}`;
  const run = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const r = await bringInSync(libraryKey, search && !paused);
      toast({
        tone: "ok",
        title: "Brought in sync",
        body: r.libraries.filter((l) => l.count > 0).map((l) => `${titles(l.count)} added to ${l.label}`).join("; ") + ".",
        duration: 10_000,
      });
      setOpen(false);
      await qc.invalidateQueries({ queryKey: ["settings", "versions", "bring-in-sync"] });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return <div className="flex flex-wrap items-center gap-2" data-testid="bring-in-sync">
    <span className="text-sm">{titles(p.titles)} would gain a copy in this group</span>
    <Button type="button" size="xs" variant="outline" onClick={() => { setSearch(false); setError(undefined); setOpen(true); }}>Bring in sync…</Button>
    {open ? <ConfirmDialog danger={false} busy={busy} error={error} onClose={() => setOpen(false)} onConfirm={() => void run()} testid="bring-in-sync-dialog"
      scope={{
        title: "Bring this group in sync",
        does: [...gains.map((l) => `${titles(l.count)} added to ${l.label}: ${l.names.join(", ")}${l.more ? `, and ${l.more} more` : ""}`), p.source],
        keeps: ["Removed copies stay removed, and existing copies are left as they are", ...(search && !paused ? [] : ["No search queued by this action"])],
        confirm: `Add ${titles(p.titles)}`,
      }}>
      <Choice checked={search && !paused} onChange={setSearch} disabled={!!paused} testid="bring-in-sync-search">Search for the new copies (paced, within the hourly search budget)</Choice>
      {paused ? <p className="pl-6 text-xs text-[var(--ink-3)]" data-testid="bring-in-sync-paused">{pausedWords(paused)}</p> : null}
    </ConfirmDialog> : null}
  </div>;
}
