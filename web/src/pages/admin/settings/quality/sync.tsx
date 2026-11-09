import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { useQualityReviews } from "@/api/reviews";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { confirmationHeaders } from "@/api/settings";
import { useWriteConfirmation } from "@/components/settings/confirm-write";
import { Announcer, ExplainBox, useAnnouncement } from "@/components/settings/parts";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { explainError } from "@/lib/settings";
import { text } from "./fields";
import type { QualityRecord } from "./index";
import { TrashLink } from "./link";

type Change = { key: string; trash_id?: string; reason?: string };
// A score of a bound profile waiting for take or keep: ours while TRaSH changed it, or differing on the first run
// after binding (last null). ours is null when the profile does not score the format yet.
export type HeldScore = { profile: string; format: string; ours: number | null; last: number | null; trash: number };
// A managed profile field of a Recyclarr source waiting for take or keep: ours differs and the source changed it (or
// asks for it the first time, last null). Values are JSON as the profile records spell them; null is "not set".
export type HeldField = { profile: string; field: string; ours: unknown; last: unknown; upstream: unknown };
type FieldChange = { profile: string; field: string; before: unknown; after: unknown };
export type Preview = {
  id: number; generation: number; source: string; commit: string; state: string; kind?: string;
  added: Change[]; spec_changed: Change[]; retired: Change[]; profiles_changed: string[];
  scores: { profile: string; format: string; before: number; after: number }[];
  held?: HeldScore[]; held_fields?: HeldField[]; fields?: FieldChange[]; notes?: string[];
  impact: { files: number; score_changed: number; cutoff_changed: number; upgrade_changed: number; examples: { title: string; version: string; file: string; before_score: number; after_score: number; before_cutoff_met: boolean; after_cutoff_met: boolean; before_upgrade: boolean; after_upgrade: boolean }[] };
};
type Run = { id: number; source: string; started_at: number; commit: string; outcome: string; error: string };

// One line per source: where its TRaSH data comes from, its mode, and the commit of its newest run that did not fail.
export function describeSource(r: QualityRecord, runs: Run[]): string {
  const c = r.config;
  const origin = c.origin === "github" ? `tracks ${text(c.repo) || "TRaSH-Guides/Guides"} ${text(c.branch) || "master"}` : c.archive_url ? "pinned archive" : "local TRaSH";
  const own = runs.filter((x) => x.source === r.key);
  const good = own.find((x) => x.commit && x.outcome !== "error");
  const failed = own[0]?.outcome === "error" ? ` · last run failed: ${own[0].error}` : "";
  return `${text(c.kind)} · ${origin} · mode ${text(c.mode) || "review"} · last commit ${good ? good.commit.slice(0, 12) : "none yet"}${failed}`;
}

