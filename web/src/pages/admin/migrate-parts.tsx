import type { Gap, Inventory, Plan, Step, VerifyReport } from "@/api/migrate";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Choice } from "@/components/ui/confirm-dialog";

const topics: Record<Gap["topic"], string> = {
  secret: "Credentials to enter", usenet: "Usenet", local_library: "Local libraries",
  profile: "Profiles", paths: "Paths", prerequisite: "Prerequisites", unsupported: "Unsupported settings", unread: "Sources not read",
};

export function Gaps({ gaps }: { gaps: Gap[] | null }) {
  return <div className="space-y-3"><h3 className="font-semibold">Gaps and manual work ({gaps?.length ?? 0})</h3>
    {!gaps?.length ? <p>No gaps reported.</p> : Object.entries(topics).map(([topic, title]) => {
      const rows = gaps.filter((g) => g.topic === topic);
      return rows.length ? <Card key={topic} className="space-y-2"><h4 className="font-semibold">{title} ({rows.length})</h4>{rows.map((g, i) => <div key={i}><p><strong>{g.subject}:</strong> {g.what}</p><p className="text-muted-foreground">Why: {g.why}</p></div>)}</Card> : null;
    })}
  </div>;
}

export function InventoryView({ inventory: inv, plan }: { inventory: Inventory; plan: Plan }) {
  return <div className="space-y-3">
    <h3 className="font-semibold">Instances ({inv.arr?.length ?? 0}) · Libraries ({plan.libraries?.length ?? 0})</h3>
    {(plan.libraries ?? []).map((library) => <Card key={library.name}><h4 className="font-semibold">{library.name} · {library.kind}</h4><ul className="list-disc pl-5">{(library.versions ?? []).map((v) => <li key={v.key}>{v.key} from {v.instance} · {v.library} · profile {v.profile}</li>)}</ul></Card>)}
    {(inv.arr ?? []).map((a) => <Card key={a.name} className="space-y-2">
      <h4 className="font-semibold">{a.name} · {a.flavour} {a.app_version}</h4><p>{a.url}</p>
      {a.error ? <p role="alert">{a.error}</p> : <>
        <p>Titles: {a.titles.total} · Monitored: {a.titles.monitored} · With files: {a.titles.with_files} · Anime: {a.titles.anime}</p>
        <InventoryList title="Root folders" rows={(a.root_folders ?? []).map((r) => `${r.path} (${r.titles} titles)`)} />
        <InventoryList title="Profiles" rows={(a.quality_profiles ?? []).map((p) => `${p.name} (${p.titles} titles)`)} />
        <InventoryList title="Indexers" rows={(a.indexers ?? []).map((x) => `${x.name} · ${x.protocol}`)} />
        <InventoryList title="Download clients" rows={(a.download_clients ?? []).map((x) => `${x.name} · ${x.implementation} · ${x.protocol}`)} />
      </>}
    </Card>)}
    {inv.prowlarr ? <Card><h3 className="font-semibold">Prowlarr</h3><p>{inv.prowlarr.url}</p>{inv.prowlarr.error ? <p role="alert">{inv.prowlarr.error}</p> : <><InventoryList title="Indexers" rows={(inv.prowlarr.indexers ?? []).map((x) => `${x.name} · ${x.protocol}`)} /><InventoryList title="Applications" rows={(inv.prowlarr.applications ?? []).map((x) => x.name)} /></>}</Card> : null}
    {inv.seerr ? <Card><h3 className="font-semibold">Seerr / Overseerr</h3><p>{inv.seerr.url}</p>{inv.seerr.error ? <p role="alert">{inv.seerr.error}</p> : <InventoryList title="Servers" rows={[...(inv.seerr.radarr ?? []), ...(inv.seerr.sonarr ?? [])].map((s) => s.name)} />}</Card> : null}
    {inv.recyclarr ? <Card><h3 className="font-semibold">Recyclarr</h3><p>{inv.recyclarr.path}</p>{inv.recyclarr.error ? <p role="alert">{inv.recyclarr.error}</p> : <InventoryList title="Instances" rows={(inv.recyclarr.instances ?? []).map((s) => s.name)} />}</Card> : null}
    <Gaps gaps={plan.gaps} />
  </div>;
}

