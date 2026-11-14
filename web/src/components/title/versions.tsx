// A library's (or an episode's) release history on the title page's History tab and in the episode window
// (docs/design/ui.md, Title page), and the link to a library's releases view. The library rows are in library-rows.tsx.
import { ArrowUpCircle, CheckCircle2, CircleDashed, CirclePause, Download, FileX2, PencilLine, Search, XCircle } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { usePerms, type HistoryEvent } from "@/api/library";
import { Mono } from "@/components/page";
import { journalLink } from "@/lib/admin-links";
import { Button } from "@/components/ui/button";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { ProviderTag } from "@/components/provider-pills";

const EVENTS: Record<HistoryEvent["event"], { label: string; icon: typeof Download; cls: string }> = {
  searched: { label: "Searched", icon: Search, cls: "text-muted-foreground" },
  grabbed: { label: "Grabbed", icon: Download, cls: "text-[var(--accent-ink)]" },
  imported: { label: "Imported", icon: CheckCircle2, cls: "text-[var(--ok-ink)]" },
  upgraded: { label: "Upgraded", icon: ArrowUpCircle, cls: "text-[var(--ok-ink)]" },
  failed: { label: "Failed", icon: XCircle, cls: "text-[var(--bad-ink)]" },
  // A download never tried (downloads were off when its turn came): not a failure, so not red.
  not_started: { label: "Not started", icon: CirclePause, cls: "text-[var(--ink-3)]" },
  deleted: { label: "File removed", icon: FileX2, cls: "text-[var(--ink-3)]" },
  renamed: { label: "Renamed", icon: PencilLine, cls: "text-[var(--ink-3)]" },
  ignored: { label: "Ignored", icon: CircleDashed, cls: "text-[var(--ink-3)]" },
};

/** The releases view of a title's library, with the row of one release (by hash) picked out when given. */
export function releasesHref(titleId: number, version: string, hash?: string): string {
  const q = new URLSearchParams({ version });
  if (hash) q.set("hash", hash.toLowerCase());
  return `/title/${titleId}/releases?${q.toString()}`;
}

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s);

/** One line for a searched event: results, accepted, and what it grabbed or why nothing. */
function searchLine(h: HistoryEvent): string {
  const counts = h.results != null ? `${fmtNumber(h.results)} results, ${fmtNumber(h.accepted ?? 0)} accepted` : "";
  return [counts, h.summary].filter(Boolean).join(" · ");
}

/** A version's (or an episode's) release history, newest first. With titleId and version, a manager's grabs and
 *  failures link to their release's row in the releases view (managers only, as that view), and the newest search to
 *  its results: the releases view shows the newest search only. */
export function History({ events, titleId, version }: { events: HistoryEvent[]; titleId?: number; version?: string }) {
  const perms = usePerms();
  const admin = perms.admin;
  const [all, setAll] = useState(false);
  if (events.length === 0) return <p className="text-sm text-muted-foreground">No history yet.</p>;
  const shown = all ? events : events.slice(0, 6);
  const target = perms.manage && titleId && version ? { titleId, version } : undefined;
  const rowLink = (h: HistoryEvent) => (target && h.info_hash ? releasesHref(target.titleId, target.version, h.info_hash) : undefined);
  return (
    <div>
      <ol className="relative space-y-3 border-l border-[var(--line)] pl-4">
        {shown.map((h, i) => {
          const e = EVENTS[h.event] ?? EVENTS.ignored;
          const release = h.event === "grabbed" || h.event === "failed" ? rowLink(h) : undefined;
          return (
            <li key={i} className="relative" data-testid="history-event">
              <e.icon className={cn("absolute -left-[25px] top-0.5 size-[17px] rounded-full bg-card", e.cls)} />
              <div className="flex flex-wrap items-baseline gap-x-2 text-sm">
                <span className="font-medium">{e.label}</span>
                {h.provider && (h.event === "grabbed" || h.event === "failed") ? <span className="text-muted-foreground">via <ProviderTag account={h.provider} /></span> : null}
                {h.episode ? <Mono className="text-muted-foreground">{h.episode}</Mono> : null}
                {h.quality ? <span className="text-muted-foreground">{h.quality}</span> : null}
                {h.score !== undefined && h.score !== null ? <span className="text-xs text-muted-foreground">Score then {h.score}</span> : null}
                <span className="ml-auto whitespace-nowrap text-xs text-muted-foreground" title={fmtTime(h.date)}>{fmtAgo(h.date)}</span>
              </div>
              {h.release_title ? (
                release ? (
                  <Link to={release} className="mt-0.5 block break-all font-mono text-[11.5px] text-muted-foreground hover:text-primary hover:underline" data-testid="history-release">{h.release_title}</Link>
                ) : (
                  <div className="mt-0.5 break-all font-mono text-[11.5px] text-muted-foreground">{h.release_title}</div>
                )
              ) : null}
              {h.event === "failed" && h.reason ? <div className="text-xs text-[var(--bad-ink)]" data-testid="history-reason">{h.reason}</div> : null}
              {h.event === "not_started" && h.reason ? <div className="text-xs text-[var(--ink-3)]" data-testid="history-reason">{h.reason}</div> : null}
              {h.event === "searched" && searchLine(h) ? <div className="text-xs text-muted-foreground" data-testid="history-search">{searchLine(h)}</div> : null}
              {h.trigger || h.source ? <div className="text-[11.5px] text-[var(--ink-3)]" data-testid="history-trigger">{cap(h.trigger || h.source || "")}</div> : null}
              {h.event === "searched" && h.latest && target ? (
                <Link className="mr-3 text-xs text-primary hover:underline" to={releasesHref(target.titleId, target.version, h.info_hash)}>See this search's results</Link>
              ) : null}
              {admin && h.decision_id ? <Link className="text-[11.5px] text-[var(--ink-3)] hover:text-foreground hover:underline" to={journalLink(String(h.decision_id))}>Journal entry</Link> : null}
            </li>
          );
        })}
      </ol>
      {events.length > shown.length || all ? (
        <Button variant="ghost" size="xs" className="mt-2" onClick={() => setAll(!all)}>
          {all ? "Show fewer" : `Show all ${events.length}`}
        </Button>
      ) : null}
    </div>
  );
}
