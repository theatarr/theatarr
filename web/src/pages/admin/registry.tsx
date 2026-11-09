// Admin › Advanced › Saved inventory (the registry): contents by hash or name (GET /registry) and one content with everything that points at it
// (GET /registry/{hash}).
import { TorrentTabs } from "./torrent-tabs";
import { Search } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useContent, useRegistry } from "@/api/hooks";
import { TitleThumb } from "@/components/poster-card";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Section, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtBytes, fmtNumber, fmtTime } from "@/lib/format";

const STATES = ["", "present", "absent", "unreferenced", "gone", "unknown"];
const HASH = /^[0-9a-fA-F]{40}$/;

function StateBadge({ s }: { s?: string }) {
  const tone = s === "present" ? "positive" : s === "gone" || s === "absent" ? "negative" : s === "unreferenced" ? "warning" : "neutral";
  return <Badge tone={tone}>{s ?? "—"}</Badge>;
}

export function Registry() {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const q = params.get("q") ?? "";
  const state = params.get("state") ?? "";
  const offset = Number(params.get("offset") ?? 0) || 0;
  const [text, setText] = useState(q);
  const reg = useRegistry(q, state, offset);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const t = text.trim();
    if (HASH.test(t)) return navigate(`/admin/torrents/inventory/${t.toLowerCase()}`);
    const n = new URLSearchParams();
    if (t) n.set("q", t);
    if (state) n.set("state", state);
    setParams(n);
  };
  const setState = (s: string) => {
    const n = new URLSearchParams(params);
    if (s) n.set("state", s);
    else n.delete("state");
    n.delete("offset");
    setParams(n);
  };
  const total = reg.data?.total ?? 0;
  return (
    <div>
      <PageHeader crumb={[{ label: "Admin", to: "/admin" }, { label: "Torrents", to: "/admin/torrents" }]} title="Saved inventory" description="Every torrent Theatarr knows about, by info hash, and which providers hold it." />
      <TorrentTabs />
      <form onSubmit={submit} className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative min-w-0 flex-1 sm:max-w-md">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input className="pl-9" placeholder="Hash, hash prefix or name" value={text} onChange={(e) => setText(e.target.value)} aria-label="Search the registry" />
        </div>
        <Button type="submit" variant="secondary">Search</Button>
      </form>
      <div className="mb-4 flex flex-wrap gap-1">
        {STATES.map((s) => (
          <Button key={s || "any"} size="xs" variant={s === state ? "secondary" : "ghost"} onClick={() => setState(s)}>{s || "any state"}</Button>
        ))}
      </div>
      {reg.isPending ? <Loading /> : reg.error ? <ErrorState error={reg.error} /> : (reg.data?.items ?? []).length === 0 ? <Card><Empty>Nothing matches.</Empty></Card> : (
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Hash</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>State</TableHead>
                <TableHead className="hidden text-right sm:table-cell">Libraries</TableHead>
                <TableHead className="hidden md:table-cell">Last seen</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(reg.data?.items ?? []).map((c) => (
                <TableRow key={c.info_hash}>
                  <TableCell><Link className="hover:text-primary" to={`/admin/torrents/inventory/${c.info_hash}`}><Mono>{c.info_hash?.slice(0, 12)}</Mono></Link></TableCell>
                  <TableCell className="max-w-[30rem] text-[13px] [overflow-wrap:anywhere]">{c.name}</TableCell>
                  <TableCell><StateBadge s={c.state} /></TableCell>
                  <TableCell className="hidden text-right tabular-nums sm:table-cell">{c.versions}</TableCell>
                  <TableCell className="hidden whitespace-nowrap md:table-cell">{fmtAgo(c.last_seen_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      <div className="mt-3 flex items-center gap-2 text-sm text-muted-foreground">
        <span>{fmtNumber(total)} contents{total > 0 ? ` · ${offset + 1}–${Math.min(total, offset + 50)}` : ""}</span>
        <Button size="sm" variant="outline" disabled={offset === 0} onClick={() => { const n = new URLSearchParams(params); n.set("offset", String(Math.max(0, offset - 50))); setParams(n); }}>Previous</Button>
        <Button size="sm" variant="outline" disabled={offset + 50 >= total} onClick={() => { const n = new URLSearchParams(params); n.set("offset", String(offset + 50)); setParams(n); }}>Next</Button>
      </div>
    </div>
  );
}

export function RegistryContent() {
  const { hash = "" } = useParams();
  const q = useContent(hash);
  const crumb = [{ label: "Admin", to: "/admin" }, { label: "Torrents", to: "/admin/torrents" }, { label: "Saved inventory", to: "/admin/torrents/inventory" }];
  if (q.isPending) return <Loading />;
  if (q.error) return (<div><PageHeader crumb={crumb} title={<Mono className="text-xl">{hash.slice(0, 12)}</Mono>} /><ErrorState error={q.error} /></div>);
  const c = q.data;
  return (
    <div>
      <PageHeader crumb={crumb} title={<span className="break-all">{c.name || hash}</span>} description={<span className="flex flex-wrap items-center gap-2"><StateBadge s={c.state} /><Mono className="break-all">{c.info_hash}</Mono></span>} />
      <TorrentTabs />
      <Card>
        <KV items={[
          ["Size", fmtBytes(c.size_bytes)],
          ["Mount folder", <Mono key="m" className="break-all">{c.mount_folder || "—"}</Mono>],
          ["First seen", fmtTime(c.first_seen_at)],
          ["Last seen", fmtTime(c.last_seen_at)],
        ]} />
      </Card>
      <Section title="Libraries">
        {(c.versions ?? []).length === 0 ? <Card><Empty>No library points at this content.</Empty></Card> : (
          <Card className="p-0"><Table><TableHeader><TableRow><TableHead>Title</TableHead><TableHead>Library</TableHead><TableHead className="hidden md:table-cell">Release</TableHead><TableHead className="hidden sm:table-cell">Imported</TableHead></TableRow></TableHeader>
            <TableBody>{(c.versions ?? []).map((v, i) => (
              <TableRow key={i}>
                <TableCell><span className="flex items-center gap-3"><TitleThumb id={v.title_id} /><span><a className="hover:text-primary" href={`/title/${v.title_id}`}>{v.title_name}</a><div className="text-xs text-muted-foreground">{v.source}</div></span></span></TableCell>
                <TableCell><VersionTag k={v.version_key ?? "?"} /></TableCell>
                <TableCell className="hidden max-w-[26rem] break-words text-[13px] md:table-cell">{v.release_title}</TableCell>
                <TableCell className="hidden whitespace-nowrap sm:table-cell">{v.failed_at ? <Badge tone="negative">failed {fmtTime(v.failed_at)}</Badge> : fmtTime(v.imported_at ?? v.grabbed_at)}</TableCell>
              </TableRow>))}
            </TableBody></Table></Card>
        )}
      </Section>
      <Section title="Providers">
        {(c.providers ?? []).length === 0 ? <Card><Empty>No provider lists it.</Empty></Card> : (
          <Card className="p-0"><Table><TableBody>{(c.providers ?? []).map((p, i) => (
            <TableRow key={i}><TableCell><Mono>{p.provider}</Mono></TableCell><TableCell className="break-all text-[13px]">{p.folder_name}</TableCell><TableCell className="hidden sm:table-cell"><Mono>{p.torrent_id}</Mono></TableCell><TableCell className="hidden whitespace-nowrap md:table-cell">{fmtAgo(p.seen_at)}</TableCell></TableRow>
          ))}</TableBody></Table></Card>
        )}
      </Section>
      <Section title="Arr files">
        {(c.arr_files ?? []).length === 0 ? <Card><Empty>No arr file has this hash.</Empty></Card> : (
          <Card className="p-0"><Table><TableBody>{(c.arr_files ?? []).map((f, i) => (
            <TableRow key={i}><TableCell><Mono>{f.instance}</Mono></TableCell><TableCell><VersionTag k={f.version_key ?? "?"} /></TableCell><TableCell className="break-all text-[13px]">{f.path}</TableCell><TableCell className="hidden sm:table-cell">{f.quality}</TableCell></TableRow>
          ))}</TableBody></Table></Card>
        )}
      </Section>
      <Section title="Files">
        {(c.files ?? []).length === 0 ? <Card><Empty>No files recorded.</Empty></Card> : (
          <Card className="p-0"><Table><TableBody>{(c.files ?? []).map((f, i) => (
            <TableRow key={i}><TableCell className="break-all text-[13px]">{f.path}</TableCell><TableCell className="whitespace-nowrap text-right">{fmtBytes(f.size_bytes)}</TableCell></TableRow>
          ))}</TableBody></Table></Card>
        )}
      </Section>
      <Section title="Arr history">
        {(c.history ?? []).length === 0 ? <Card><Empty>No arr history for this hash.</Empty></Card> : (
          <Card className="p-0"><Table><TableBody>{(c.history ?? []).map((h, i) => (
            <TableRow key={i}><TableCell className="whitespace-nowrap">{fmtTime(h.date)}</TableCell><TableCell><Badge>{h.event_type}</Badge></TableCell><TableCell><Mono>{h.instance}</Mono></TableCell><TableCell className="hidden max-w-[28rem] break-words text-[13px] md:table-cell">{h.source_title}</TableCell></TableRow>
          ))}</TableBody></Table></Card>
        )}
      </Section>
    </div>
  );
}
