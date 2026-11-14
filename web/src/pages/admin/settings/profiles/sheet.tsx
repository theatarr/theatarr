// The profile editor sheet (docs/design/settings.md, "Profiles, presets and formats"): who the profile affects, the
// preset it started from with "Compare with TRaSH", the quality ladder with its cutoff, size limits per quality, and the scores grouped as
// TRaSH groups them, each with its score state. An edit of a saved profile shows its decision impact before it is saved;
// a change applies to the next searches and upgrades of the Live libraries using it. A new profile starts from a TRaSH preset.
import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { buildPreset, MODIFIER_LABEL, originLabel, profileImpact, usePresets, useQualityOverview, type Built, type ProfileImpact, type ProfileOrigin, type ProfileSummary, type ScoreRow } from "@/api/settings-profiles";
import { useSettingsEditor } from "@/components/settings/use-settings-editor";
import { ExplainBox, SELECT_CLASS } from "@/components/settings/parts";
import { ErrorState, Loading } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Sheet, SheetLayout } from "@/components/ui/sheet";
import { explainError } from "@/lib/settings";
import { cn } from "@/lib/utils";
import { FormGrid, NumberInput, PROPERS, PROPERS_LABELS, Select, TextField, Toggle, object, type Config } from "../quality/fields";
import { DiffLists, QualityLadder, StateTag } from "./parts";
import { profileFlavours, SizeLimits } from "./sizes";
import { useLibraryName } from "@/lib/library-names";

export type ProfileSession =
  | { mode: "edit"; key: string; id?: number; generation: number; config: Config; summary: ProfileSummary }
  | { mode: "create"; generation: number; flavour: string; key: string; config?: Config; preset?: { trash_id: string; modifiers: string[]; built: Built } };

type Row = { format: string; score: number; trash: number | null; state: string; group: string; saved?: number };
const OTHER = "Other formats";

/** The score rows of a draft: states and groups from the saved profile, or from the preset a new one is built from. */
function rowsOf(config: Config, summary?: ProfileSummary, built?: Built): Row[] {
  const scores = object(config.formats) as Record<string, number>;
  const saved = new Map<string, ScoreRow>((summary?.scores ?? []).map((r) => [r.format, r]));
  const trash = object(built?.profile.formats) as Record<string, number>;
  return Object.keys(scores).sort((a, b) => a.localeCompare(b)).map((format) => {
    const s = saved.get(format);
    if (s) return { format, score: scores[format], trash: s.trash ?? null, state: s.state, group: s.group || OTHER, saved: s.score };
    if (built && format in trash) {
      const unscored = built.unscored.includes(format);
      return { format, score: scores[format], trash: unscored ? null : trash[format], state: unscored ? "no_score" : scores[format] === trash[format] ? "follows" : "pinned", group: built.groups[format] || OTHER };
    }
    return { format, score: scores[format], trash: null, state: "ours", group: OTHER };
  });
}

function ScoreGroups({ rows, readOnly, onScore }: { rows: Row[]; readOnly: boolean; onScore: (format: string, score: number) => void }) {
  const groups = useMemo(() => {
    const m = new Map<string, Row[]>();
    for (const r of rows) m.set(r.group, [...(m.get(r.group) ?? []), r]);
    // Our formats last; the others by name.
    return [...m.entries()].sort(([a], [b]) => (a === "Our formats" ? 1 : b === "Our formats" ? -1 : a.localeCompare(b)));
  }, [rows]);
  if (!rows.length) return <p className="text-sm text-muted-foreground">No format is scored.</p>;
  return <div className="space-y-2" data-testid="score-groups">
    {groups.map(([group, list]) => {
      const ours = list.filter((r) => r.state === "pinned" || r.state === "differs").length;
      return <details key={group} open className="rounded-lg border border-[var(--line)]" data-group={group}>
        <summary className="cursor-pointer select-none px-3 py-2 text-sm font-medium [overflow-wrap:anywhere]">{group} <span className="font-normal text-muted-foreground">· {list.length} {list.length === 1 ? "format" : "formats"}{ours ? ` · ${ours} not following TRaSH` : ""}</span></summary>
        <ul className="divide-y divide-[var(--line)] border-t border-[var(--line)]">
          {list.map((r) => <li key={r.format} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5" data-format={r.format}>
            <Link className="min-w-0 flex-1 basis-40 text-sm underline decoration-[var(--line)] [overflow-wrap:anywhere]" to={`/admin/settings/formats?filter=${encodeURIComponent(r.format)}`} title={`The definition of ${r.format}`}>{r.format}</Link>
            <span className="flex flex-wrap items-center gap-1.5">
              <StateTag state={r.state} />
              {r.saved !== undefined && r.saved !== r.score ? <Badge tone="warning">edited</Badge> : null}
              {r.trash !== null ? <span className="text-xs tabular-nums text-muted-foreground">TRaSH {r.trash}</span> : null}
            </span>
            <NumberInput className="h-8 w-24" value={String(r.score)} disabled={readOnly} aria-label={`Score for ${r.format}`} onChange={(v) => onScore(r.format, Number(v))} />
          </li>)}
        </ul>
      </details>;
    })}
  </div>;
}

