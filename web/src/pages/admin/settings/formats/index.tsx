// Settings › Custom formats (docs/design/settings.md, "Profiles, presets and formats"): every format,
// grouped as TRaSH groups it, with where its definition comes from (TRaSH, a frozen copy without a TRaSH id, or ours)
// and which profiles score it, each score with its score state and a link to that profile. This is the one list of
// definitions: a standalone format's "Edit definition" opens the format editor (?edit=); a profile's own formats are
// edited through the profile. ?filter= seeds the filter (profile score rows link here with the key they score: a
// name, a format key or a TRaSH ID). A stored definition the merged list leaves out (two sharing a TRaSH ID and flavour
// merge into one row with the last one's key) keeps its own row under Standalone formats.
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQualityOverview, type FormatSummary } from "@/api/settings-profiles";
import { ErrorState, Loading } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { API_BASE } from "@/api/client";
import { QualityPanel, type QualityRecord } from "../quality";
import { DEFINITION, StateTag } from "../profiles/parts";
import { wordMatcher } from "@/lib/words";
import { plural } from "@/lib/format";

function FormatRow({ f, onEdit }: { f: FormatSummary; onEdit?: () => void }) {
  const d = DEFINITION[f.definition] ?? DEFINITION.ours;
  const other = f.names.filter((n) => n !== f.name);
  return <li className="flex flex-col gap-1.5 px-3 py-2 sm:flex-row sm:items-start sm:gap-4" data-format={f.name} data-flavour={f.flavour}>
    <div className="min-w-0 sm:w-[34%]">
      <div className="text-sm font-medium [overflow-wrap:anywhere]">{f.name}</div>
      <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
        <Badge tone={d.tone} title={d.help}>{d.label}</Badge><span>{f.flavour}</span>
        {other.length ? <span className="[overflow-wrap:anywhere]">also named {other.join(", ")}</span> : null}
        {onEdit ? <button type="button" className="text-[var(--accent-ink)] underline" onClick={onEdit} aria-label={`Edit definition ${f.key}`}>Edit definition</button> : null}
        {onEdit && f.id ? <a className="text-[var(--accent-ink)] underline" href={`${API_BASE}/settings/formats/${f.id}/export`} download={`${f.key}.json`}>Export arr JSON</a> : null}
      </div>
    </div>
    <div className="min-w-0 flex-1">
      {f.scored_by.length ? <ul className="flex flex-wrap gap-1.5" aria-label={`Profiles scoring ${f.name}`}>{f.scored_by.map((u) => <li key={u.profile} className="flex items-center gap-1 rounded-md border border-[var(--line)] px-1.5 py-0.5 text-xs">
        <Link className="font-mono underline decoration-[var(--line)] [overflow-wrap:anywhere]" to={`/admin/settings/profiles?edit=${encodeURIComponent(u.profile)}`} title={`Edit the scores of ${u.profile}`}>{u.profile}</Link><span className="tabular-nums">{u.score > 0 ? `+${u.score}` : u.score}</span><StateTag state={u.state} />
      </li>)}</ul> : <span className="text-xs text-muted-foreground">No profile scores it.</span>}
    </div>
  </li>;
}

export function FormatsPage({ compact = false }: { compact?: boolean }) {
  const overview = useQualityOverview();
  const [params, setParams] = useSearchParams();
  const [filter, setFilter] = useState(() => params.get("filter") ?? "");
  const groups = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const match = wordMatcher(filter);
    const m = new Map<string, FormatSummary[]>();
    for (const f of overview.data?.formats ?? []) {
      // A profile's scores may be keyed by TRaSH ID or format key, so those match exactly too.
      if (!match(...f.names, ...f.scored_by.map((u) => u.profile)) && f.trash_id?.toLowerCase() !== q && f.key?.toLowerCase() !== q) continue;
      m.set(f.group, [...(m.get(f.group) ?? []), f]);
    }
    return [...m.entries()];
  }, [overview.data, filter]);
  // The same filter for a definition listed under Standalone formats: words in its key or name, its TRaSH ID exactly.
  const matchRecord = wordMatcher(filter);
  const recordMatches = (r: QualityRecord) => {
    const q = filter.trim().toLowerCase();
    const name = String(r.config.name ?? r.specification?.name ?? "");
    return matchRecord(r.key, name) || String(r.config.trash_id ?? "").toLowerCase() === q;
  };
  const edit = (key: string) => setParams((prev) => { const next = new URLSearchParams(prev); next.set("edit", key); return next; }, { replace: true });
  const o = overview.data;
  return <div className="space-y-4">
    {overview.isPending ? <Loading /> : overview.error ? <ErrorState error={overview.error} /> : o ? <section className="space-y-3" aria-label="Formats by group">
      <div className="flex flex-wrap items-center gap-3">
        <Input className="h-9 w-full sm:w-72" placeholder="Filter by format or profile" aria-label="Filter formats" value={filter} onChange={(e) => setFilter(e.target.value)} />
        <span className="text-[13px] text-muted-foreground">{o.formats.length} formats · {o.formats.filter((f) => f.definition === "trash").length} TRaSH · {plural(o.formats.filter((f) => f.definition === "frozen").length, "frozen copy", "frozen copies")} · {o.formats.filter((f) => f.definition === "ours").length} ours</span>
      </div>
      {o.formats.some((f) => f.definition === "frozen") ? <p className="text-[13px] text-muted-foreground">Frozen copies carry a TRaSH name but no TRaSH id, so no sync updates them: <Link className="underline" to="/admin/settings/quality-sync">Link formats to TRaSH</Link> on the Quality sync page.</p> : null}
      {groups.map(([group, list]) => <details key={group} open className="rounded-xl border border-[var(--line)]" data-group={group}>
        <summary className="cursor-pointer select-none px-3 py-2 text-sm font-semibold [overflow-wrap:anywhere]">{group} <span className="font-normal text-muted-foreground">· {list.length}</span></summary>
        <ul className="divide-y divide-[var(--line)] border-t border-[var(--line)]">{list.map((f) => <FormatRow key={`${f.flavour}/${f.trash_id || f.name}`} f={f} onEdit={f.key && o.source === "db" ? () => edit(f.key!) : undefined} />)}</ul>
      </details>)}
      {!groups.length ? <Card className="text-sm text-muted-foreground">No format matches.</Card> : null}
    </section> : null}
    <section className="space-y-2" aria-label="Standalone formats">
      <h3 className="font-semibold">Standalone formats</h3>
      <p className="text-[13px] text-muted-foreground">Formats outside any profile's own set, which profiles reference by key. Each is in the list above with Edit definition, except one sharing another's TRaSH ID, listed here; add one here.</p>
      <QualityPanel compact={compact} entity="formats" skip={o?.formats.flatMap((f) => f.key ? [f.key] : [])} match={recordMatches} />
    </section>
  </div>;
}
