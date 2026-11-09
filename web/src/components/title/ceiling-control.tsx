import { useState } from "react";
import { useCeiling, type CeilingOverrideRequest, type SourceCeiling } from "@/api/manage";
import { Button } from "@/components/ui/button";

const TIERS = [
  ["uhd_bd", "4K disc"], ["web_2160", "2160p WEB"], ["bd", "Blu-ray"],
  ["web_1080", "1080p WEB"], ["dvd", "DVD"], ["tv", "TV"],
] as const;

export function CeilingControl({ titleId, season, ceiling }: { titleId: number; season?: number; ceiling?: SourceCeiling }) {
  const save = useCeiling(titleId);
  const [tier, setTier] = useState<CeilingOverrideRequest["tier"]>(ceiling?.override?.tier ?? "bd");
  const [note, setNote] = useState(ceiling?.override?.note ?? "");
  return (
    <details className="text-xs">
      <summary className="cursor-pointer">Set source ceiling</summary>
      <form className="mt-2 flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); save.mutate({ season, tier, note }); }}>
        <select aria-label="Source ceiling" className="rounded border bg-background p-1" value={tier} onChange={(e) => setTier(e.target.value as CeilingOverrideRequest["tier"])}>
          {TIERS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
        <input aria-label="Ceiling note" placeholder="Note (optional)" className="rounded border bg-background p-1" maxLength={2000} value={note} onChange={(e) => setNote(e.target.value)} />
        <Button size="sm" variant="outline" type="submit" disabled={save.isPending}>Set ceiling</Button>
        {ceiling?.override ? <Button size="sm" variant="ghost" type="button" disabled={save.isPending} onClick={() => save.mutate({ clear: true, season, tier })}>Clear override</Button> : null}
        {save.error ? <span role="alert">{save.error.message}</span> : null}
      </form>
      <a className="mt-2 inline-block underline" href="https://www.blu-ray.com/" target="_blank" rel="noreferrer">Check disc releases on blu-ray.com</a>
    </details>
  );
}
