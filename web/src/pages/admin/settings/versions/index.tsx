import { useQueryClient } from "@tanstack/react-query";
import { setupKey } from "@/api/setup";
import { qk } from "@/api/query-keys";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { checkRoot, confirmVersion, saveVersion, useVersions, type Confirmation, type VersionBody, type VersionConfig, type VersionList, type VersionRecord } from "@/api/settings-versions";
import { CATEGORY_LABEL, PRESET_PICK, categoryOf, ensurePresetProfile, originLabel, overviewKey, presetCopies, presetPick, usePresets, useQualityOverview, type Category, type Preset, type ProfileSummary } from "@/api/settings-profiles";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { PageHeader } from "@/components/page";
import { EffectsConfirmDialog, useWriteConfirmation } from "@/components/settings/confirm-write";
import { Announcer, ChangedMark, ExplainBox, SELECT_CLASS, useAnnouncement } from "@/components/settings/parts";
import { changedPaths } from "@/components/settings/section-draft";
import { changeCount } from "@/components/settings/unsaved-bar";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Sheet, SheetLayout, useEditParam } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { explainError } from "@/lib/settings";
import { CHOICE_HELP, CHOICE_LABEL, choiceOf, repeatsChoice, withChoice, withGrab, type LibraryChoice } from "@/lib/version-grab";

/** saved: the record as stored (a new library: its first draft), for the changed marks, the count and Discard. */
type Session = { record: VersionRecord; saved: VersionRecord; generation: number; create: boolean; draftID: number };
/** create: the body adds a library; inSheet: it came from the edit sheet (a row's Watch only / Live control saves
 * without one). */
type Pending = { body: VersionBody; effects: Confirmation["effects"]; proof?: Confirmation; create: boolean; inSheet: boolean };
/** The arr flavour whose TRaSH defaults fit a library kind. */
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
export function libraryChanges(record: VersionRecord, saved: VersionRecord): number {
  const view = (r: VersionRecord) => ({ key: r.key, config: r.config, roots: r.roots ?? [] });
  return changedPaths(view(record), view(saved)).length;
}

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

// A new version starts in shadow mode with the default profile for movies (Quality profiles, "Defaults for new
// versions"), else the first profile.
function blank(data: VersionList, defaults: Record<string, string> = {}): VersionRecord {
  const profile = defaults.movie && Object.hasOwn(data.profiles, defaults.movie) ? defaults.movie : Object.keys(data.profiles)[0] ?? "";
  return { id: 0, key: "", created_at: "", updated_at: "", roots: [], config: { mode: "shadow", kind: "movie", anime: false, profile, library: "", storage: "debrid", indexers: [], providers: [], client: data.clients[0] ?? "", import: "symlink", upgrades: "auto", follows: "" } };
}

const GROUPS: Category[] = ["movie", "series", "anime_movie", "anime_series"];

/** "from TRaSH WEB 2160p + 1080p fallbacks", "copy of arr profile …", "our own": where a profile came from. */
function profileOrigin(p: ProfileSummary | undefined): string {
  if (!p) return "";
  if (p.from_arr) return `copy of arr profile ${p.from_arr}`;
  if (p.origin) return `from TRaSH ${originLabel(p.origin)}${p.guessed ? " (closest preset)" : ""}`;
  return "our own profile";
}

/** The libraries that follow key (parallel libraries): a followed library stays Watch only, the server refuses Live. */
const followersOf = (versions: readonly VersionRecord[], key: string) => versions.filter((v) => key && v.config.follows === key).map((v) => v.key);

/** A library's Watch only / Live control. A mode it cannot express shows as text; a followed library offers Live
 * disabled, with the reason. */
function ChoicePick({ mode, onChange, disabled, followedBy, label }: { mode: string; onChange: (choice: LibraryChoice) => void; disabled?: boolean; followedBy: string[]; label: string }) {
  const choice = choiceOf(mode);
  if (!choice) return <span className="text-sm" data-testid="choice-readonly">{mode}</span>;
  return <select aria-label={label} className={SELECT_CLASS} value={choice} disabled={disabled} data-testid="library-choice" onChange={(event) => onChange(event.target.value as LibraryChoice)}>
    <option value="watch">{CHOICE_LABEL.watch}</option>
    <option value="live" disabled={!!followedBy.length && choice !== "live"}>{CHOICE_LABEL.live}{followedBy.length && choice !== "live" ? " (not while followed)" : ""}</option>
  </select>;
}

