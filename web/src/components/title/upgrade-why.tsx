// The quality word (lib/upgrade.ts upgradeBadge) with its Why? explanation in the shared
// Modal: what falls short with its numbers first, then current against preferred source (folded when they are the
// same source), then the scores and cutoff arithmetic. Used by the copy cards, season headers and the episode overview.
import { ArrowUpCircle, HelpCircle, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { usePerms } from "@/api/library";
import type { Cutoff, UpgradeInfo } from "@/api/manage";
import { Badge } from "@/components/ui/badge";
import { Modal } from "@/components/ui/modal";
import { cutoffLine, nextLine, shortfall, upgradeBadge, whyOf } from "@/lib/upgrade";
import { cn } from "@/lib/utils";

/**
 * The status badge, and a Why? button when the caller sees files and there is something to explain: not on a
 * Preferred quality met copy.
 */
export function UpgradeStatus({
  u,
  what,
  score,
  cutoff,
  releases,
  className,
}: {
  u: UpgradeInfo | undefined | null;
  /** What the status is about, for the dialog's heading: "4K Remux copy", "Season 1 · Anime". */
  what: string;
  score?: number | null;
  cutoff?: Cutoff | null;
  /** Where "Show releases" goes. */
  releases?: string;
  className?: string;
}) {
  const perms = usePerms();
  const [open, setOpen] = useState(false);
  const w = upgradeBadge(u);
  if (!u || !w) return null;
  return (
    <span className={cn("inline-flex flex-wrap items-center gap-1.5", className)} data-testid="upgrade-status" data-state={u.state}>
      <Badge tone={w.tone}>
        {u.state === "upgrade_available" ? <ArrowUpCircle /> : null}
        {w.label}
      </Badge>
      {perms.seeFiles && u.state !== "cutoff_met" ? (
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="inline-flex items-center gap-0.5 text-xs font-medium text-[var(--accent-ink)] hover:underline"
          data-testid="upgrade-why"
        >
          <HelpCircle className="size-3.5" /> Why?
        </button>
      ) : null}
      {open ? (
        <WhyDialog u={u} what={what} score={score} cutoff={cutoff} releases={releases} onClose={() => setOpen(false)} />
      ) : null}
    </span>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[8.5rem_1fr] gap-2 py-1.5 text-sm max-sm:grid-cols-1 max-sm:gap-0.5">
      <dt className="text-[var(--ink-3)]">{label}</dt>
      <dd className="min-w-0 [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

function WhyDialog({
  u,
  what,
  score,
  cutoff,
  releases,
  onClose,
}: {
  u: UpgradeInfo;
  what: string;
  score?: number | null;
  cutoff?: Cutoff | null;
  releases?: string;
  onClose: () => void;
}) {
  const why = whyOf(u);
  const sc = score !== undefined ? score : u.score;
  const co = cutoff ?? u.cutoff;
  const line = cutoffLine(u.quality, sc, co);
  // Every state short of the preferred quality leads with the profile score when it falls short (the quality can
  // match while the score does not).
  const short = u.state !== "cutoff_met" && u.state !== "none" ? shortfall(sc, co) : "";
  // The source lines say one thing when current and preferred are the same source: folded.
  const sameSource = why.current === why.preferred;
  const sources = (
    <>
      <Row label="Current source">{why.current}</Row>
      <Row label="Preferred quality">{why.preferred}{co && !co.upgrades ? " · upgrades off" : ""}</Row>
    </>
  );
  const next = nextLine(u);
  return (
    <Modal
      label={`Why: ${why.headline}`}
      onClose={onClose}
      testid="upgrade-why-dialog"
      className="z-[80] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-xl sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <div className="text-xs font-medium text-[var(--ink-3)]">{what}</div>
        <h2 className="mt-0.5 text-lg font-semibold text-[var(--ink)]">{why.headline}</h2>
        {short ? <p className="mt-0.5 text-sm font-medium tabular-nums text-[var(--ink)]" data-testid="why-short">{short}</p> : null}
        {why.detail ? <p className="mt-0.5 text-sm text-[var(--ink-2)]" data-testid="why-detail">{why.detail}</p> : null}
        <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
        {sameSource ? (
          <details className="text-sm" data-testid="why-sources">
            <summary className="cursor-pointer font-medium">Source: {why.current}, the preferred one</summary>
            <dl className="mt-1 divide-y divide-[var(--line)]">{sources}</dl>
          </details>
        ) : null}
        {sameSource ? null : <dl className="divide-y divide-[var(--line)]">{sources}</dl>}
        <div className="rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2.5 text-xs" data-testid="why-scores">
          <div className="mb-1 font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Scores</div>
          {line ? <p className="font-medium tabular-nums text-[var(--ink)]" data-testid="cutoff-line">{line}</p> : null}
          {next ? <p className="mt-1 text-[var(--ink-3)] [overflow-wrap:anywhere]">{next}</p> : null}
          {releases ? (
            <Link to={releases} className="mt-1 inline-block font-medium text-primary hover:underline" data-testid="show-releases-link">
              Show releases
            </Link>
          ) : null}
        </div>
      </div>
    </Modal>
  );
}