/** Adds a standalone custom format of the profile's flavour that the profile does not score yet, at score 0. With a
 * format set, the save adds the format to the set too (only the set's formats count). */
function AddFormat({ flavour, scored, disabled, onAdd }: { flavour: string; scored: Record<string, unknown>; disabled: boolean; onAdd: (format: string) => void }) {
  const overview = useQualityOverview();
  const [chosen, setChosen] = useState("");
  const keys = (overview.data?.formats ?? []).filter((f) => f.key && (f.flavour || "radarr") === flavour && !(f.key in scored)).map((f) => f.key!).sort((a, b) => a.localeCompare(b));
  if (!keys.length) return null;
  return <div className="flex flex-wrap items-end gap-2" data-testid="add-format">
    <label className="flex flex-col gap-1 text-sm">Add a custom format
      <select className={SELECT_CLASS} value={chosen} disabled={disabled} onChange={(e) => setChosen(e.target.value)}>
        <option value="">Choose a format</option>
        {keys.map((k) => <option key={k} value={k}>{k}</option>)}
      </select>
    </label>
    <Button type="button" variant="outline" disabled={disabled || !keys.includes(chosen)} onClick={() => { onAdd(chosen); setChosen(""); }}>Add format</Button>
  </div>;
}

function ImpactView({ impact }: { impact: ProfileImpact }) {
  const i = impact.impact;
  const nameOf = useLibraryName();
  return <div className="space-y-2 rounded-lg border border-[var(--th-info-border)] bg-[var(--accent-soft)] p-3 text-sm text-[var(--accent-ink)]" data-testid="profile-impact" role="status">
    {!impact.available ? <p>Impact not available: {impact.reason}. You can still save.</p> : i.changed === 0 ? <p>No file in the library would be decided differently ({i.files} checked in the libraries this change affects).</p> : <p>
      {i.changed} {i.changed === 1 ? "file" : "files"} in the library would be decided differently (of {i.files} checked in the libraries this change affects): {i.score_changed} score {i.score_changed === 1 ? "change" : "changes"}, {i.cutoff_changed} cutoff {i.cutoff_changed === 1 ? "change" : "changes"}, {i.upgrade_changed} upgrade {i.upgrade_changed === 1 ? "decision" : "decisions"}.
    </p>}
    {impact.available && i.examples.length ? <ul className="space-y-0.5">{i.examples.slice(0, 6).map((e, n) => <li key={n} className="[overflow-wrap:anywhere]">{e.title} <span className="opacity-80">({nameOf(e.version)})</span>: score {e.before_score} → {e.after_score}{e.before_upgrade !== e.after_upgrade ? (e.after_upgrade ? " · now upgrades" : " · stops upgrading") : ""}{e.before_cutoff_met !== e.after_cutoff_met ? (e.after_cutoff_met ? " · cutoff met" : " · cutoff no longer met") : ""}</li>)}</ul> : null}
    <p className="opacity-90">Nothing is grabbed or searched by saving: decisions change from the next search or upgrade check.</p>
  </div>;
}

