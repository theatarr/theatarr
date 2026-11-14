import { SettingsAuthority } from "@/components/settings/authority";
// Settings › Profiles (docs/design/settings.md, "Profiles, presets and formats"): every profile
// with its allowed qualities (best first, cutoff marked), how many formats it scores, the TRaSH preset it started
// from and how ours differs, and the versions that use it (unused ones are shown, never deleted by themselves). The
// default profile per kind of version is what Add version preselects. New profiles start from a TRaSH preset; the
// TRaSH defaults (every preset, built in) are listed below the profiles.
import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";
import { useSettings } from "@/api/settings";
import { CATEGORY_LABEL, freeKey, keyFromName, originLabel, overviewKey, saveDefaults, useQualityOverview, type Category, type ProfileSummary } from "@/api/settings-profiles";
import { ErrorState, Loading, Section } from "@/components/page";
import { Announcer, ExplainBox, SELECT_CLASS, useAnnouncement } from "@/components/settings/parts";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useEditParam } from "@/components/ui/sheet";
import { explainError } from "@/lib/settings";
import { QualityPanel, type QualityRecord } from "../quality";
import { TrashDefaults } from "./defaults";
import { QualityChips } from "./parts";
import { ProfileSheet, type ProfileSession } from "./sheet";
import { useLibraryName } from "@/lib/library-names";

const CATEGORIES: Category[] = ["movie", "series", "anime_movie", "anime_series"];

function OriginCell({ p }: { p: ProfileSummary }) {
  if (!p.origin) return <span className="text-[13px] text-muted-foreground">Our own</span>;
  const lines = p.diff?.summary ?? [];
  return <div className="min-w-0 space-y-0.5 text-[13px]">
    <div className="flex flex-wrap items-center gap-1.5"><span className="font-medium">TRaSH {originLabel(p.origin)}</span>{p.guessed ? <Badge tone="outline" title="No origin is stored: the closest TRaSH preset.">closest</Badge> : null}</div>
    {p.diff?.same ? <div className="text-muted-foreground">no edits</div> : <ul className="text-muted-foreground">{lines.slice(0, 3).map((l) => <li key={l} className="[overflow-wrap:anywhere]">{l}</li>)}{lines.length > 3 ? <li>…</li> : null}</ul>}
  </div>;
}

function UsedBy({ p }: { p: ProfileSummary }) {
  const nameOf = useLibraryName();
  if (!p.used_by.length) return <Badge tone="warning" title="No library uses this profile. It is kept; delete it from its editor if you no longer need it.">unused</Badge>;
  return <ul className="flex flex-wrap gap-1">{p.used_by.map((u) => <li key={u.key}><Badge tone={u.active ? "positive" : "outline"} title={u.key}>{nameOf(u.key)}{u.active ? " · Live" : ""}</Badge></li>)}</ul>;
}

