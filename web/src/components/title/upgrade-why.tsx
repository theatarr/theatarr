// The quality word (lib/upgrade.ts upgradeBadge) with its Why? explanation in the shared Modal, in three plain lines
// (whyLines): the file held, where upgrades stop, and the verdict with the better release found and what came of it.
// Used by the Libraries table, season headers and the episode overview.
import { ArrowUpCircle, HelpCircle, X } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import type { Schemas } from "@/api/client";
import { usePerms } from "@/api/library";
import type { Cutoff, UpgradeInfo } from "@/api/manage";
import { Badge } from "@/components/ui/badge";
import { Modal } from "@/components/ui/modal";
import { upgradeBadge, whyLines } from "@/lib/upgrade";
import { cn } from "@/lib/utils";

type HistoryEvent = Schemas["HistoryEvent"];

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
  history,
  className,
}: {
  u: UpgradeInfo | undefined | null;
  /** What the status is about, for the dialog's heading: "4K Remux copy", "Season 1 · Anime". */
  what: string;
  score?: number | null;
  cutoff?: Cutoff | null;
  /** Where "Show releases" goes. */
  releases?: string;
  /** The library's history on the page: a failed download of the release found is said ("Tried Nov 12: …"). */
  history?: readonly HistoryEvent[] | null;
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
        <WhyDialog u={u} what={what} score={score} cutoff={cutoff} releases={releases} history={history} onClose={() => setOpen(false)} />
      ) : null}
    </span>
  );
}

function WhyDialog({
  u,
  what,
  score,
  cutoff,
  releases,
  history,
  onClose,
}: {
  u: UpgradeInfo;
  what: string;
  score?: number | null;
  cutoff?: Cutoff | null;
  releases?: string;
  history?: readonly HistoryEvent[] | null;
  onClose: () => void;
}) {
  const headline = upgradeBadge(u)?.label ?? "";
  const lines = whyLines(u, score !== undefined ? score : u.score, cutoff ?? u.cutoff, history);
  return (
    <Modal
      label={`Why: ${headline}`}
      onClose={onClose}
      testid="upgrade-why-dialog"
      className="z-[80] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-xl sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <div className="text-xs font-medium text-[var(--ink-3)]">{what}</div>
        <h2 className="mt-0.5 text-lg font-semibold text-[var(--ink)]">{headline}</h2>
        <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 text-sm text-[var(--ink-2)]" data-testid="why-lines">
        {lines.map((l, i) => (
          <p key={i} className={cn("tabular-nums [overflow-wrap:anywhere]", i < lines.length - 1 && "text-[var(--ink)]")}>{l}</p>
        ))}
        {releases ? (
          <Link to={releases} className="inline-block font-medium text-primary hover:underline" data-testid="show-releases-link">
            Show releases
          </Link>
        ) : null}
      </div>
    </Modal>
  );
}
