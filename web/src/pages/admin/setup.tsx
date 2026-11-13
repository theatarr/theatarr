// Admin › Setup (docs/quickstart.md, docs/design/web.md "Setup"): the first-run screen. It leads a fresh install
// through the admin password, the debrid providers, the indexers (Prowlarr feeds as Torznab and the native catalog),
// the TMDb key, a library root and a first version from a preset, then the preflight. Everything it creates goes through the
// settings API. The first library can start downloading at once (POST setup/start: one save, one confirmation that
// lists what turning Pause off starts), or start Watch only, and then nothing Setup does can grab.
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { startSetup, usePreflight, type LastPreflight, type PreflightItem, type PreflightReport, type SetupStartBody, type SetupState } from "@/api/setup";
import { useSetupProgress } from "@/api/useSetupProgress";
import { libraryLabel } from "@/lib/library-label";
import { settingHref } from "@/lib/settings-map";
import { createFromPreset, usePresets } from "@/api/settings-profiles";
import { checkRoot, confirmVersion, saveVersion, useVersions, type Confirmation, type VersionBody, type VersionConfig, type VersionList } from "@/api/settings-versions";
import { useChangePassword } from "@/api/users";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { globalDownloadsOff } from "@/lib/version-grab";
import { ErrorState, Loading, PageHeader } from "@/components/page";
import { EffectsConfirmDialog } from "@/components/settings/confirm-write";
import { SELECT_CLASS, TestResult } from "@/components/settings/parts";
import { useSettings } from "@/api/settings";
import { qk } from "@/api/query-keys";
import { TMDbKey } from "@/components/settings/tmdb-key";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtTime } from "@/lib/format";
import { confirmationOf, type WriteConfirmation } from "@/lib/settings";
import { journalHome } from "@/lib/admin-links";

const message = (error: unknown) => error instanceof Error ? error.message : String(error);

function Step({ n, title, done, children }: { n: number; title: string; done: boolean; children: ReactNode }) {
  return <li><Card>
    <div className="mb-2 flex flex-wrap items-center gap-2"><h2 className="text-base font-semibold">{n}. {title}</h2><Badge tone={done ? "positive" : "neutral"}>{done ? "Done" : "Not yet"}</Badge></div>
    <div className="space-y-2 text-sm">{children}</div>
  </Card></li>;
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="flex flex-col gap-1 text-sm"><span>{label}</span>{children}</label>;
}

