// Admin › Setup (docs/quickstart.md, docs/design/web.md "Setup"): the first-run screen. It leads a fresh install
// through the admin password, the debrid providers, the indexers (Prowlarr feeds as Torznab and the native catalog),
// the TMDb key, a library root and a first version from a preset, then the preflight. Everything it creates goes through the
// settings API; the version starts in Shadow with its downloads off, so nothing it does can grab.
import { useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { usePreflight, type LastPreflight, type PreflightItem, type PreflightReport, type SetupState } from "@/api/setup";
import { useSetupProgress } from "@/api/useSetupProgress";
import { libraryLabel } from "@/lib/library-label";
import { settingHref } from "@/lib/settings-map";
import { createFromPreset, usePresets } from "@/api/settings-profiles";
import { checkRoot, confirmVersion, saveVersion, useVersions, type Confirmation, type VersionBody, type VersionList } from "@/api/settings-versions";
import { useChangePassword } from "@/api/users";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { globalDownloadsOff } from "@/lib/version-grab";
import { ErrorState, Loading, PageHeader } from "@/components/page";
import { SELECT_CLASS } from "@/components/settings/parts";
import { TMDbKey } from "@/components/settings/tmdb-key";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtTime } from "@/lib/format";

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
  if (setup.isPending) return <Loading />;
  if (setup.error) return <ErrorState error={setup.error} />;
  const s = setup.data;
  const refresh = () => { void setup.refetch(); void progress.versions.refetch(); };
  // An older server sent null for a complete install.
  const missing = s.missing ?? [];
  return <div className="space-y-4">
    <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Setup" description="What this install still needs, step by step, then a preflight of everything it relies on." />
    <Card className="space-y-2 text-sm"><h2 className="text-base font-semibold">Coming from Sonarr/Radarr?</h2><p>Inspect your existing stack and bring its settings into Theatarr, disabled or in shadow.</p><Link className="underline" to="/admin/advanced/arr/migrate">Migrate from arrs</Link></Card>
    {missing.length ? <Card><p className="text-sm font-medium">Off until set up:</p><ul className="mt-1 list-disc pl-5 text-sm" data-testid="setup-missing">{missing.map((m) => <li key={m}>{m}</li>)}</ul></Card> : null}
    <p>Getting started: {progress.done} of {progress.total} steps complete</p>
    <progress className="h-2 w-full" aria-label="Setup progress" value={progress.done} max={progress.total} />
    <ol className="space-y-3">
      <Step n={1} title="Secure the admin account" done={progress.steps[0]}>
        {s.admin_password_generated ? <AdminPassword onDone={refresh} /> : <p>The admin account has its own password.</p>}
      </Step>
      <Step n={2} title="Connect downloads" done={progress.steps[1]}>
        <h3>Debrid providers <Badge tone={s.providers > 0 ? "positive" : "neutral"}>{s.providers > 0 ? "Done" : "Not yet"}</Badge></h3>
        <p>{s.providers ? `${s.providers} enabled ${s.providers === 1 ? "provider" : "providers"} with an API key.` : "No enabled debrid provider with an API key yet: nothing can be added to debrid."} Add Real-Debrid, AllDebrid or TorBox with its API key under <Link className="underline" to="/admin/settings/downloads">Settings › Download clients</Link>; the key is tested with a read-only account call.</p>
        <h3>Indexers <Badge tone={s.indexers > 0 ? "positive" : "neutral"}>{s.indexers > 0 ? "Done" : "Not yet"}</Badge></h3>
        <p>{s.indexers ? `${s.indexers} enabled ${s.indexers === 1 ? "indexer" : "indexers"} ready to search.` : "No enabled indexer yet (Torznab and DMM also need their key): nothing can be searched."} Both ways work, and they mix:</p>
        <ul className="list-disc pl-5">
          <li><strong>Keep Prowlarr:</strong> add each Prowlarr feed as a Torznab indexer (Built-in sources), or use Migrate from Prowlarr once, under <Link className="underline" to="/admin/settings/indexers">Settings › Indexers › Add indexer</Link>.</li>
          <li><strong>Native catalog:</strong> pick indexers from the catalog (Cardigann definitions) or the built-in sources (DMM, Torrentio, Zilean, SeaDex) in the same sheet.</li>
        </ul>
        <p>The VPN check is optional and off by default. Once set up under <Link className="underline" to="/admin/settings/indexers/vpn">Settings › Indexers › VPN</Link>, requests of the traffic it requires (indexers unless you change it) are refused while the VPN exit is not confirmed.</p>
      </Step>
      <Step n={3} title="Add metadata (TMDb)" done={progress.steps[2]}>
        {s.tmdb
          ? <p>TMDb is set. Its sources and key, TVDB and the artwork cache are under <Link className="underline" to="/admin/settings/metadata">Settings › Metadata &amp; artwork</Link>.</p>
          : <><p>Without a TMDb key, search and adding titles only see what the library already has, and there is no artwork or release date. TVDB (optional, for series numbering and anime) is under <Link className="underline" to="/admin/settings/metadata">Settings › Metadata &amp; artwork</Link>.</p><TMDbKey onSaved={refresh} /></>}
      </Step>
      <Step n={4} title="Connect your library" done={progress.steps[3]}>
        <p>Connect the debrid mount under <Link className="underline" to={settingHref("library", "mount")!}>Settings › Libraries › File management</Link>, then the Plex URL and token under <Link className="underline" to={settingHref("library", "plex")!}>Settings › Plex &amp; playback</Link>.</p>
        <p>Arr views and notifications are under <Link className="underline" to="/admin/settings">Admin › Settings</Link>.</p>
        <h3 className="font-medium">Create your first library</h3>

        {s.versions > 0
          ? <p>{s.versions} {s.versions === 1 ? "library" : "libraries"}, {s.versions_with_roots} with a library root. Edit them under <Link className="underline" to="/admin/settings/libraries">Libraries</Link>.</p>
          : <FirstVersion state={s} onDone={refresh} />}
        <DownloadsOffNotice off={globalDownloadsOff((s.version_states ?? []).map((v) => v.grab_state))} canManage />
        <div className="space-y-1">{(s.version_states ?? []).map((v) => <div key={v.key} className="flex flex-wrap items-start gap-2"><span className="font-mono text-[13px]">{v.key}</span><VersionStatus state={v.grab_state} showMode settingsKey={v.key} canManage /></div>)}</div>
      </Step>
      <Step n={5} title="Check readiness" done={progress.steps[4]}>
        <Preflight generation={progress.versions.data?.generation} report={progress.report} last={progress.last} />
        <Link className="underline" to="/admin/advanced/journal">See example decisions</Link>
      </Step>
      <Step n={6} title="Review activation" done={progress.steps[5]}>
        <p>Theatarr doesn't turn these on for you. When you're ready, set a library to Active, turn on Allow downloads for this library, and Allow new downloads.</p>
        {progress.versions.isPending ? <Loading /> : progress.versions.error ? <ErrorState error={progress.versions.error} /> : <Activation data={progress.versions.data} />}
      </Step>
    </ol>
  </div>;
}

