// Upgrade approval (docs/design/manage.md "Upgrades waiting"): a held upgrade with Approve / Decline, used by the title
// page's copy card and the Requests page's "Upgrades waiting" tab. Approving routes the held release through the
// router like any grab (the server refuses while the copy is in shadow or downloads are off, and says why);
// declining keeps that release from being offered again. Managers only: the API answers 403 to anyone else, and the
// callers render nothing without the manage permission.
import { ArrowUpCircle, Check, Loader2, Send, X } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { usePerms } from "@/api/library";
import { useApproveUpgrade, useDeclineUpgrade, useUpgradeHolds, type UpgradeHold } from "@/api/upgrades";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { fmtAgo, fmtTime } from "@/lib/format";
import { approveState, decisionLine, episodesText, HOLD_HEADLINE, HOLD_STATE, holdLines, holdsFor } from "@/lib/upgrade-holds";
import { cn } from "@/lib/utils";

function errText(e: unknown): string {
  return e instanceof ApiError ? e.message : String((e as Error)?.message ?? e);
}

/** One hold: the release against the file it replaces, why it is better, and the decision buttons. */
export function UpgradeHoldBlock({ h, showTitle, className }: { h: UpgradeHold; showTitle?: boolean; className?: string }) {
  const approve = useApproveUpgrade();
  const decline = useDeclineUpgrade();
  const [declining, setDeclining] = useState(false);
  const [note, setNote] = useState("");
  const busy = approve.isPending || decline.isPending;
  const can = approveState(h, busy);
  const lines = holdLines(h);
  const eps = episodesText(h.episodes);
  const st = HOLD_STATE[h.state];
  const err = approve.error ?? decline.error;
  return (
    <div className={cn("min-w-0 space-y-2 rounded-lg border border-[var(--line)] p-3", className)} data-testid="upgrade-hold" data-hold-id={h.id} data-state={h.state}>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone={st?.tone ?? "neutral"}>
          {h.state === "pending" ? <ArrowUpCircle /> : null}
          {h.state === "pending" ? HOLD_HEADLINE : st?.label ?? h.state}
        </Badge>
        {showTitle ? (
          <Link to={`/title/${h.title.id}`} className="text-sm font-semibold text-[var(--ink)] hover:underline [overflow-wrap:anywhere]">
            {h.title.name}
            {h.title.year ? <span className="font-normal text-[var(--ink-3)]"> ({h.title.year})</span> : null}
          </Link>
        ) : null}
        {showTitle ? <span className="text-xs text-[var(--ink-3)]">{h.version_label || h.version}</span> : null}
        {eps ? <span className="font-mono text-xs text-[var(--ink-3)]">{eps}</span> : null}
      </div>
      <p className="break-all font-mono text-[12px] text-[var(--ink)]" data-testid="upgrade-hold-release">{h.release}</p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-sm">
        <dt className="text-[var(--ink-3)]">Would bring</dt>
        <dd className="[overflow-wrap:anywhere]" data-testid="upgrade-hold-held">{lines.held}</dd>
        <dt className="text-[var(--ink-3)]">Replaces</dt>
        <dd className="[overflow-wrap:anywhere]" data-testid="upgrade-hold-replaces">{lines.replaces}</dd>
      </dl>
      {h.reason ? <p className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]">{h.reason}</p> : null}
      <p className="text-xs text-[var(--ink-3)]">
        Found <span title={fmtTime(h.created_at)}>{fmtAgo(h.created_at)}</span>
        {h.seen_at !== h.created_at ? <> · last offered <span title={fmtTime(h.seen_at)}>{fmtAgo(h.seen_at)}</span></> : null}
        {h.indexer ? ` · ${h.indexer}` : ""}
      </p>
      {h.state !== "pending" ? (
        <p className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]" data-testid="upgrade-hold-decision">
          {decisionLine(h)}
          {h.decided_at ? <span title={fmtTime(h.decided_at)}> · {fmtAgo(h.decided_at)}</span> : null}
        </p>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Button size="xs" disabled={!can.approve} title={can.reason} onClick={() => approve.mutate(h.id)} data-testid="upgrade-approve">
            {approve.isPending ? <Loader2 className="animate-spin" /> : <Check />} Approve
          </Button>
          <Button size="xs" variant="ghost" className="text-[var(--bad-ink)]" disabled={!can.decline} aria-expanded={declining} onClick={() => setDeclining(!declining)} data-testid="upgrade-decline">
            <X /> Decline
          </Button>
          {can.reason ? <span className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]" data-testid="upgrade-approve-reason">{can.reason}</span> : null}
        </div>
      )}
      {declining && h.state === "pending" ? (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            decline.mutate({ id: h.id, note: note.trim() }, { onSuccess: () => setDeclining(false) });
          }}
        >
          <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="Note (optional)" maxLength={500} className="min-w-0 flex-1 basis-48" aria-label="Note" data-testid="upgrade-decline-note" autoFocus />
          <Button size="xs" variant="destructive" type="submit" disabled={busy} data-testid="upgrade-decline-confirm">
            {decline.isPending ? <Loader2 className="animate-spin" /> : <Send />} Decline
          </Button>
        </form>
      ) : null}
      {err ? <p className="text-xs text-[var(--bad-ink)] [overflow-wrap:anywhere]">{errText(err)}</p> : null}
    </div>
  );
}

/** The copy card's part: this copy's held upgrades, for managers only. */
export function CopyUpgradeHolds({ titleId, version }: { titleId: number; version?: string }) {
  const perms = usePerms();
  const q = useUpgradeHolds({ title: titleId }, perms.manage && titleId > 0);
  const holds = holdsFor(q.data?.items, version);
  if (!perms.manage || holds.length === 0) return null;
  return (
    <div className="space-y-2" data-testid="copy-upgrade-holds">
      {holds.map((h) => <UpgradeHoldBlock key={h.id} h={h} />)}
    </div>
  );
}
