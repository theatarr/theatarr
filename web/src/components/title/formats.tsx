// The custom formats a release or a file matched, each a small chip with its score: the release list's Formats
// (release-grid.tsx) and the episode table's Formats column (seasons.tsx).
import { fmtNumber } from "@/lib/format";
import { cn } from "@/lib/utils";

/** A score with its sign and grouped digits: "+1,500", "-10,000", "0" (the chips, the score column, the detail). */
export const signedScore = (n: number) => (n > 0 ? `+${fmtNumber(n)}` : fmtNumber(n));

/** The matched formats with their scores; zeros: also a 0 score (the opened detail and the cards). */
export function Formats({ c, zeros = false }: { c: { formats?: { name?: string; score?: number }[] | null }; zeros?: boolean }) {
  const fs = c.formats ?? [];
  if (fs.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-1">
      {fs.map((f, i) => (
        <span key={i} className="rounded border border-[var(--line)] px-1 text-[10.5px] text-[var(--ink-3)]">
          {f.name}
          {f.score || zeros ? <span className={cn("ml-1 tabular-nums", (f.score ?? 0) < 0 ? "text-[var(--bad-ink)]" : (f.score ?? 0) > 0 ? "text-[var(--ok-ink)]" : undefined)}>{signedScore(f.score ?? 0)}</span> : null}
        </span>
      ))}
    </span>
  );
}