export function SyncReviews({ part, records, generation, draft, baseGeneration, onChanged }: { part: "sources" | "reviews"; records: QualityRecord[]; generation: number; draft?: QualityRecord; baseGeneration?: number; onChanged: () => Promise<void> }) {
  const reviews = useQualityReviews();
  const runs = useQuery({ enabled: part === "sources", queryKey: ["quality-runs", generation], queryFn: () => api<Run[]>("/settings/sync/runs"), refetchInterval: 60_000 });
  const [preview, setPreview] = useState<Preview>();
  const [linking, setLinking] = useState<QualityRecord>();
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [announcement, announce] = useAnnouncement();
  const confirm = useWriteConfirmation();
  const errorBox = useRef<HTMLDivElement>(null);
  const previewHeading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { if (error) errorBox.current?.querySelector<HTMLElement>("[role=alert]")?.focus(); }, [error]);
  useEffect(() => { if (preview) previewHeading.current?.focus(); }, [preview]);
  // fn answers "confirm" when a 422 opened the confirmation dialog: nothing was applied yet.
  const action = async (fn: () => Promise<void | "confirm">) => { setBusy(true); setError(undefined); try { const pending = await fn(); await reviews.refetch(); await runs.refetch(); announce(pending === "confirm" ? "Confirmation needed: review what this change enables." : "Sync action finished."); } catch (e) { setError(e); announce("Sync action failed; review the error."); } finally { setBusy(false); } };
  const inspect = (r: QualityRecord, base: number) => action(async () => { setPreview(await api<Preview>("/settings/sync/preview", { method: "POST", body: { generation: base, key: r.key, config: r.config } })); });
  // A review that widens an active version's profile answers 422 with its effects: confirm, then apply with the token.
  const approve = (p: Preview, token?: string) => action(async () => {
    try { await api(`/settings/sync/reviews/${p.id}/apply`, { method: "POST", body: { generation: p.generation }, headers: confirmationHeaders(token) }); }
    catch (e) { if (confirm.ask(e, (next) => { void approve(p, next); })) return "confirm"; throw e; }
    setPreview(undefined); await onChanged();
  });
  // A held score is taken (TRaSH's, which follows from then on) or kept (ours, pinned) on its own, at the current generation.
  const decide = (p: Preview, h: HeldScore, choice: "take" | "keep", token?: string) => action(async () => {
    try { await api(`/settings/sync/reviews/${p.id}/scores`, { method: "POST", body: { generation, decisions: [{ profile: h.profile, format: h.format, choice }] }, headers: confirmationHeaders(token) }); }
    catch (e) { if (confirm.ask(e, (next) => { void decide(p, h, choice, next); })) return "confirm"; throw e; }
    const rest = (p.held ?? []).filter((x) => x.profile !== h.profile || x.format !== h.format);
    setPreview(rest.length || p.held_fields?.length ? { ...p, held: rest } : undefined); await onChanged();
  });
  // A held field of a Recyclarr source: take the source's value (it follows from then on) or keep ours (pinned).
  const decideField = (p: Preview, h: HeldField, choice: "take" | "keep", token?: string) => action(async () => {
    try { await api(`/settings/sync/reviews/${p.id}/scores`, { method: "POST", body: { generation, decisions: [{ profile: h.profile, field: h.field, choice }] }, headers: confirmationHeaders(token) }); }
    catch (e) { if (confirm.ask(e, (next) => { void decideField(p, h, choice, next); })) return "confirm"; throw e; }
    const rest = (p.held_fields ?? []).filter((x) => x.profile !== h.profile || x.field !== h.field);
    setPreview(rest.length || p.held?.length ? { ...p, held_fields: rest } : undefined); await onChanged();
  });
  return <section className="space-y-4" aria-label="TRaSH and Recyclarr sync">
    <h2 className="text-lg font-semibold">{part === "reviews" ? "Quality updates" : "TRaSH / Recyclarr sync"}</h2>
    {part === "sources" ? <details><summary>How sync works</summary><p className="text-sm text-muted-foreground">Review is the default: scheduled changes wait here for approval. Retired or renamed upstream formats are retained. A profile bound to a TRaSH score set follows TRaSH while its score still equals TRaSH's last one; a score of our own waits here only when TRaSH changes it: take TRaSH's (it follows from then on) or keep ours (asked again only when TRaSH changes it again). Only those scores wait; the rest of the run applies. A Recyclarr source changes only the profile values its config sets (qualities, cutoff, upgrade settings, assigned scores, and quality sizes from quality_definition when its quality sizes switch is on); every other value of the profile stays ours. A value we changed ourselves waits here as a held field when the config changes it: take the source's or keep ours. Keys the sync cannot apply are listed as skipped. Auto applies every other change as the source actor, except profile value changes, which always wait for approval. Auto definitions (the default for a tracked GitHub branch) applies specification changes of formats already held and the scores that follow TRaSH, and holds added or retired formats for review. An unchanged run creates no generation; a tracked source whose branch has not moved fetches nothing.</p></details> : null}
    <Announcer announcement={announcement} testid="quality-sync-announcer" />
    {reviews.data?.unavailable ? <p>{reviews.data.unavailable === 409 ? "Quality sync is not available while settings come from YAML" : "Quality sync is not running"}</p> : null}
    {confirm.dialog}
    <div ref={errorBox}>{error ? <ExplainBox error={explainError(error)} onReload={() => { void onChanged(); void reviews.refetch(); }} /> : null}</div>
    {reviews.error || runs.error ? <ExplainBox error={explainError(reviews.error ?? runs.error)} /> : null}
    {part === "sources" ? <>
    <p>{reviews.isPending ? "Loading" : reviews.error || reviews.data?.unavailable ? "unavailable" : reviews.data?.items.length} updates waiting — <Link className="text-primary underline" to="/admin/reviews/quality">Review</Link></p>
    {draft ? <Button disabled={busy || !draft.key} onClick={() => { void inspect(draft, baseGeneration ?? generation); }}>Preview draft import</Button> : null}
    {records.map((r) => <div className="flex flex-wrap items-center gap-2" key={r.id}><span>{r.key}</span><span className="text-sm text-muted-foreground" data-testid={`sync-source-${r.key}`}>{describeSource(r, runs.data ?? [])}</span><Button disabled={busy} onClick={() => { void inspect(r, generation); }}>Preview</Button><Button disabled={busy} variant="outline" onClick={() => { void action(async () => { await api(`/settings/sync_sources/${r.id}/run`, { method: "POST", body: { generation } }); await onChanged(); }); }}>Run sync now</Button><Button disabled={busy} variant="outline" onClick={() => setLinking(r)}>Link formats to TRaSH</Button></div>)}
    {linking ? <TrashLink key={linking.id} source={linking} generation={generation} onChanged={onChanged} onClose={() => setLinking(undefined)} /> : null}
    </> : null}
    {part === "reviews" ? <>
    <h3 className="font-medium">Pending reviews ({reviews.isPending ? "Loading" : reviews.error || reviews.data?.unavailable ? "unavailable" : reviews.data?.items.length})</h3>
    {(reviews.data?.items ?? []).map((p) => <Card key={p.id} className="flex flex-wrap items-center gap-3 p-3"><span>{p.source} · {p.commit} · {p.kind === "scores" ? heldCount(p) : `generation ${p.generation}`}</span><Button disabled={busy} onClick={() => setPreview(p)}>{p.kind === "scores" ? "Review held scores" : "Review changes"}</Button></Card>)}
    </> : null}
    {preview?.kind === "scores" ? <Card className="space-y-3 p-4"><h3 ref={previewHeading} tabIndex={-1} className="font-semibold outline-none">{preview.source} · held scores · {preview.commit}</h3>
      <HeldScores held={preview.held ?? []} busy={busy} onDecide={(h, choice) => { void decide(preview, h, choice); }} />
      <HeldFields held={preview.held_fields ?? []} busy={busy} onDecide={(h, choice) => { void decideField(preview, h, choice); }} />
      <Button variant="outline" onClick={() => setPreview(undefined)}>Close review</Button>
    </Card> : preview ? <Card className="space-y-3 p-4"><h3 ref={previewHeading} tabIndex={-1} className="font-semibold outline-none">{preview.source} · {preview.commit}</h3>
      {([ ["Added", preview.added], ["Specifications changed", preview.spec_changed], ["Retired (retained)", preview.retired] ] as const).map(([label, changes]) => <div key={label}><h4 className="font-medium">{label}: {changes.length}</h4><ul>{changes.map((c) => <li key={`${c.key}:${c.trash_id}`}>{c.key} {c.reason}</li>)}</ul></div>)}
      <h4 className="font-medium">Score changes</h4><ul>{preview.scores.map((s) => <li key={`${s.profile}/${s.format}`}>{s.profile} / {s.format}: {s.before} → {s.after}</li>)}</ul>
      {preview.held?.length ? <><h4 className="font-medium">Held scores (waiting in their own review): {preview.held.length}</h4><HeldScores held={preview.held} /></> : null}
      {preview.held_fields?.length ? <><h4 className="font-medium">Held profile values (waiting in their own review): {preview.held_fields.length}</h4><HeldFields held={preview.held_fields} /></> : null}
      {preview.fields?.length ? <><h4 className="font-medium">Profile value changes</h4><ul>{preview.fields.map((f) => <li key={`${f.profile}/${f.field}`}>{f.profile} / {f.field}: {fieldValue(f.before)} → {fieldValue(f.after)}</li>)}</ul></> : null}
      <p>Other profile changes: {preview.profiles_changed.join(", ") || "None"}</p>
      {preview.notes?.length ? <><h4 className="font-medium">Skipped from the source</h4><ul className="text-sm">{preview.notes.map((n) => <li key={n}>{n}</li>)}</ul></> : null}
      <h4 className="font-medium">Held file decision impact</h4><p>{preview.impact.files} files evaluated; {preview.impact.score_changed} scores, {preview.impact.cutoff_changed} cutoff states and {preview.impact.upgrade_changed} upgrade eligibility states would change.</p>
      <ul className="space-y-2 text-sm">{preview.impact.examples.map((x, i) => <li key={i}>{x.title} / {x.version}: {x.file}<br />Score {x.before_score} → {x.after_score}; cutoff met {String(x.before_cutoff_met)} → {String(x.after_cutoff_met)}; upgrade eligible {String(x.before_upgrade)} → {String(x.after_upgrade)}</li>)}</ul>
      {preview.id > 0 ? <Button disabled={busy || preview.generation !== generation} onClick={() => { void approve(preview); }}>Approve this preview</Button> : <p>No changes to apply.</p>}
      {preview.generation !== generation ? <p>Settings changed since this preview. Fetch a fresh preview before approval.</p> : null}
      <Button variant="outline" onClick={() => setPreview(undefined)}>Close review</Button>
    </Card> : null}
    {part === "sources" ? <><h3 className="font-medium">Recent runs</h3><ul className="space-y-1 text-sm">{(runs.data ?? []).map((r) => <li key={r.id}>{new Date(r.started_at).toLocaleString()} · {r.source}: {r.outcome} · {r.commit} {r.error}</li>)}</ul></> : null}
  </section>;
}