export function SetupPage() {
  const progress = useSetupProgress();
  const { setup } = progress;
  // What the one-step start reported as forced off: kept here, since its form goes away once the library exists.
  const [forcedOff, setForcedOff] = useState<string[]>([]);
  const qc = useQueryClient();
  if (setup.isPending) return <Loading />;
  if (setup.error) return <ErrorState error={setup.error} />;
  const s = setup.data;
  const refresh = () => { void setup.refetch(); void progress.versions.refetch(); };
  // An older server sent null for a complete install.
  const missing = s.missing ?? [];
  return <div className="space-y-4">
    <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Setup" description="What this install still needs, step by step, then a preflight of everything it relies on." />
    <Card className="space-y-2 text-sm"><h2 className="text-base font-semibold">Coming from Sonarr/Radarr?</h2><p>Once your library exists (step 4), bring in your titles, what is monitored, and the files already on your debrid mount. Theatarr only reads Sonarr/Radarr.</p><Link className="underline" to="/admin/arr-import">Import from Sonarr/Radarr</Link></Card>
    {missing.length ? <Card><p className="text-sm font-medium">Off until set up:</p><ul className="mt-1 list-disc pl-5 text-sm" data-testid="setup-missing">{missing.map((m) => <li key={m}>{m}</li>)}</ul></Card> : null}
    <p>Getting started: {progress.done} of {progress.total} steps complete</p>
    <progress className="h-2 w-full" aria-label="Setup progress" value={progress.done} max={progress.total} />
    <ol className="space-y-3">
      <Step n={1} title="Secure the admin account" done={progress.steps[0]}>
        {s.admin_password_generated ? <AdminPassword onDone={refresh} /> : <p>The admin account has its own password.</p>}
      </Step>
      <Step n={2} title="Connect downloads" done={progress.steps[1]}>
        <p>Add a download provider and an indexer. Each form can test before it saves, and a passed test offers enabling in the same save.</p>
        <h3>Debrid providers <Badge tone={s.providers > 0 ? "positive" : "neutral"}>{s.providers > 0 ? "Done" : "Not yet"}</Badge></h3>
        <p>{s.providers ? `${s.providers} enabled ${s.providers === 1 ? "provider" : "providers"} with an API key: configured; the connection is not verified here.` : "No enabled debrid provider with an API key yet: nothing can be added to debrid."} Add Real-Debrid, AllDebrid or TorBox with its API key under <Link className="underline" to="/admin/settings/downloads">Settings › Download clients</Link>; the key is tested with a read-only account call.</p>
        <SourceStates entity="providers" ready={s.ready_providers ?? []} />
        <h3>Indexers <Badge tone={s.indexers > 0 ? "positive" : "neutral"}>{s.indexers > 0 ? "Done" : "Not yet"}</Badge></h3>
        <p>{s.indexers ? `${s.indexers} enabled ${s.indexers === 1 ? "indexer" : "indexers"} configured; the connection is not verified here.` : "No enabled indexer yet (Torznab and DMM also need their key): nothing can be searched."} Both ways work, and they mix:</p>
        <SourceStates entity="indexers" ready={s.ready_indexers ?? []} />
        <ul className="list-disc pl-5">
          <li><strong>Built in:</strong> the catalog of tracker sites Theatarr runs itself, DMM (needs a paid DMM key), Torrentio, Zilean and SeaDex, under <Link className="underline" to="/admin/settings/indexers">Settings › Indexers › Add indexer</Link>.</li>
          <li><strong>Already use Prowlarr?</strong> Link Prowlarr there adds its indexers as feeds; the trackers stay in Prowlarr.</li>
        </ul>
        <p>The VPN check is optional and off by default. Once set up under <Link className="underline" to="/admin/settings/indexers/vpn">Settings › Indexers › VPN</Link>, requests of the traffic it requires (indexers unless you change it) are refused while the VPN exit is not confirmed.</p>
      </Step>
      <Step n={3} title="Add metadata (TMDb)" done={progress.steps[2]}>
        {s.tmdb
          ? <p>A TMDb key is set: configured; the connection is not verified here (a search for a title shows whether TMDb answers). Its sources and key, TVDB and the artwork cache are under <Link className="underline" to="/admin/settings/metadata">Settings › Metadata &amp; artwork</Link>.</p>
          : <><p>Without a TMDb key, search and adding titles only see what the library already has, and there is no artwork or release date. TVDB (optional, for series numbering and anime) is under <Link className="underline" to="/admin/settings/metadata">Settings › Metadata &amp; artwork</Link>.</p><TMDbKey onSaved={refresh} /></>}
      </Step>
      <Step n={4} title="Connect your library" done={progress.steps[3]}>
        <p>Connect the debrid mount under <Link className="underline" to={settingHref("library", "mount")!}>Settings › Libraries › File management</Link>, then the Plex URL and token under <Link className="underline" to={settingHref("library", "plex")!}>Settings › Plex &amp; playback</Link>.</p>
        <p>Arr views and notifications are under <Link className="underline" to="/admin/settings">Admin › Settings</Link>.</p>
        <h3 className="font-medium">Create your first library</h3>

        {s.versions > 0
          ? <p>{s.versions} {s.versions === 1 ? "library" : "libraries"}, {s.versions_with_roots} with a library root. Edit them under <Link className="underline" to="/admin/settings/libraries">Libraries</Link>.</p>
          : <FirstVersion state={s} onDone={(forced) => { setForcedOff(forced ?? []); refresh(); void qc.invalidateQueries({ queryKey: qk.settings.overview }); }} />}
        {forcedOff.length ? <p role="status" data-testid="start-forced-off">The library was created and Pause is saved as off, but THEATARR_FORCE_OFF keeps Pause on: nothing is searched, downloaded or linked until that override is removed and the service restarted.</p> : null}
        <DownloadsOffNotice off={globalDownloadsOff((s.version_states ?? []).map((v) => v.grab_state))} canManage />
        <div className="space-y-1">{(s.version_states ?? []).map((v) => <div key={v.key} className="flex flex-wrap items-start gap-2"><span className="font-mono text-[13px]">{v.key}</span><VersionStatus state={v.grab_state} showMode settingsKey={v.key} canManage /></div>)}</div>
      </Step>
      <Step n={5} title="Check readiness" done={progress.steps[4]}>
        <Preflight generation={progress.versions.data?.generation} report={progress.report} last={progress.last} />
        <Link className="underline" to={journalHome()}>Open the decision journal</Link>
      </Step>
      <Step n={6} title="Review activation" done={progress.steps[5]}>
        <p>A library created with "Create library and start downloading" is Live with Pause off already. For any other library: when you're ready, set it to Live on Libraries and turn Pause off at the top of the page.</p>
        {progress.versions.isPending ? <Loading /> : progress.versions.error ? <ErrorState error={progress.versions.error} /> : <Activation data={progress.versions.data} />}
      </Step>
    </ol>
  </div>;
}

