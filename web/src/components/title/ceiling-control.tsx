import { useState } from "react";
import { useCeiling, type CeilingOverrideRequest, type SourceCeiling } from "@/api/manage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

const TIERS = [
  ["uhd_bd", "4K disc"], ["web_2160", "2160p WEB"], ["bd", "Blu-ray"],
  ["web_1080", "1080p WEB"], ["dvd", "DVD"], ["tv", "TV"],
] as const;

export function CeilingControl({ titleId, season, ceiling }: { titleId: number; season?: number; ceiling?: SourceCeiling }) {
  const save = useCeiling(titleId);
  // Preset to the override, else the source now known for the title, so a careless Set ceiling never lowers it.
  const known = ceiling?.tier && ceiling.tier !== "unknown" ? ceiling.tier : undefined;
  const [tier, setTier] = useState<CeilingOverrideRequest["tier"]>(ceiling?.override?.tier ?? known ?? "uhd_bd");
  const [note, setNote] = useState(ceiling?.override?.note ?? "");
  return (
    <details className="text-xs">
      <summary className="cursor-pointer">Set source ceiling</summary>
      <p className="mt-1 text-[var(--ink-3)]">The best source this title is released on, when Theatarr guessed it wrong. It changes what the page says about upgrades, not what is searched or grabbed.</p>
      <form className="mt-2 flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); save.mutate({ season, tier, note }); }}>
        <Select aria-label="Source ceiling" className="w-auto" value={tier} onChange={(e) => setTier(e.target.value as CeilingOverrideRequest["tier"])}>
          {TIERS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </Select>
        <Input aria-label="Ceiling note" placeholder="Note (optional)" className="h-8 w-auto min-w-0 flex-1" maxLength={2000} value={note} onChange={(e) => setNote(e.target.value)} />
        <Button size="sm" variant="outline" type="submit" disabled={save.isPending}>Set ceiling</Button>
        {ceiling?.override ? <Button size="sm" variant="ghost" type="button" disabled={save.isPending} onClick={() => save.mutate({ clear: true, season, tier })}>Clear override</Button> : null}
        {save.error ? <span role="alert">{save.error.message}</span> : null}
      </form>
      <a className="mt-2 inline-block underline" href="https://www.blu-ray.com/" target="_blank" rel="noreferrer">Check disc releases on blu-ray.com</a>
    </details>
  );
}
