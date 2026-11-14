// Admin › Metadata review: the anime review queue (one plain-language card per item, components/anime/review-card.tsx), franchises and Plex playlists (GET /anime/*), and a franchise
// with its orders and mappings. Accept and reject go through POST /anime/review/{id}/accept|reject. Rebuild and Build
// queue anime_build jobs (POST /anime/franchises/{slug}/build, POST /anime/build); "Anime series without a franchise"
// is GET /anime/uncovered.
import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { ApiError } from "@/api/client";
import { useAnimeBuild, useFranchise, useFranchises, usePlaylists, useReview, useUncovered, type Uncovered } from "@/api/hooks";
import { ReviewCard } from "@/components/anime/review-card";
import { Empty, ErrorState, Loading, Mono, PageHeader, Section } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { fmtAgo, fmtGoDuration, fmtNumber, fmtTime } from "@/lib/format";

type BuildJob = { id?: number; state?: string; error?: string } | null | undefined;
type UncoveredItem = Uncovered["items"][number];

const OUTCOME: Record<string, { label: string; tone: "neutral" | "warning" | "negative" | "info" | "positive" }> = {
  queued: { label: "queued", tone: "info" },
  built: { label: "built", tone: "positive" },
  covered: { label: "covered by another franchise", tone: "neutral" },
  no_mapping: { label: "not in the mapping lists", tone: "warning" },
  failed: { label: "failed", tone: "negative" },
};

/** The latest build job: queued or running (with a dot), failed with its error; nothing once done. */
function BuildState({ job }: { job: BuildJob }) {
  if (!job?.state || job.state === "done") return null;
  if (job.state === "failed") return <Badge tone="negative" title={job.error}>build failed</Badge>;
  return <Badge tone="info">{job.state === "running" ? "building" : "build queued"}</Badge>;
}

/** Rebuild (a franchise) or Build (an anime series): queues the job; the button waits while one is queued or running. */
function BuildButton({ to, job, label }: { to: { slug: string } | { tvdb_id: number }; job: BuildJob; label: string }) {
  const build = useAnimeBuild();
  const [err, setErr] = useState("");
  const active = job?.state === "queued" || job?.state === "running";
  return (
    <span className="inline-flex flex-wrap items-center justify-end gap-2">
      <Button size="xs" variant="outline" disabled={active || build.isPending}
        onClick={() => { setErr(""); build.mutate(to, { onError: (e) => setErr(e instanceof ApiError ? e.message : String(e)) }); }}>
        {label}
      </Button>
      {err ? <span className="text-xs text-[var(--bad-ink)]">{err}</span> : null}
    </span>
  );
}

/** A Go duration in whole days ("168h0m0s" is "7 d"), else as fmtGoDuration shows it. */
function fmtDays(d: string | undefined) {
  const h = /^(\d+)h0m0s$/.exec(d ?? "");
  return h && Number(h[1]) % 24 === 0 ? `${Number(h[1]) / 24} d` : fmtGoDuration(d);
}

/** "next refresh in 3 d", or why there is none. */
function nextRefresh(f: { built_at?: string | null; next_refresh?: string | null; pending?: boolean }) {
  if (f.pending) return "refresh waits for review";
  if (!f.next_refresh) return "";
  return new Date(f.next_refresh).getTime() <= Date.now() ? "refresh due" : `next refresh ${fmtAgo(f.next_refresh)}`;
}

const STATES: [string, string][] = [["open", "Waiting"], ["accepted", "Accepted"], ["rejected", "Rejected"], ["superseded", "Replaced"], ["all", "All"]];

