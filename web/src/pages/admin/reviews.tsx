import { Link, useLocation } from "react-router";
import { useAcknowledgeResolved, useFlaggedDecisions, useResolvedFlags, useReviewCounts } from "@/api/reviews";
import { Empty, ErrorState, Loading, PageHeader, RouteTabs, VersionTag } from "@/components/page";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Card } from "@/components/ui/card";
import { DefinitionHolds } from "@/components/settings/add-indexer";
import { fmtTime, plural } from "@/lib/format";
import { useJournalVerdict } from "@/api/hooks";
import { toast } from "@/components/toaster";
import { episodesWords } from "@/components/activity/row-bits";
import { journalHome, journalLink } from "@/lib/admin-links";
import { AnimeMappings } from "./anime-mappings";

const KINDS = [
  { id: "anime", label: "Anime mappings", help: "Episode numbering Theatarr could not match on its own. Accept or reject the proposed match." },
  { id: "indexers", label: "Indexer definitions", help: "Updated indexer definitions from the catalog. Approve to use the new version." },
  { id: "decisions", label: "Flagged decisions", help: "Wrong blocks the release and searches again only when Pause is off, the decision was for an active library and the release is known; otherwise it records the verdict." },
] as const;
const TABS = [{ id: "waiting", label: "Waiting", to: "/admin/reviews" }, ...KINDS.map(k => ({ ...k, to: `/admin/reviews/${k.id}` }))];

export function ReviewsPage() {
  const { pathname } = useLocation();
  const tab = TABS.find(t => t.to === pathname)?.id ?? "waiting";
  return <div>
    <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Reviews" description="Decisions waiting for you." />
    <RouteTabs tabs={TABS} active={tab} />
    {tab === "waiting" ? <Waiting /> : tab === "anime" ? <AnimeMappings /> : tab === "indexers" ? <DefinitionHolds showEmpty /> : <FlaggedDecisions />}
  </div>;
}
export function Waiting() {
  const counts = useReviewCounts();
  if (counts.isPending) return <Loading />;
  return <>
    {counts.error ? <ErrorState error={counts.error} /> : null}
    {counts.total === 0 && !counts.decisionsMore && !counts.error ? <Card><EmptyState title="Nothing is waiting for you">Anime mappings, indexer definitions and flagged decisions.</EmptyState></Card> : null}
    <div className="grid gap-3 sm:grid-cols-2">{KINDS.filter(k => counts[k.id] > 0 || k.id === "decisions" && counts.decisionsMore).map(k => <Card key={k.id}>
      <h2 className="text-[16px] font-semibold text-[var(--ink)]">{k.label} ({k.id === "decisions" && counts.decisionsMore ? counts.decisions ? `${counts.decisions}+` : "?" : counts[k.id]})</h2>
      <p className="my-3 text-[13px] text-[var(--ink-2)]">{k.help}</p>
      <Button asChild size="sm"><Link to={`/admin/reviews/${k.id}`} aria-label={`Review ${k.label.toLowerCase()}`}>Review</Link></Button>
    </Card>)}</div>
  </>;
}
function FlaggedDecisions() {
  const q = useFlaggedDecisions();
  const resolved = useResolvedFlags();
  const settled = new Set((resolved.data?.items ?? []).map(r => r.entry));
  return <>
    <p className="mb-3 text-[13px]"><Link className="text-[var(--accent-ink)] underline-offset-2 hover:underline" to={journalHome()}>See the full journal</Link></p>
    <ResolvedFlags />
    {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : !q.data?.items?.length ? <Empty>{q.data?.next_cursor ? "None in the newest entries." : "No flagged decisions."}</Empty> : <ul className="divide-y divide-[var(--line)] rounded-[8px] border border-[var(--line)] bg-[var(--surface)]">{q.data.items.map(e => {
      // The item: the title (or the link's folder) with its episodes; a link entry also names its file.
      const eps = episodesWords(e.release?.episodes);
      const path = (e.import?.links as { path?: string }[] | undefined)?.[0]?.path;
      return <li key={e.id} className="space-y-1 px-4 py-3" data-testid="flagged">
        <Link className="font-medium text-[var(--accent-ink)] [overflow-wrap:anywhere] hover:underline" to={journalLink(e.id ?? "")}>{e.title?.name ?? e.summary ?? "Decision"}{eps ? ` ${eps}` : ""}</Link>
        <p className="flex flex-wrap items-center gap-2 text-[12.5px] text-[var(--ink-3)]">{e.version ? <VersionTag k={e.version} /> : null}<span>{fmtTime(e.at)}</span></p>
        {e.release?.title ? <p className="text-[12.5px] text-[var(--ink-3)] [overflow-wrap:anywhere]">{e.release.title}</p> : path ? <p className="font-mono text-[11.5px] text-[var(--ink-3)] [overflow-wrap:anywhere]">{path}</p> : null}
        <div className="flex flex-wrap items-start gap-2">
          <p className="min-w-0 flex-1 text-[13px] text-[var(--ink-2)] [overflow-wrap:anywhere]">{e.flags?.filter(f => !f.acked).map(f => f.reason || f.code).join("; ") || e.summary}</p>
          {/* A dead link that is still broken is not acknowledged from here: it needs a new grab. */}
          {e.id && (!e.flags?.some(f => !f.acked && f.code === "dead_link") || settled.has(e.id)) ? <AcknowledgeOne id={e.id} /> : null}
        </div>
      </li>;
    })}</ul>}
    {q.data?.next_cursor ? <p className="mt-3 text-[13px]"><Link className="text-[var(--accent-ink)] underline-offset-2 hover:underline" to={journalHome("?suspect=1")}>See all flagged decisions</Link></p> : null}
  </>;
}

/** The OK verdict on one entry: it acknowledges the entry's flags; the record stays. */
function AcknowledgeOne({ id }: { id: string }) {
  const v = useJournalVerdict();
  return <Button size="xs" variant="outline" disabled={v.isPending} onClick={() => v.mutate({ id, verdict: "ok", reason: "Acknowledged on Reviews" }, {
    onError: (err: Error) => toast({ tone: "bad", title: "Acknowledge failed", body: err.message }),
  })}>Acknowledge</Button>;
}

/** Dead-link flags that are settled (the link is gone, repaired or works again, or a newer flag for it exists),
 * acknowledged in one go. The entries stay in the journal with an OK verdict. Nothing shows while it loads. */
function ResolvedFlags() {
  const q = useResolvedFlags();
  const ack = useAcknowledgeResolved();
  if (q.error) return <p className="mb-3 text-[13px] text-[var(--ink-3)]">Could not check for settled dead-link flags.</p>;
  const n = q.data?.count ?? 0;
  if (n === 0) return null;
  return <Card className="mb-3 flex flex-wrap items-center gap-3" data-testid="resolved-flags">
    <p className="min-w-0 flex-1 text-[13px] text-[var(--ink-2)]">{plural(n, "dead-link flag")} {n === 1 ? "is" : "are"} settled: the link is gone, repaired or works again, or a newer flag for the same link exists. Acknowledging keeps the entries in the journal.</p>
    <Button size="sm" variant="outline" disabled={ack.isPending} onClick={() => ack.mutate()}>Acknowledge {plural(n, "settled flag")}</Button>
  </Card>;
}
