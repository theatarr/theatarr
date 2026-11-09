// Admin › Advanced › Comparison (the shadow run): the comparison report (GET /shadow/summary) and its diff rows (GET /shadow/diffs, /shadow/diffs/{id}).
import { ArrTabs } from "./arr-tabs";
import { PRODUCT_NAME } from "@/brand";
import { Link, useParams, useSearchParams } from "react-router";
import { useShadowDiff, useShadowDiffs, useShadowSummary, type ShadowDiff } from "@/api/hooks";
import { TitleThumb } from "@/components/poster-card";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Section, Stat, StatGrid, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtBytes, fmtNumber, fmtPercent, fmtTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { resultsSummary } from "@/lib/comparison";

export const BUCKETS: Record<string, { name: string; tone: "positive" | "negative" | "warning" | "info" }> = {
  agree: { name: "Agree", tone: "positive" },
  arr_grabbed_we_wouldnt: { name: "Arr only / rejected", tone: "negative" },
  we_grab_another: { name: "Different choice", tone: "warning" },
  we_grab_arr_didnt: { name: "We grab, arr didn't", tone: "info" },
};

function BucketBadge({ b, reason }: { b?: string; reason?: string }) {
  const x = reason === "metadata_pending" ? { name: "Waiting for metadata", tone: "info" as const } : BUCKETS[b ?? ""];
  return <Badge tone={x?.tone ?? "neutral"}>{x?.name ?? b ?? "—"}</Badge>;
}