export function AnimeMappings() {
  const [params, setParams] = useSearchParams();
  const state = params.get("state") ?? "open";
  const review = useReview(state);
  const fr = useFranchises();
  return (
    <div>
      <Section
        title="Review queue"
        className="mt-0"
        aside={
          <span className="flex flex-wrap gap-1">
            {STATES.map(([s, label]) => (
              <Button key={s} size="xs" variant={s === state ? "secondary" : "ghost"} onClick={() => setParams(s === "open" ? {} : { state: s })}>
                {label}
              </Button>
            ))}
          </span>
        }
      >
        {review.isPending ? <Loading /> : review.error ? <ErrorState error={review.error} /> : (review.data?.items ?? []).length === 0 ? (
          <Card><Empty>No {state === "all" ? "" : state} items.</Empty></Card>
        ) : (
          <div className="space-y-3">{(review.data?.items ?? []).map((it) => <ReviewCard key={it.id} it={it} />)}</div>
        )}
      </Section>
      <Section title="Franchises Theatarr built">
        {fr.isPending ? <Loading /> : fr.error ? <ErrorState error={fr.error} /> : (
          <Card className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Franchise</TableHead>
                  <TableHead className="text-right">Entries</TableHead>
                  <TableHead className="hidden sm:table-cell">Statuses</TableHead>
                  <TableHead className="text-right">Open</TableHead>
                  <TableHead className="hidden md:table-cell">Built</TableHead>
                  <TableHead className="text-right">Build</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(fr.data?.items ?? []).map((f) => (
                  <TableRow key={f.slug}>
                    <TableCell>
                      <Link className="font-medium hover:text-primary" to={`/admin/reviews/anime/${f.slug}`}>{f.name}</Link>
                      {f.pending ? <Badge tone="warning" className="ml-2">refresh held</Badge> : null}
                      <div className="text-xs text-muted-foreground"><Mono>{f.slug}</Mono> · {f.source}</div>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{f.entries}</TableCell>
                    <TableCell className="hidden text-[13px] sm:table-cell">{Object.entries(f.statuses ?? {}).map(([k, v]) => `${k} ${v}`).join(" · ")}</TableCell>
                    <TableCell className="text-right tabular-nums">{f.open_review ? <Badge tone="warning">{f.open_review}</Badge> : 0}</TableCell>
                    <TableCell className="hidden whitespace-nowrap md:table-cell">{fmtAgo(f.built_at)}<div className="text-xs text-muted-foreground">{nextRefresh(f)}</div></TableCell>
                    <TableCell className="text-right"><span className="inline-flex flex-wrap items-center justify-end gap-2"><BuildState job={f.job} /><BuildButton to={{ slug: f.slug ?? "" }} job={f.job} label="Rebuild" /></span></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </Section>
      <UncoveredSection />
    </div>
  );
}

/** Anime series no franchise maps into: the last discovery and a Build button each. */
function UncoveredSection() {
  const q = useUncovered();
  const builds = q.data?.builds;
  const items = q.data?.items ?? [];
  return (
    <Section title="Anime series without a franchise">
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : (
        <>
          <p className="mb-3 text-sm text-muted-foreground">
            {!builds?.ready ? `Builds cannot run: ${builds?.off ?? "unknown"}.`
              : builds.auto_build ? `Built automatically, one at a time; franchises are rebuilt every ${fmtDays(builds.refresh_interval)}.`
              : "Automatic builds are off (metadata.anime.auto_build); Build and Rebuild still work."}
            {" "}Building only writes mappings; episodes whose mapping is not settled import through TVDB's numbering, and their conflicts show in the review queue above.
          </p>
          {items.length === 0 ? <Card><Empty>Every anime series has a franchise.</Empty></Card> : (
            <Card className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Series</TableHead>
                    <TableHead>Last try</TableHead>
                    <TableHead className="text-right">Build</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((u) => <UncoveredRow key={u.title_id} u={u} />)}
                </TableBody>
              </Table>
            </Card>
          )}
        </>
      )}
    </Section>
  );
}

function UncoveredRow({ u }: { u: UncoveredItem }) {
  const o = u.outcome ? OUTCOME[u.outcome] : undefined;
  return (
    <TableRow>
      <TableCell><Link className="font-medium hover:text-primary hover:underline" to={`/title/${u.title_id}`}>{u.name}</Link><div className="text-xs text-muted-foreground">TVDB <Mono>{u.tvdb_id}</Mono></div></TableCell>
      <TableCell className="text-[13px]">
        {o ? <Badge tone={o.tone}>{o.label}</Badge> : <span className="text-muted-foreground">not tried yet</span>}
        {u.franchise ? <Link className="ml-2 hover:text-primary" to={`/admin/reviews/anime/${u.franchise}`}><Mono>{u.franchise}</Mono></Link> : null}
        {u.tried_at ? <span className="ml-2 text-xs text-muted-foreground">{fmtAgo(u.tried_at)}</span> : null}
        {u.detail ? <div className="max-w-[28rem] break-words text-xs text-muted-foreground">{u.detail}</div> : null}
      </TableCell>
      <TableCell className="text-right"><span className="inline-flex flex-wrap items-center justify-end gap-2"><BuildState job={u.job} /><BuildButton to={{ tvdb_id: u.tvdb_id }} job={u.job} label="Build" /></span></TableCell>
    </TableRow>
  );
}

export function MetadataFranchise() {
  const { slug = "" } = useParams();
  const q = useFranchise(slug);
  const crumb = [{ label: "Admin", to: "/admin" }, { label: "Reviews", to: "/admin/reviews" }, { label: "Anime mappings", to: "/admin/reviews/anime" }];
  if (q.isPending) return <Loading />;
  if (q.error) return (<div><PageHeader crumb={crumb} title={slug} /><ErrorState error={q.error} /></div>);
  const f = q.data;
  const orders = f.orders ?? [];
  return (
    <div>
      <PageHeader crumb={crumb} title={f.name}
        description={<span><Mono>{f.slug}</Mono> · {f.source} · built {fmtTime(f.built_at)}{nextRefresh(f) ? ` · ${nextRefresh(f)}` : ""} · {fmtNumber(f.entries)} entries</span>}
        actions={<span className="inline-flex items-center gap-2"><BuildState job={f.job} /><BuildButton to={{ slug: f.slug ?? slug }} job={f.job} label="Rebuild" /></span>} />
      {orders.length > 0 ? (
        <Tabs defaultValue={orders[0].kind}>
          <TabsList>
            {orders.map((o) => <TabsTrigger key={o.kind} value={o.kind ?? ""}>{o.kind} order</TabsTrigger>)}
          </TabsList>
          {orders.map((o) => (
            <TabsContent key={o.kind} value={o.kind ?? ""}>
              <Card className="p-0">
                <Table density="dense">
                  <TableHeader>
                    <TableRow>
                      <TableHead>#</TableHead>
                      <TableHead>Entry</TableHead>
                      <TableHead className="hidden sm:table-cell">Format</TableHead>
                      <TableHead className="hidden md:table-cell">Date</TableHead>
                      <TableHead>Mapping</TableHead>
                      <TableHead>Source</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {[...(o.items ?? []), ...(o.alternates ?? [])].map((it) => (
                      <TableRow key={`${it.n}-${it.key}`}>
                        <TableCell className="tabular-nums text-muted-foreground">{it.n}</TableCell>
                        <TableCell>{it.title}{it.part ? <span className="text-xs text-muted-foreground"> ({it.part})</span> : null}<div className="text-xs text-muted-foreground"><Mono>{it.key}</Mono>{it.optional ? " · optional" : ""}{it.role ? ` · ${it.role}` : ""}{it.alternate_of ? ` · alternate of ${it.alternate_of}` : ""}{it.route ? ` · route ${it.route}` : ""}</div>
                          <div className="text-xs text-muted-foreground">{it.tvdb ? `TVDB ${it.tvdb}` : ""}{it.tmdb?.length ? `${it.tvdb ? " · " : ""}TMDb ${it.tmdb.join(", ")}` : ""}</div>
                        </TableCell>
                        <TableCell className="hidden sm:table-cell">{it.format}</TableCell>
                        <TableCell className="hidden whitespace-nowrap md:table-cell">{it.date}</TableCell>
                        <TableCell className="text-[13px]">{it.mapping || <span className="text-muted-foreground">—</span>}{it.override ? <Badge tone="accent" className="ml-1">override</Badge> : null}</TableCell>
                        <TableCell className="text-xs">{it.source}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </Card>
            </TabsContent>
          ))}
        </Tabs>
      ) : null}
      {f.routes?.failed?.length ? <Card>Route keys not found: {f.routes.failed.join(", ")}</Card> : null}
      <Section title="Alternate cuts">
        {(f.alt_cuts ?? []).length === 0 ? <Card><Empty>No alternate cuts.</Empty></Card> : (f.alt_cuts ?? []).map((cut, i) => (
          <Card key={i} className="mb-3">
            <h3 className="font-semibold">{cut.name}</h3>
            <p className="text-sm text-muted-foreground">TVDB {cut.tvdb}{cut.entry ? ` · ${cut.entry}` : ""}</p>
            <Table density="dense">
              <TableHeader><TableRow><TableHead>Cut episode</TableHead><TableHead>TVDB episodes</TableHead></TableRow></TableHeader>
              <TableBody>{Object.entries(cut.episodes ?? {}).sort(([a], [b]) => Number(a) - Number(b)).map(([n, episodes]) => (
                <TableRow key={n}><TableCell>{n}</TableCell><TableCell>{episodes.map(episodeLabel).join(", ")}</TableCell></TableRow>
              ))}</TableBody>
            </Table>
          </Card>
        ))}
      </Section>
      <Section title="Episode numbering">
        <Card className="p-0">
          {(f.numbering ?? []).length === 0 ? <Empty>No mapped episodes.</Empty> : <Table density="dense">
            <TableHeader><TableRow><TableHead>TVDB</TableHead><TableHead>Episode</TableHead><TableHead>Absolute</TableHead><TableHead>Entry</TableHead><TableHead>Entry episode</TableHead><TableHead>Mapping</TableHead><TableHead>Source</TableHead></TableRow></TableHeader>
            <TableBody>{(f.numbering ?? []).map((n, i) => <TableRow key={i}>
              <TableCell>{n.tvdb}</TableCell><TableCell><Mono>{n.episode}</Mono></TableCell><TableCell>{n.absolute ?? "—"}</TableCell>
              <TableCell><span>{n.entry_title}</span><div className="text-xs"><Mono>{n.entry}</Mono></div></TableCell><TableCell>{n.entry_number}</TableCell>
              <TableCell><Badge tone={n.importable ? "positive" : "warning"}>{n.status}</Badge>{!n.importable ? <span className="ml-2">TVDB's numbering</span> : null}</TableCell><TableCell>{n.source}</TableCell>
            </TableRow>)}</TableBody>
          </Table>}
        </Card>
      </Section>
      <Section title="Mappings">
        <Card className="p-0">
          <Table density="dense">
            <TableHeader>
              <TableRow>
                <TableHead>Entry</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="hidden sm:table-cell">TVDB range</TableHead>
                <TableHead className="hidden md:table-cell">Reason</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(f.mappings ?? []).map((m) => (
                <TableRow key={m.entry}>
                  <TableCell>{m.title}<div className="text-xs text-muted-foreground"><Mono>{m.entry}</Mono></div></TableCell>
                  <TableCell><Badge tone={m.importable ? "positive" : "warning"}>{m.status}</Badge></TableCell>
                  <TableCell className="hidden sm:table-cell"><Mono>{m.tvdb ? `${m.tvdb} ${m.range ?? ""}` : "—"}</Mono></TableCell>
                  <TableCell className="hidden max-w-[28rem] break-words text-[13px] text-muted-foreground md:table-cell">{m.reason}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </Section>
    </div>
  );
}

export function PlexPlaylists() {
  const pl = usePlaylists();
  return (
      <Section title="Playlists Theatarr keeps">
        {pl.isPending ? <Loading /> : pl.error ? <ErrorState error={pl.error} /> : (pl.data?.items ?? []).length === 0 ? (
          <Card><Empty>No playlists yet.</Empty></Card>
        ) : (
          <Card className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Playlist</TableHead>
                  <TableHead>Mode</TableHead>
                  <TableHead className="text-right">Items</TableHead>
                  <TableHead className="hidden md:table-cell">Synced</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(pl.data?.items ?? []).map((p, i) => (
                  <TableRow key={i}>
                    <TableCell>{p.name}<div className="text-xs text-muted-foreground"><Mono>{p.franchise}</Mono> · {p.order}{p.route ? ` · ${p.route}` : ""}</div></TableCell>
                    <TableCell><Badge tone={p.mode === "applied" ? "positive" : p.mode === "failed" ? "negative" : "neutral"}>{p.mode}</Badge></TableCell>
                    <TableCell className="text-right tabular-nums">{p.items}</TableCell>
                    <TableCell className="hidden md:table-cell">{fmtTime(p.synced_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </Section>
  );
}

// Anime episode pairs are stored as [season, episode].
function episodeLabel(pair: unknown) {
  if (!Array.isArray(pair) || pair.length !== 2) return "—";
  return `S${String(pair[0]).padStart(2, "0")}E${String(pair[1]).padStart(2, "0")}`;
}
