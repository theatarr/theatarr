// One anime review item as a card: the question it asks in plain words, the AniList entry beside the TVDB episodes
// each choice means, and buttons named after what they do. Ids, score parts and the overrides YAML sit behind
// "Show the raw rule". Actions: POST /anime/review/{id}/accept (no body: the proposed mapping; {candidate}; or a
// hand-set {mapping}) and POST /anime/review/{id}/reject.
import { useState } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { useResolveReview } from "@/api/hooks";
import { Mono } from "@/components/page";
import { TitleThumb } from "@/components/poster-card";
import { toast } from "@/components/toaster";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  KIND_WORDS, entryName, episodes, isHeldRefresh, optionWords, outcomeWords, proposedOption, rangeWords, reviewQuestion, seriesName, sourceWords,
  type ReviewItem, type ReviewOption,
} from "@/lib/anime-review";
import { fmtDay } from "@/lib/library";

type Sample = NonNullable<ReviewOption["samples"]>[number];
type Mapping = { tvdb: number; season: number; first: number; count: number };
type Action = { accept: boolean; candidate?: number; mapping?: Mapping; chose: string };

const STATE_WORDS: Record<string, string> = { accepted: "Accepted", rejected: "Rejected", superseded: "Replaced" };
const ext = "text-xs font-medium text-primary hover:underline";
const pad = (n: number | undefined) => String(n ?? 0).padStart(2, "0");

/** S02E13 “Name” · Apr 8, 2024; a link to the episode when the series is a Theatarr title and the episode is in its list. */
function SampleLine({ e, titleId }: { e: Sample; titleId?: number }) {
  const text = (
    <>
      <Mono>S{pad(e.season)}E{pad(e.number)}</Mono>
      {e.name ? ` “${e.name}”` : ""}
    </>
  );
  return (
    <li>
      {titleId && (e.id ?? 0) > 0 ? <Link className="hover:text-primary" to={`/title/${titleId}?tab=episodes&episode=${e.id}`}>{text}</Link> : text}
      {e.air_date ? <span className="text-muted-foreground"> · {fmtDay(e.air_date)}</span> : null}
    </li>
  );
}

function SeriesLink({ s }: { s: ReviewOption["series"] }) {
  return s?.title_id ? <Link className="font-medium hover:text-primary" to={`/title/${s.title_id}`}>{seriesName(s)}</Link> : <span className="font-medium">{seriesName(s)}</span>;
}

const tvdbUrl = (tvdb: number | undefined) => `https://www.thetvdb.com/dereferrer/series/${tvdb}`;