export function Shadow() {
  const [params, setParams] = useSearchParams();
  const f = {
    bucket: params.get("bucket") ?? undefined,
    reason: params.get("reason") ?? undefined,
    version: params.get("version") ?? undefined,
    search_found_better: params.get("search_found_better") ?? undefined,
    offset: Number(params.get("offset") ?? 0) || 0,
  };
  const sum = useShadowSummary();
  const diffs = useShadowDiffs(f);
  const set = (k: string, v?: string) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v);
    else n.delete(k);
    n.delete("offset");
    setParams(n);
  };
  if (sum.isPending) return <Loading />;
  if (sum.error) return <ErrorState error={sum.error} />;
  const s = sum.data;
  const met = s.agreement !== null && s.agreement !== undefined && s.agreement >= (s.bar ?? 0.95);
  const filtered = !!(f.bucket || f.reason || f.version || f.search_found_better);
  return (
    <div>
      <PageHeader
        crumb={[{ label: "Admin", to: "/admin" }, { label: "Advanced", to: "/admin/advanced" }, { label: "Sonarr/Radarr migration", to: "/admin/advanced/arr" }]}
        title="Comparison"
        description={`Shadow libraries compare dry-run decisions; active libraries with a comparison arr compare real grabs. Bar: ${fmtPercent(s.bar, 0)} agreement over ${s.window ?? "14d"}. Live comparisons include grabs made by either side; dry-run hand grabs are excluded.`}
      />
      <ArrTabs />
      <StatGrid>
        <Stat testid="shadow-agreement" value={fmtPercent(s.agreement)} tone={s.agreement === null || s.agreement === undefined ? undefined : met ? "ok" : "warn"} label={`agreement (bar ${fmtPercent(s.bar, 0)})`} />
        <Stat value={fmtNumber(s.grabs)} label={`comparisons since ${fmtTime(s.since)}`} />
        <Stat value={`${fmtNumber(s.searched)} / ${fmtNumber(s.grabs)}`} label={`searched · ${fmtNumber(s.search_found_better)} found better`} />
        <Stat value={fmtNumber(s.open_picks)} label="open RSS picks" />
        <Stat value={fmtNumber(s.pending)} label="pending jobs" />
        <Stat value={fmtNumber(s.pending_metadata)} label="waiting for metadata · excluded" />
        <Stat value={fmtNumber(s.hand_grabs?.grabs)} label={`hand grabs · ${fmtNumber(s.hand_grabs?.accepted)} accepted`} />
      </StatGrid>

      <div className="mt-6 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card className="p-0">
          <h3 className="px-4 pt-4 font-semibold">By library</h3>
          <Table wrapperClassName="mt-2">
            <TableBody>
              {(s.versions ?? []).length === 0 ? (
                <TableRow><TableCell className="text-muted-foreground">No grabs in the window.</TableCell></TableRow>
              ) : (
                (s.versions ?? []).map((v) => (
                  <TableRow key={v.version}>
                    <TableCell><button type="button" onClick={() => set("version", v.version)}><VersionTag k={v.version ?? "?"} /></button></TableCell>
                    <TableCell className="tabular-nums">{fmtNumber(v.agree)} / {fmtNumber(v.grabs)}</TableCell>
                    <TableCell className="text-right font-medium">{fmtPercent(v.agreement)}</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </Card>
        <Card className="p-0">
          <h3 className="px-4 pt-4 font-semibold">Buckets and reasons</h3>
          <Table wrapperClassName="mt-2">
            <TableBody>
              {Object.entries(BUCKETS).map(([k]) => {
                const reasons = (s.reasons ?? []).filter((r) => r.bucket === k);
                return (
                  <TableRow key={k}>
                    <TableCell><button type="button" onClick={() => set("bucket", k)}><BucketBadge b={k} /></button></TableCell>
                    <TableCell className="tabular-nums">{fmtNumber(s.buckets?.[k] ?? 0)}</TableCell>
                    <TableCell className="text-[13px]">
                      {reasons.length === 0 ? <span className="text-muted-foreground">—</span> : reasons.map((r) => (
                        <button key={r.reason} type="button" onClick={() => { const n = new URLSearchParams(); n.set("bucket", k); n.set("reason", r.reason ?? ""); setParams(n); }} className="mr-3 inline-flex min-w-0 items-center gap-1 hover:text-primary">
                          <Mono className="[overflow-wrap:anywhere]">{r.reason}</Mono> <span className="text-muted-foreground">{r.count}</span>
                        </button>
                      ))}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      </div>

      <Section
        title="Differences and agreements"
        aside={filtered ? (
          <span className="flex flex-wrap items-center gap-2">
            {f.bucket ? <BucketBadge b={f.bucket} /> : null}
            {f.reason ? <Mono>{f.reason}</Mono> : null}
            {f.version ? <VersionTag k={f.version} /> : null}
            {f.search_found_better ? <Badge tone="info">search found better</Badge> : null}
            <Button size="xs" variant="ghost" onClick={() => setParams(new URLSearchParams())}>Clear</Button>
          </span>
        ) : `${fmtNumber(diffs.data?.total)} rows`}
      >
        {diffs.isPending ? <Loading /> : diffs.error ? <ErrorState error={diffs.error} /> : <DiffTable rows={diffs.data?.items ?? []} />}
        {(diffs.data?.total ?? 0) > f.offset + 50 || f.offset > 0 ? (
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="outline" disabled={f.offset === 0} onClick={() => { const n = new URLSearchParams(params); n.set("offset", String(Math.max(0, f.offset - 50))); setParams(n); }}>Previous</Button>
            <Button size="sm" variant="outline" disabled={(diffs.data?.total ?? 0) <= f.offset + 50} onClick={() => { const n = new URLSearchParams(params); n.set("offset", String(f.offset + 50)); setParams(n); }}>Next</Button>
          </div>
        ) : null}
      </Section>

      <Section title="Arr cursors">
        {(s.cursors ?? []).length === 0 ? <Card><Empty>No arr history read yet.</Empty></Card> : <Card className="p-0">
          <Table>
            <TableBody>
              {(s.cursors ?? []).map((c) => (
                <TableRow key={c.instance}>
                  <TableCell><Mono>{c.instance}</Mono></TableCell>
                  <TableCell className="tabular-nums">{c.history_id}</TableCell>
                  <TableCell className={cn("text-muted-foreground", (c.behind ?? 0) > 0 && "text-[var(--warn)]")}>{c.behind ?? 0} behind</TableCell>
                  <TableCell className="hidden text-muted-foreground sm:table-cell">{fmtAgo(c.updated_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>}
      </Section>
    </div>
  );
}

function DiffTable({ rows }: { rows: ShadowDiff[] }) {
  if (rows.length === 0) return <Card><Empty>No rows.</Empty></Card>;
  return (
    <Card className="p-0">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>Title</TableHead>
            <TableHead className="hidden sm:table-cell">Library</TableHead>
            <TableHead>Result</TableHead>
            <TableHead className="hidden lg:table-cell">Release</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((d) => (
            <TableRow key={d.id}>
              <TableCell className="text-[13px] sm:whitespace-nowrap"><Link className="hover:text-primary" to={`/admin/advanced/arr/comparison/${d.id}`}>{fmtTime(d.at)}</Link></TableCell>
              <TableCell>
                <span className="flex items-center gap-3">
                  <TitleThumb id={d.title_id} />
                  <span>
                    <Link className="font-medium [overflow-wrap:anywhere] hover:text-primary" to={`/admin/advanced/arr/comparison/${d.id}`}>{d.title}</Link>
                    {d.year ? <span className="text-muted-foreground"> ({d.year})</span> : null}
                  </span>
                </span>
              </TableCell>
              <TableCell className="hidden sm:table-cell">{d.version ? <VersionTag k={d.version} /> : null}</TableCell>
              <TableCell>
                <BucketBadge b={d.bucket} reason={d.reason} /> <Mono className="text-muted-foreground [overflow-wrap:anywhere]">{d.reason}</Mono>
              </TableCell>
              <TableCell className="hidden max-w-[28rem] break-words text-[13px] lg:table-cell">
                {d.arr_release || d.our_release}
                <span className="text-muted-foreground"> · {d.basis}</span>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

export function ShadowDiffPage() {
  const { id = "" } = useParams();
  const q = useShadowDiff(id);
  const crumb = [{ label: "Admin", to: "/admin" }, { label: "Advanced", to: "/admin/advanced" }, { label: "Sonarr/Radarr migration", to: "/admin/advanced/arr" }, { label: "Comparison", to: "/admin/advanced/arr/comparison" }];
  if (q.isPending) return <Loading />;
  if (q.error) return (<div><PageHeader crumb={crumb} title={`#${id}`} /><ErrorState error={q.error} /></div>);
  const d = q.data as ShadowDiff & { grab?: { router_plan?: StoredPlan }; rss_pick?: { router_plan?: StoredPlan } };
  const results = d.grab?.results ?? [];
  return (
    <div className="min-w-0 [overflow-wrap:anywhere]">
      <PageHeader
        crumb={crumb}
        title={<span className="flex items-center gap-4"><TitleThumb id={d.title_id} className="w-12" /><span>{d.title}{d.year ? <span className="text-muted-foreground"> ({d.year})</span> : null}</span></span>}
        description={<span className="flex flex-wrap items-center gap-2"><BucketBadge b={d.bucket} reason={d.reason} /><Mono>{d.reason}</Mono>{d.version ? <VersionTag k={d.version} /> : null}<span>{fmtTime(d.at)}</span></span>}
        actions={<Button asChild variant="outline" size="sm"><a href={`/title/${d.title_id}`}>Title page</a></Button>}
      />
      <ArrTabs />
      {d.detail ? <Card className="text-sm leading-relaxed [overflow-wrap:anywhere]">{d.detail}</Card> : null}
      <div className="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card className="min-w-0">
          <h3 className="mb-3 font-semibold">The arr grabbed</h3>
          <KV items={[
            ["Release", <span key="r" className="break-words">{d.arr_release || "—"}</span>],
            ["Quality", d.arr_quality || "—"],
            ["Score", d.arr_score ?? "—"],
            ["Instance", <Mono key="i">{d.instance}</Mono>],
            ["Source", d.release_source || "—"],
          ]} />
        </Card>
        <Card className="min-w-0">
          <h3 className="mb-3 font-semibold">{PRODUCT_NAME} {d.basis === "live" ? "grabbed" : "would grab"}</h3>
          <KV items={[
            ["Release", <span key="r" className="break-words">{d.our_release || "—"}</span>],
            ["Quality", d.our_quality || "—"],
            ["Score", d.our_score ?? "—"],
            ["Basis", d.basis || "—"],
            ["Router", d.router_plan || "—"],
          ]} />
        </Card>
      </div>
      {d.search_found_better ? (
        <Section title="Search found better">
          <Card className="text-sm">
            {d.search_found_better.release} · {d.search_found_better.quality} · score {d.search_found_better.score} · <Mono>{d.search_found_better.indexer}</Mono>
          </Card>
        </Section>
      ) : null}
      {d.grab ? <Section title="History and search context">
        <Card className="space-y-2 text-sm">
          <p>{d.grab.instance} history #{d.grab.history_id}</p>
          <p>Grabbed: <time dateTime={d.grab.grabbed_at}>{fmtTime(d.grab.grabbed_at)}</time></p>
          <p>{d.grab.release_source} · {d.grab.arr_indexer}</p>
          <p>{d.grab.arr_quality} · score {d.grab.arr_score ?? "—"}</p>
          <p>File then: {d.grab.existing_source || "—"}</p>
          {d.grab.searched ? <>
            <p>Searched: <time dateTime={d.grab.searched_at}>{fmtTime(d.grab.searched_at)}</time></p>
            <p>{d.grab.found_arr ? "The arr's release was among the results." : "The arr's release was not among the results."}</p>
          </> : <p>Not searched.</p>}
        </Card>
      </Section> : null}
      {d.rss_pick ? <Section title="RSS context"><Card className="text-sm">
        {d.rss_pick.indexer} · first seen <time dateTime={d.rss_pick.seen_at}>{fmtTime(d.rss_pick.seen_at)}</time> · {d.rss_pick.status}
      </Card></Section> : null}
      {d.grab?.arr_decision ? <Section title="Our decision of the arr release"><Card><CandidateDetail candidate={d.grab.arr_decision} /></Card></Section> : null}
      {d.grab?.pick ? <Section title="Our search pick"><Card><CandidateDetail candidate={d.grab.pick} /></Card></Section> : null}
      {d.rss_pick?.decision ? <Section title="RSS pick"><Card><CandidateDetail candidate={d.rss_pick.decision} /></Card></Section> : null}
      {d.grab?.router_plan ? <RouterPlan title="Search router plan" plan={d.grab.router_plan} /> : null}
      {d.rss_pick?.router_plan ? <RouterPlan title="RSS router plan" plan={d.rss_pick.router_plan} /> : null}
      {d.grab ? (
        <Section title="Search results" aside={resultsSummary(d.grab.result_count, results.length, d.grab.accepted_count, d.grab.search_note)}>
          {/* Readable columns at phone width: the table scrolls inside its card instead of wrapping per character. */}
          {results.length === 0 ? <Card><Empty>No results kept.</Empty></Card> : (
            <Card className="min-w-0 max-w-full p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Release</TableHead>
                    <TableHead>Quality</TableHead>
                    <TableHead className="min-w-[5rem] whitespace-nowrap text-right">Score</TableHead>
                    <TableHead>Evidence</TableHead>
                    <TableHead>Custom formats</TableHead>
                    <TableHead>Decision</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {results.map((r, i) => (
                    <TableRow key={i}>
                      <TableCell className="min-w-[16rem] max-w-[34rem] text-[13px] [overflow-wrap:anywhere]">
                        {r.release}
                        <div className="text-xs text-muted-foreground"><Mono>{r.indexer}</Mono>{r.arr ? " · the arr's" : ""}</div>
                      </TableCell>
                      <TableCell className="min-w-[7rem]">{r.quality}</TableCell>
                      <TableCell className="min-w-[5rem] whitespace-nowrap text-right tabular-nums">{r.score}</TableCell>
                      <TableCell className="min-w-[14rem]"><CandidateEvidence candidate={r} /></TableCell>
                      <TableCell className="min-w-[11rem]"><CandidateFormats candidate={r} /></TableCell>
                      <TableCell className="min-w-[9rem] text-[13px]">
                        {r.accepted ? <Badge tone="positive">accepted</Badge> : <Badge tone="negative">rejected</Badge>}
                        {(r.rejections ?? []).map((x, j) => <div key={j} className="mt-1 text-xs text-muted-foreground">{x.message || x.reason}</div>)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </Card>
          )}
        </Section>
      ) : null}
    </div>
  );
}

type Candidate = NonNullable<NonNullable<ShadowDiff["grab"]>["arr_decision"]>;

function CandidateFormats({ candidate }: { candidate: Candidate }) {
  return <div className="space-y-1 text-xs">{candidate.formats?.length ? candidate.formats.map((f, i) =>
    <div key={i}><span>{f.name}</span> · <span className="tabular-nums">{f.score}</span></div>
  ) : <span className="text-muted-foreground">No custom formats</span>}</div>;
}

function CandidateEvidence({ candidate: c }: { candidate: Candidate }) {
  return <div className="space-y-1 text-xs [overflow-wrap:anywhere]">
    {c.info_hash ? <p>Hash: <Mono>{c.info_hash}</Mono></p> : null}
    {c.size != null ? <p>{fmtBytes(c.size)}</p> : null}
    {c.seeders != null && c.seeders >= 0 ? <p>{c.seeders} seeders</p> : null}
    {c.languages?.length ? <p>{c.languages.join(", ")}</p> : null}
    {c.published ? <p>Published: <time dateTime={c.published}>{fmtTime(c.published)}</time></p> : null}
  </div>;
}

function CandidateDetail({ candidate: c }: { candidate: Candidate }) {
  return <div className="space-y-3 text-sm">
    <p className="break-words">{c.release}</p>
    <p>{c.quality} · score {c.score} {c.indexer ? `· ${c.indexer}` : ""}</p>
    <CandidateEvidence candidate={c} />
    <CandidateFormats candidate={c} />
    {c.existing ? <div className="space-y-1">
      <h4 className="font-medium">Existing file</h4>
      <p>{c.existing.quality} · score {c.existing.score}</p>
      <CandidateFormats candidate={{ formats: c.existing.formats as Candidate["formats"] }} />
      <p>Upgrade: {c.upgrade ? "yes" : "no"}</p>
    </div> : null}
    <Badge tone={c.accepted ? "positive" : "negative"}>{c.accepted ? "accepted" : "rejected"}</Badge>
    {(c.rejections ?? []).map((r, i) => <p key={i}>{r.message || r.reason}</p>)}
    {c.upgrade_reason ? <p>{c.upgrade_reason}</p> : null}
  </div>;
}


// Stored plan fields are present in JSON but not yet described by OpenAPI.
type StoredPlan = { summary?: string; steps?: { release?: string; provider?: string; outcome?: string; code?: string; message?: string }[] };

function RouterPlan({ title, plan }: { title: string; plan: StoredPlan }) {
  return <Section title={title}>
    <Card className="min-w-0">
      <p className="mb-3 text-sm [overflow-wrap:anywhere]">{plan.summary}</p>
      <Table>
        <TableHeader><TableRow><TableHead>Release</TableHead><TableHead>Provider</TableHead><TableHead>Outcome</TableHead><TableHead>Code</TableHead><TableHead>Why</TableHead></TableRow></TableHeader>
        <TableBody>{plan.steps?.length ? plan.steps.map((step, i) => <TableRow key={i}>
          <TableCell>{step.release}</TableCell><TableCell>{step.provider}</TableCell><TableCell>{step.outcome}</TableCell><TableCell>{step.code}</TableCell><TableCell>{step.message}</TableCell>
        </TableRow>) : <TableRow><TableCell colSpan={5}>No accepted release to place.</TableCell></TableRow>}</TableBody>
      </Table>
    </Card>
  </Section>;
}
