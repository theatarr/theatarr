import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { Announcer, ExplainBox, useAnnouncement } from "@/components/settings/parts";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { explainError } from "@/lib/settings";
import { text } from "./fields";
import type { QualityRecord } from "./index";
import { TrashLink } from "./link";

type Change = { key: string; trash_id?: string; reason?: string };
// A score of a bound profile kept as yours: you changed it (it differs from TRaSH's last score) and TRaSH changed it
// (now trash). ours is null when the profile does not score the format.
export type HeldScore = { profile: string; format: string; ours: number | null; last: number | null; trash: number };
// A managed profile value of a Recyclarr source kept as yours: you changed it and the source changed it. Values are
// JSON as the profile records spell them; null is "not set".
export type HeldField = { profile: string; field: string; ours: unknown; last: unknown; upstream: unknown };
type FieldChange = { profile: string; field: string; before: unknown; after: unknown };
export type Preview = {
  id: number; generation: number; source: string; commit: string; state: string; applied_at?: number;
  added: Change[]; spec_changed: Change[]; retired: Change[]; profiles_changed: string[];
  scores: { profile: string; format: string; before: number; after: number }[];
  held?: HeldScore[]; held_fields?: HeldField[]; fields?: FieldChange[]; notes?: string[];
  impact: { files: number; score_changed: number; cutoff_changed: number; upgrade_changed: number; examples: { title: string; version: string; file: string; before_score: number; after_score: number; before_cutoff_met: boolean; after_cutoff_met: boolean; before_upgrade: boolean; after_upgrade: boolean }[] };
};
type Run = { id: number; source: string; started_at: number; commit: string; outcome: string; error: string };

// One line per source: where its TRaSH data comes from and the commit of its newest run that did not fail.
export function describeSource(r: QualityRecord, runs: Run[]): string {
  const c = r.config;
  const origin = c.origin === "github" ? `tracks ${text(c.repo) || "TRaSH-Guides/Guides"} ${text(c.branch) || "master"}` : c.archive_url ? "pinned archive" : "local TRaSH";
  const own = runs.filter((x) => x.source === r.key);
  const good = own.find((x) => x.commit && x.outcome !== "error");
  const failed = own[0]?.outcome === "error" ? ` · last run failed: ${own[0].error}` : "";
  return `${text(c.kind)} · ${origin} · last commit ${good ? good.commit.slice(0, 12) : "none yet"}${failed}`;
}

// The update log: one entry per run that applied an upstream change, newest first.
const useSyncChanges = (generation: number) => useQuery({
  queryKey: ["quality-changes", generation], queryFn: async () => {
    try { return { items: await api<Preview[]>("/settings/sync/changes"), unavailable: undefined }; }
    catch (e) { if (e instanceof ApiError && (e.status === 409 || e.status === 503)) return { items: [] as Preview[], unavailable: e.status }; throw e; }
  }, refetchInterval: 60_000,
});

// One log line: "<date> · <source> · <commit 12>: <n> formats updated, ..." with zero counts left out.
export function changeLine(p: Preview): string {
  const counts = ([[p.spec_changed.length, "formats updated"], [p.added.length, "added"], [p.scores.length, "scores changed"], [p.fields?.length ?? 0, "profile values changed"], [(p.held?.length ?? 0) + (p.held_fields?.length ?? 0), "kept yours"]] as const)
    .filter(([n]) => n > 0).map(([n, label]) => `${n} ${label}`);
  return `${p.applied_at ? new Date(p.applied_at).toLocaleString() : ""} · ${p.source} · ${p.commit.slice(0, 12)}: ${counts.join(", ") || "remembered values updated"}`;
}