function Defaults({ profiles, defaults, readOnly, onSaved }: { profiles: string[]; defaults: Record<string, string>; readOnly: boolean; onSaved: (text: string) => void }) {
  const [draft, setDraft] = useState(defaults);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  useEffect(() => setDraft(defaults), [defaults]);
  const save = async () => {
    setBusy(true); setError(undefined);
    try { await saveDefaults(draft); onSaved("Default profiles saved."); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return <Card className="space-y-3" data-testid="profile-defaults">
    <div><h3 className="font-semibold">Defaults for new libraries</h3><p className="text-[13px] text-muted-foreground">Add library preselects these. A library always names its own profile; changing a default changes no library.</p></div>
    <fieldset disabled={readOnly || busy} className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      {CATEGORIES.map((c) => <label key={c} className="flex min-w-0 flex-col gap-1 text-sm">{CATEGORY_LABEL[c]}
        <select className={SELECT_CLASS} value={draft[c] ?? ""} onChange={(e) => setDraft({ ...draft, [c]: e.target.value })}>
          <option value="">None</option>
          {profiles.map((k) => <option key={k} value={k}>{k}</option>)}
          {draft[c] && !profiles.includes(draft[c]) ? <option value={draft[c]}>{draft[c]} (missing)</option> : null}
        </select>
      </label>)}
    </fieldset>
    {error ? <ExplainBox error={explainError(error)} /> : null}
    <Button type="button" variant="outline" disabled={readOnly || busy} onClick={() => void save()}>{busy ? "Saving…" : "Save defaults"}</Button>
  </Card>;
}

export function ProfilesPage({ compact = false }: { compact?: boolean }) {
  const settings = useSettings();
  const overview = useQualityOverview();
  const qc = useQueryClient();
  const [params] = useSearchParams();
  const raw = params.get("raw") === "1";
  const db = overview.data?.source === "db";
  const records = useQuery({ queryKey: [...qk.settings.all, "quality", "profiles"], queryFn: () => api<{ generation: number; records: QualityRecord[] }>("/settings/profiles"), enabled: db && !raw });
  const [session, setSession] = useState<ProfileSession>();
  const [announcement, announce] = useAnnouncement();
  const newButton = useRef<HTMLButtonElement>(null);
  const deleted = useRef(false);
  const o = overview.data;
  const readOnly = !db || settings.data?.writable === false;
  const sessionFor = (key: string, list = records.data, ov = o): ProfileSession | undefined => {
    const summary = ov?.profiles.find((p) => p.key === key);
    if (!summary) return;
    const rec = list?.records.find((r) => r.key === key);
    // Under YAML there are no records: the sheet shows the running profile read-only.
    const config = rec?.config ?? { qualities: summary.qualities, cutoff: summary.cutoff, formats: Object.fromEntries(summary.scores.map((s) => [s.format, s.score])) };
    return { mode: "edit", key, id: rec?.id, generation: list?.generation ?? ov?.generation ?? 0, config, summary };
  };
  const ready = !!o && (!db || !!records.data);
  const setEdit = useEditParam(ready && !raw, !!session, (key) => {
    const s = key === "new" ? { mode: "create" as const, generation: o?.generation ?? 0, flavour: "radarr", key: "" } : sessionFor(key);
    if (s) setSession(s); else setEdit(null);
  });
  if (raw) return <div className="space-y-3"><Link className="text-sm text-[var(--accent-ink)] underline" to="/admin/settings/profiles">Back to the profiles overview</Link><QualityPanel compact={compact} entity="profiles" /></div>;
  if (overview.isPending || settings.isPending) return <Loading />;
  if (overview.error) return <ErrorState error={overview.error} />;
  if (!o) return null;
  const close = () => { setSession(undefined); setEdit(null); };
  const refresh = async () => { await qc.invalidateQueries({ queryKey: qk.settings.all }); await qc.invalidateQueries({ queryKey: overviewKey }); };
  const open = (key: string) => { const s = sessionFor(key); if (s) { deleted.current = false; setSession(s); setEdit(key); } };
  const unused = o.profiles.filter((p) => p.used_by.length === 0).length;
  return <div className="space-y-4">
    {settings.data ? <SettingsAuthority compact={compact} overview={settings.data} /> : null}
    <Announcer announcement={announcement} testid="profiles-announcer" />
    {!db ? <p className="text-sm font-medium" data-testid="read-only-note">Settings service unavailable.</p> : null}
    {!o.trash.length ? <p className="text-sm text-muted-foreground" data-testid="trash-note">{o.trash_note ?? "No TRaSH guides tree"}: presets, TRaSH scores and comparisons are unavailable. A profile can still be made by hand in the <Link className="underline" to="/admin/settings/profiles?raw=1">raw profile editor</Link>.</p>
      : o.trash_note ? <p className="text-sm text-[var(--warn-ink)]" data-testid="trash-note">{o.trash_note}.</p> : null}
    <div className="flex flex-wrap items-center gap-2">
      <Button ref={newButton} disabled={readOnly || !o.trash.length} onClick={() => { deleted.current = false; setSession({ mode: "create", generation: o.generation, flavour: "radarr", key: "" }); setEdit("new"); }}>New profile from TRaSH preset</Button>
      <span className="text-[13px] text-muted-foreground">{o.profiles.length} {o.profiles.length === 1 ? "profile" : "profiles"}{unused ? ` · ${unused} unused` : ""}</span>
    </div>
    <Card className="p-0">
      <Table>
        <TableHeader><TableRow>
          <TableHead>Profile</TableHead>
          <TableHead className="hidden md:table-cell">Qualities</TableHead>
          <TableHead className="hidden lg:table-cell">Formats</TableHead>
          <TableHead className="hidden sm:table-cell">Origin</TableHead>
          <TableHead className="hidden sm:table-cell">Used by</TableHead>
          <TableHead><span className="sr-only">Actions</span></TableHead>
        </TableRow></TableHeader>
        <TableBody>
          {o.profiles.map((p) => <TableRow key={p.key} data-profile={p.key} data-selected={session?.mode === "edit" && session.key === p.key ? "true" : undefined}>
            <TableCell className="align-top">
              <div className="font-mono text-[13px] [overflow-wrap:anywhere]">{p.key}</div>
              <div className="text-xs text-muted-foreground">{p.flavour || "no flavour"}</div>
              <div className="mt-1.5 space-y-1.5 md:hidden"><QualityChips qualities={p.qualities} cutoff={p.cutoff} /></div>
              <div className="mt-1.5 space-y-1.5 sm:hidden"><OriginCell p={p} /><UsedBy p={p} /></div>
            </TableCell>
            <TableCell className="hidden max-w-[22rem] align-top md:table-cell"><QualityChips qualities={p.qualities} cutoff={p.cutoff} /></TableCell>
            <TableCell className="hidden align-top tabular-nums lg:table-cell">{p.formats}</TableCell>
            <TableCell className="hidden max-w-[20rem] align-top sm:table-cell"><OriginCell p={p} /></TableCell>
            <TableCell className="hidden align-top sm:table-cell"><UsedBy p={p} /></TableCell>
            <TableCell className="align-top text-right"><Button size="sm" variant="outline" aria-label={`${readOnly ? "View" : "Edit"} ${p.key}`} onClick={() => open(p.key)}>{readOnly ? "View" : "Edit"}</Button></TableCell>
          </TableRow>)}
        </TableBody>
      </Table>
    </Card>
    <Defaults profiles={o.profiles.map((p) => p.key)} defaults={o.defaults} readOnly={readOnly} onSaved={(t) => { announce(t); void refresh(); }} />
    <TrashDefaults overview={o} readOnly={readOnly} onUse={async (flavour, preset, built) => {
      // The editor opens on a copy of the default, under a free key the user can change before creating it.
      // Its key must be free among every format set too, read now: deleting a profile leaves its set, which no
      // profile names any more. When that read fails the sets the profiles use are all that is known.
      const sets = await api<{ records: { key: string }[] }>("/settings/format_sets").catch(() => undefined);
      const taken = [...o.profiles.map((p) => p.key), ...o.profiles.map((p) => p.format_set), ...(sets?.records ?? []).map((r) => r.key)];
      deleted.current = false;
      setSession({ mode: "create", generation: o.generation, flavour, key: freeKey(keyFromName(preset.name), taken), config: built.profile, preset: { trash_id: preset.trash_id, modifiers: [], built } });
      setEdit("new");
    }} />
    <Section title="Format sets"><p className="text-[13px] text-muted-foreground">Each profile scores the formats of its own format set; editing a profile's scores edits its set. Format sets are not a page of their own any more; the raw <Link className="underline" to="/admin/settings/profiles/format_sets">format set editor</Link> and the <Link className="underline" to="/admin/settings/profiles?raw=1">raw profile editor</Link> stay for exceptions.</p></Section>
    {session ? <ProfileSheet key={session.mode === "edit" ? session.key : "new"} session={session} readOnly={readOnly} onClose={close}
      onReload={async () => { const [r, ov] = await Promise.all([records.refetch(), overview.refetch()]); return session.mode === "edit" ? sessionFor(session.key, r.data, ov.data) : undefined; }}
      onSaved={(text, gone) => { deleted.current = !!gone; announce(text); close(); void refresh(); if (gone) newButton.current?.focus(); }} /> : null}
  </div>;
}
