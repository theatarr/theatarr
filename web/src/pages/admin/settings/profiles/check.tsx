// Settings › Profiles › Check a release (docs/design/decide.md, "Release check"): paste a release name, pick a title and
// one of its libraries, and see how Theatarr would judge it through POST /decide: the parsed quality and its rank in
// the profile, matched formats and the total score, rejections, and whether it would upgrade the library's file.
// Read-only: the decision records nothing, searches nothing and grabs nothing.
import { useMutation } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useDebounced, useSearch, type LibraryHit } from "@/api/add";
import { api, type Schemas } from "@/api/client";
import { KV, Mono, Section } from "@/components/page";
import { SELECT_CLASS } from "@/components/settings/parts";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtBytes } from "@/lib/format";
import { useLibraryName } from "@/lib/library-names";

type Decision = Schemas["Decision"];
type DecideRequest = Schemas["DecideRequest"];

const GIB = 1024 ** 3;
// The settings forms' size units (components/settings/field-input.tsx): GB is 10^9 bytes, GiB 2^30.
const UNITS: Record<string, number> = { kb: 1e3, kib: 1024, mb: 1e6, gb: 1e9, tb: 1e12, mib: 1024 ** 2, gib: GIB, tib: 1024 ** 4 };

/** The typed size in bytes: a number in GiB (sizes are shown in GiB), or with a unit such as "36 GB" or "4.2 GiB";
 * undefined when empty (unknown size), false when it is not a size. */
export function parseCheckSize(text: string): number | undefined | false {
  const t = text.trim().toLowerCase().replace(",", ".");
  if (!t) return undefined;
  const m = /^(\d+(?:\.\d*)?|\.\d+)\s*([kmgt]i?b)?$/.exec(t);
  const unit = m?.[2] ? UNITS[m[2]] : GIB;
  if (!m || !unit || !(Number(m[1]) > 0)) return false;
  return Math.round(Number(m[1]) * unit);
}

function Formats({ formats, score }: { formats?: { name?: string; score?: number }[]; score?: number }) {
  return <Table>
    <TableHeader><TableRow><TableHead>Format</TableHead><TableHead className="text-right">Score</TableHead></TableRow></TableHeader>
    <TableBody>
      {(formats ?? []).map((f) => <TableRow key={f.name}><TableCell>{f.name}</TableCell><TableCell className="text-right tabular-nums">{f.score ?? 0}</TableCell></TableRow>)}
      {(formats ?? []).length === 0 ? <TableRow><TableCell colSpan={2} className="text-muted-foreground">No format matched.</TableCell></TableRow> : null}
      <TableRow data-testid="total-score"><TableCell className="font-medium">Total</TableCell><TableCell className="text-right font-medium tabular-nums">{score ?? 0}</TableCell></TableRow>
    </TableBody>
  </Table>;
}

const rankText = (rank: number | undefined, of: number | undefined) =>
  rank ? `rank ${rank} of ${of ?? rank} allowed` : "not allowed by the profile";

// languagesText: a name with no language gets the title's original language, as in the arrs; say so.
function languagesText(d: Decision) {
  const langs = d.languages ?? [];
  if (langs.every((l) => l === "Unknown")) return "unknown";
  return d.languages_assumed ? `${langs.join(", ")} (assumed: the title's original language; the name states none)` : langs.join(", ");
}

