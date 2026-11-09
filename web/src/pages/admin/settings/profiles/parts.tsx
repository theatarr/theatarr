// Small pieces of the quality profile pages: score state tags, quality chips with the cutoff marked, the ladder
// editor, and the "preset + our edits" lists.
import type { ReactNode } from "react";
import type { OriginDiff } from "@/api/settings-profiles";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { qualityName, type Config, type Quality } from "../quality/fields";

type Tone = "positive" | "negative" | "warning" | "neutral" | "info" | "accent" | "outline";
export const STATE: Record<string, { label: string; tone: Tone; help: string }> = {
  follows: { label: "follows TRaSH", tone: "positive", help: "Equal to TRaSH's score: a TRaSH change applies by itself." },
  pinned: { label: "ours", tone: "accent", help: "Our score (pinned): kept when TRaSH changes; the change is held for you." },
  held: { label: "held", tone: "warning", help: "TRaSH changed it and the change waits in TRaSH sync for your decision." },
  differs: { label: "differs", tone: "info", help: "Differs from TRaSH and was never asked about: the next sync run asks once." },
  ours: { label: "our format", tone: "neutral", help: "A format of ours: TRaSH has nothing like it." },
  unbound: { label: "not bound", tone: "outline", help: "The profile does not follow a TRaSH score set: TRaSH's score is shown only." },
  unlinked: { label: "frozen copy", tone: "outline", help: "A copy of a TRaSH format without its TRaSH id: no sync updates it (Link formats to TRaSH)." },
  no_score: { label: "no TRaSH score", tone: "outline", help: "TRaSH sets no score for it in this score set: the score is ours." },
};
export function StateTag({ state, className }: { state: string; className?: string }) {
  const s = STATE[state] ?? { label: state, tone: "outline" as Tone, help: "" };
  return <Badge tone={s.tone} className={className} title={s.help} data-state={state}>{s.label}</Badge>;
}

export const DEFINITION: Record<string, { label: string; tone: Tone; help: string }> = {
  trash: { label: "TRaSH", tone: "info", help: "Defined by TRaSH (carries its id): definition updates come from TRaSH sync." },
  frozen: { label: "frozen copy", tone: "warning", help: "A copy from the arrs with a TRaSH name but no id: TRaSH sync never updates it." },
  ours: { label: "ours", tone: "neutral", help: "Our own definition." },
};

/** Allowed qualities as chips, best first, the cutoff marked. */
export function QualityChips({ qualities, cutoff }: { qualities: string[]; cutoff: string }) {
  return <ol className="flex min-w-0 flex-wrap gap-1" aria-label="Allowed qualities, best first">
    {qualities.map((q) => <li key={q}>
      <Badge tone={q === cutoff ? "accent" : "outline"} className="max-w-full whitespace-normal [overflow-wrap:anywhere]" data-cutoff={q === cutoff ? "true" : undefined}>
        {q}{q === cutoff ? <span className="font-normal"> · cutoff</span> : null}
      </Badge>
    </li>)}
  </ol>;
}