/** What the server already knows about the sources a step counts: the latest test of each one as saved (a list row's
 * Test, Test all or a full preflight; the editor's test of unsaved values is not recorded) since Theatarr started and
 * an active back-off, from the settings list (GET /settings). Nothing is tested and no request leaves the server. */
function SourceStates({ entity, ready }: { entity: "indexers" | "providers"; ready: string[] }) {
  const settings = useSettings();
  const page = entity === "indexers" ? "/admin/settings/indexers" : "/admin/settings/downloads";
  const rows = (settings.data?.[entity] ?? []).filter((r) => ready.includes(r.key));
  if (!rows.length) return null;
  return <ul className="space-y-1" data-testid={`source-states-${entity}`}>{rows.map((r) => {
    const backoff = r.backoff as { active?: boolean; until?: string } | undefined;
    return <li key={r.key} className="flex flex-wrap items-center gap-2"><Link className="font-mono text-[13px] underline" to={`${page}?edit=${encodeURIComponent(r.key)}`}>{r.key}</Link>
      {r.last_test ? <><span>last test of the saved source{r.last_test.tested_at ? ` (${fmtAgo(r.last_test.tested_at)})` : ""}:</span><TestResult result={r.last_test} /></> : <span className="text-muted-foreground">the saved source has not been tested since Theatarr started. Test in its row of the {entity === "indexers" ? "indexer" : "download client"} list records one; a test of unsaved values in the editor is not recorded.</span>}
      {backoff?.active ? <Badge tone="warning">backing off{backoff.until ? ` until ${new Date(backoff.until).toLocaleTimeString()}` : ""}</Badge> : null}
    </li>;
  })}</ul>;
}

function Activation({ data }: { data: VersionList }) {
 return <div className="space-y-3 [overflow-wrap:anywhere]">
  {data.versions.length ? data.versions.map(v => {
   const running = data.switches.versions.find(s => s.key === v.key)?.grab_state;
   return <div key={v.key} className="rounded border p-3">
   <h3 className="font-medium"><span className="font-mono">{v.key}</span> <span className="text-sm font-normal text-muted-foreground">{libraryLabel(v.key, v.config.profile, v.config.name)}</span></h3><p>Root: {v.config.library || (v.roots ?? []).join(", ") || "Not set"}</p>
   {running ? <VersionStatus state={running} showMode settingsKey={v.key} canManage /> : <p>State: Unknown</p>}
   {running && v.config.mode !== running.mode ? <p className="text-xs text-muted-foreground">Mode saved: {v.config.mode}, running: {running.mode}</p> : null}
   <p>Indexers: {(v.config.indexers ?? []).join(", ") || "None"} · Providers: {(v.config.providers ?? []).join(", ") || "None"}</p>
   <Link className="underline" to={`/admin/settings/libraries?edit=${encodeURIComponent(v.key)}`}>Edit {v.key}</Link>
  </div>; }) : <p>No libraries yet.</p>}
  <p>Pause: <strong>{data.switches.paused ? "On" : "Off"}</strong>{data.switches.pause_forced ? " (forced by THEATARR_FORCE_OFF)" : " · change it at the top of the page"}</p>
 </div>;
}

