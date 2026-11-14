// Admin › Torrents › Saved inventory (the registry): contents by hash or name (GET /registry) and one content with
// everything that points at it (GET /registry/{hash}). "Used" is the Torrents page's answer from the record
// (docs/design/torrents.md "Used, one answer"); the state is the inventory's own, said in words that do not claim usage.
import { TorrentTabs } from "./torrent-tabs";
import { Search } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { qk } from "@/api/query-keys";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useContent, useRegistry, useStatus } from "@/api/hooks";
import { useCopyTorrent, useHashCopies } from "@/api/provider-copies";
import { AddToProviders, OtherProviders } from "@/components/title/provider-copies";
import { UsageBadge, UsageDetail } from "@/components/torrent-usage";
import { otherProvidersLine } from "@/lib/provider-copies";
import { globalDownloadsOff } from "@/lib/version-grab";
import { Pager } from "@/components/ui/pager";
import { TitleThumb } from "@/components/poster-card";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Section, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtBytes, fmtDayYear, fmtTime, plural } from "@/lib/format";
import { ProviderTag } from "@/components/provider-pills";
import { TorrentLink } from "@/components/title/info-hash";
import { ApiError } from "@/api/client";
import { torrentsLink } from "@/lib/admin-links";

/** The inventory's own states (content.state, docs/design/providers.md) in words that claim no more than the state
 *  proves: "present" is also set by a copy, so it reads plain "Held"; "unreferenced" means no import, arr file or link
 *  is recorded with it. Whether anything uses the torrent is the Used column. */
const STATE_WORDS: Record<string, string> = {
  present: "Held",
  unreferenced: "Held, no import recorded",
  absent: "On no account",
  gone: "Removed",
  unknown: "Not known yet",
};
const STATES = ["", "present", "unreferenced", "absent", "gone", "unknown"];
const STATE_HELP = "The saved inventory's own state. No import recorded: no Theatarr import, arr file or link is recorded with this torrent. Whether anything uses it is the Used column.";
const HASH = /^[0-9a-fA-F]{40}$/;

function StateBadge({ s }: { s?: string }) {
  const tone = s === "present" ? "positive" : s === "absent" || s === "gone" ? "negative" : "neutral";
  return <Badge tone={tone} title={s}>{STATE_WORDS[s ?? ""] ?? s ?? "—"}</Badge>;
}