function Result({ d, sent }: { d: Decision; sent: DecideRequest }) {
  const rev = d.revision;
  const quality = `${d.quality ?? "Unknown"} v${rev?.version ?? 1}${rev?.real ? ` real ${rev.real}` : ""}`;
  const parsed = d.series_title ? `${d.series_title}${d.episodes?.length ? ` · ${d.episodes.join(", ")}` : ""}`
    : (d.movie_titles ?? []).join(" / ") + (d.year ? ` (${d.year})` : "");
  return <div data-testid="release-check-result">
    <Card className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {d.accepted ? <Badge tone="positive">Would be accepted</Badge> : <Badge tone="negative">Rejected</Badge>}
        <span className="text-[13px] text-muted-foreground">Library <Mono>{d.version}</Mono>, profile <Mono>{d.profile}</Mono>. Nothing was recorded, searched or grabbed.</span>
      </div>
      <KV items={[
        ["Parsed", d.parsed ? <span key="p">{parsed || "—"}{d.release_group ? <> · group <Mono>{d.release_group}</Mono></> : null}{d.edition ? ` · ${d.edition}` : null}</span> : "Could not parse the name"],
        ["Quality", <span key="q" data-testid="quality">{quality} · {rankText(d.profile_rank, d.profile_ranks)}</span>],
        ["Languages", languagesText(d)],
        ["Size", sent.size ? fmtBytes(sent.size) : "unknown"],
        ["Indexer", sent.indexer || "unknown"],
        ["Seeders", sent.seeders ?? "unknown"],
        ["Info hash", sent.info_hash ? <Mono key="h">{sent.info_hash}</Mono> : "unknown"],
      ]} />
      {d.prefer_seadex
        ? <p className="text-[13px] text-muted-foreground" data-testid="comparison-note">This library prefers SeaDex releases, so SeaDex standing comes first: the configured winner, then SeaDex's best, then its other listed releases, then every other release. Within the same standing Theatarr compares as the arrs do: the quality's rank (qualities in one group tie), then the revision (proper, repack), and only then the format score. Inputs shown as unknown were not given, so checks that need them could not judge them.</p>
        : <p className="text-[13px] text-muted-foreground" data-testid="comparison-note">Theatarr compares releases as the arrs do: the quality's rank first (qualities in one group tie), then the revision (proper, repack), and only then the format score. A higher score never beats a better quality. Inputs shown as unknown were not given, so checks that need them could not judge them.</p>}
      {d.prefer_seadex && !sent.info_hash ? <p className="text-[13px] text-[var(--warn-ink)]" data-testid="seadex-unknown">No info hash was given, so SeaDex standing is unknown: if this is a SeaDex-curated release the verdict may differ. Add its info hash to judge it as Theatarr would.</p> : null}
    </Card>
    <Section title="Rejections">
      {(d.rejections ?? []).length === 0 ? <Card><p className="text-sm text-muted-foreground">None.</p></Card> : (
        <Card className="p-0"><ul className="divide-y" aria-label="Rejections">{(d.rejections ?? []).map((r, i) => <li key={i} className="flex flex-wrap items-baseline gap-2 px-4 py-2.5 text-sm">
          <Mono>{r.reason}</Mono>
          {r.ours ? <Badge tone="accent" title="A Theatarr rule, not an arr one">ours</Badge> : null}
          {r.temporary ? <Badge tone="warning" title="May pass later without a change">temporary</Badge> : null}
          <span className="min-w-0 [overflow-wrap:anywhere]">{r.message}</span>
        </li>)}</ul></Card>
      )}
    </Section>
    <Section title="Formats"><Card className="p-0"><Formats formats={d.formats} score={d.score} /></Card></Section>
    <Section title="Against the current file">
      {d.existing ? <Card className="space-y-3" data-testid="existing">
        <KV items={[
          ["File quality", `${d.existing.quality} · ${rankText(d.existing.profile_rank, d.profile_ranks)}`],
          ["File score", d.existing.score ?? 0],
          ["Upgrade", <span key="u"><Badge tone={d.upgrade ? "positive" : "neutral"}>{d.upgrade ? "yes" : "no"}</Badge> {d.upgrade_reason}</span>],
        ]} />
        <Formats formats={d.existing.formats} score={d.existing.score} />
      </Card> : !d.parsed || !d.match ? <Card><p className="text-sm text-muted-foreground" data-testid="no-comparison">Comparison not available: {d.parsed ? "the release did not match this title" : "the name could not be parsed"}, so the current file was not compared.</p></Card>
        : <Card><p className="text-sm text-muted-foreground">The library has no file for this yet: an accepted release would fill it.</p></Card>}
    </Section>
  </div>;
}

export function ReleaseCheck() {
  const nameOf = useLibraryName();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 250);
  const search = useSearch(dq, "library", 12);
  const [title, setTitle] = useState<LibraryHit>();
  const [version, setVersion] = useState("");
  const [release, setRelease] = useState("");
  const [size, setSize] = useState("");
  const [hash, setHash] = useState("");
  const sizeBad = parseCheckSize(size) === false;
  const hits = search.data?.library ?? [];
  const options = title && !hits.some((h) => h.id === title.id) ? [title, ...hits] : hits;
  const check = useMutation({
    mutationFn: (body: DecideRequest) => api<Decision>("/decide", { method: "POST", body }).then((d) => ({ d, sent: body })),
  });
  const pick = (id: string) => {
    const t = options.find((h) => String(h.id) === id);
    setTitle(t);
    setVersion(t?.versions?.length === 1 ? t.versions[0].key ?? "" : "");
    check.reset();
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!title?.id || !version || !release.trim()) return;
    const body: DecideRequest = { title_id: title.id, version, release: release.trim() };
    const bytes = parseCheckSize(size);
    if (bytes === false) return;
    if (bytes) body.size = bytes;
    if (hash.trim()) body.info_hash = hash.trim();
    check.mutate(body);
  };
  return <div className="space-y-4">
    <p className="text-sm text-muted-foreground">Paste a release name to see how one library would judge it. This only reads: nothing is recorded, searched or grabbed.</p>
    <Card>
      <form onSubmit={submit} className="grid gap-3 sm:grid-cols-2" aria-label="Check a release">
        <label className="flex min-w-0 flex-col gap-1 text-sm">Find a title
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Name in your library" />
        </label>
        <label className="flex min-w-0 flex-col gap-1 text-sm">Title
          <select className={SELECT_CLASS} value={title?.id ?? ""} onChange={(e) => pick(e.target.value)}>
            <option value="">{dq.trim() ? (search.isFetching ? "Searching…" : `${hits.length} found`) : "Type a name first"}</option>
            {options.map((h) => <option key={h.id} value={h.id}>{h.name}{h.year ? ` (${h.year})` : ""}</option>)}
          </select>
        </label>
        <label className="flex min-w-0 flex-col gap-1 text-sm">Library
          <select className={SELECT_CLASS} value={version} disabled={!title} onChange={(e) => { setVersion(e.target.value); check.reset(); }}>
            <option value="">{title && !title.versions?.length ? "This title is in no library" : "Choose a library"}</option>
            {(title?.versions ?? []).map((v) => <option key={v.key} value={v.key}>{v.label || nameOf(v.key ?? "")}{v.has_file ? "" : " · no file"}</option>)}
          </select>
        </label>
        <div className="flex min-w-0 flex-col gap-1 text-sm"><label className="flex min-w-0 flex-col gap-1">Size in GiB (optional)
          <Input inputMode="decimal" value={size} onChange={(e) => setSize(e.target.value)} placeholder="unknown, or e.g. 36 GB" aria-invalid={sizeBad || undefined} aria-describedby={sizeBad ? "check-size-error" : undefined} />
        </label>{sizeBad ? <p id="check-size-error" role="alert" className="text-xs text-[var(--bad-ink)]">Not a size: type a number of GiB, or one with a unit such as 36 GB.</p> : null}</div>
        <label className="flex min-w-0 flex-col gap-1 text-sm sm:col-span-2">Release name
          <Input value={release} onChange={(e) => setRelease(e.target.value)} placeholder="Movie.Name.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-GROUP" className="font-mono" />
        </label>
        <label className="flex min-w-0 flex-col gap-1 text-sm sm:col-span-2">Info hash (optional)
          <Input value={hash} onChange={(e) => setHash(e.target.value)} placeholder="unknown: SeaDex standing needs it" className="font-mono" />
        </label>
        <div className="sm:col-span-2"><Button type="submit" disabled={!title || !version || !release.trim() || sizeBad || check.isPending}>{check.isPending ? "Checking…" : "Check"}</Button></div>
      </form>
    </Card>
    {check.error ? <p role="alert" className="text-sm text-[var(--bad-ink)]">{check.error instanceof Error ? check.error.message : String(check.error)}</p> : null}
    {check.data ? <Result d={check.data.d} sent={check.data.sent} /> : null}
  </div>;
}