function AdminPassword({ onDone }: { onDone: () => void }) {
  const change = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [error, setError] = useState<string>();
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (next !== again) { setError("The new passwords do not match."); return; }
    setError(undefined);
    try { await change.mutateAsync({ current, new: next }); onDone(); } catch (failure) { setError(message(failure)); }
  }
  return <form className="grid max-w-md gap-2" onSubmit={(event) => void submit(event)}>
    <p>The admin password is still the generated one (initial-admin-password next to the database). Set your own.</p>
    <Field label="Current password"><Input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} /></Field>
    <Field label="New password"><Input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} /></Field>
    <Field label="New password again"><Input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} /></Field>
    {error ? <p role="alert" className="text-[var(--bad-ink)]">{error}</p> : null}
    <Button type="submit" className="w-fit" disabled={change.isPending || !current || !next}>Change password</Button>
  </form>;
}

type Draft = { key: string; kind: "movie" | "series"; library: string; profile: string; indexers: string[]; providers: string[] };

/** The profile choice: "preset:<trash_id>" builds a new profile from a TRaSH preset, else an existing profile key. */
function FirstVersion({ state, onDone }: { state: SetupState; onDone: (forcedOff?: string[]) => void }) {
  const versions = useVersions();
  if (versions.isPending) return <Loading />;
  if (versions.error) return <ErrorState error={versions.error} />;
  return <VersionForm data={versions.data} state={state} onDone={onDone} reload={async () => (await versions.refetch()).data} />;
}

