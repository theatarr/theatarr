// The quality word (lib/upgrade.ts upgradeBadge) with its Why? explanation in the shared
// Modal: current against preferred source, the source ceiling's evidence kind and date, search coverage, sightings
// and any manual override (who, when, note), then the scores and cutoff arithmetic. Managers reach the source
// ceiling control (ceiling-control.tsx) from there. Used by the copy cards, season headers and the episode overview.
import { useQuery } from "@tanstack/react-query";
import { ArrowUpCircle, HelpCircle, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useAuth } from "@/api/auth";
import { api } from "@/api/client";
import { usePerms } from "@/api/library";
import type { Cutoff, UpgradeInfo } from "@/api/manage";
import type { UsersPage } from "@/api/users";
import { CeilingControl } from "@/components/title/ceiling-control";
import { Badge } from "@/components/ui/badge";
import { Modal } from "@/components/ui/modal";
import { cutoffLine, nextLine, shortfall, upgradeBadge, whyOf } from "@/lib/upgrade";
import { cn } from "@/lib/utils";

/**
 * The status badge, and a Why? button when the caller sees files and there is something to explain: not on a plain
 * Preferred quality met copy, unless it carries a manual ceiling that Why? lets a manager clear.
 */
export function UpgradeStatus({
  u,
  what,
  titleId,
  season,
  score,
  cutoff,
  releases,
  ceilingControl = true,
  className,
}: {
  u: UpgradeInfo | undefined | null;
  /** What the status is about, for the dialog's heading: "4K Remux copy", "Season 1 · Anime". */
  what: string;
  titleId: number;
  /** A series season, for the ceiling control (movies have none). */
  season?: number;
  score?: number | null;
  cutoff?: Cutoff | null;
  /** Where "Show releases" goes. */
  releases?: string;
  /** False where the ceiling has no single scope (a series copy across seasons: the season headers set it). */
  ceilingControl?: boolean;
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
      {perms.seeFiles && (u.state !== "cutoff_met" || u.ceiling?.override) ? (
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
        <WhyDialog u={u} what={what} titleId={titleId} season={season} score={score} cutoff={cutoff} releases={releases} manage={perms.manage && ceilingControl} onClose={() => setOpen(false)} />
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

/** Override authors by name for callers who may list users; others see "user #3" (or "you"). */
function useAuthorName(enabled: boolean): (id: number) => string | undefined {
  const me = useAuth();
  const perms = usePerms();
  const users = useQuery({ queryKey: ["users"], queryFn: () => api<UsersPage>("/users"), enabled: enabled && perms.users });
  return (id) => {
    if (me.data?.id === id) return "you";
    const u = users.data?.items?.find((x) => x.id === id);
    return u ? u.display_name || u.username : undefined;
  };
}

function WhyDialog({
  u,
  what,
  titleId,
  season,
  score,
  cutoff,
  releases,
  manage,
  onClose,
}: {
  u: UpgradeInfo;
  what: string;
  titleId: number;
  season?: number;
  score?: number | null;
  cutoff?: Cutoff | null;
  releases?: string;
  manage: boolean;
  onClose: () => void;
}) {
  const name = useAuthorName(!!u.ceiling?.override?.by);
  const why = whyOf(u, new Date(), (id) => name(id) ?? `user #${id}`);
  const sc = score !== undefined ? score : u.score;
  const co = cutoff ?? u.cutoff;
  const line = cutoffLine(u.quality, sc, co);
  // Every state short of the preferred quality says which part falls short, Best source seen so far and Best
  // available included (the quality can match while the score does not).
  const short = u.state !== "cutoff_met" && u.state !== "none" ? shortfall(u.quality, sc, co) : "";
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
        {why.detail ? <p className="mt-0.5 text-sm text-[var(--ink-2)]" data-testid="why-detail">{why.detail}</p> : null}
        <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
        <dl className="divide-y divide-[var(--line)]">
          <Row label="Current source">{why.current}</Row>
          <Row label="Preferred quality">{why.preferred}{co && !co.upgrades ? " · upgrades off" : ""}</Row>
          {why.reachable ? <Row label="Reachable now">{why.reachable}</Row> : null}
          {why.evidence ? <Row label="Evidence">{why.evidence}</Row> : null}
          {why.checked ? <Row label="Checked">{why.checked}</Row> : null}
          {why.override ? <Row label="Manual ceiling">{why.override}</Row> : null}
        </dl>
        {why.coverage.length > 0 ? (
          <details className="text-sm">
            <summary className="cursor-pointer font-medium">Search coverage ({why.coverage.length})</summary>
            <ul className="mt-2 space-y-1 text-xs text-muted-foreground">{why.coverage.map((c, i) => <li key={i}>{c}</li>)}</ul>
          </details>
        ) : null}
        {why.sightings.length > 0 ? (
          <details className="text-sm">
            <summary className="cursor-pointer font-medium">Sources seen ({why.sightings.length})</summary>
            <ul className="mt-2 space-y-1 break-all text-xs text-muted-foreground">{why.sightings.map((c, i) => <li key={i} title={c.source}>{c.text}</li>)}</ul>
          </details>
        ) : null}
        <div className="rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2.5 text-xs" data-testid="why-scores">
          <div className="mb-1 font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Scores</div>
          {line ? <p className="font-medium tabular-nums text-[var(--ink)]" data-testid="cutoff-line">{line}</p> : null}
          {short ? <p className="mt-1 text-[var(--ink-3)]">Short of the preferred quality because {short}.</p> : null}
          {next ? <p className="mt-1 text-[var(--ink-3)] [overflow-wrap:anywhere]">{next}</p> : null}
          {releases ? (
            <Link to={releases} className="mt-1 inline-block font-medium text-primary hover:underline" data-testid="show-releases-link">
              Show releases
            </Link>
          ) : null}
        </div>
        {manage ? <CeilingControl titleId={titleId} season={season} ceiling={u.ceiling} /> : null}
      </div>
    </Modal>
  );
}