/** One TVDB mapping: series, range, count, source, samples, the TVDB link and an optional action button. */
function OptionBox({ o, heading, action, pick = "Theatarr's pick" }: { o: ReviewOption; heading?: string; action?: React.ReactNode; pick?: string }) {
  return (
    <div className="rounded-lg border border-border p-3 text-sm">
      {heading ? <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">{heading}</div> : null}
      <div className="flex flex-wrap items-baseline gap-x-2">
        <SeriesLink s={o.series} />
        <span>{rangeWords(o.episodes)}</span>
        {o.proposed && !heading ? <Badge tone="accent">{pick}</Badge> : null}
      </div>
      {o.source ? <div className="text-xs text-muted-foreground">{sourceWords(o.source)}</div> : null}
      {(o.samples ?? []).length > 0 ? <ul className="mt-2 space-y-0.5 text-[13px]">{(o.samples ?? []).map((e, i) => <SampleLine key={i} e={e} titleId={o.series?.title_id} />)}</ul> : null}
      <div className="mt-2 flex flex-wrap items-center gap-3">
        <a className={ext} href={tvdbUrl(o.series?.tvdb)} target="_blank" rel="noreferrer">TVDB</a>
        {o.dropped ? <span className="text-xs text-[var(--bad-ink)]">Can't be used: {o.dropped}</span> : action}
      </div>
    </div>
  );
}

function EntryColumn({ it }: { it: ReviewItem }) {
  const e = it.entry;
  if (!e) return null;
  return (
    <div className="space-y-1 text-sm">
      <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">AniList</div>
      <div className="font-medium">{e.title || e.title_en || e.key}</div>
      {e.title_en && e.title_en !== e.title ? <div className="text-muted-foreground">{e.title_en}</div> : null}
      <div className="text-[13px] text-muted-foreground">
        {[e.format, e.episodes ? episodes(e.episodes) : "", e.start || e.end ? `${e.start ?? "?"} – ${e.end ?? "?"}` : ""].filter(Boolean).join(" · ")}
      </div>
      <div className="flex gap-3">
        {e.anilist_id ? <a className={ext} href={`https://anilist.co/anime/${e.anilist_id}`} target="_blank" rel="noreferrer">AniList</a> : null}
        {e.anidb_id ? <a className={ext} href={`https://anidb.net/anime/${e.anidb_id}`} target="_blank" rel="noreferrer">AniDB</a> : null}
      </div>
    </div>
  );
}

/** "Now" and "After the refresh" for an open held refresh. */
function RefreshBoxes({ it }: { it: ReviewItem }) {
  const r = it.refresh;
  if (!r) return null;
  const box = (heading: string, body: React.ReactNode) => (
    <div className="rounded-lg border border-border p-3 text-sm">
      <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">{heading}</div>
      {body}
    </div>
  );
  if (it.kind === "refresh_shrink") {
    const s = r.series;
    return (
      <>
        {box("Now", <div><SeriesLink s={s} /> {episodes(r.before ?? 0)} mapped</div>)}
        {box("After the refresh", (
          <>
            <div><SeriesLink s={s} /> {episodes(r.after ?? 0)} mapped</div>
            {(r.lost ?? []).length > 0 ? (
              <>
                <div className="mt-2 text-xs text-muted-foreground">No longer mapped</div>
                <ul className="space-y-0.5 text-[13px]">{(r.lost ?? []).map((e, i) => <SampleLine key={i} e={e} titleId={s?.title_id} />)}</ul>
              </>
            ) : null}
            {s?.tvdb ? <a className={`${ext} mt-2 inline-block`} href={tvdbUrl(s.tvdb)} target="_blank" rel="noreferrer">TVDB</a> : null}
          </>
        ))}
      </>
    );
  }
  return (
    <>
      {r.from ? <OptionBox o={r.from} heading="Now" /> : box("Now", "Not mapped: imports take TVDB's numbering.")}
      {r.to ? <OptionBox o={r.to} heading="After the refresh" /> : box("After the refresh", "Not mapped: new files take TVDB's numbering.")}
    </>
  );
}

/** Edit season/offset…: one contiguous run of one season of one of the options' series. */
function EditForm({ it, busy, onUse, onCancel }: { it: ReviewItem; busy: boolean; onUse: (m: Mapping, chose: string) => void; onCancel: () => void }) {
  const opts = it.options ?? [];
  const series = opts.map((o) => o.series).filter((s, i, a) => s?.tvdb && a.findIndex((x) => x?.tvdb === s.tvdb) === i);
  const p = proposedOption(it);
  const first = (p?.episodes?.[0] ?? []) as number[];
  const [tvdb, setTvdb] = useState(p?.series?.tvdb ?? series[0]?.tvdb ?? 0);
  const [season, setSeason] = useState(String(first[0] ?? 1));
  const [start, setStart] = useState(String(first[1] ?? 1));
  const [count, setCount] = useState(String(it.entry?.episodes || p?.count || 1));
  const s = Number(season), f = Number(start), n = Number(count);
  const valid = tvdb > 0 && Number.isInteger(s) && s >= 0 && Number.isInteger(f) && f >= 1 && Number.isInteger(n) && n >= 1;
  const preview = valid ? `${seriesName(series.find((x) => x?.tvdb === tvdb) ?? { tvdb })} ${rangeWords(Array.from({ length: Math.min(n, 500) }, (_, i) => [s, f + i]))}` : "";
  const field = "flex flex-col gap-1 text-xs text-muted-foreground";
  return (
    <div className="mt-3 rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-end gap-3">
        <label className={field}>Series
          <Select className="w-auto" value={tvdb} onChange={(e) => setTvdb(Number(e.target.value))}>
            {series.map((x) => <option key={x?.tvdb} value={x?.tvdb}>{seriesName(x)}</option>)}
          </Select>
        </label>
        <label className={field}>Season<Input className="h-8 w-20" inputMode="numeric" value={season} onChange={(e) => setSeason(e.target.value)} /></label>
        <label className={field}>First episode<Input className="h-8 w-20" inputMode="numeric" value={start} onChange={(e) => setStart(e.target.value)} /></label>
        <label className={field}>Episodes<Input className="h-8 w-20" inputMode="numeric" value={count} onChange={(e) => setCount(e.target.value)} /></label>
      </div>
      {preview ? <p className="mt-2 text-sm">{preview}</p> : null}
      <div className="mt-2 flex gap-2">
        <Button size="sm" disabled={!valid || busy} onClick={() => onUse({ tvdb, season: s, first: f, count: n }, preview)}>Use this mapping</Button>
        <Button size="sm" variant="ghost" onClick={onCancel}>Cancel</Button>
      </div>
    </div>
  );
}

/** Ids, score parts, the summary and the overrides snippet, and the optional note. */
function RawRule({ it, note, setNote }: { it: ReviewItem; note: string; setNote: (s: string) => void }) {
  const cands = it.mapping?.candidates ?? [];
  return (
    <div className="mt-2 space-y-2 text-sm">
      <p className="text-muted-foreground">{it.summary}</p>
      <div className="text-xs text-muted-foreground">Kind <Mono>{it.kind}</Mono> · entry <Mono>{it.entry_key}</Mono> · item #{it.id}</div>
      {cands.length > 0 ? (
        <Table density="dense">
          <TableHeader>
            <TableRow>
              <TableHead>Candidate</TableHead>
              <TableHead className="text-right">Score</TableHead>
              <TableHead className="hidden sm:table-cell">Fit</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {cands.map((c, i) => (
              <TableRow key={i}>
                <TableCell><Mono>{c.source}:{c.series_id}</Mono> {c.range}{c.anchor ? <span className="text-muted-foreground"> · {c.anchor}</span> : null}{c.dropped ? <span className="text-muted-foreground"> · dropped: {c.dropped}</span> : null}</TableCell>
                <TableCell className="text-right tabular-nums">{c.score?.toFixed(2)}</TableCell>
                <TableCell className="hidden text-xs text-muted-foreground sm:table-cell">exists {c.exists} · date {c.date_fit} · cover {c.cover} · title {c.title_sim}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      ) : null}
      {it.snippet ? <pre className="overflow-x-auto rounded-lg bg-[var(--sunken)] p-3 font-mono text-xs">{it.snippet}</pre> : null}
      {!it.snippet && it.detail && Object.keys(it.detail).length > 0 ? (
        <pre className="overflow-x-auto rounded-lg bg-[var(--sunken)] p-3 font-mono text-xs">{JSON.stringify(it.detail, null, 2)}</pre>
      ) : null}
      {it.state === "open" ? <Input className="h-8 max-w-xs" placeholder="Note (optional, sent with your choice)" value={note} onChange={(e) => setNote(e.target.value)} /> : null}
    </div>
  );
}

export function ReviewCard({ it }: { it: ReviewItem }) {
  const resolve = useResolveReview();
  const [note, setNote] = useState("");
  const [err, setErr] = useState("");
  const [raw, setRaw] = useState(false);
  const [editing, setEditing] = useState(false);
  const open = it.state === "open";
  const held = isHeldRefresh(it);
  const mappingKind = it.kind === "conflict" || it.kind === "heuristic";
  const label = it.entry ? entryName(it) : it.franchise_name || it.franchise || "";
  const busy = resolve.isPending;

  const act = ({ chose, ...a }: Action) => {
    setErr("");
    // the promise outlives the card: the refetch after a save can drop it before mutate's callbacks would run
    resolve.mutateAsync({ id: it.id ?? 0, ...a, note: note || undefined }).then(
      () => { toast({ tone: "ok", title: "Saved", body: `${label}: ${chose}` }); setEditing(false); },
      (e) => setErr(e instanceof ApiError ? e.message : String(e)),
    );
  };

  const p = proposedOption(it);
  const head = held ? it.refresh?.series ?? it.refresh?.from?.series ?? it.refresh?.to?.series : p?.series;
  const hasColumns = it.entry || (it.options ?? []).length > 0 || it.refresh;
  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <TitleThumb id={head?.title_id} />
          <div className="min-w-0">
            {head ? <SeriesLink s={head} /> : <span className="font-medium">{label}</span>}
            <div className="text-xs text-muted-foreground">
              Franchise: <Link className="hover:text-primary" to={`/admin/reviews/anime/${it.franchise}`}>{it.franchise_name || it.franchise}</Link>
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone="info">{KIND_WORDS[it.kind ?? ""] ?? it.kind}</Badge>
          {!open ? <Badge tone={it.state === "accepted" ? "positive" : "neutral"}>{STATE_WORDS[it.state ?? ""] ?? it.state}</Badge> : null}
        </div>
      </div>
      <p className="mt-2 text-sm">{reviewQuestion(it)}</p>
      {!open ? <p className="mt-1 text-sm text-muted-foreground">{outcomeWords(it)}</p> : null}
      {hasColumns ? (
        <div className="mt-3 grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
          <EntryColumn it={it} />
          <div className={`space-y-2 ${it.entry ? "" : "md:col-span-2"}`}>
            {!held && (it.options ?? []).length > 0 ? <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{open && it.kind === "conflict" ? "TVDB's numbering · in use now" : "TVDB"}</div> : null}
            {held ? <RefreshBoxes it={it} /> : (it.options ?? []).map((o, i) => (
              <OptionBox key={i} o={o} pick={open && it.kind === "heuristic" ? "In use now" : undefined} action={open ? (
                <Button size="xs" variant="outline" disabled={busy}
                  onClick={() => act({ accept: true, candidate: o.proposed ? undefined : o.candidate, chose: optionWords(o) })}>
                  Use this mapping
                </Button>
              ) : null} />
            ))}
          </div>
        </div>
      ) : null}
      {open && editing ? (
        <EditForm it={it} busy={busy} onCancel={() => setEditing(false)} onUse={(mapping, chose) => act({ accept: true, mapping, chose })} />
      ) : null}
      {open ? (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {mappingKind && (it.options ?? []).length > 0 && !editing ? <Button size="sm" variant="outline" disabled={busy} onClick={() => setEditing(true)}>Edit season/offset…</Button> : null}
          {it.kind === "conflict" ? <Button size="sm" variant="outline" disabled={busy} onClick={() => act({ accept: false, chose: "Keep TVDB's numbering" })}>Keep TVDB's numbering</Button> : null}
          {it.kind === "heuristic" ? <Button size="sm" variant="outline" disabled={busy} onClick={() => act({ accept: false, chose: "Don't use this match" })}>Don't use this match</Button> : null}
          {held ? (
            <>
              <Button size="sm" disabled={busy} onClick={() => act({ accept: true, chose: "Refresh applied" })}>Apply the refresh</Button>
              <Button size="sm" variant="outline" disabled={busy} onClick={() => act({ accept: false, chose: "Current mapping kept" })}>Keep current mapping</Button>
            </>
          ) : null}
          {err ? <span className="text-sm text-[var(--bad-ink)]">{err}</span> : null}
        </div>
      ) : null}
      {open && it.kind === "heuristic" ? <p className="mt-1 text-xs text-muted-foreground">Don't use this match: new files take TVDB's numbering; files already in the library stay where they are.</p> : null}
      {open && held && (it.refresh?.items ?? 0) > 1 ? <p className="mt-1 text-xs text-muted-foreground">This answers all {it.refresh?.items} changes of this refresh.</p> : null}
      <Button size="xs" variant="ghost" className="mt-2" aria-expanded={raw} onClick={() => setRaw(!raw)}>{raw ? "Hide the raw rule" : "Show the raw rule"}</Button>
      {raw ? <RawRule it={it} note={note} setNote={setNote} /> : null}
    </Card>
  );
}
