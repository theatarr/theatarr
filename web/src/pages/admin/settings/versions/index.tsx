import { useQueryClient } from "@tanstack/react-query";
import { setupKey } from "@/api/setup";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { checkRoot, confirmVersion, saveVersion, useVersions, type Confirmation, type VersionBody, type VersionConfig, type VersionList, type VersionRecord } from "@/api/settings-versions";
import { CATEGORY_LABEL, categoryOf, originLabel, useQualityOverview, type Category, type ProfileSummary } from "@/api/settings-profiles";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { PageHeader } from "@/components/page";
import { EffectsConfirmDialog } from "@/components/settings/confirm-write";
import { Announcer, ChangedMark, ExplainBox, SELECT_CLASS, useAnnouncement } from "@/components/settings/parts";
import { changedPaths } from "@/components/settings/section-draft";
import { changeCount } from "@/components/settings/unsaved-bar";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Sheet, SheetLayout, useEditParam } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { explainError } from "@/lib/settings";

/** saved: the record as stored (a new library: its first draft), for the changed marks, the count and Discard. */
type Session = { record: VersionRecord; saved: VersionRecord; generation: number; create: boolean; draftID: number };
type Pending = { body: VersionBody; effects: Confirmation["effects"]; proof?: Confirmation };
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

