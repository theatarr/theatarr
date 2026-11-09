import { Link } from "react-router";
// Admin › Connections › Providers & indexers (GET /sources): each provider's sync, registry counts, router pacing and attempts;
// each indexer's budget and its last failure or back-off from the gateway.
import { useSources } from "@/api/hooks";
import { Empty, ErrorState, KV, Loading, Mono, Section, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";

const KIND: Record<string, string> = { realdebrid: "Real-Debrid", alldebrid: "AllDebrid", torbox: "TorBox" };

export function ProviderStatus() { return <SourcesPanel kind="providers" />; }
export function IndexerStatus() { return <SourcesPanel kind="indexers" />; }
export function SourcesPanel({ kind }: { kind?: "providers" | "indexers" }) {
  const q = useSources();
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const s = q.data;
  return (
    <div>
      {kind !== "indexers" ? <Section title="Status" className="mt-0">
        {s.providers.length === 0 ? <Card><Empty>No providers configured.</Empty></Card> : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {s.providers.map((p) => {
              const sync = p.last_sync;
              const tone = !sync ? "neutral" : sync.ok ? "positive" : "negative";
              const backedOff = p.backoff?.active || (p.pacing?.backoff_until && new Date(p.pacing.backoff_until) > new Date());
              return (
                <Card key={p.key} className="min-w-0 [overflow-wrap:anywhere]" data-provider={p.key}><Link to={`/admin/settings/downloads?edit=${encodeURIComponent(p.key)}`}>Edit</Link>
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <h3 className="font-semibold">{KIND[p.kind] ?? p.kind} <Mono className="text-muted-foreground">{p.key}</Mono></h3>
                      <div className="text-xs text-muted-foreground">order {p.order} · budget <Mono>{p.budget ?? "—"}</Mono></div>
                    </div>
                    <Badge tone={backedOff ? "warning" : tone}>{backedOff ? "backed off" : !sync ? "never synced" : sync.ok ? "ok" : "sync failed"}</Badge>
                  </div>
                  {sync && !sync.ok && sync.error ? <p className="mt-2 break-words text-[13px] text-[var(--bad-ink)]">{sync.error}</p> : null}
                  <div className="mt-3">
                    <KV items={[
                      ["Last sync", sync ? `${fmtTime(sync.finished_at)} (${fmtAgo(sync.finished_at)})` : "—"],
                      ["Last good sync", fmtTime(p.last_ok_sync)],
                      ["Registry", `${fmtNumber(p.counts.present)} present · ${fmtNumber(p.counts.absent)} absent · ${fmtNumber(p.counts.unreferenced)} unreferenced`],
                      ["Router, 24 h", Object.keys(p.attempts_24h).length === 0 ? "no attempts" : Object.entries(p.attempts_24h).map(([k, v]) => `${v} ${k}`).join(" · ")],
                      ["Pacing", p.pacing ? `${p.pacing.window_adds} adds this minute · ${p.pacing.hour_uncached} uncached this hour${p.pacing.failures ? ` · ${p.pacing.failures} failures` : ""}${p.pacing.reason ? ` (${p.pacing.reason})` : ""}` : "—"],
                    ]} />
                  </div>
                  <div className="mt-3 flex flex-wrap gap-1">{p.versions.map((v) => <VersionTag key={v} k={v} />)}</div>
                </Card>
              );
            })}
          </div>
        )}
      </Section> : null}
      {kind !== "providers" ? <Section title="Status" aside="Last result: the gateway records failures and back-offs; successful searches are not tracked yet.">
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Indexer</TableHead>
                <TableHead className="hidden sm:table-cell">Kind</TableHead>
                <TableHead>Privacy</TableHead>
                <TableHead className="hidden md:table-cell">Budget</TableHead>
                <TableHead>Last failure</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {s.indexers.map((ix) => (
                <TableRow key={ix.key} data-indexer={ix.key}>
                  <TableCell><Link className="text-[var(--accent-ink)] underline-offset-2 hover:underline" to={`/admin/settings/indexers?edit=${encodeURIComponent(ix.key)}`} aria-label={`Edit ${ix.key}`}><Mono className="[overflow-wrap:anywhere]">{ix.key}</Mono></Link><div className="mt-1 flex flex-wrap gap-1 sm:hidden">{ix.kind}</div></TableCell>
                  <TableCell className="hidden sm:table-cell">{ix.kind}{ix.proxy ? <Badge className="ml-2">proxy</Badge> : null}</TableCell>
                  <TableCell><Badge tone={ix.privacy === "public" ? "positive" : "warning"}>{ix.privacy}</Badge></TableCell>
                  <TableCell className="hidden md:table-cell">
                    <Mono>{ix.budget ?? "—"}</Mono>
                    {ix.budget_use ? <span className="text-xs text-muted-foreground"> · {fmtNumber(ix.budget_use.used_hour)} this hour · {fmtNumber(ix.budget_use.used_day)} today</span> : null}
                  </TableCell>
                  <TableCell className="max-w-[22rem] text-[13px]">
                    {ix.backoff ? (
                      <>
                        {ix.backoff.active ? <Badge tone="warning">backed off until {fmtTime(ix.backoff.until)}</Badge> : null}
                        <div className="break-words">{ix.backoff.reason || "failure"} <span className="text-muted-foreground">· {fmtAgo(ix.backoff.updated_at)}</span></div>
                      </>
                    ) : <span className="text-muted-foreground">none</span>}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </Section> : null}
    </div>
  );
}
