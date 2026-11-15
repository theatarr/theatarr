// The title page's details for admins (under About): the release dates, then one folded "Technical details" with the
// identifiers in words (the TMDb collection linked to its page), every file per version and the saved inventory
// entries, each only when it has something; the outside links are About's buttons, not repeated here. The grab
// journal for the title (GET /journal?title=<id>) is TitleJournal, on the History tab.
import { ChevronRight } from "lucide-react";
import { Link } from "react-router";
import { useTitleJournal, type TitleDetail, type Version } from "@/api/library";
import { Empty, ErrorState, KV, Loading, Mono, Section, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { inventoryLink, journalHome, journalLink } from "@/lib/admin-links";
import { fmtBytes, fmtDayYear, fmtNumber, fmtTime } from "@/lib/format";
import { readableCode } from "@/lib/code-words";
import { fileInventoryRows } from "@/lib/file-inventory";
import { releaseTypeWords } from "@/lib/tmdb-words";
import { journalKindWords } from "@/lib/journal-words";

function outside(d: TitleDetail): { label: string; url: string }[] {
  const out: { label: string; url: string }[] = [];
  if (d.tmdb_id) out.push({ label: "TMDb", url: `https://www.themoviedb.org/${d.kind === "series" ? "tv" : "movie"}/${d.tmdb_id}` });
  if (d.imdb_id) out.push({ label: "IMDb", url: `https://www.imdb.com/title/${d.imdb_id}/` });
  if (d.tvdb_id) out.push({ label: "TVDB", url: `https://www.thetvdb.com/dereferrer/series/${d.tvdb_id}` });
  return out;
}

export function outsideLinks(d: TitleDetail) {
  return outside(d);
}

/** The title's grab journal with a link to the full journal (admins). */
export function TitleJournal({ id }: { id: number }) {
  return (
    <Section title="Journal" aside={<Link to={journalHome(`?title=${id}`)} className="hover:text-foreground">Open in the journal ›</Link>}>
      <Journal id={id} />
    </Section>
  );
}

function Journal({ id }: { id: number }) {
  const j = useTitleJournal(String(id), true);
  if (j.isPending) return <Loading />;
  if (j.error) return <ErrorState error={j.error} />;
  const items = j.data.items ?? [];
  if (items.length === 0) return <Card><Empty>No journal entries for this title.</Empty></Card>;
  return (
    <Card className="p-0">
      <Table density="dense">
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>Kind</TableHead>
            <TableHead className="hidden sm:table-cell">Library</TableHead>
            <TableHead>Summary</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((e) => (
            <TableRow key={e.id}>
              <TableCell className="whitespace-nowrap text-muted-foreground">{fmtTime(e.at)}</TableCell>
              <TableCell>
                <Badge tone={e.suspect ? "warning" : "neutral"} title={e.kind}>{journalKindWords(e.kind)}</Badge>
              </TableCell>
              <TableCell className="hidden sm:table-cell">{e.version ? <VersionTag k={e.version} /> : "—"}</TableCell>
              <TableCell>
                <Link to={journalLink(e.id ?? "")} className="hover:text-primary">{e.summary || e.id}</Link>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function Files({ versions }: { versions: Version[] }) {
  const rows = fileInventoryRows(versions);
  return (
    <Card className="p-0">
      <Table density="dense">
        <TableHeader>
          <TableRow>
            <TableHead>Library</TableHead>
            <TableHead>File</TableHead>
            <TableHead className="hidden md:table-cell">Quality</TableHead>
            <TableHead className="hidden text-right sm:table-cell">Size</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.slice(0, 300).map((f) => {
            const facts = f.custom_format_score != null ? `profile score ${fmtNumber(f.custom_format_score)}` : "";
            return (
              <TableRow key={f.key}>
                <TableCell>{f.versions.map((v) => <VersionTag key={v} k={v} />)}</TableCell>
                <TableCell>
                  <Mono className="break-all">{f.path}</Mono>
                  <div className="text-xs text-muted-foreground">
                    {facts}
                    {f.info_hash ? <>{facts ? " · " : ""}<Link className="font-mono text-primary hover:underline" to={inventoryLink(f.info_hash)}>{f.info_hash.slice(0, 12)}</Link></> : null}
                  </div>
                </TableCell>
                <TableCell className="hidden whitespace-nowrap md:table-cell">{f.quality || "—"}</TableCell>
                <TableCell className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">{f.size_bytes != null ? fmtBytes(f.size_bytes) : "—"}</TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      {rows.length > 300 ? <p className="px-4 py-2 text-xs text-muted-foreground">Showing 300 of {rows.length} files.</p> : null}
    </Card>
  );
}

type ReleaseDate = NonNullable<TitleDetail["release_dates"]>[number];

/** Where a stored release date came from: TMDb (its refresh or the art job), else the importing arr instance. */
export function releaseSourceWords(s: string | undefined): string {
  if (!s) return "—";
  return /^tmdb/i.test(s) ? "TMDb" : readableCode(s);
}

/** The labels of the further ids TMDb gives (title_external_id), in words. */
const ID_LABELS: Record<string, string> = { tmdb_collection: "TMDb collection", wikidata: "Wikidata", facebook: "Facebook", instagram: "Instagram", twitter: "X (Twitter)", tvrage: "TVRage", freebase: "Freebase" };

/** An identifier's label: a known source in words, else the source made readable. */
export function idLabel(source: string): string {
  return ID_LABELS[source] ?? readableCode(source);
}

/** The rows behind the release-date choice the calendar, Home and the lists use: a refreshed title's primary release
 *  (one country, named once under the table; a country-less cinema row beside it is the first cinema date anywhere,
 *  marked "first release"), else the arrs' dates. */
function ReleaseDates({ rows }: { rows: ReleaseDate[] }) {
  if (rows.length === 0) return <Card><Empty>No release dates.</Empty></Card>;
  const country = rows.find((r) => r.country)?.country;
  return (
    <Card className="p-0" data-testid="release-dates">
      <Table density="dense">
        <TableHeader>
          <TableRow><TableHead>Date</TableHead><TableHead>Type</TableHead><TableHead className="hidden sm:table-cell">Source</TableHead></TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r, i) => (
            <TableRow key={i}>
              <TableCell className="whitespace-nowrap">{fmtDayYear(r.date)}</TableCell>
              <TableCell title={r.type}>
                {releaseTypeWords(r.type)}
                {country && r.source === "tmdb-art" ? <span className="text-muted-foreground"> · first release, not {country}</span> : null}
              </TableCell>
              <TableCell className="hidden sm:table-cell" title={r.source}>{releaseSourceWords(r.source)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {country ? <p className="border-t border-[var(--line)] px-3 py-2 text-xs text-muted-foreground" data-testid="release-dates-country">{country} dates{rows.some((r) => r.source === "tmdb-art") ? ", except the first release" : ""}</p> : null}
    </Card>
  );
}

export function Details({ d }: { d: TitleDetail }) {
  const versions = (d.versions ?? []) as Version[];
  const m = d.meta;
  const ids: [React.ReactNode, React.ReactNode][] = [
    ["Theatarr id", <Mono key="id">{d.id}</Mono>],
    ["Kind", [d.kind === "series" ? "Series" : "Movie", d.anime ? "anime" : "", d.monitored ? "" : "not monitored"].filter(Boolean).join(" · ")],
  ];
  if (d.tmdb_id) ids.push(["TMDb", <Mono key="t">{d.tmdb_id}</Mono>]);
  if (d.tvdb_id) ids.push(["TVDB", <Mono key="v">{d.tvdb_id}</Mono>]);
  if (d.imdb_id) ids.push(["IMDb", <Mono key="i">{d.imdb_id}</Mono>]);
  for (const e of d.external_ids ?? []) {
    // The collection's own page when it is stored (managers).
    const page = e.source === "tmdb_collection" && d.collection && String(d.collection.tmdb_id) === e.id ? `/collections/${d.collection.tmdb_id}` : undefined;
    ids.push([idLabel(e.source ?? ""), page ? <Link key={`${e.source}${e.id}`} to={page} className="text-primary hover:underline">{d.collection?.name} <Mono>({e.id})</Mono></Link> : <Mono key={`${e.source}${e.id}`}>{e.id}</Mono>]);
  }
  ids.push(["Added to Theatarr", fmtTime(d.created_at)], ["Updated", fmtTime(d.updated_at)]);
  if (m) {
    ids.push(["Metadata", <span key="m">{m.refreshed_at ? `fetched ${fmtTime(m.refreshed_at)}` : "never fetched"}{m.error ? <span className="text-[var(--bad-ink)]"> · {m.error}</span> : null}; next {fmtTime(m.refresh_after)}</span>]);
  }
  const files = fileInventoryRows(versions).length;
  return (
    <div>
      <Section title="Release dates">
        <ReleaseDates rows={d.release_dates ?? []} />
      </Section>
      <details className="group mt-8" data-testid="technical-details">
        <summary className="flex cursor-pointer list-none items-center gap-2 text-[16px] font-semibold text-[var(--ink)] [&::-webkit-details-marker]:hidden">
          <ChevronRight className="size-4 text-[var(--ink-3)] transition-transform group-open:rotate-90" aria-hidden />
          Technical details
        </summary>
        <Section title="Identifiers" className="mt-4">
          <Card><KV items={ids} /></Card>
        </Section>
        {files > 0 ? (
          <Section title={`Files (${fmtNumber(files)})`}>
            <Files versions={versions} />
          </Section>
        ) : null}
      </details>
    </div>
  );
}