/** The Torrents page's usage of a hash with its link count; "—" when no account holds it in the record. */
function UsedCell({ usage, links, reason }: { usage?: string; links?: number; reason?: string }) {
  if (!usage) return <span className="text-muted-foreground" title="No account holds it in the record">—</span>;
  return (
    <span className="flex flex-wrap items-baseline gap-x-1.5" title={reason}>
      <UsageBadge u={{ usage, reasons: reason ? [reason] : undefined }} />
      {links ? <span className="text-xs text-muted-foreground tabular-nums">{plural(links, "link")}</span> : null}
    </span>
  );
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
      <div className="mb-4 flex flex-wrap items-center gap-1" title={STATE_HELP}>
        {STATES.map((s) => (
          <Button key={s || "any"} size="xs" variant={s === state ? "secondary" : "ghost"} onClick={() => setState(s)} data-testid={`state-${s || "any"}`}>{s ? STATE_WORDS[s] : "Any state"}</Button>
        ))}
      </div>
      {reg.isPending ? <Loading /> : reg.error ? <ErrorState error={reg.error} /> : (reg.data?.items ?? []).length === 0 ? <Card><Empty>Nothing matches.</Empty></Card> : (
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Hash</TableHead>
                <TableHead>Name</TableHead>
                <TableHead title="The Torrents page's answer, as of the last read of the accounts">Used</TableHead>
                <TableHead className="hidden sm:table-cell" title={STATE_HELP}>State</TableHead>
                <TableHead className="hidden md:table-cell">Last seen</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(reg.data?.items ?? []).map((c) => (
                <TableRow key={c.info_hash}>
                  <TableCell><Link className="hover:text-primary" to={`/admin/torrents/inventory/${c.info_hash}`}><Mono>{c.info_hash?.slice(0, 12)}</Mono></Link></TableCell>
                  <TableCell className="max-w-[30rem] text-[13px] [overflow-wrap:anywhere]">{c.name}</TableCell>
                  <TableCell data-testid="inventory-used"><UsedCell usage={c.usage} links={c.linked_files} reason={c.usage_reason} /></TableCell>
                  <TableCell className="hidden sm:table-cell"><StateBadge s={c.state} /></TableCell>
                  <TableCell className="hidden whitespace-nowrap md:table-cell">{fmtAgo(c.last_seen_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      {reg.data ? <div className="mt-3 space-y-1">
        <p className="text-center text-sm text-muted-foreground" data-testid="inventory-total">{plural(total, "torrent")} (unique hashes)</p>
        <Pager offset={offset} limit={50} total={total} onPage={(o) => { const n = new URLSearchParams(params); if (o > 0) n.set("offset", String(o)); else n.delete("offset"); setParams(n); }} />
      </div> : null}
    </div>
  );
}

export function RegistryContent() {
  const { hash = "" } = useParams();
  const q = useContent(hash);
  const crumb = [{ label: "Admin", to: "/admin" }, { label: "Torrents", to: "/admin/torrents" }, { label: "Saved inventory", to: "/admin/torrents/inventory" }];
  if (q.isPending) return <Loading />;
  // The Torrents page lists what the providers hold now; the saved inventory catches up at the next sync.
  if (q.error instanceof ApiError && q.error.status === 404) return (
    <div>
      <PageHeader crumb={crumb} title={<Mono className="text-xl">{hash.slice(0, 12)}</Mono>} />
      <Card data-testid="inventory-unknown">
        <p className="text-sm">Not in the saved inventory. It appears after a provider sync if one of your provider accounts holds it.</p>
        <Link className="mt-2 inline-block text-sm text-primary hover:underline" to={torrentsLink(hash)}>See it on Torrents</Link>
      </Card>
    </div>
  );
  if (q.error) return (<div><PageHeader crumb={crumb} title={<Mono className="text-xl">{hash.slice(0, 12)}</Mono>} /><ErrorState error={q.error} /></div>);
  const c = q.data;
  return (
    <div>
      <PageHeader crumb={crumb} title={<span className="break-all">{c.name || hash}</span>} description={<span className="flex flex-wrap items-center gap-2">{c.usage ? <UsageBadge u={c.usage} /> : null}<StateBadge s={c.state} /><Mono className="break-all">{c.info_hash}</Mono><TorrentLink hash={c.info_hash} /></span>} />
      <TorrentTabs />
      <Card>
        <KV items={[
          ["Size", fmtBytes(c.size_bytes)],
          ["Mount folder", <Mono key="m" className="break-all">{c.mount_folder || "—"}</Mono>],
          ["First seen", fmtTime(c.first_seen_at)],
          ["Last seen", fmtTime(c.last_seen_at)],
        ]} />
      </Card>
      <Section title="Used">
        <Card className="text-[13px]" data-testid="inventory-usage">
          {c.usage ? (
            <div className="space-y-1">
              <div className="flex flex-wrap items-baseline gap-2"><UsageBadge u={c.usage} />{c.usage.linked_files > 0 ? <span className="text-muted-foreground">{plural(c.usage.linked_files, "link")} into it</span> : null}</div>
              <div className="text-muted-foreground"><UsageDetail u={c.usage} /></div>
              <p className="text-xs text-muted-foreground">Used and unused are worked out again after each read of the accounts or Refresh (Torrents).</p>
            </div>
          ) : <p className="text-muted-foreground">No account holds it in the record, so its usage is not worked out.</p>}
        </Card>
      </Section>
      {/* Theatarr's own imports recorded with this content; most library links are counted under Used instead. */}
      {(c.versions ?? []).length > 0 ? (
        <Section title="Imports">
          <Card className="p-0"><Table><TableHeader><TableRow><TableHead>Title</TableHead><TableHead>Library</TableHead><TableHead className="hidden md:table-cell">Release</TableHead><TableHead className="hidden sm:table-cell">Imported</TableHead></TableRow></TableHeader>
            <TableBody>{(c.versions ?? []).map((v, i) => (
              <TableRow key={i}>
                <TableCell><span className="flex items-center gap-3"><TitleThumb id={v.title_id} /><span><a className="hover:text-primary" href={`/title/${v.title_id}`}>{v.title_name}</a><div className="text-xs text-muted-foreground">{v.source}</div></span></span></TableCell>
                <TableCell><VersionTag k={v.version_key ?? "?"} /></TableCell>
                <TableCell className="hidden max-w-[26rem] break-words text-[13px] md:table-cell">{v.release_title}</TableCell>
                <TableCell className="hidden whitespace-nowrap sm:table-cell">{v.failed_at ? <Badge tone="negative">failed {fmtTime(v.failed_at)}</Badge> : fmtTime(v.imported_at ?? v.grabbed_at)}</TableCell>
              </TableRow>))}
            </TableBody></Table></Card>
        </Section>
      ) : null}
      <Section title="Providers">
        {(c.providers ?? []).length === 0 ? <Card><Empty>No provider lists it.</Empty></Card> : (
          <Card className="p-0"><Table>
            <TableHeader><TableRow><TableHead>Provider</TableHead><TableHead>Name</TableHead><TableHead className="hidden sm:table-cell">Torrent id</TableHead><TableHead className="hidden md:table-cell">First seen</TableHead></TableRow></TableHeader>
            <TableBody>{(c.providers ?? []).map((p, i) => (
              <TableRow key={i} data-testid="inventory-provider">
                <TableCell className="whitespace-nowrap"><ProviderTag account={p.provider ?? ""} className="font-medium" />{p.state === "absent" ? <Badge tone="negative" className="ml-2" data-testid="provider-gone">gone since {p.absent_at ? fmtDayYear(p.absent_at) : "?"}</Badge> : null}</TableCell>
                <TableCell className="break-all text-[13px]">{p.folder_name}</TableCell>
                <TableCell className="hidden sm:table-cell"><Mono>{p.torrent_id}</Mono></TableCell>
                <TableCell className="hidden whitespace-nowrap md:table-cell" title={p.seen_at ? fmtTime(p.seen_at) : undefined}>{p.state === "absent" ? "—" : fmtAgo(p.seen_at)}</TableCell>
              </TableRow>
            ))}</TableBody></Table></Card>
        )}
        <InventoryCopies hash={c.info_hash ?? hash} providers={c.providers ?? []} />
      </Section>
      {(c.arr_files ?? []).length > 0 ? (
        <Section title="Arr files">
          <Card className="p-0"><Table><TableBody>{(c.arr_files ?? []).map((f, i) => (
            <TableRow key={i}><TableCell><Mono>{f.instance}</Mono></TableCell><TableCell><VersionTag k={f.version_key ?? "?"} /></TableCell><TableCell className="break-all text-[13px]">{f.path}</TableCell><TableCell className="hidden sm:table-cell">{f.quality}</TableCell></TableRow>
          ))}</TableBody></Table></Card>
        </Section>
      ) : null}
      <Section title="Files">
        {(c.files ?? []).length === 0 ? <Card><Empty>No files recorded.</Empty></Card> : (
          <Card className="p-0"><Table><TableBody>{(c.files ?? []).map((f, i) => (
            <TableRow key={i}><TableCell className="break-all text-[13px]">{f.path}</TableCell><TableCell className="whitespace-nowrap text-right">{fmtBytes(f.size_bytes)}</TableCell></TableRow>
          ))}</TableBody></Table></Card>
        )}
      </Section>
    </div>
  );
}

/** "Add to other providers" from a provider that holds the hash now (the Torrents row's action, unchanged); nothing
 *  when no provider holds it. */
function InventoryCopies({ hash, providers }: { hash: string; providers: { provider?: string; torrent_id?: string; state?: string }[] }) {
  const held = providers.find((p) => p.state !== "absent" && p.provider && p.torrent_id);
  return held ? <HeldCopies hash={hash} provider={held.provider ?? ""} id={held.torrent_id ?? ""} /> : null;
}

/** The copies of a held hash, read when the entry opens and polled while one is unfinished, and the action. */
function HeldCopies({ hash, provider, id }: { hash: string; provider: string; id: string }) {
  const m = useCopyTorrent(hash, "inventory");
  const copies = useHashCopies(hash).data?.copies;
  const status = useStatus();
  // A copy that finishes as added is a new holder: read the entry again so Providers lists it.
  const qc = useQueryClient();
  const added = (copies ?? []).filter((c) => c.state === "added").map((c) => c.provider).sort().join(",");
  const seen = useRef<string | null>(null);
  const loaded = copies !== undefined;
  useEffect(() => {
    if (!loaded) return;
    if (seen.current !== null && seen.current !== added) void qc.invalidateQueries({ queryKey: qk.registry.content(hash) });
    seen.current = added;
  }, [loaded, added, hash, qc]);
  const paused = globalDownloadsOff((status.data?.versions ?? []).map((v) => v.grab_state));
  return (
    <div className="mt-2 space-y-1 text-[13px]" data-testid="inventory-copies">
      {otherProvidersLine(copies) ? <div>Other providers: <OtherProviders copies={copies} /></div> : null}
      <AddToProviders m={m} vars={{ provider, id }} paused={paused} current={copies} />
    </div>
  );
}