function Activation({ data }: { data: VersionList }) {
 return <div className="space-y-3 [overflow-wrap:anywhere]">
  {data.versions.length ? data.versions.map(v => {
   const running = data.switches.versions.find(s => s.key === v.key)?.grab_state;
   return <div key={v.key} className="rounded border p-3">
   <h3 className="font-medium"><span className="font-mono">{v.key}</span> <span className="text-sm font-normal text-muted-foreground">{libraryLabel(v.key, v.config.profile)}</span></h3><p>Root: {v.config.library || (v.roots ?? []).join(", ") || "Not set"}</p>
   {running ? <VersionStatus state={running} showMode settingsKey={v.key} canManage /> : <p>State: Unknown</p>}
   {running && v.config.mode !== running.mode ? <p className="text-xs text-muted-foreground">Mode saved: {v.config.mode}, running: {running.mode}</p> : null}
   {running && !!v.config.grab !== running.grab ? <p className="text-xs text-muted-foreground">Allow downloads for this library saved: {v.config.grab ? "On" : "Off"}, running: {running.grab ? "On" : "Off"}</p> : null}
   <p>Indexers: {(v.config.indexers ?? []).join(", ") || "None"} · Providers: {(v.config.providers ?? []).join(", ") || "None"}</p>
   <Link className="underline" to={`/admin/settings/libraries?edit=${encodeURIComponent(v.key)}`}>Edit {v.key}</Link>
  </div>; }) : <p>No libraries yet.</p>}
  <ul className="space-y-2">{([
   ["Allow new downloads", "router", data.switches.router_enabled],
   ["Write library links", "library", data.switches.library_enabled],
   ["Send notifications", "notify", data.switches.notify_enabled],
  ] as const).map(([label, section, on]) => <li key={section}>{label}: <strong>{on ? "On" : "Off"}</strong>{data.switches.sources?.[section] ? ` (${data.switches.sources[section]})` : ""} · <Link className="underline" to={settingHref(section, "enabled")!}>Change {label.toLowerCase()}</Link></li>)}</ul>
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
function FirstVersion({ state, onDone }: { state: SetupState; onDone: () => void }) {
  const versions = useVersions();
  if (versions.isPending) return <Loading />;
  if (versions.error) return <ErrorState error={versions.error} />;
  return <VersionForm data={versions.data} state={state} onDone={onDone} reload={async () => (await versions.refetch()).data} />;
}

function VersionForm({ data, state, onDone, reload }: { data: VersionList; state: SetupState; onDone: () => void; reload: () => Promise<VersionList | undefined> }) {
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
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => { setDraft({ ...draft, [k]: v }); setReview(false); setEffects(undefined); setPending(undefined); };
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
      await submit({ generation, key: draft.key, roots: [draft.library], config: {
        mode: "shadow", grab: false, kind: draft.kind, anime: false, profile: preset ? draft.key : draft.profile, library: draft.library,
        storage: "debrid", indexers: draft.indexers, providers: draft.providers, client: "", import: "symlink", upgrades: "auto", follows: "",
      } });
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
    <p>Each library has its own settings: a kind, a profile, a root it links into, the indexers it searches and the providers it adds to. It starts in <strong>Shadow</strong> mode (Preview only): it decides and records what it would download, but downloads nothing and writes nothing until you make it Active.</p>
    <VersionStatus showMode state={{ mode: "shadow", grab: false, library_enabled: data.switches.library_enabled, router_enabled: data.switches.router_enabled, router_forced_off: data.switches.sources?.router === "forced off by environment" }} />
    <Field label="Key"><Input value={draft.key} onChange={(e) => set("key", e.target.value)} /></Field>
    <Field label="Kind"><select className={SELECT_CLASS} value={draft.kind} onChange={(e) => set("kind", e.target.value as Draft["kind"])}><option value="movie">Movies</option><option value="series">Series</option></select></Field>
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
    {presets.data?.unavailable ? <p data-testid="presets-off">{presets.data.unavailable}. Pick an existing profile, or put a TRaSH-Guides checkout in THEATARR_TRASH and reload.</p> : presets.error ? <p data-testid="presets-off">{message(presets.error)}</p> : null}
    <Checks label="Indexers" choices={data.indexers} values={draft.indexers} onChange={(v) => set("indexers", v)} empty="Add an indexer first (step 2)." />
    <Checks label="Debrid providers" choices={data.providers} values={draft.providers} onChange={(v) => set("providers", v)} empty="Add a provider first (step 2)." />
    {state.profiles === 0 && !presets.data?.presets.length ? <p>There is no profile yet. Create one in the <Link className="underline" to="/admin/settings/profiles?raw=1">raw profile editor</Link>, or put a TRaSH-Guides checkout in THEATARR_TRASH for the presets.</p> : null}
    {missing.length ? <p>Still needed: {missing.join(", ")}.</p> : null}
    {!review
      ? <Button type="button" className="w-fit" disabled={missing.length > 0} onClick={() => setReview(true)}>Review</Button>
      : <div className="space-y-2 rounded-md border border-border p-3" data-testid="version-review">
        <p>This creates {newProfile}the library <strong>{draft.key}</strong> ({draft.kind}){profileUse} in Shadow mode with its downloads off, linking into {draft.library}, searching {draft.indexers.join(", ")} and adding to {draft.providers.join(", ")}. It downloads nothing until you make it Active on Libraries.</p>
        {effects?.length ? <><p>The settings API asks to confirm:</p><ul className="list-disc pl-5">{effects.map((e, i) => <li key={i}>{e.version}: {e.effect}</li>)}</ul><Button type="button" disabled={busy} onClick={() => void confirmEffects()}>Confirm and create</Button></>
          : <Button type="button" disabled={busy} onClick={() => void create()}>Create library</Button>}
      </div>}
    {error ? <p role="alert" className="text-[var(--bad-ink)]">{error}</p> : null}
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