function Field({ label, children, changed, onUndo, name, error }: { label: string; children: ReactNode; changed?: boolean; onUndo?: () => void; name?: string; error?: string }) {
  return <div className="flex flex-col gap-1 text-sm" data-changed={changed || undefined}>
    <label className="flex flex-col gap-1"><span>{label}</span>{children}</label>
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

function VersionGroup({ title, versions, switches, selected, readOnly, onOpen }: { title: string; versions: VersionRecord[]; switches: VersionList["switches"]; selected?: string; readOnly: boolean; onOpen: (record: VersionRecord) => void }) {
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
          const where = <><div className="break-all font-mono text-[12.5px]">{c.library || "no root"}</div><div className="text-xs text-muted-foreground">{c.storage} · {c.client} · {c.import}</div></>;
          const profile = <><div className="font-mono text-[12.5px] [overflow-wrap:anywhere]">{c.profile}</div></>;
          const sources = <div className="text-xs [overflow-wrap:anywhere]"><div>{(c.indexers ?? []).length ? (c.indexers ?? []).join(", ") : "no indexers"}</div><div className="text-muted-foreground">{c.add_via === "client" ? `via client ${c.client || "(none)"}` : (c.providers ?? []).length ? (c.providers ?? []).join(" › ") : "no providers"}</div></div>;
          return <TableRow key={record.key} data-record={record.key} data-selected={selected === record.key ? "true" : undefined}>
            <TableCell className="align-top">
              <div className="flex flex-wrap items-center gap-1.5"><span className="font-mono text-[13px] [overflow-wrap:anywhere]">{record.key}</span><VersionStatus showMode settingsKey={record.key} canManage state={switches.versions.find((v) => v.key === record.key)?.grab_state ?? { mode: "off", grab: false, library_enabled: switches.library_enabled, router_enabled: switches.router_enabled, router_forced_off: switches.sources?.router === "forced off by environment" }} /></div>
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
  const [busy, setBusy] = useState(false);
  // Discard remounts the form (its root text) without moving focus off the footer.
  const [discards, setDiscards] = useState(0);
  const [notice, setNotice] = useAnnouncement();
  const heading = useRef<HTMLHeadingElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const errorBox = useRef<HTMLDivElement>(null);
  const stopped = useRef(false);
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

  async function apply(body: VersionBody) {
    if (!session) return;
    setBusy(true);
    try {
      await saveVersion(body, session.create);
      void qc.invalidateQueries({ queryKey: setupKey });
      setNotice(`${body.key} ${body.delete ? "is no longer managed" : "saved"}.`);
      // A stopped version's row goes away: focus goes to Add library instead of its Edit button.
      stopped.current = !!body.delete;
      close();
      await query.refetch();
      // Stopping a library drops it from the arr apps that served it (Settings › Connected apps).
      void qc.invalidateQueries({ queryKey: ["settings", "view-bindings"] });
    } catch (failure) {
      const effects = confirmationEffects(failure);
      if (effects) setPending({ body, effects });
      else { setPending(undefined); setError(failure); }
    }
    finally { setBusy(false); }
  }

  async function review(deleting = false) {
    if (!session) return;
    const body: VersionBody = { generation: session.generation, key: session.record.key, ...(deleting ? { delete: true } : { config: session.record.config, roots: session.record.roots ?? [] }) };
    setBusy(true);
    setError(undefined);
    try {
      if (!deleting) {
        await apply(body);
        return;
      }
      const proof = await confirmVersion(body);
      setPending({ body, effects: proof.effects ?? [], proof });
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
      await apply({ ...current.body, confirmation_token: proof.token });
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
      <Button ref={addButton} onClick={() => open(undefined)} disabled={readOnly}>Add library</Button>
      {GROUPS.map((group) => {
        const list = data.versions.filter((record) => categoryOf(record.config.kind, record.config.anime) === group);
        return list.length ? <VersionGroup key={group} title={CATEGORY_LABEL[group]} versions={list} switches={data.switches} selected={session && !session.create ? session.record.key : undefined} readOnly={readOnly} onOpen={open} /> : null;
      })}
      {session ? <Sheet label={session.create ? "New library" : `Edit library ${session.record.key}`} onClose={close} initialFocus={heading} returnFocus={() => (stopped.current ? addButton.current : undefined)}>
        <SheetLayout key={session.draftID} testid="version-form" headingRef={heading} onClose={close}
          title={`${session.create ? "New library" : `Edit library ${session.record.key}`}`}
          subtitle={session.create ? "Starts in Shadow mode unless you choose otherwise." : `${session.record.config.kind}${session.record.config.anime ? " · anime" : ""} · ${session.record.config.mode}`}
          footer={<><Button onClick={() => void review()} disabled={readOnly || busy}>Save</Button><Button variant="outline" onClick={close}>Cancel</Button>{libraryChanges(session.record, session.saved) ? <><span className="text-[13px] font-medium text-[var(--ink)]" data-testid="unsaved-count" aria-live="polite">{changeCount(libraryChanges(session.record, session.saved))}</span><Button variant="ghost" size="sm" disabled={busy} data-testid="version-discard" onClick={() => { setSession({ ...session, record: copy(session.saved) }); setError(undefined); setPending(undefined); setDiscards(discards + 1); }}>Discard</Button></> : null}<Link className="text-sm text-[var(--accent-ink)] underline" to="/admin/settings/libraries/naming">Edit naming</Link>{!session.create ? <Button className="ml-auto" variant="destructive" onClick={() => void review(true)} disabled={readOnly || busy}>Stop managing library</Button> : null}</>}>
          {session.record.config.mode === "active" && !session.create ? <p className="mb-4 rounded-md border border-[var(--th-warning-border)] bg-[var(--warn-soft)] px-3 py-2 text-[13px] text-[var(--warn-ink)]">Active library: a change that widens what it downloads or where it writes asks for confirmation first.</p> : null}
          {error ? <div ref={errorBox} tabIndex={-1} className="mb-4"><ExplainBox error={explainError(error)} onReload={() => void reload(false)} onReloadKeep={() => void reload(true)} /></div> : null}
          <fieldset disabled={readOnly || busy}>
            <VersionForm key={discards} session={session} data={data} defaults={defaults} profiles={profiles} error={error} onChange={(record) => { setSession({ ...session, record }); setPending(undefined); }} />
          </fieldset>
        </SheetLayout>
      </Sheet> : null}
      <p className="text-sm text-muted-foreground">Which arr apps (Seerr, Radarr- and Sonarr-compatible clients) see each library is set under <Link className="underline" to="/admin/settings/apps">Connected apps</Link>.</p>
    </> : null}
    {pending ? <EffectsConfirmDialog subject={pending.body.key} library={pending.body.key} effects={pending.effects ?? []} scope={{ title: "Confirm library change", does: [], keeps: ["Global switches keep their current settings; environment force-off overrides stay in force.", "Existing links, mount files and trash entries stay where they are."], confirm: "Confirm this change" }} busy={busy} onClose={() => setPending(undefined)} onConfirm={() => void approvePending()} /> : null}
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
  // A new version takes the default profile of its kind while its profile is still the previous kind's default.
  const recategorize = (next: VersionConfig) => {
    if (session.create) {
      const was = defaults[categoryOf(config.kind, config.anime)] ?? "";
      const now = defaults[categoryOf(next.kind, next.anime)];
      if (now && Object.hasOwn(data.profiles, now) && (config.profile === was || !config.profile)) next = { ...next, profile: now };
    }
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
  return <div className="space-y-6">
    <div className="flex flex-wrap items-center gap-2 text-sm"><span>Draft after saving:</span><VersionStatus showMode state={{ mode: config.mode, grab: config.grab ?? false, library_enabled: data.switches.library_enabled, router_enabled: data.switches.router_enabled, router_forced_off: data.switches.sources?.router === "forced off by environment" }} /></div>
    <Part title="Where" help="What the library holds, where its folder lives and how files get there.">
      <Field label="Key" changed={record.key !== saved.key} onUndo={() => onChange({ ...record, key: saved.key })}><Input value={record.key} disabled={!session.create} onChange={(event) => onChange({ ...record, key: event.target.value })} /></Field>
      <Field label="Mode (Shadow previews and compares; Active manages files)" name="Mode" {...mark("mode")}><Pick value={config.mode} options={["shadow", "active"]} labels={{ shadow: "Shadow", active: "Active" }} onChange={(value) => update("mode", value as VersionConfig["mode"])} /></Field>
      <Field label="Allow downloads for this library (adds torrents when the library is Active and Allow new downloads is on)" name="Allow downloads for this library" {...mark("grab")}><Pick value={String(config.grab ?? false)} options={["false", "true"]} labels={{ false: "Off", true: "On" }} onChange={(value) => update("grab", value === "true")} /></Field>
      <Field label="Kind" {...mark("kind")}><Pick value={config.kind} options={["movie", "series"]} onChange={(value) => recategorize({ ...config, kind: value as VersionConfig["kind"] })} /></Field>
      <Field label="Anime" {...mark("anime")}><Pick value={config.anime === null ? "both" : String(config.anime)} options={["false", "true", "both"]} labels={{ false: "Not anime", true: "Anime only", both: "Anime and not" }} onChange={(value) => recategorize({ ...config, anime: value === "both" ? null : value === "true" })} /></Field>
      <Field label="Root" changed={!same(config.library, saved.config.library)} error={fieldMessage(error, (path) => path === `versions[${record.key}].library`)}><Input value={config.library} onChange={(event) => { const root = event.target.value; const roots = record.roots?.length && record.roots.length > 1 ? record.roots.map((value) => value === config.library ? root : value) : [root]; setRootsText(roots.join(", ")); onChange({ ...record, config: { ...config, library: root }, roots }); }} /><span role="status" className="break-all text-xs">{rootStatus}</span></Field>
      <Field label="Write roots (comma separated)" name="Write roots" changed={rootsChanged} error={fieldMessage(error, (path) => path.startsWith(`library.versions[${record.key}].roots`))}><Input value={rootsText} onChange={(event) => { setRootsText(event.target.value); onChange({ ...record, roots: split(event.target.value) }); }} /></Field>
      <Field label="Storage" {...mark("storage")}><Pick value={config.storage} options={["debrid", "seedbox"]} onChange={(value) => update("storage", value as VersionConfig["storage"])} /></Field>
      <Field label="Send grabs to" {...mark("add_via")}><Pick value={viaClient ? "client" : "providers"} options={["providers", "client"]} labels={{ providers: "Debrid providers", client: "Download client" }} onChange={(value) => update("add_via", value as VersionConfig["add_via"])} /></Field>
      <Field label={viaClient ? "Client (a decypharr client: it adds to its own debrid account)" : "Client (seedbox libraries; debrid libraries need none)"} name="Client" {...mark("client")}><Pick value={config.client} options={["", ...data.clients]} onChange={(value) => update("client", value)} /></Field>
      <Field label="Import mode" {...mark("import")}><Pick value={config.import} options={["symlink", "hardlink", "copy"]} onChange={(value) => update("import", value as VersionConfig["import"])} /></Field>
      <Field label="Follows" {...mark("follows")}><Pick value={config.follows} options={["", ...data.versions.filter((version) => version.key !== record.key).map((version) => version.key)]} onChange={(value) => update("follows", value)} /></Field>
    </Part>
    <Part title="Which releases" help="The quality profile decides which releases it takes and when it upgrades.">
      <div className="flex flex-col gap-1"><Field label="Profile" {...mark("profile")}><Pick value={config.profile} options={Object.keys(data.profiles)} onChange={(value) => update("profile", value)} /></Field>
        <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]">{summary ? `${profileOrigin(summary)} · ` : ""}its format set comes with it{data.profiles[config.profile] ? `: ${data.profiles[config.profile]}` : ""}. <Link className="underline" to={`/admin/settings/profiles?edit=${encodeURIComponent(config.profile)}`}>Open the profile</Link></span>
      </div>
      <Field label="Upgrades" {...mark("upgrades")}><Pick value={config.upgrades} options={["auto", "approve", "off"]} onChange={(value) => update("upgrades", value as VersionConfig["upgrades"])} /></Field>
      <div className="flex flex-col gap-1"><Field label="Prefer SeaDex" {...mark("prefer_seadex")}><Pick value={String(config.prefer_seadex ?? false)} options={["false", "true"]} onChange={(value) => update("prefer_seadex", value === "true")} /></Field><span className="text-xs text-muted-foreground">SeaDex picks take priority among allowed qualities and can replace higher-resolution files. Actual quality remains shown; size limits, upgrades and the other checks still apply.</span></div>
    </Part>
    <Part title="Sources" help="Where it searches and which debrid or seedbox providers it adds through, in fallback order.">
      <div className="space-y-1"><Multi label="Indexers" choices={data.indexers} values={config.indexers ?? []} onChange={(values) => update("indexers", values)} />{mark("indexers").changed ? <ChangedMark name="Indexers" onUndo={mark("indexers").onUndo} /> : null}</div>
      {viaClient ? <p className="text-sm text-muted-foreground">Grabs go to download client {config.client || "(none picked)"}, which adds them to its own debrid account and decides cached-only, pacing and stalled downloads itself; the providers below are not used.</p> : null}
      <div className="space-y-1"><Multi label="Providers (fallback order)" choices={data.providers} values={config.providers ?? []} onChange={(values) => update("providers", values)} />{mark("providers").changed ? <ChangedMark name="Providers" onUndo={mark("providers").onUndo} /> : null}</div>
    </Part>
  </div>;
}
