import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { confirmationHeaders } from "@/api/settings";
import { useWriteConfirmation } from "@/components/settings/confirm-write";
import { ExplainBox } from "@/components/settings/parts";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { explainError } from "@/lib/settings";
import type { QualityRecord } from "./index";

type Candidate = { trash_id: string; name: string; same_spec: boolean; same_name: boolean };
type Item = { set?: string; key: string; status: "linked" | "matched" | "diverged" | "ambiguous" | "unmatched"; trash_id?: string; candidates?: Candidate[]; reason?: string };
export type LinkPreview = { generation: number; source: string; flavour: string; commit: string; items: Item[]; counts: Record<string, number> };

const itemKey = (i: Item) => `${i.set ?? ""}\u0000${i.key}`;
const label = (i: Item) => (i.set ? `${i.set} / ${i.key}` : `${i.key} (standalone)`);
// Only a same-spec or same-name TRaSH format may be linked (the server refuses the rest).
const linkable = (i: Item) => (i.candidates ?? []).filter((c) => c.same_spec || c.same_name);
const describe = (c: Candidate) => `${c.name}${c.same_spec ? " (same specification)" : " (other specification)"}`;

// Link formats to TRaSH: preview a source's match of the formats of its flavour, preselect the matches, let the
// administrator add diverged or ambiguous ones explicitly, and link the selection in one audited generation.
export function TrashLink({ source, generation, onChanged, onClose }: { source: QualityRecord; generation: number; onChanged: () => Promise<void>; onClose: () => void }) {
  const [preview, setPreview] = useState<LinkPreview>();
  const [chosen, setChosen] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<string>();
  const confirm = useWriteConfirmation();
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { heading.current?.focus(); }, [preview]);

  const load = async () => {
    setBusy(true); setError(undefined); setDone(undefined);
    try {
      const p = await api<LinkPreview>(`/settings/sync_sources/${source.id}/link/preview`, { method: "POST", body: { generation } });
      setPreview(p);
      setChosen(Object.fromEntries(p.items.filter((i) => i.status === "matched" && i.trash_id).map((i) => [itemKey(i), i.trash_id as string])));
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  // Once per opened source: a preview fetches the source's TRaSH tree. A later settings change marks it stale.
  useEffect(() => { void load(); }, [source.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const apply = async (token?: string) => {
    if (!preview) return;
    const links = preview.items.filter((i) => chosen[itemKey(i)]).map((i) => ({ set: i.set ?? "", key: i.key, trash_id: chosen[itemKey(i)] }));
    setBusy(true); setError(undefined);
    try {
      const res = await api<{ linked: number }>(`/settings/sync_sources/${source.id}/link/apply`, { method: "POST", body: { generation: preview.generation, commit: preview.commit, links }, headers: confirmationHeaders(token) });
      setDone(`Linked ${res.linked} formats to TRaSH.`);
      setPreview(undefined);
      await onChanged();
    } catch (e) {
      if (confirm.ask(e, (next) => { void apply(next); })) return;
      setError(e);
    } finally { setBusy(false); }
  };

  const by = (status: Item["status"]) => preview?.items.filter((i) => i.status === status) ?? [];
  const toggle = (i: Item, id: string) => setChosen((c) => { const next = { ...c }; if (id) next[itemKey(i)] = id; else delete next[itemKey(i)]; return next; });
  const selected = Object.keys(chosen).length;

  return <Card className="space-y-3 p-4" aria-label={`Link formats to TRaSH for ${source.key}`}>
    <h3 ref={heading} tabIndex={-1} className="font-semibold outline-none">Link formats to TRaSH · {source.key}{preview ? ` · ${preview.flavour} · ${preview.commit.slice(0, 12)}` : ""}</h3>
    <p className="text-sm text-muted-foreground">Formats are matched by specification; the name (or the name without " - Notifiarr") only picks between TRaSH formats with the same specification. Linking stores the TRaSH id on the format so the source keeps its definition current. Display names, scores and set membership do not change. A diverged format, once linked, takes TRaSH's specification at the next sync.</p>
    {confirm.dialog}
    {error ? <ExplainBox error={explainError(error)} onReload={() => { void load(); }} /> : null}
    {done ? <p role="status">{done}</p> : null}
    {busy && !preview ? <p>Matching formats…</p> : null}
    {preview ? <>
      <p data-testid="trash-link-counts">Matched {preview.counts.matched ?? 0} · diverged {preview.counts.diverged ?? 0} · ambiguous {preview.counts.ambiguous ?? 0} · unmatched {preview.counts.unmatched ?? 0} · already linked {preview.counts.linked ?? 0}</p>
      <fieldset className="space-y-1"><legend className="font-medium">Matched ({by("matched").length})</legend>
        {by("matched").map((i) => <label key={itemKey(i)} className="flex items-center gap-2 text-sm"><input type="checkbox" checked={!!chosen[itemKey(i)]} onChange={(e) => toggle(i, e.target.checked ? i.trash_id ?? "" : "")} />{label(i)}{i.reason ? <span className="text-muted-foreground">({i.reason})</span> : null}</label>)}
      </fieldset>
      {(["diverged", "ambiguous"] as const).map((status) => <fieldset key={status} className="space-y-1"><legend className="font-medium">{status === "diverged" ? "Diverged" : "Ambiguous"} ({by(status).length}): not linked unless chosen</legend>
        {by(status).map((i) => <label key={itemKey(i)} className="flex flex-wrap items-center gap-2 text-sm">{label(i)} <span className="text-muted-foreground">{i.reason}</span>
          <select aria-label={`TRaSH format for ${label(i)}`} className="rounded border p-1" value={chosen[itemKey(i)] ?? ""} onChange={(e) => toggle(i, e.target.value)}>
            <option value="">Leave unlinked</option>
            {linkable(i).map((c) => <option key={c.trash_id} value={c.trash_id}>{describe(c)}</option>)}
          </select></label>)}
      </fieldset>)}
      <div><h4 className="font-medium">Unmatched ({by("unmatched").length}): our own, never linked</h4><p className="text-sm">{by("unmatched").map(label).join("; ") || "None"}</p></div>
      <div className="flex flex-wrap gap-2">
        <Button disabled={busy || selected === 0 || preview.generation !== generation} onClick={() => { void apply(); }}>Link {selected} formats</Button>
        <Button variant="outline" disabled={busy} onClick={() => { void load(); }}>Preview again</Button>
      </div>
      {preview.generation !== generation ? <p>Settings changed since this preview. Preview again before linking.</p> : null}
    </> : null}
    <Button variant="outline" onClick={onClose}>Close</Button>
  </Card>;
}
