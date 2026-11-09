// Admin › Metadata review: the anime review queue, franchises and Plex playlists (GET /anime/*), and a franchise
// with its orders and mappings. Accept and reject go through POST /anime/review/{id}/accept|reject.
import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { ApiError } from "@/api/client";
import { useFranchise, useFranchises, usePlaylists, useResolveReview, useReview, type Review } from "@/api/hooks";
import { Empty, ErrorState, Loading, Mono, PageHeader, Section } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";

type Item = NonNullable<Review["items"]>[number];

const STATES = ["open", "accepted", "rejected", "superseded", "all"];

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
            {STATES.map((s) => (
              <Button key={s} size="xs" variant={s === state ? "secondary" : "ghost"} onClick={() => setParams(s === "open" ? {} : { state: s })}>
                {s}
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
                    <TableCell className="hidden whitespace-nowrap md:table-cell">{fmtAgo(f.built_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </Section>
    </div>
  );
}

function ReviewCard({ it }: { it: Item }) {
  const resolve = useResolveReview();
  const [note, setNote] = useState("");
  const [err, setErr] = useState("");
  const open = it.state === "open";
  const act = (accept: boolean, candidate?: number) => {
    if (!accept && !window.confirm("Reject: this range gets no automatic import.")) return;
    setErr("");
    resolve.mutate({ id: it.id ?? 0, accept, candidate, note: note || undefined }, {
      onError: (e) => setErr(e instanceof ApiError ? e.message : String(e)),
    });
  };
  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="font-medium">
            <Link className="hover:text-primary" to={`/admin/reviews/anime/${it.franchise}`}>{it.franchise}</Link> <Mono className="text-muted-foreground">{it.entry_key}</Mono> {it.title}
          </div>
          <div className="mt-1 text-sm text-muted-foreground">{it.summary}</div>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone="info">{it.kind}</Badge>
          <Badge tone={it.state === "open" ? "warning" : it.state === "accepted" ? "positive" : "neutral"}>{it.state}</Badge>
        </div>
      </div>
      {(it.mapping?.candidates ?? []).length > 0 ? (
        <Table density="dense" wrapperClassName="mt-3">
          <TableHeader>
            <TableRow>
              <TableHead>Candidate</TableHead>
              <TableHead className="text-right">Score</TableHead>
              <TableHead className="hidden sm:table-cell">Fit</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {(it.mapping?.candidates ?? []).map((c, i) => (
              <TableRow key={i}>
                <TableCell><Mono>{c.source}:{c.series_id}</Mono> {c.range}{c.anchor ? <span className="text-muted-foreground"> · {c.anchor}</span> : null}</TableCell>
                <TableCell className="text-right tabular-nums">{c.score?.toFixed(2)}</TableCell>
                <TableCell className="hidden text-xs text-muted-foreground sm:table-cell">exists {c.exists} · date {c.date_fit} · cover {c.cover} · title {c.title_sim}</TableCell>
                <TableCell className="text-right">{open ? <Button size="xs" variant="outline" onClick={() => act(true, i)} disabled={resolve.isPending}>Accept this</Button> : null}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      ) : null}
      {it.snippet ? <pre className="mt-3 overflow-x-auto rounded-lg bg-[var(--sunken)] p-3 font-mono text-xs">{it.snippet}</pre> : null}
      {open ? (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Input className="h-8 max-w-xs" placeholder="Note (optional)" value={note} onChange={(e) => setNote(e.target.value)} />
          <Button size="sm" onClick={() => act(true)} disabled={resolve.isPending}>Accept proposed</Button>
          <Button size="sm" variant="destructive" onClick={() => act(false)} disabled={resolve.isPending}>Reject</Button>
          {err ? <span className="text-sm text-[var(--bad-ink)]">{err}</span> : null}
        </div>
      ) : it.note ? <p className="mt-2 text-sm text-muted-foreground">Note: {it.note}</p> : null}
    </Card>
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
      <PageHeader crumb={crumb} title={f.name} description={<span><Mono>{f.slug}</Mono> · {f.source} · built {fmtTime(f.built_at)} · {fmtNumber(f.entries)} entries</span>} />
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
              <TableCell><Badge tone={n.importable ? "positive" : "warning"}>{n.status}</Badge>{!n.importable ? <span className="ml-2">No import</span> : null}</TableCell><TableCell>{n.source}</TableCell>
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