const heldCount = (p: Preview) => [p.held?.length ? `${p.held.length} held score(s)` : "", p.held_fields?.length ? `${p.held_fields.length} held profile value(s)` : ""].filter(Boolean).join(", ") || "nothing held";

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

const heldFieldLine = (h: HeldField) => `${h.profile} / ${h.field}: ours ${fieldValue(h.ours)} · source ${h.last === null ? "" : `${fieldValue(h.last)} → `}${fieldValue(h.upstream)}${h.last === null ? " (first run that sees it)" : ""}`;

// The held profile values of a review, each with its own take or keep (read-only without onDecide).
export function HeldFields({ held, busy, onDecide }: { held: HeldField[]; busy?: boolean; onDecide?: (h: HeldField, choice: "take" | "keep") => void }) {
  return <ul className="space-y-2">{held.map((h) => <li key={`${h.profile}/${h.field}`} className="flex flex-wrap items-center gap-2"><span>{heldFieldLine(h)}</span>
    {onDecide ? <><Button disabled={busy} aria-label={`Take the source's value for ${h.profile} / ${h.field}`} onClick={() => onDecide(h, "take")}>Take the source's</Button><Button disabled={busy} variant="outline" aria-label={`Keep our value for ${h.profile} / ${h.field}`} onClick={() => onDecide(h, "keep")}>Keep ours</Button></> : null}</li>)}</ul>;
}

const heldLine = (h: HeldScore) => `${h.profile} / ${h.format}: ours ${h.ours ?? "not scored"} · TRaSH ${h.last === null ? "" : `${h.last} → `}${h.trash}${h.last === null ? " (first run after binding)" : ""}`;

// The held scores of a review, each with its own take or keep (read-only without onDecide).
export function HeldScores({ held, busy, onDecide }: { held: HeldScore[]; busy?: boolean; onDecide?: (h: HeldScore, choice: "take" | "keep") => void }) {
  return <ul className="space-y-2">{held.map((h) => <li key={`${h.profile}/${h.format}`} className="flex flex-wrap items-center gap-2"><span>{heldLine(h)}</span>
    {onDecide ? <><Button disabled={busy} aria-label={`Take TRaSH's score for ${h.profile} / ${h.format}`} onClick={() => onDecide(h, "take")}>Take TRaSH's</Button><Button disabled={busy} variant="outline" aria-label={`Keep our score for ${h.profile} / ${h.format}`} onClick={() => onDecide(h, "keep")}>Keep ours</Button></> : null}</li>)}</ul>;
}