function VersionForm({ data, state, onDone, reload }: { data: VersionList; state: SetupState; onDone: (forcedOff?: string[]) => void; reload: () => Promise<VersionList | undefined> }) {
  const [draft, setDraft] = useState<Draft>({ key: "movies", kind: "movie", library: "", profile: Object.keys(data.profiles)[0] ?? "", indexers: data.indexers, providers: data.providers });
  const flavour = draft.kind === "series" ? "sonarr" : "radarr";
  const presets = usePresets(flavour);
  const [root, setRoot] = useState<string>();
  const [review, setReview] = useState(false);
  const [effects, setEffects] = useState<Confirmation["effects"]>();
  const [pending, setPending] = useState<VersionBody>();
  // The preset profile this form already created ({key, preset}): a retry reuses it only for the same preset.
  const [made, setMade] = useState<{ key: string; preset: string }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  // The one-step start waiting for its confirmation: the request and what the server says it turns on.
  const [starting, setStarting] = useState<{ body: SetupStartBody; confirmation: WriteConfirmation }>();
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => { setDraft({ ...draft, [k]: v }); setReview(false); setEffects(undefined); setPending(undefined); };
  // A profile is picked for one kind: another kind starts without one, so a movie profile is never kept for a series
  // library unnoticed. Picking it again afterwards is a deliberate choice.
  const setKind = (kind: Draft["kind"]) => { if (kind === draft.kind) return; setDraft({ ...draft, kind, profile: "" }); setReview(false); setEffects(undefined); setPending(undefined); };
  const preset = draft.profile.startsWith("preset:") ? presets.data?.presets.find((p) => `preset:${p.trash_id}` === draft.profile) : undefined;
  const reuse = !!preset && made?.key === draft.key && made.preset === preset.trash_id;
  // Review names a profile as created only when Create makes it; an existing one is the profile the library uses.
  const newProfile = preset && !reuse ? `a new profile "${draft.key}" from the TRaSH preset ${preset.name} and ` : "";
  const profileUse = reuse ? ` with the profile "${draft.key}" created earlier from the TRaSH preset ${preset.name}`
    : preset ? "" : ` with the existing profile ${draft.profile}`;
  const missing = [
    !/^[a-z0-9][a-z0-9_-]*$/.test(draft.key) && "a key (lower case letters, digits, - and _)",
    !draft.library.startsWith("/") && "an absolute library root",
    !draft.profile && "a profile",
    !draft.indexers.length && "an indexer",
    !draft.providers.length && "a debrid provider",
    // The profile under this key was made from another preset: it cannot be made again, and using it would not be
    // the preset under review.
    !!preset && made?.key === draft.key && !reuse && `another key (the profile "${draft.key}" was already created from a different preset; pick it as "Profile: ${draft.key}" to use it)`,
  ].filter(Boolean) as string[];

  // Starting would do nothing without a source to search, a provider to add to and the mount to link from. It is
  // the library's own picks that count: a disabled indexer or provider can be picked, and does nothing.
  const readyProviders = state.ready_providers ?? [], readyIndexers = state.ready_indexers ?? [];
  const noTrash = state.trash === false;
  const blocked = [
    !readyProviders.length ? "no enabled debrid provider with an API key (step 2)"
      : !draft.providers.some((k) => readyProviders.includes(k)) && `none of the selected debrid providers is enabled with an API key (ready: ${readyProviders.join(", ")})`,
    !readyIndexers.length ? "no enabled indexer (step 2)"
      : !draft.indexers.some((k) => readyIndexers.includes(k)) && `none of the selected indexers is enabled and ready (ready: ${readyIndexers.join(", ")})`,
    (state.missing ?? []).some((m) => /no debrid mount/i.test(m)) && "the debrid mount is not connected (above)",
    noTrash && "the trash folder is not set, and writing library links needs it",
  ].filter(Boolean) as string[];

  const config = (mode: "shadow" | "active"): VersionConfig => ({
    mode, kind: draft.kind, anime: false, profile: preset ? draft.key : draft.profile, library: draft.library,
    storage: "debrid", indexers: draft.indexers, providers: draft.providers, client: "", import: "symlink", upgrades: "auto",
  });

  // The one-step start: one request creates the preset's profile, the active library and turns Pause off. The
  // server answers 422 with what that enables; confirming sends the same request again with the token.
  async function start(pendingStart?: { body: SetupStartBody; token: string }) {
    setBusy(true);
    setError(undefined);
    setStarting(undefined);
    setReview(false);
    let body = pendingStart?.body;
    try {
      if (!body) {
        const generation = (await reload())?.generation ?? data.generation;
        body = { generation, key: draft.key, roots: [draft.library], config: config("active"),
          ...(preset && !reuse ? { preset: { flavour, preset: preset.trash_id, modifiers: [] } } : {}) };
      }
      const started = await startSetup(pendingStart ? { ...body, confirmation_token: pendingStart.token } : body);
      onDone(started.forced_off ?? []);
    } catch (failure) {
      const confirmation = pendingStart ? undefined : confirmationOf(failure);
      if (confirmation && body) setStarting({ body, confirmation });
      else setError(message(failure));
    } finally { setBusy(false); }
  }

  async function check() {
    setRoot(undefined);
    try {
      const r = await checkRoot(data.generation, draft.key, draft.library, new AbortController().signal);
      // writable is probed only for an active library: false means not checked, so it is not shown.
      setRoot(`Root resolves to ${r.resolved}${r.writable ? " · writable" : ""}`);
    } catch (failure) { setRoot(message(failure)); }
  }

  // A preset profile is created once: a retry after a failed version save reuses it while key and preset are unchanged.
  async function create() {
    setBusy(true);
    setError(undefined);
    try {
      let generation = (await reload())?.generation ?? data.generation;
      if (preset && !reuse) {
        generation = (await createFromPreset({ generation, key: draft.key, flavour, preset: preset.trash_id, modifiers: [] })).generation;
        setMade({ key: draft.key, preset: preset.trash_id });
      }
      await submit({ generation, key: draft.key, roots: [draft.library], config: config("shadow") });
    } catch (failure) { setError(message(failure)); }
    finally { setBusy(false); }
  }

  // A save that needs confirmation answers 422 with its effects; Confirm gets a token for the same body and sends it.
  async function submit(body: VersionBody) {
    try {
      await saveVersion(body, true);
    } catch (failure) {
      const list = failure instanceof ApiError && failure.status === 422 ? (failure.body as { effects?: Confirmation["effects"] } | undefined)?.effects : undefined;
      if (!body.confirmation_token && list?.length) { setPending(body); setEffects(list); return; }
      throw failure;
    }
    onDone();
  }

  async function confirmEffects() {
    if (!pending) return;
    setBusy(true);
    setError(undefined);
    try {
      const proof = await confirmVersion(pending);
      setEffects(undefined);
      setPending(undefined);
      await submit({ ...pending, confirmation_token: proof.token });
    } catch (failure) { setError(message(failure)); }
    finally { setBusy(false); }
  }

  return <div className="grid max-w-xl gap-3" data-testid="first-version">
    <p>Each library has its own settings: a kind, a profile, a root it links into, the indexers it searches and the providers it adds to. Create it and start downloading at once, or create it as <strong>Watch only</strong>: Theatarr then searches, downloads and links nothing for it until you make it Live.</p>
    <Field label="Key"><Input value={draft.key} onChange={(e) => set("key", e.target.value)} /></Field>
    <Field label="Kind"><select className={SELECT_CLASS} value={draft.kind} onChange={(e) => setKind(e.target.value as Draft["kind"])}><option value="movie">Movies</option><option value="series">Series</option></select></Field>
    <Field label="Library root (absolute path the library links into; not inside the debrid mount)">
      <div className="flex gap-2"><Input value={draft.library} placeholder="/media/movies" onChange={(e) => { set("library", e.target.value); setRoot(undefined); }} /><Button type="button" variant="outline" disabled={!draft.library.startsWith("/")} onClick={() => void check()}>Check root</Button></div>
    </Field>
    {root ? <p data-testid="root-check">{root}</p> : null}
    <Field label="Quality profile">
      <select className={SELECT_CLASS} value={draft.profile} onChange={(e) => set("profile", e.target.value)}>
        <option value="">Pick a profile</option>
        {presets.data?.presets.map((p) => <option key={p.trash_id} value={`preset:${p.trash_id}`}>TRaSH preset: {p.name}</option>)}
        {Object.keys(data.profiles).map((k) => <option key={k} value={k}>Profile: {k}</option>)}
      </select>
    </Field>
    {presets.data?.unavailable ? <p data-testid="presets-off">{presets.data.unavailable}. Pick an existing profile.</p> : presets.error ? <p data-testid="presets-off">{message(presets.error)}</p> : null}
    <Checks label="Indexers" choices={data.indexers} values={draft.indexers} onChange={(v) => set("indexers", v)} empty="Add an indexer first (step 2)." />
    <Checks label="Debrid providers" choices={data.providers} values={draft.providers} onChange={(v) => set("providers", v)} empty="Add a provider first (step 2)." />
    {state.profiles === 0 && !presets.data?.presets.length ? <p>There is no profile yet. Create one in the <Link className="underline" to="/admin/settings/profiles?raw=1">raw profile editor</Link>.</p> : null}
    {missing.length ? <p>Still needed: {missing.join(", ")}.</p> : null}
    <div className="space-y-1" data-testid="one-step-start">
      <Button type="button" className="w-fit" disabled={busy || missing.length > 0 || blocked.length > 0} onClick={() => void start()}>Create library and start downloading</Button>
      {blocked.length ? <p data-testid="start-blocked">Starting is not possible yet: {blocked.join("; ")}.{noTrash ? <> Set the trash folder under <Link className="underline" to={settingHref("library", "trash")!}>Settings › Libraries › File management › Trash</Link>.</> : null}</p>
        : <p className="text-muted-foreground">Sets the library to Live and turns Pause off for the whole install. You confirm the list first.</p>}
    </div>
    {!review
      ? <Button type="button" variant="outline" className="w-fit" disabled={missing.length > 0} onClick={() => setReview(true)}>Create as Watch only (decide later)</Button>
      : <div className="space-y-2 rounded-md border border-border p-3" data-testid="version-review">
        <p>This creates {newProfile}the library <strong>{draft.key}</strong> ({draft.kind}){profileUse} as Watch only, linking into {draft.library}, searching {draft.indexers.join(", ")} and adding to {draft.providers.join(", ")}. It downloads nothing until you make it Live on Libraries.</p>
        {effects?.length ? <><p>The settings API asks to confirm:</p><ul className="list-disc pl-5">{effects.map((e, i) => <li key={i}>{e.version}: {e.effect}</li>)}</ul><Button type="button" disabled={busy} onClick={() => void confirmEffects()}>Confirm and create</Button></>
          : <Button type="button" disabled={busy} onClick={() => void create()}>Create library</Button>}
      </div>}
    {error ? <p role="alert" className="text-[var(--bad-ink)]">{error}</p> : null}
    {starting ? <EffectsConfirmDialog
      testid="start-confirm"
      effects={starting.confirmation.effects}
      library={draft.key}
      scope={{
        title: "Create the library and start downloading",
        does: [],
        keeps: ["Send notifications stays as it is.", "Environment force-off overrides stay in force."],
        confirm: "Confirm and start",
      }}
      onClose={() => setStarting(undefined)}
      onConfirm={() => void start({ body: starting.body, token: starting.confirmation.token })}
    >
      <p>In plain words: the library <strong>{draft.key}</strong> is created Live{starting.body.preset ? ` with a new profile "${draft.key}" from the TRaSH preset ${preset?.name ?? starting.body.preset.preset}` : ""}; and for the whole install, Pause is turned off.</p>
    </EffectsConfirmDialog> : null}
  </div>;
}