/** Why a followed library stays Watch only, beside its control. */
function FollowedNote({ followedBy }: { followedBy: string[] }) {
  return followedBy.length ? <p className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="followed-note">Stays Watch only: <span className="font-mono">{followedBy.join(", ")}</span> follows it, and a followed library's folders belong to its arr app.</p> : null;
}

function VersionGroup({ title, versions, all, switches, selected, readOnly, busy, onOpen, onChoose }: { title: string; versions: VersionRecord[]; all: VersionRecord[]; switches: VersionList["switches"]; selected?: string; readOnly: boolean; busy: boolean; onOpen: (record: VersionRecord) => void; onChoose: (record: VersionRecord, choice: LibraryChoice) => void }) {
  return <section className="space-y-2" aria-label={title}>
    <h2 className="text-base font-semibold">{title}</h2>
    <Card className="p-0">
      <Table>
        <TableHeader><TableRow>
          <TableHead>Library</TableHead>
          <TableHead className="hidden md:table-cell">Where</TableHead>
          <TableHead className="hidden md:table-cell">Profile</TableHead>
          <TableHead className="hidden lg:table-cell">Sources</TableHead>
          <TableHead><span className="sr-only">Actions</span></TableHead>
        </TableRow></TableHeader>
        <TableBody>{versions.map((record) => {
          const c = record.config;
          const where = <><div className="break-all font-mono text-[12.5px]">{c.library || "no root"}</div><div className="text-xs text-muted-foreground" data-testid="library-where">{[c.storage, c.client, c.import].filter(Boolean).join(" · ")}</div></>;
          const profile = <><div className="font-mono text-[12.5px] [overflow-wrap:anywhere]">{c.profile}</div></>;
          const sources = <div className="text-xs [overflow-wrap:anywhere]"><div>{(c.indexers ?? []).length ? (c.indexers ?? []).join(", ") : "no indexers"}</div><div className="text-muted-foreground">{c.add_via === "client" ? `via client ${c.client || "(none)"}` : (c.providers ?? []).length ? (c.providers ?? []).join(" › ") : "no providers"}</div></div>;
          return <TableRow key={record.key} data-record={record.key} data-selected={selected === record.key ? "true" : undefined}>
            <TableCell className="align-top">
              <div className="flex flex-wrap items-center gap-1.5"><span className="font-mono text-[13px] [overflow-wrap:anywhere]">{record.key}</span></div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5" data-testid="library-state-row"><ChoicePick mode={c.mode} label={`State of ${record.key}`} disabled={readOnly || busy} followedBy={followersOf(all, record.key)} onChange={(choice) => onChoose(record, choice)} /><VersionStatus words beside={c.mode} settingsKey={record.key} canManage state={switches.versions.find((v) => v.key === record.key)?.grab_state ?? { mode: "off", grab: false, library_enabled: switches.library_enabled, router_enabled: switches.router_enabled, router_forced_off: switches.sources?.router === "forced off by environment" }} /></div>
              <FollowedNote followedBy={c.mode === "shadow" ? followersOf(all, record.key) : []} />
              <div className="text-xs text-muted-foreground">{c.anime === null ? "anime and regular" : ""}{c.follows ? `${c.anime === null ? " · " : ""}follows ${c.follows}` : ""}</div>
              <div className="mt-1.5 space-y-1.5 md:hidden">{where}{profile}</div>
              <div className="mt-1.5 lg:hidden">{sources}</div>
            </TableCell>
            <TableCell className="hidden max-w-[16rem] align-top md:table-cell">{where}</TableCell>
            <TableCell className="hidden max-w-[16rem] align-top md:table-cell">{profile}</TableCell>
            <TableCell className="hidden max-w-[16rem] align-top lg:table-cell">{sources}</TableCell>
            <TableCell className="align-top text-right"><Button variant="outline" size="sm" onClick={() => onOpen(record)} data-testid="edit">{readOnly ? "View" : "Edit"}</Button></TableCell>
          </TableRow>;
        })}</TableBody>
      </Table>
    </Card>
  </section>;
}