/** Pick a TRaSH preset and its modifiers; Continue builds it and opens the editor. */
function PresetPicker({ flavour, setFlavour, onBuilt, onCancel }: { flavour: string; setFlavour: (f: string) => void; onBuilt: (trashID: string, modifiers: string[], built: Built) => void; onCancel: () => void }) {
  const presets = usePresets(flavour);
  const [chosen, setChosen] = useState("");
  const [mods, setMods] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const preset = presets.data?.presets.find((p) => p.trash_id === chosen);
  const go = async () => {
    if (!preset) return;
    setBusy(true); setError(undefined);
    try { onBuilt(preset.trash_id, mods, await buildPreset(flavour, preset.trash_id, mods)); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return <div className="space-y-4" data-testid="preset-picker">
    <p className="text-sm text-muted-foreground">A new profile starts from a TRaSH quality profile and the formats of its default groups, scored as TRaSH scores them. Those scores then follow TRaSH; the ones you change are yours.</p>
    <fieldset className="flex flex-wrap gap-3 text-sm"><legend className="mb-1 font-medium">Arr flavour</legend>
      {["radarr", "sonarr"].map((f) => <label key={f} className="flex items-center gap-1.5"><input type="radio" name="flavour" checked={flavour === f} onChange={() => { setFlavour(f); setChosen(""); setMods([]); }} />{f === "radarr" ? "Movies (Radarr)" : "Series (Sonarr)"}</label>)}
    </fieldset>
    {presets.isPending ? <Loading /> : presets.error ? <ErrorState error={presets.error} /> : presets.data.unavailable ? <p className="text-sm text-muted-foreground" data-testid="presets-off">{presets.data.unavailable}.</p> : <fieldset className="space-y-2"><legend className="mb-1 text-sm font-medium">TRaSH preset</legend>
      {presets.data.presets.map((p) => <label key={p.trash_id} className={cn("flex cursor-pointer gap-3 rounded-lg border p-3", chosen === p.trash_id ? "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_8%,transparent)]" : "border-[var(--line)]")} data-preset={p.trash_id}>
        <input type="radio" name="preset" className="mt-1" aria-label={p.name} aria-describedby={`preset-${p.trash_id}`} checked={chosen === p.trash_id} onChange={() => { setChosen(p.trash_id); setMods(mods.filter((m) => p.modifiers.includes(m))); }} />
        <span className="min-w-0 space-y-0.5 text-sm">
          <span className="block font-medium">{p.name}</span>
          <span className="block text-muted-foreground [overflow-wrap:anywhere]">{p.qualities.join(" › ")} · cutoff {p.cutoff} · {p.formats} formats</span>
          {p.description ? <span className="block text-xs text-muted-foreground [overflow-wrap:anywhere]">{p.description}</span> : null}
        </span>
      </label>)}
    </fieldset>}
    {preset && preset.modifiers.length ? <fieldset className="space-y-1.5"><legend className="mb-1 text-sm font-medium">Modifiers</legend>
      {preset.modifiers.map((m) => <label key={m} className="flex items-center gap-2 text-sm"><input type="checkbox" checked={mods.includes(m)} onChange={(e) => setMods(e.target.checked ? [...mods, m] : mods.filter((x) => x !== m))} />{MODIFIER_LABEL[m] ?? m}</label>)}
    </fieldset> : null}
    {error ? <ExplainBox error={explainError(error)} /> : null}
    <div className="flex flex-wrap gap-2"><Button type="button" disabled={!preset || busy} onClick={() => void go()}>{busy ? "Building…" : "Continue to editor"}</Button><Button type="button" variant="outline" onClick={onCancel}>Cancel</Button></div>
  </div>;
}

export function ProfileSheet({ session: initial, readOnly, onClose, onSaved, onReload }: { session: ProfileSession; readOnly: boolean; onClose: () => void; onSaved: (message: string, deleted?: boolean) => void; onReload: () => Promise<ProfileSession | undefined> }) {
  const nameOf = useLibraryName();
  const editor = useSettingsEditor({
    initial,
    rebase: () => onReload(),
    gone: { kind: "not_found", title: "Profile deleted elsewhere", message: "This profile was deleted. Close this editor." },
    onReload: (next, keep) => {
      setImpact(undefined);
      if (!keep && next.mode === "edit") setConfig(next.config);
    },
  });
  const { base: session, setBase: setSession, error, setError, busy, setBusy, reload, confirm, failure } = editor;
  const [config, setConfig] = useState<Config | undefined>(initial.mode === "edit" ? initial.config : initial.config);
  const [flavour, setFlavour] = useState(initial.mode === "create" ? initial.flavour : "radarr");
  const [compare, setCompare] = useState(false);
  const [sizesInvalid, setSizesInvalid] = useState(false);
  const [impact, setImpact] = useState<ProfileImpact>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const errorBox = useRef<HTMLDivElement>(null);
  useEffect(() => { if (error) errorBox.current?.querySelector<HTMLElement>("[role=alert]")?.focus(); }, [error]);
  const edit = session.mode === "edit" ? session : undefined;
  const create = session.mode === "create" ? session : undefined;
  const built = create?.preset?.built;
  const summary = edit?.summary;
  const origin: ProfileOrigin | null | undefined = edit ? summary?.origin : built?.origin;
  const change = (next: Config) => { setConfig(next); setImpact(undefined); };
  const setField = (key: string, value: unknown) => config && change({ ...config, [key]: value });
  const rows = config ? rowsOf(config, summary, built) : [];
  const used = summary?.used_by ?? [];
  const active = used.filter((u) => u.active);

  const save = () => {
    if (!config) return;
    if (edit) {
      const body = { generation: edit.generation, key: edit.key, config };
      void editor.save(
        { path: `/settings/profiles/${edit.id}`, method: "PUT", body },
        () => onSaved(`${edit.key} saved.`),
      );
    } else if (create?.preset) {
      const body = { generation: create.generation, key: create.key, flavour: create.flavour, preset: create.preset.trash_id, modifiers: create.preset.modifiers, config };
      void editor.save(
        { path: "/settings/presets/create", method: "POST", body },
        () => onSaved(`${create.key} created from ${create.preset!.built.origin.name}.`),
      );
    }
  };
  // An edit of a saved profile shows its impact first; Save then saves exactly what was previewed.
  const review = async () => {
    if (!edit || !config) return;
    setBusy(true); setError(undefined);
    try { setImpact(await profileImpact(edit.generation, edit.key, config)); } catch (e) { editor.reportError(e); } finally { setBusy(false); }
  };
  const remove = async () => {
    if (!edit) return;
    await editor.save(
      { path: `/settings/profiles/${edit.id}?generation=${edit.generation}`, method: "DELETE", body: { generation: edit.generation } },
      () => onSaved(`${edit.key} deleted.`, true),
    );
    setConfirmDelete(false);
  };

  const picking = !!create && !create.preset;
  const title = picking ? "New profile from a TRaSH preset" : create ? `New profile · ${originLabel(create.preset!.built.origin)}` : `Edit profile ${edit!.key}`;
  const footer = picking ? undefined : <>
    {edit && !impact ? <Button type="button" disabled={busy || readOnly || sizesInvalid} onClick={() => void review()}>{busy ? "Checking…" : "Review and save"}</Button> : null}
    {edit && impact ? <><Button type="button" disabled={busy || readOnly || sizesInvalid} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button><Button type="button" variant="outline" onClick={() => setImpact(undefined)}>Back to editing</Button></> : null}
    {create ? <Button type="button" disabled={busy || readOnly || sizesInvalid || !create.key.trim()} onClick={() => void save()}>{busy ? "Creating…" : "Create profile"}</Button> : null}
    <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
    {edit && used.length === 0 && edit.id ? <Button type="button" variant="destructive" className="ml-auto" disabled={busy || readOnly} onClick={() => setConfirmDelete(true)}>Delete</Button> : null}
  </>;

  return <Sheet label={picking ? "New profile from a TRaSH preset" : create ? "New profile" : `Edit profile ${edit!.key}`} onClose={onClose} initialFocus={heading}>
    <SheetLayout testid="profile-sheet" headingRef={heading} onClose={onClose} title={title}
      subtitle={edit ? `${summary?.flavour || "flavour unknown"} · ${summary?.formats ?? 0} formats` : create?.preset ? `${create.flavour} · ${create.preset.built.formats} formats from TRaSH` : undefined}
      footer={footer}>
      {confirm.dialog}
      {picking ? <PresetPicker flavour={flavour} setFlavour={setFlavour} onCancel={onClose} onBuilt={(trash_id, modifiers, b) => { setSession({ ...create, flavour, preset: { trash_id, modifiers, built: b } }); setConfig(b.profile); window.setTimeout(() => heading.current?.focus(), 0); }} /> : config ? <div className="space-y-5">
        <div ref={errorBox}>{error ? <ExplainBox error={error} onReload={edit ? () => void reload(false) : undefined} onReloadKeep={edit ? () => void reload(true) : undefined} /> : null}{failure instanceof ApiError && object(failure.body).uses ? <p className="text-sm">In use by: {String(object((failure as ApiError).body).uses)}</p> : null}</div>
        {/* Who a save affects. */}
        <div data-testid="profile-affects" className={cn("rounded-lg border px-3 py-2 text-[13px]", active.length ? "border-[var(--th-warning-border)] bg-[var(--warn-soft)] text-[var(--warn-ink)]" : "border-[var(--line)] text-muted-foreground")}>
          {create ? "A new profile: no library uses it until you pick it for one." : used.length === 0 ? "No library uses this profile: saving changes no decision." : <>Used by {used.map((u) => `${nameOf(u.key)}${u.active ? " (Live)" : " (Watch only)"}`).join(", ")}. {active.length ? "Live libraries grab with it: a change applies to their next searches and upgrades." : "Watch only libraries alone: nothing is grabbed with it."}</>}
        </div>
        {/* Where it started. */}
        <section className="space-y-2" aria-label="Started from">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">Started from</span>
            {origin ? <><span className="font-medium">TRaSH {originLabel(origin)}</span>{edit && summary?.guessed ? <Badge tone="outline" title="No origin is stored: this is the closest TRaSH preset.">closest preset</Badge> : null}</> : <span className="font-medium">our own profile</span>}
            {edit && summary?.diff ? <Button type="button" size="xs" variant="outline" aria-expanded={compare} onClick={() => setCompare(!compare)}>Compare with TRaSH</Button> : null}
          </div>
          {create?.preset?.built.notes.map((n) => <p key={n} className="text-[13px] text-[var(--warn-ink)]">{n}</p>)}
          {compare && summary?.diff ? <div className="rounded-lg border border-[var(--line)] p-3"><p className="mb-2 text-xs text-muted-foreground">The saved profile against {summary.diff.preset} as TRaSH builds it today.</p><DiffLists diff={summary.diff} /></div> : null}
        </section>
        <fieldset disabled={busy || readOnly} className="space-y-5">
          {create ? <label className="flex flex-col gap-1 text-sm">Key<Input className="h-9" required value={create.key} onChange={(e) => setSession({ ...create, key: e.target.value })} /></label> : null}
          <section className="space-y-2"><h3 className="font-semibold">Qualities</h3><QualityLadder config={config} onChange={change} /></section>
          <section className="space-y-2" aria-labelledby="profile-sizes"><h3 id="profile-sizes" className="font-semibold">Size limits</h3>
            <p className="text-xs text-muted-foreground">MB per minute of runtime, as in the arrs' quality definitions. A blank field uses the arr default shown in it; type none for no limit (Max) or no preferred size.</p>
            <SizeLimits config={config} flavours={create?.preset ? [create.flavour] : profileFlavours(used.map((u) => u.kind))} rawHref={edit ? `/admin/settings/profiles?raw=1&edit=${encodeURIComponent(edit.key)}` : undefined}
              onChange={change} onInvalid={setSizesInvalid} readOnly={readOnly} />
          </section>
          <section className="space-y-2"><h3 className="font-semibold">Upgrades and scores</h3>
            <FormGrid>
              <TextField label="Minimum format score" type="number" value={config.min_score ?? 0} onChange={(v) => setField("min_score", Number(v))} />
              <TextField label="Cutoff format score (upgrade until)" type="number" value={config.cutoff_score ?? 0} onChange={(v) => setField("cutoff_score", Number(v))} />
              <TextField label="Minimum score gain for an upgrade" type="number" value={config.min_upgrade_step ?? 1} onChange={(v) => setField("min_upgrade_step", Number(v))} />
              <Select label="Propers and repacks" value={typeof config.propers === "string" ? config.propers : ""} values={PROPERS} labels={PROPERS_LABELS} onChange={(v) => setField("propers", v)} />
              <Toggle label="Upgrades allowed" checked={config.upgrades !== false} onChange={(v) => setField("upgrades", v)} />
            </FormGrid>
          </section>
          <section className="space-y-2"><h3 className="font-semibold">Formats and scores</h3>
            <p className="text-xs text-muted-foreground">Grouped as TRaSH groups them. A score equal to TRaSH's follows TRaSH; one you change is yours and is kept when TRaSH changes it.</p>
            <ScoreGroups rows={rows} readOnly={readOnly} onScore={(format, score) => change({ ...config, formats: { ...object(config.formats), [format]: score } })} />
            {edit && summary?.flavour ? <AddFormat flavour={summary.flavour} scored={object(config.formats)} disabled={readOnly} onAdd={(format) => change({ ...config, formats: { ...object(config.formats), [format]: 0 } })} /> : null}
          </section>
        </fieldset>
        {impact ? <ImpactView impact={impact} /> : null}
      </div> : null}
    </SheetLayout>
    {confirmDelete && edit ? <ConfirmDialog testid="delete-confirm" subject={`profile ${edit.key}`} scope={{ title: `Delete profile ${edit.key}?`, does: [`Delete the profile ${edit.key}. No library uses it.`], keeps: ["Its format set and the custom formats stay; delete them separately if nothing else needs them."], confirm: "Confirm delete" }} busy={busy} onClose={() => setConfirmDelete(false)} onConfirm={() => void remove()} /> : null}
  </Sheet>;
}