function Checks({ label, choices, values, onChange, empty }: { label: string; choices: string[]; values: string[]; onChange: (v: string[]) => void; empty: string }) {
  return <fieldset className="space-y-1"><legend className="text-sm">{label}</legend>
    {choices.length ? <div className="flex flex-wrap gap-3">{choices.map((k) => <label key={k} className="flex items-center gap-1 text-sm"><input type="checkbox" checked={values.includes(k)} onChange={(e) => onChange(choices.filter((c) => c === k ? e.target.checked : values.includes(c)))} />{k}</label>)}</div> : <p className="text-sm">{empty}</p>}
  </fieldset>;
}

const tone = (s: PreflightItem["status"]) => s === "ok" ? "positive" : s === "info" ? "info" : s === "warn" ? "warning" : "negative";

function Preflight({ generation, report, last }: { generation?: number; report?: PreflightReport; last?: LastPreflight }) {
  const run = usePreflight(generation);
  const [sources, setSources] = useState(true);
  const r = report;
  return <>
    <p>Checks the debrid mount, library roots and trash, provider credentials (a read-only account call), indexers and their search types, metadata keys, the arr views, Plex and the VPN egress. The same report: <code>theatarr preflight</code>.</p>
    <label className="flex items-center gap-2"><input type="checkbox" checked={sources} onChange={(e) => setSources(e.target.checked)} />Test providers, indexers and Plex (outbound calls)</label>
    <Button type="button" className="w-fit" disabled={run.isPending} onClick={() => run.mutate(sources)}>{run.isPending ? "Running…" : "Run preflight"}</Button>
    {run.error ? <p role="alert">{message(run.error)}</p> : null}
    {!r && last ? <p data-testid="preflight-last">Last run {fmtTime(last.at)}{last.sources ? "" : " (without the provider, indexer and Plex tests)"}: {last.ok} ok · {last.warn} warn · {last.fail} fail. Run it again to see each check.</p> : null}
    {r ? <>
      <p data-testid="preflight-summary">{r.ok} ok · {r.warn} warn · {r.fail} fail</p>
      <Table>
        <TableHeader><TableRow><TableHead>Check</TableHead><TableHead>Status</TableHead><TableHead>Why</TableHead><TableHead>Next step</TableHead></TableRow></TableHeader>
        <TableBody>{r.items.map((it, i) => <TableRow key={i} data-testid={`preflight-${it.area}-${it.check}`}>
          <TableCell className="font-mono text-[13px]">{it.area} · {it.check}</TableCell>
          <TableCell><Badge tone={tone(it.status)}>{it.status}</Badge></TableCell>
          <TableCell>{it.why}</TableCell>
          <TableCell>{it.next ?? ""}</TableCell>
        </TableRow>)}</TableBody>
      </Table>
    </> : null}
  </>;
}