// What a preview or a log entry changes, as lists.
function ChangeLists({ p, retiredLabel }: { p: Preview; retiredLabel: string }) {
  return <>
    {([ ["Added", p.added], ["Specifications changed", p.spec_changed], [retiredLabel, p.retired] ] as const).map(([label, changes]) => <div key={label}><h4 className="font-medium">{label}: {changes.length}</h4><ul>{changes.map((c) => <li key={`${c.key}:${c.trash_id}`}>{c.key} {c.reason}</li>)}</ul></div>)}
    <h4 className="font-medium">Score changes</h4><ul>{p.scores.map((s) => <li key={`${s.profile}/${s.format}`}>{s.profile} / {s.format}: {s.before} → {s.after}</li>)}</ul>
    {p.held?.length || p.held_fields?.length ? <><h4 className="font-medium">Kept yours (the source changed them)</h4><HeldScores held={p.held ?? []} /><HeldFields held={p.held_fields ?? []} /></> : null}
    {p.fields?.length ? <><h4 className="font-medium">Profile value changes</h4><ul>{p.fields.map((f) => <li key={`${f.profile}/${f.field}`}>{f.profile} / {f.field}: {fieldValue(f.before)} → {fieldValue(f.after)}</li>)}</ul></> : null}
    <p>Other profile changes: {p.profiles_changed.join(", ") || "None"}</p>
    {p.notes?.length ? <><h4 className="font-medium">Skipped from the source</h4><ul className="text-sm">{p.notes.map((n) => <li key={n}>{n}</li>)}</ul></> : null}
  </>;
}

export function SyncSources({ records, generation, draft, baseGeneration, onChanged }: { records: QualityRecord[]; generation: number; draft?: QualityRecord; baseGeneration?: number; onChanged: () => Promise<void> }) {
  const changes = useSyncChanges(generation);
  const runs = useQuery({ queryKey: ["quality-runs", generation], queryFn: () => api<Run[]>("/settings/sync/runs"), refetchInterval: 60_000 });
  const [preview, setPreview] = useState<Preview>();
  const [linking, setLinking] = useState<QualityRecord>();
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [announcement, announce] = useAnnouncement();
  const errorBox = useRef<HTMLDivElement>(null);
  const previewHeading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { if (error) errorBox.current?.querySelector<HTMLElement>("[role=alert]")?.focus(); }, [error]);
  useEffect(() => { if (preview) previewHeading.current?.focus(); }, [preview]);
  const action = async (fn: () => Promise<void>) => { setBusy(true); setError(undefined); try { await fn(); await changes.refetch(); await runs.refetch(); announce("Sync action finished."); } catch (e) { setError(e); announce("Sync action failed; review the error."); } finally { setBusy(false); } };
  const inspect = (r: QualityRecord, base: number) => action(async () => { setPreview(await api<Preview>("/settings/sync/preview", { method: "POST", body: { generation: base, key: r.key, config: r.config } })); });
  return <section className="space-y-4" aria-label="TRaSH and Recyclarr sync">
    <h2 className="text-lg font-semibold">TRaSH / Recyclarr sync</h2>
    <details><summary>How sync works</summary><p className="text-sm text-muted-foreground">Each enabled source runs on its schedule and applies what changed upstream by itself, as Recyclarr does. Formats and profiles are never deleted: a format TRaSH no longer has is kept as it is. A score of a profile bound to a TRaSH score set follows TRaSH while it still has TRaSH's last score; a score you changed is yours and stays, and the log notes when TRaSH changed it. The first run after you bind a profile or link a format takes TRaSH's scores. A Recyclarr source changes only the profile values its config sets, with the same rule for values you changed. Turn a source off to stop its updates.</p></details>
    <Announcer announcement={announcement} testid="quality-sync-announcer" />
    {changes.data?.unavailable ? <p>{changes.data.unavailable === 409 ? "Quality sync is not available while settings come from YAML" : "Quality sync is not running"}</p> : null}
    <div ref={errorBox}>{error ? <ExplainBox error={explainError(error)} onReload={() => { void onChanged(); void changes.refetch(); }} /> : null}</div>
    {changes.error || runs.error ? <ExplainBox error={explainError(changes.error ?? runs.error)} /> : null}
    {draft ? <Button disabled={busy || !draft.key} onClick={() => { void inspect(draft, baseGeneration ?? generation); }}>Preview draft import</Button> : null}
    {records.map((r) => <div className="flex flex-wrap items-center gap-2" key={r.id}><span>{r.key}</span><span className="text-sm text-muted-foreground" data-testid={`sync-source-${r.key}`}>{describeSource(r, runs.data ?? [])}</span><Button disabled={busy} onClick={() => { void inspect(r, generation); }}>Preview</Button><Button disabled={busy} variant="outline" onClick={() => { void action(async () => { await api(`/settings/sync_sources/${r.id}/run`, { method: "POST", body: { generation } }); await onChanged(); }); }}>Run sync now</Button><Button disabled={busy} variant="outline" onClick={() => setLinking(r)}>Link formats to TRaSH</Button></div>)}
    {linking ? <TrashLink key={linking.id} source={linking} generation={generation} onChanged={onChanged} onClose={() => setLinking(undefined)} /> : null}
    {preview ? <Card className="space-y-3 p-4"><h3 ref={previewHeading} tabIndex={-1} className="font-semibold outline-none">{preview.source} · {preview.commit}</h3>
      <ChangeLists p={preview} retiredLabel="Retired (retained)" />
      <h4 className="font-medium">Effect on existing files</h4><p>{preview.impact.files} files evaluated; {preview.impact.score_changed} scores, {preview.impact.cutoff_changed} cutoff states and {preview.impact.upgrade_changed} upgrade eligibility states would change.</p>
      <ul className="space-y-2 text-sm">{preview.impact.examples.map((x, i) => <li key={i}>{x.title} / {x.version}: {x.file}<br />Score {x.before_score} → {x.after_score}; cutoff met {String(x.before_cutoff_met)} → {String(x.after_cutoff_met)}; upgrade eligible {String(x.before_upgrade)} → {String(x.after_upgrade)}</li>)}</ul>
      <p>A preview saves nothing. The next run applies these changes.</p>
      <Button variant="outline" onClick={() => setPreview(undefined)}>Close preview</Button>
    </Card> : null}
    <h3 className="font-medium">Update log</h3>
    {changes.isPending ? <p>Loading</p> : changes.error || changes.data?.unavailable ? null : !changes.data?.items.length ? <p className="text-sm">No updates applied yet.</p> : <ul className="space-y-1 text-sm">{changes.data.items.map((p) => <li key={p.id}><details><summary>{changeLine(p)}</summary><div className="space-y-2 p-2"><ChangeLists p={p} retiredLabel="No longer in TRaSH (kept as it is)" /></div></details></li>)}</ul>}
    <h3 className="font-medium">Recent runs</h3>{runs.isPending || runs.error ? null : !runs.data?.length ? <p className="text-sm" data-testid="sync-no-runs">No runs yet.</p> : <ul className="space-y-1 text-sm">{runs.data.map((r) => <li key={r.id}>{new Date(r.started_at).toLocaleString()} · {r.source}: {r.outcome} · {r.commit} {r.error}</li>)}</ul>}
  </section>;
}

