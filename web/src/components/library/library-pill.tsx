// A library's status pieces (docs/design/ui.md "Library views"): the thin status bar and the library pill, both in the
// one colour rule (lib/library-status.ts copyTone) and both named in words for the tooltip and screen readers. Used by
// the posters and the index table.
import { widthClass } from "@/components/activity-bits";
import { pct } from "@/lib/copies";
import type { CopyTone } from "@/lib/library-status";
import { cn } from "@/lib/utils";
import "@/styles/library.css";

/**
 * A thin bar in the tone's colour. Without a total it is the whole title or one movie library: full in the tone's
 * colour, striped while in progress, empty when idle. With a total (a series library) the held part is in the tone's
 * colour and the part in progress striped. label: the words for the tooltip and the accessible name.
 */
export function StatusBar({ tone, label, have, flight, total, className }: { tone: CopyTone; label: string; have?: number; flight?: number; total?: number; className?: string }) {
  const whole = total === undefined;
  const held = whole ? (tone === "idle" || tone === "progress" ? 0 : 100) : pct(have ?? 0, total);
  const going = whole ? (tone === "progress" ? 100 : 0) : pct(flight ?? 0, total);
  return (
    <span data-testid="status-bar" data-tone={tone} className={cn("status-track", className)} role="img" aria-label={label} title={label}>
      {held ? <span className={cn("status-have", widthClass(held))} /> : null}
      {going ? <span className={cn("status-flight", widthClass(going))} /> : null}
    </span>
  );
}

/** A library's pill: a dot in the tone's colour and the label, then detail if given ("4K · Remux-2160p"). words: the
 *  status in words ("missing"); the tooltip and accessible name read "4K: missing". */
export function LibraryPill({ label, tone, words, detail, className }: { label: string; tone: CopyTone; words: string; detail?: string; className?: string }) {
  const name = `${label}: ${words}`;
  return (
    <span data-testid="library-pill" data-tone={tone} role="img" aria-label={name} title={name} className={cn("inline-flex min-w-0 items-center gap-1 text-[10.5px] text-[var(--ink-2)]", className)}>
      <span aria-hidden className="status-dot size-1.5 shrink-0 rounded-full" />
      <span aria-hidden className="truncate">{detail ? `${label} · ${detail}` : label}</span>
    </span>
  );
}