export function VersionsPage({ embedded = false }: { embedded?: boolean }) {
  const qc = useQueryClient();
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
    const first = record ?? blank(data, defaults);
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
    if (keep) setSession({ ...session, saved: record && !session.create ? copy(record) : session.saved, generation: refreshed.data.generation });
    else {
      if (!record && !session.create) { close(); return; }
      const first = copy(record ?? blank(refreshed.data, defaults));
      setSession({ record: first, saved: copy(first), generation: refreshed.data.generation, create: session.create, draftID: Date.now() });
    }
    setError(undefined);
    setPending(undefined);
  }

  // inSheet: the save came from the edit sheet, which closes on success; a row's Watch only / Live control saves the
  // stored library with only mode and grab changed, through the same confirmation.
  async function apply(body: VersionBody, create: boolean, inSheet: boolean) {
    if (inSheet && !session) return;
    setBusy(true);
    try {
      await saveVersion(body, create);
      void qc.invalidateQueries({ queryKey: setupKey });
      // The header Pause saves with the overview's generation, which this save moved on.
      void qc.invalidateQueries({ queryKey: qk.settings.overview });
      setNotice(`${body.key} ${body.delete ? "is no longer managed" : "saved"}.`);
      // A stopped version's row goes away: focus goes to Add library instead of its Edit button.
      stopped.current = !!body.delete;
      if (inSheet) close();
      else { setPending(undefined); setError(undefined); }
      await query.refetch();
      // Stopping a library drops it from the arr apps that served it (Settings › Connected apps).
      void qc.invalidateQueries({ queryKey: ["settings", "view-bindings"] });
    } catch (failure) {
      const effects = confirmationEffects(failure);
      if (effects) setPending({ body, effects, create, inSheet });
      else { setPending(undefined); setError(failure); }
    }
    finally { setBusy(false); }
  }

  /** A row's control: the stored library as it is, with the chosen mode (and grab, see withChoice). */
  function choose(record: VersionRecord, choice: LibraryChoice) {
    if (!data || busy || choiceOf(record.config.mode) === choice) return;
    const config = withChoice(record.config, choice, record.config);
    setError(undefined);
    void apply({ generation: data.generation, key: record.key, config, roots: record.roots ?? [] }, false, false);
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
      const body: VersionBody = { generation: current.generation, key: current.record.key, ...(deleting ? { delete: true } : { config: current.record.config, roots: current.record.roots ?? [] }) };
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
      {readOnly ? <p>Read-only: these settings cannot be edited here. Allow new downloads {data.switches.router_enabled ? "ON" : "OFF"} · Write library links {data.switches.library_enabled ? "ON" : "OFF"} · Send notifications {data.switches.notify_enabled ? "ON" : "OFF"}</p> : null}
      <DownloadsOffNotice off={data.switches.sources?.router === "forced off by environment" ? "locked" : !data.switches.router_enabled ? "off" : undefined} canManage />
      {error && !session ? <div ref={errorBox} tabIndex={-1} data-testid="row-error"><ExplainBox error={explainError(error)} onReload={() => { setError(undefined); void query.refetch(); }} onReloadKeep={() => { setError(undefined); void query.refetch(); }} /></div> : null}
      <p className="text-sm">Coming from Sonarr/Radarr? <Link className="underline" to="/admin/arr-import">Import from Sonarr/Radarr</Link></p>
      <Button ref={addButton} onClick={() => open(undefined)} disabled={readOnly}>Add library</Button>
      {GROUPS.map((group) => {
        const list = data.versions.filter((record) => categoryOf(record.config.kind, record.config.anime) === group);
        return list.length ? <VersionGroup key={group} title={CATEGORY_LABEL[group]} versions={list} all={data.versions} busy={busy} onChoose={choose} switches={data.switches} selected={session && !session.create ? session.record.key : undefined} readOnly={readOnly} onOpen={open} /> : null;
      })}
      {session ? <Sheet label={session.create ? "New library" : `Edit library ${session.record.key}`} onClose={close} initialFocus={heading} returnFocus={() => (stopped.current ? addButton.current : undefined)}>
        <SheetLayout key={session.draftID} testid="version-form" headingRef={heading} onClose={close}
          title={`${session.create ? "New library" : `Edit library ${session.record.key}`}`}
          subtitle={session.create ? "Starts as Watch only unless you choose otherwise." : `${session.record.config.kind}${session.record.config.anime ? " · anime" : ""} · ${CHOICE_LABEL[choiceOf(session.record.config.mode) ?? "watch"]}`}
          footer={<><Button onClick={() => void review()} disabled={readOnly || busy}>Save</Button><Button variant="outline" onClick={close}>Cancel</Button>{libraryChanges(session.record, session.saved) ? <><span className="text-[13px] font-medium text-[var(--ink)]" data-testid="unsaved-count" aria-live="polite">{changeCount(libraryChanges(session.record, session.saved))}</span><Button variant="ghost" size="sm" disabled={busy} data-testid="version-discard" onClick={() => { setSession({ ...session, record: copy(session.saved) }); setError(undefined); setPending(undefined); setDiscards(discards + 1); }}>Discard</Button></> : null}<Link className="text-sm text-[var(--accent-ink)] underline" to="/admin/settings/libraries/naming">Edit naming</Link>{!session.create ? <Button className="ml-auto" variant="destructive" onClick={() => void review(true)} disabled={readOnly || busy}>Stop managing library</Button> : null}</>}>
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
    {pending ? <EffectsConfirmDialog subject={pending.body.key} library={pending.body.key} effects={pending.effects ?? []} scope={{ title: "Confirm library change", does: [], keeps: ["Pause and the global switches keep their current settings; environment force-off overrides stay in force.", "Existing links, mount files and trash entries stay where they are."], confirm: "Confirm this change" }} busy={busy} onClose={() => setPending(undefined)} onConfirm={() => void approvePending()} /> : null}
  </div>;
}

function Part({ title, help, children }: { title: string; help: string; children: ReactNode }) {
  return <section className="space-y-3" aria-label={title}>
    <div><h3 className="font-semibold">{title}</h3><p className="text-xs text-muted-foreground">{help}</p></div>
    <div className="grid gap-4 md:grid-cols-2">{children}</div>
  </section>;
}

function VersionForm({ session, data, defaults, profiles, error, onChange }: { session: Session; data: VersionList; defaults: Record<string, string>; profiles: Map<string, ProfileSummary>; error?: unknown; onChange: (record: VersionRecord) => void }) {
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
    onChange({ ...record, config: next });
  };
  useEffect(() => {
    if (data.source !== "db" || !record.key || !config.library) { setRootStatus(""); return; }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setRootStatus("Checking root…");
      void checkRoot(generation, record.key, config.library, controller.signal).then((result) => setRootStatus(`Root resolves to ${result.resolved}${result.writable ? " · writable" : ""}`), (failure: unknown) => { if (!controller.signal.aborted) setRootStatus(`root check: ${fieldMessage(failure, (path) => path === "root") ?? message(failure).replace(/^settings refused:\s*(?:root:\s*)?/, "")}`); });
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
  const advancedError = rootsError ?? fieldError("storage") ?? fieldError("add_via") ?? fieldError("client") ?? fieldError("import") ?? fieldError("follows");
  // Advanced starts open when the library uses something in it: write roots other than the root, a download client
  // for its grabs, a followed library, or a storage or import mode that is not the supported one. A refused field
  // inside keeps it open.
  const [opened, setAdvanced] = useState(() => (!!record.roots?.length && !same(record.roots, [config.library])) || viaClient || !!config.follows || storage.options.length > 1 || importMode.options.length > 1 || (config.mode === "active" && !config.grab));
  // The Watch only / Live control: changed when the mode changed. Live with downloads off is the tick under Advanced.
  const choiceChanged = config.mode !== saved.config.mode;
  const followedBy = followersOf(data.versions, record.key);
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
  const draftState = { mode: config.mode, grab: config.grab ?? false, library_enabled: data.switches.library_enabled, router_enabled: data.switches.router_enabled, router_forced_off: data.switches.sources?.router === "forced off by environment" };
  const select = (value: string) => { pickedByHand.current = true; setProfileFrom(undefined); update("profile", value); };
  return <div className="space-y-6">
    {repeatsChoice(draftState, config.mode) ? null : <div className="flex flex-wrap items-center gap-2 text-sm"><span>Draft after saving:</span><VersionStatus words state={draftState} /></div>}
    <Part title="Where" help="What the library holds, where its folder lives and how files get there.">
      <Field label="Name" help="Optional. Shown in place of the guessed label, such as 4K WEB. Connected apps never see it." changed={(config.name ?? "") !== (saved.config.name ?? "")} onUndo={() => setName(saved.config.name ?? "")} error={fieldError("name")}><Input value={config.name ?? ""} maxLength={40} onChange={(event) => setName(event.target.value)} /></Field>
      <Field label="Key" help="Used by connected apps and in settings; fixed once the library exists." changed={record.key !== saved.key} onUndo={() => onChange({ ...record, key: saved.key })}><Input value={record.key} disabled={!session.create} onChange={(event) => onChange({ ...record, key: event.target.value })} /></Field>
      <div className="flex flex-col gap-1 text-sm" data-changed={choiceChanged || undefined}>
        <span>State</span>
        <ChoicePick mode={config.mode} label="State" followedBy={followedBy} onChange={(choice) => onChange({ ...record, config: withChoice(config, choice, saved.config) })} />
        <p className="text-xs text-muted-foreground">{config.mode === "active" && !config.grab ? "Live, downloads off: see Don't download for this library under Advanced. " : ""}{CHOICE_HELP}</p>
        {fieldError("mode") ? <p role="alert" className="text-xs text-destructive">{fieldError("mode")}</p> : null}
        <FollowedNote followedBy={followedBy} />
        {choiceChanged ? <ChangedMark name="State" onUndo={() => onChange({ ...record, config: { ...withGrab(config, saved.config.grab ?? false, saved.config), mode: saved.config.mode } })} /> : null}
      </div>
      <Field label="Kind" {...mark("kind")}><Pick value={config.kind} options={["movie", "series"]} onChange={(value) => recategorize({ ...config, kind: value as VersionConfig["kind"] })} /></Field>
      <Field label="Anime" {...mark("anime")}><Pick value={config.anime === null ? "both" : String(config.anime)} options={["false", "true", "both"]} labels={{ false: "Not anime", true: "Anime only", both: "Anime and not" }} onChange={(value) => recategorize({ ...config, anime: value === "both" ? null : value === "true" })} /></Field>
      <Field label="Root" changed={!same(config.library, saved.config.library)} error={fieldError("library")}><Input value={config.library} onChange={(event) => { const root = event.target.value; const roots = record.roots?.length && record.roots.length > 1 ? record.roots.map((value) => value === config.library ? root : value) : [root]; setRootsText(roots.join(", ")); onChange({ ...record, config: { ...config, library: root }, roots }); }} /><span role="status" className="break-all text-xs">{rootStatus}</span></Field>
      <details className="md:col-span-2" open={advanced} data-testid="library-advanced">
        {/* The click is handled here, so the open state has one owner. */}
        <summary className="cursor-pointer text-sm font-medium" onClick={(event) => { event.preventDefault(); setAdvanced(!opened); }}>Advanced</summary>
        {advanced ? <div className="mt-3 grid gap-4 md:grid-cols-2">
          {config.mode === "active" ? <Field label="Don't download for this library" help="Ticked: nothing is downloaded for this library. RSS checks and manual searches may still record decisions." changed={(config.grab ?? false) !== (saved.config.grab ?? false)} onUndo={() => onChange({ ...record, config: withGrab(config, saved.config.grab ?? false, saved.config) })} error={fieldError("grab")}><input type="checkbox" className="self-start" checked={!config.grab} onChange={(event) => onChange({ ...record, config: withGrab(config, !event.target.checked, saved.config) })} /></Field> : null}
          <Field label="Write roots" help="Extra folders this library may write to, comma separated. Leave as the root." changed={rootsChanged} error={rootsError}><Input value={rootsText} onChange={(event) => { setRootsText(event.target.value); onChange({ ...record, roots: split(event.target.value) }); }} /></Field>
          {storage.options.length > 1 ? <Field label="Storage" {...mark("storage")} error={fieldError("storage")}><Pick value={config.storage} {...storage} onChange={(value) => update("storage", value as VersionConfig["storage"])} /></Field> : null}
          <Field label="Send grabs to" {...mark("add_via")} error={fieldError("add_via")}><Pick value={viaClient ? "client" : "providers"} options={["providers", "client"]} labels={{ providers: "Debrid providers", client: "Download client" }} onChange={(value) => update("add_via", value as VersionConfig["add_via"])} /></Field>
          <Field label={viaClient ? "Client (a decypharr client: it adds to its own debrid account)" : "Client (only when grabs go to a download client; debrid libraries need none)"} name="Client" {...mark("client")} error={fieldError("client")}><Pick value={config.client} options={["", ...data.clients]} onChange={(value) => update("client", value)} /></Field>
          {importMode.options.length > 1 ? <Field label="Import mode" {...mark("import")} error={fieldError("import")}><Pick value={config.import} {...importMode} onChange={(value) => update("import", value as VersionConfig["import"])} /></Field> : null}
          <Field label="Follows" {...mark("follows")} error={fieldError("follows")}><Pick value={config.follows} options={["", ...data.versions.filter((version) => version.key !== record.key).map((version) => version.key)]} onChange={(value) => update("follows", value)} /></Field>
        </div> : null}
      </details>
    </Part>
    <Part title="Which releases" help="The quality profile decides which releases it takes and when it upgrades.">
      <div className="flex flex-col gap-1"><Field label="Profile" {...mark("profile")} error={fieldError("profile")}><select className={SELECT_CLASS} value={config.profile} onChange={(event) => select(event.target.value)}>
          {/* One select element whether or not the TRaSH defaults have loaded, so a load never drops focus. */}
          {presets.length || picked ? <>
            {config.profile === "" || (!picked && !stored.includes(config.profile)) ? <option value={config.profile}>{config.profile || "Choose a profile"}</option> : null}
            {stored.length ? <optgroup label="Your profiles">{stored.map((key) => <option key={key} value={key}>{key}</option>)}</optgroup> : null}
            <optgroup label="TRaSH defaults">
              {presets.map((p) => { const copies = copiesOf(p.trash_id); return <option key={p.trash_id} value={PRESET_PICK + p.trash_id}>{p.name}{copies.length ? ` (copy: ${copies.join(", ")})` : ""}</option>; })}
              {picked && !pickedPreset ? <option value={config.profile}>TRaSH default {picked}</option> : null}
            </optgroup>
          </> : <>{config.profile === "" ? <option value="">Choose a profile</option> : null}{stored.map((key) => <option key={key} value={key}>{key || "None"}</option>)}</>}
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
    <Part title="Sources" help="Where it searches and which debrid providers it adds through, in fallback order.">
      <div className="space-y-1"><Multi label="Indexers" choices={data.indexers} values={config.indexers ?? []} onChange={(values) => update("indexers", values)} />{mark("indexers").changed ? <ChangedMark name="Indexers" onUndo={mark("indexers").onUndo} /> : null}</div>
      {viaClient ? <p className="text-sm text-muted-foreground">Grabs go to download client {config.client || "(none picked)"}, which adds them to its own debrid account and decides cached-only, pacing and stalled downloads itself; the providers below are not used.</p> : null}
      <div className="space-y-1"><Multi label="Providers (fallback order)" choices={data.providers} values={config.providers ?? []} onChange={(values) => update("providers", values)} />{mark("providers").changed ? <ChangedMark name="Providers" onUndo={mark("providers").onUndo} /> : null}</div>
      {viaClient ? null : <div className="flex flex-col gap-1"><Field label="Provider preference" {...mark("provider_preference")}><Pick value={config.provider_preference || "ordered"} options={["ordered", "none"]} labels={{ ordered: "Fallback order", none: "No preference" }} onChange={(value) => update("provider_preference", value as VersionConfig["provider_preference"])} /></Field><span className="text-xs text-muted-foreground">Among releases of equal quality and format score, the one cached on more providers wins. Fallback order first prefers the release cached on an earlier provider; No preference only counts providers. Providers are tried in fallback order either way.</span></div>}
    </Part>
  </div>;
}