// A profile value in words: "not set", qualities by name (groups with their members), sizes as min / preferred / max.
export function fieldValue(v: unknown): string {
  if (v === null || v === undefined) return "not set";
  if (Array.isArray(v)) return v.map((q) => typeof q === "string" ? q : `${String((q as { group?: string }).group)} (${((q as { qualities?: string[] }).qualities ?? []).join(", ")})`).join(", ");
  if (typeof v === "object") {
    const s = v as { min?: number; preferred?: number; max?: number };
    if ("min" in s) return `min ${s.min ?? 0} · preferred ${s.preferred ? s.preferred : "unlimited"} · max ${s.max ? s.max : "unlimited"} MB/min`;
    return JSON.stringify(v);
  }
  return String(v);
}

// The profile values kept as yours, read-only.
export function HeldFields({ held }: { held: HeldField[] }) {
  return <ul className="space-y-1">{held.map((h) => <li key={`${h.profile}/${h.field}`}>{h.profile} / {h.field}: yours {fieldValue(h.ours)} · source now {fieldValue(h.upstream)}</li>)}</ul>;
}

// The scores kept as yours, read-only.
export function HeldScores({ held }: { held: HeldScore[] }) {
  return <ul className="space-y-1">{held.map((h) => <li key={`${h.profile}/${h.format}`}>{h.profile} / {h.format}: yours {h.ours ?? "not scored"} · TRaSH now {h.trash}</li>)}</ul>;
}
