// The library views' copy pieces (docs/design/ui.md "Library views"): a copy's colour dot, its bar (solid linked,
// striped in progress), its acquisition stage and its ownership line. Shared by the table, the posters and the title
// page.
import { Lock } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import type { Schemas } from "@/api/client";
import { widthClass } from "@/components/activity-bits";
import { activityHref, ownershipWords, pct, stageTone, stageWords, type CopyBar } from "@/lib/copies";
import { cn } from "@/lib/utils";
import "@/styles/library.css";

type Acquisition = Schemas["Acquisition"];
type VersionControl = Schemas["VersionControl"];

/** A copy's colour (data-vkey sets --vc). */
export function CopyDot({ vkey, className }: { vkey?: string; className?: string }) {
  return <span aria-hidden data-vkey={vkey} className={cn("inline-block size-2 shrink-0 rounded-[2px] bg-[var(--vc)]", className)} />;
}

/** The bar alone: linked solid, in progress striped. */
export function CopyTrack({ vkey, bar, className }: { vkey?: string; bar: CopyBar; className?: string }) {
  return (
    <span data-vkey={vkey} className={cn("copy-track", className)} role="img" aria-label={bar.label} title={bar.label}>
      <span className={cn("copy-have", widthClass(pct(bar.have, bar.total)))} />
      {bar.flight ? <span className={cn("copy-flight", widthClass(pct(bar.flight, bar.total)))} data-testid="copy-flight" /> : null}
    </span>
  );
}

/** One copy's bar line: dot, bar and "13/17". */
export function CopyBarLine({ vkey, label, bar, compact }: { vkey?: string; label: string; bar: CopyBar; compact?: boolean }) {
  return (
    <div className="flex min-w-0 items-center gap-1.5" data-testid="copy-bar" aria-label={`${label}: ${bar.label}`}>
      <CopyDot vkey={vkey} className="size-1.5" />
      <CopyTrack vkey={vkey} bar={bar} />
      <span className={cn("shrink-0 text-right tabular-nums text-[var(--ink-3)]", compact ? "min-w-6 text-[9px]" : "min-w-8 text-[10px]")}>{bar.count}</span>
    </div>
  );
}

/** The stage in words, linked to the Activity page. */
export function StageLink({ a, className }: { a: Acquisition | undefined | null; className?: string }) {
  if (!a) return null;
  const tone = stageTone(a.stage);
  return (
    <Link
      to={activityHref(a.stage)}
      data-stage={a.stage}
      title={a.words}
      className={cn(
        "inline-flex min-w-0 items-center gap-1 text-xs font-medium hover:underline",
        tone === "warning" ? "text-[var(--warn-ink)]" : "text-[var(--accent-ink)]",
        className,
      )}
    >
      <span className="truncate">{stageWords(a)}</span>
    </Link>
  );
}

/** "Followed · sonarr_4k · read-only" with a lock, or "Theatarr-managed". */
export function OwnerLine({ c, className }: { c: VersionControl | undefined | null; className?: string }) {
  const w = ownershipWords(c);
  if (!w) return null;
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1 text-[11px] text-[var(--ink-3)]", className)} data-testid="copy-owner">
      {c?.managed_by ? <Lock className="size-3 shrink-0" aria-hidden /> : null}
      <span className="truncate">{w}</span>
    </span>
  );
}

/** True from a min-width up (true without matchMedia, as in tests): the library table below it is a stacked list. */
export function useMinWidth(px: number): boolean {
  const mq = () => (typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(`(min-width: ${px}px)`) : null);
  const [wide, setWide] = useState(() => mq()?.matches ?? true);
  useEffect(() => {
    const m = typeof window.matchMedia === "function" ? window.matchMedia(`(min-width: ${px}px)`) : null;
    if (!m) return;
    const on = () => setWide(m.matches);
    on();
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, [px]);
  return wide;
}