function InventoryList({ title, rows }: { title: string; rows: string[] }) {
  return <div><h4 className="font-medium">{title} ({rows.length})</h4>{rows.length ? <ul className="list-disc pl-5">{rows.map((r, i) => <li key={i}>{r}</li>)}</ul> : <p className="text-muted-foreground">None reported.</p>}</div>;
}

export const selectable = (s: Step) => s.automatic && s.status !== "done" && !s.blocked;

export function PlanSteps({ plan, selected, onSelect }: { plan: Plan; selected?: string[]; onSelect?: (step: Step, checked: boolean) => void }) {
  const steps = plan.steps ?? [];
  return <div className="space-y-3">{[...new Set(steps.map((s) => s.kind))].map((kind) => <Card key={kind} className="space-y-3">
    <h3 className="font-semibold capitalize">{kind} ({steps.filter((s) => s.kind === kind).length})</h3>
    {steps.filter((s) => s.kind === kind).map((s) => <div key={s.id} className="space-y-1 border-t border-border pt-2">
      {onSelect ? <Choice checked={selected?.includes(s.id) ?? false} disabled={!selectable(s)} onChange={(v) => onSelect(s, v)}>{s.what}</Choice> : <h4 className="font-medium">{s.what}</h4>}
      <Badge tone={s.status === "done" ? "positive" : "outline"}>{s.status.replaceAll("_", " ")}</Badge>
      <p>Why: {s.why}</p><p>Reversible: {s.reversible ? "yes" : "no"} · Undo: {s.undo}</p>
      {s.depends_on?.length ? <p className="text-muted-foreground">Requires: {s.depends_on.join(", ")}</p> : null}
      {s.blocked ? <p className="text-[var(--warn-ink)]">Blocked: {s.blocked}</p> : null}
      {s.error ? <p role="alert" className="text-[var(--bad-ink)]">{s.error}</p> : null}
      {(s.notes ?? []).map((n, i) => <p key={i} className="text-muted-foreground">{n}</p>)}
    </div>)}
  </Card>)}</div>;
}

export function Checkpoints({ plan }: { plan: Plan }) {
  return <div className="space-y-2"><h3 className="font-semibold">Backup checkpoints ({plan.checkpoints?.length ?? 0})</h3>
    {(plan.checkpoints ?? []).map((cp, i) => <Card key={i}><p>Backup: {cp.backup}</p><p>Settings: {cp.settings ?? "not reported"}</p><p>Generation {cp.generation} · {cp.at}</p></Card>)}
  </div>;
}

export function Verification({ report }: { report: VerifyReport }) {
  return <div className="space-y-3">
    <h3 className="font-semibold">Pending items ({report.pending?.length ?? 0})</h3>
    <ul className="list-disc pl-5">{(report.pending ?? []).map((p, i) => <li key={i}>{p}</li>)}</ul>
    {(report.versions ?? []).map((v) => <Card key={v.key} className="space-y-2"><h3 className="font-semibold">{v.key} · {v.instance}</h3>
      <Badge tone={v.ready ? "positive" : "warning"}>{v.ready ? "Ready for review" : "Needs attention"}</Badge>
      {(v.checks ?? []).map((c, i) => <div key={i}><p><strong>{c.status} · {c.name}:</strong> {c.message}</p>{c.fix ? <p>Next: {c.fix}</p> : null}</div>)}
      <details><summary>Later cutover checklist</summary><ul className="list-disc pl-5">{(v.cutover ?? []).map((c, i) => <li key={i}>{c}</li>)}</ul></details>
      <details><summary>Rollback</summary><ul className="list-disc pl-5">{(v.rollback ?? []).map((r, i) => <li key={i}>{r}</li>)}</ul></details>
    </Card>)}
    {report.rollback ? <p>Rollback backup: {report.rollback.backup}</p> : null}
  </div>;
}