/** The ordered list of allowed qualities with the cutoff, best first: move, remove, add from the disallowed ones. */
export function QualityLadder({ config, onChange }: { config: Config; onChange: (c: Config) => void }) {
  const allowed = (config.qualities ?? []) as Quality[];
  const order = (config.quality_order ?? []) as Quality[];
  const cutoff = typeof config.cutoff === "string" ? config.cutoff : "";
  const rest = order.filter((q) => !allowed.some((a) => qualityName(a) === qualityName(q)));
  const withOrder = (next: Quality[]): Config => {
    const out: Config = { ...config, qualities: next };
    if (order.length) out.quality_order = [...next, ...order.filter((q) => !next.some((a) => qualityName(a) === qualityName(q)))];
    return out;
  };
  const move = (i: number, step: number) => { const next = [...allowed]; [next[i], next[i + step]] = [next[i + step], next[i]]; onChange(withOrder(next)); };
  const remove = (i: number) => {
    const next = allowed.filter((_, n) => n !== i);
    const out = withOrder(next);
    if (qualityName(allowed[i]) === cutoff) out.cutoff = next[0] ? qualityName(next[0]) : "";
    onChange(out);
  };
  // An added quality goes where the full order puts it, so the list stays best first.
  const add = (name: string) => {
    const q = rest.find((x) => qualityName(x) === name);
    if (!q) return;
    const rank = (x: Quality) => order.findIndex((o) => qualityName(o) === qualityName(x));
    const at = allowed.findIndex((a) => rank(a) > rank(q));
    const next = [...allowed];
    next.splice(at < 0 ? next.length : at, 0, q);
    onChange(withOrder(next));
  };
  return <div className="space-y-2" data-testid="quality-ladder">
    <ol className="space-y-1.5">
      {allowed.map((q, i) => {
        const name = qualityName(q);
        return <li key={name} className={cn("flex flex-wrap items-center gap-2 rounded-lg border px-2.5 py-1.5", name === cutoff ? "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_8%,transparent)]" : "border-[var(--line)]")} data-quality={name}>
          <span className="w-5 text-right text-xs tabular-nums text-muted-foreground">{i + 1}</span>
          <span className="min-w-[10rem] flex-1 text-sm [overflow-wrap:anywhere]">
            <span className="font-medium">{name}</span>
            {typeof q !== "string" ? <span className="text-xs text-muted-foreground"> · {q.qualities.join(", ")}</span> : null}
          </span>
          <label className="flex items-center gap-1 text-xs"><input type="radio" name="cutoff" checked={name === cutoff} onChange={() => onChange({ ...config, cutoff: name })} aria-label={`Cutoff at ${name}`} />cutoff</label>
          <Button type="button" size="xs" variant="ghost" disabled={i === 0} aria-label={`Move ${name} up`} onClick={() => move(i, -1)}>Up</Button>
          <Button type="button" size="xs" variant="ghost" disabled={i === allowed.length - 1} aria-label={`Move ${name} down`} onClick={() => move(i, 1)}>Down</Button>
          <Button type="button" size="xs" variant="ghost" disabled={allowed.length === 1} aria-label={`Remove ${name}`} onClick={() => remove(i)}>Remove</Button>
        </li>;
      })}
    </ol>
    {rest.length ? <label className="flex flex-wrap items-center gap-2 text-sm">Allow another quality
      <select className="h-8 rounded-md border border-[var(--line)] bg-transparent px-2 text-sm" value="" onChange={(e) => add(e.target.value)} aria-label="Allow another quality">
        <option value="">Choose…</option>
        {rest.map((q) => <option key={qualityName(q)} value={qualityName(q)}>{qualityName(q)}</option>)}
      </select>
    </label> : null}
    <p className="text-xs text-muted-foreground">Best first. Upgrades stop once the cutoff quality is reached and the cutoff score is met.</p>
  </div>;
}

const signed = (n: number) => (n > 0 ? `+${n}` : String(n));

/** A profile against the TRaSH preset it started from, in lists. */
export function DiffLists({ diff }: { diff: OriginDiff }) {
  const rows: [string, ReactNode][] = [];
  if (diff.qualities_added.length) rows.push(["Qualities added", diff.qualities_added.join(", ")]);
  if (diff.qualities_removed.length) rows.push(["Qualities removed", diff.qualities_removed.join(", ")]);
  if (diff.cutoff) rows.push(["Cutoff", `${diff.cutoff.ours} (TRaSH ${diff.cutoff.trash})`]);
  for (const s of diff.settings) rows.push([s.field.replaceAll("_", " "), `${s.ours} (TRaSH ${s.trash})`]);
  return <div className="space-y-3 text-sm" data-testid="origin-diff">
    {diff.same ? <p>No edits: the profile is the preset as TRaSH builds it today.</p> : null}
    {rows.length ? <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[auto_1fr]">{rows.map(([k, v]) => <div key={k} className="contents"><dt className="text-muted-foreground">{k}</dt><dd className="[overflow-wrap:anywhere]">{v}</dd></div>)}</dl> : null}
    {diff.scores_changed.length ? <div><h4 className="font-medium">Scores changed</h4><ul className="mt-1 space-y-0.5">{diff.scores_changed.map((s) => <li key={s.format} className="[overflow-wrap:anywhere]">{s.format}: <b>{signed(s.ours ?? 0)}</b> <span className="text-muted-foreground">(TRaSH {s.trash})</span></li>)}</ul></div> : null}
    {diff.formats_added.length ? <div><h4 className="font-medium">Scored here, not in the preset</h4><ul className="mt-1 space-y-0.5">{diff.formats_added.map((s) => <li key={s.format} className="[overflow-wrap:anywhere]">{s.format}: {signed(s.ours ?? 0)} {s.trash_id ? null : <span className="text-muted-foreground">(ours)</span>}</li>)}</ul></div> : null}
    {diff.formats_missing.length ? <div><h4 className="font-medium">In the preset, not scored here</h4><p className="mt-1 [overflow-wrap:anywhere]">{diff.formats_missing.map((s) => `${s.format} ${signed(s.trash ?? 0)}`).join(" · ")}</p></div> : null}
  </div>;
}
