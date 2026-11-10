// The title page's technical details, for admins (under About): ids, release dates, every file and arr record per
// version and the registry; the grab journal for the title (GET /journal?title=<id>) is TitleJournal, on the
// History tab.
import { ExternalLink } from "lucide-react";
import { Link } from "react-router";
import { useTitleJournal, type TitleDetail, type Version } from "@/api/library";
import { Empty, ErrorState, KV, Loading, Mono, Section, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { journalHome, journalLink } from "@/lib/admin-links";
import { fmtBytes, fmtTime } from "@/lib/format";
import { fileInventoryRows } from "@/lib/file-inventory";

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
                <Badge tone={e.suspect ? "warning" : "neutral"}>{e.kind}</Badge>
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
  if (rows.length === 0) return <Card><Empty>No files.</Empty></Card>;
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
          {rows.slice(0, 300).map((f) => (
            <TableRow key={f.key}>
              <TableCell>{f.versions.map((v) => <VersionTag key={v} k={v} />)}</TableCell>
              <TableCell>
                <Mono className="break-all">{f.path}</Mono>
                <div className="text-xs text-muted-foreground">
                  {f.instance}
                  {f.current ? " · current" : ""}
                  {f.custom_format_score != null ? ` · profile score ${f.custom_format_score}` : ""}
                  {f.info_hash ? <> · <Link className="font-mono text-primary hover:underline" to={`/admin/registry/${f.info_hash}`}>{f.info_hash.slice(0, 12)}</Link></> : null}
                </div>
              </TableCell>
              <TableCell className="hidden md:table-cell">{f.quality || "—"}</TableCell>
              <TableCell className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">{f.size_bytes != null ? fmtBytes(f.size_bytes) : "—"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {rows.length > 300 ? <p className="px-4 py-2 text-xs text-muted-foreground">Showing 300 of {rows.length} files.</p> : null}
    </Card>
  );
}

export function Details({ d }: { d: TitleDetail }) {
  const versions = (d.versions ?? []) as Version[];
  const m = d.meta;
  const ids: [React.ReactNode, React.ReactNode][] = [
    ["Theatarr id", <Mono key="id">{d.id}</Mono>],
    ["Kind", `${d.kind}${d.anime ? " · anime" : ""}${d.monitored ? "" : " · not monitored"}`],
  ];
  if (d.tmdb_id) ids.push(["TMDb", <Mono key="t">{d.tmdb_id}</Mono>]);
  if (d.tvdb_id) ids.push(["TVDB", <Mono key="v">{d.tvdb_id}</Mono>]);
  if (d.imdb_id) ids.push(["IMDb", <Mono key="i">{d.imdb_id}</Mono>]);
  for (const e of d.external_ids ?? []) ids.push([e.source, <Mono key={`${e.source}${e.id}`}>{e.id}</Mono>]);
  ids.push(["Added to Theatarr", fmtTime(d.created_at)], ["Updated", fmtTime(d.updated_at)]);
  if (m) {
    ids.push(["Metadata", <span key="m">{m.refreshed_at ? `fetched ${fmtTime(m.refreshed_at)}` : "never fetched"}{m.error ? <span className="text-[var(--bad-ink)]"> · {m.error}</span> : null}; next {fmtTime(m.refresh_after)}</span>]);
  }
  const links = outside(d);
  if (links.length) {
    ids.push(["Links", <span key="l" className="flex flex-wrap gap-3">{links.map((l) => <a key={l.label} href={l.url} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 text-primary hover:underline">{l.label}<ExternalLink className="size-3" /></a>)}</span>]);
  }
  const arr = versions.flatMap((v) => (v.arr_records ?? []).map((a) => ({ v, a })));
  return (
    <div>
      <Section title="Identifiers">
        <Card><KV items={ids} /></Card>
      </Section>
      <Section title="Release dates">
        {(d.release_dates ?? []).length === 0 ? <Card><Empty>No release dates.</Empty></Card> : (
          <Card className="p-0">
            <Table density="dense">
              <TableHeader>
                <TableRow><TableHead>Date</TableHead><TableHead>Type</TableHead><TableHead>Country</TableHead><TableHead className="hidden sm:table-cell">Source</TableHead></TableRow>
              </TableHeader>
              <TableBody>
                {(d.release_dates ?? []).map((r, i) => (
                  <TableRow key={i}><TableCell className="whitespace-nowrap">{r.date}</TableCell><TableCell>{r.type}</TableCell><TableCell>{r.country || "—"}</TableCell><TableCell className="hidden sm:table-cell">{r.source}</TableCell></TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </Section>
      <Section title="Files">
        <Files versions={versions} />
      </Section>
      {arr.length > 0 ? (
        <Section title="Arr records">
          <Card className="p-0">
            <Table density="dense">
              <TableHeader>
                <TableRow><TableHead>Library</TableHead><TableHead>Instance</TableHead><TableHead className="hidden sm:table-cell">Profile</TableHead><TableHead className="hidden md:table-cell">Path</TableHead></TableRow>
              </TableHeader>
              <TableBody>
                {arr.map(({ v, a }) => (
                  <TableRow key={`${a.instance}-${a.arr_id}`}>
                    <TableCell><VersionTag k={v.key ?? ""} /></TableCell>
                    <TableCell>{a.instance} <span className="text-muted-foreground">#{a.arr_id}</span>{a.monitored ? null : <span className="text-muted-foreground"> · not monitored</span>}</TableCell>
                    <TableCell className="hidden sm:table-cell">{a.quality_profile || "—"}</TableCell>
                    <TableCell className="hidden md:table-cell"><Mono className="break-all">{a.path}</Mono></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        </Section>
      ) : null}
      <Section title="Registry">
        {(d.registry ?? []).length === 0 ? <Card><Empty>No registry entries.</Empty></Card> : (
          <Card className="p-0">
            <Table density="dense">
              <TableHeader>
                <TableRow><TableHead>Release</TableHead><TableHead className="hidden sm:table-cell">Library</TableHead><TableHead className="hidden md:table-cell">Imported</TableHead><TableHead>State</TableHead></TableRow>
              </TableHeader>
              <TableBody>
                {(d.registry ?? []).map((r) => (
                  <TableRow key={`${r.info_hash}-${r.version_id}`}>
                    <TableCell>
                      <Mono className="break-all">{r.release_title || r.name}</Mono>
                      <div><Link className="font-mono text-xs text-primary hover:underline" to={`/admin/registry/${r.info_hash}`}>{r.info_hash}</Link></div>
                    </TableCell>
                    <TableCell className="hidden sm:table-cell"><VersionTag k={r.version_key ?? ""} /></TableCell>
                    <TableCell className="hidden whitespace-nowrap text-muted-foreground md:table-cell">{fmtTime(r.imported_at ?? r.grabbed_at ?? r.failed_at)}</TableCell>
                    <TableCell><Badge tone={r.failed_at ? "negative" : r.state === "present" ? "positive" : "neutral"}>{r.failed_at ? "failed" : r.state}</Badge></TableCell>
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
