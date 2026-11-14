// Admin › Torrents (docs/design/torrents.md): every debrid account's torrents with what uses them, like Debrid Media
// Manager's library view. Provider tabs, group filters, sorting, multi-select and "Delete selected" (a preview, then a
// confirmation bound to the exact set; a full backup is taken first) and the torrent journal.
// Torrent-list backup controls live in components/backups/torrent-backups.tsx on System › Backup. Other libraries'
// folders (torrents.scan_roots, which the usage check reads) are edited here, below the list.
import { TorrentTabs } from "./torrent-tabs";
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useTorrentActions, useTorrentJournal, useTorrentReport } from "@/api/torrents";
import { useCopyTorrent, useTorrentCopies } from "@/api/provider-copies";
import { useStatus } from "@/api/hooks";
import { AddToProviders, OtherProviders } from "@/components/title/provider-copies";
import { otherProvidersLine } from "@/lib/provider-copies";
import { globalDownloadsOff } from "@/lib/version-grab";
import { Empty, ErrorState, Loading, PageHeader, Section, Strip } from "@/components/page";
import { ProviderPills, providerLabel } from "@/components/provider-pills";
import { inventoryLink } from "@/lib/admin-links";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Choice, ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { SectionPanel } from "@/components/settings/section-panel";
import { SETTINGS_PANELS } from "@/lib/settings-map";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtBytes, fmtNumber, fmtTime } from "@/lib/format";
import {
  GROUPS,
  REF_LABEL,
  deleteScope,
  deleteSelection,
  filterTorrents,
  keyOf,
  providerCodes,
  providerProblem,
  refSummary,
  sortTorrents,
  usageDetail,
  usageTone,
  type DeletePlan,
  type GroupID,
  type SortKey,
  type Torrent,
} from "@/lib/torrents";

const SORTS: { id: SortKey; label: string }[] = [
  { id: "size", label: "Size" },
  { id: "age", label: "Age" },
  { id: "name", label: "Name" },
];

export function TorrentsPage() {
  const report = useTorrentReport();
  const [provider, setProvider] = useState("");
  const [group, setGroup] = useState<GroupID>("");
  // ?q= fills the filter, so other pages can link to one torrent (a title's "Torrent" link: its hash's first 12).
  const [params] = useSearchParams();
  const [text, setText] = useState(() => params.get("q") ?? "");
  const [sort, setSort] = useState<SortKey>("size");
  const [selected, setSelected] = useState<Record<string, Torrent>>({});
  const [plan, setPlan] = useState<DeletePlan | null>(null);
  const [overrides, setOverrides] = useState<Record<string, string>>({});
  const [allowRecent, setAllowRecent] = useState(false);
  const [done, setDone] = useState<DeletePlan | null>(null);
  const actions = useTorrentActions();

  const rows = useMemo(
    () => (report.data ? sortTorrents(filterTorrents(report.data, provider, group, text), sort) : []),
    [report.data, provider, group, text, sort],
  );
  const chosen = Object.values(selected);
  const toggle = (t: Torrent) =>
    setSelected((s) => {
      const n = { ...s };
      if (n[keyOf(t)]) delete n[keyOf(t)];
      else n[keyOf(t)] = t;
      return n;
    });
  const allShown = rows.length > 0 && rows.every((t) => selected[keyOf(t)]);
  const toggleAll = () =>
    setSelected((s) => {
      const n = { ...s };
      for (const t of rows) {
        if (allShown) delete n[keyOf(t)];
        else n[keyOf(t)] = t;
      }
      return n;
    });

  const preview = (ov: Record<string, string>, recent: boolean) =>
    actions.deletePreview.mutate(deleteSelection(chosen, ov, recent), { onSuccess: setPlan });
  const openDelete = () => {
    setOverrides({});
    setAllowRecent(false);
    setDone(null);
    actions.del.reset();
    preview({}, false);
  };
  const setOverride = (key: string, token: string | undefined) => {
    const n = { ...overrides };
    if (token) n[key] = token;
    else delete n[key];
    setOverrides(n);
    preview(n, allowRecent);
  };
  const closeDelete = () => {
    setPlan(null);
    if (done) setSelected({});
    setDone(null);
  };
  const confirmDelete = () => {
    if (!plan) return;
    actions.del.mutate(
      { items: deleteSelection(chosen, overrides, allowRecent), token: plan.confirmation_token },
      { onSuccess: (p) => setDone(p) },
    );
  };

  const r = report.data;
  const codes = useMemo(() => (r ? providerCodes(r) : {}), [r]);
  return (
    <div>
      <PageHeader
        crumb={[{ label: "Admin", to: "/admin" }]}
        title="Torrents"
        description="Every debrid account's torrents and what uses them. Accounts are shared: another app on the same account may use a torrent this page calls unused."
        actions={
          <Button size="sm" variant="outline" onClick={() => report.refetch()} disabled={report.isFetching}>
            Refresh
          </Button>
        }
      />
      <TorrentTabs />
      {/* Before the list, which loads later: a link to #scan-roots scrolls here before the list would push it away. */}
      <SectionPanel {...SETTINGS_PANELS.find((p) => p.id === "scan-roots")!} />
      {report.isPending ? (
        <Loading />
      ) : report.error ? (
        <ErrorState error={report.error} />
      ) : r ? (
        <>
          <Strip
            className="mb-3"
            items={r.providers.map((p) => ({
              label: providerLabel(codes[p.provider] ?? p.provider),
              value: p.ok ? `${fmtNumber(p.count)}${p.limit ? ` of ${fmtNumber(p.limit)}` : ""} · ${fmtBytes(p.bytes)} · ${fmtNumber(p.usage.unused)} unused (${fmtBytes(p.usage.unused_bytes)})` : "unreadable",
              tone: !p.ok ? "bad" : p.near_limit ? "warn" : undefined,
              testid: `provider-count-${p.provider}`,
            }))}
          />
          {r.providers.filter((p) => p.ok && p.near_limit).map((p) => (
            <p key={`limit-${p.provider}`} className="mb-2 text-sm text-[var(--warn-ink)]" data-testid="near-limit">
              {providerLabel(codes[p.provider] ?? p.provider)} holds {fmtNumber(p.count)} of its {fmtNumber(p.limit ?? 0)} torrent limit (magnet_limit). Nothing is removed automatically.
            </p>
          ))}
          {r.providers.filter((p) => p.error).map((p) => {
            const problem = providerProblem(p);
            return <div key={p.provider} className="mb-2 text-sm" data-testid={`provider-problem-${p.provider}`}>
              <p className="text-[var(--bad-ink)] [overflow-wrap:anywhere]"><span className="font-medium">{problem.name}:</span> {problem.message}</p>
              <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">Details</summary><p className="font-mono [overflow-wrap:anywhere]">{problem.raw}</p></details>
            </div>;
          })}
          {(r.unknown ?? []).map((u, i) => (
            <p key={i} className="mb-2 text-sm text-[var(--warn-ink)]">Unknown for every unreferenced torrent: {u}</p>
          ))}
          <div className="mb-2 flex flex-wrap gap-1" role="tablist" aria-label="Provider">
            {["", ...r.providers.map((p) => p.provider)].map((p) => (
              <Button key={p || "all"} role="tab" aria-selected={p === provider} size="xs" variant={p === provider ? "secondary" : "ghost"} title={p || undefined} onClick={() => setProvider(p)}>
                {p ? providerLabel(codes[p] ?? p) : "All providers"}
              </Button>
            ))}
          </div>
          <div className="mb-3 flex flex-wrap items-center gap-1">
            {GROUPS.map((g) => (
              <Button key={g.id || "all"} size="xs" variant={g.id === group ? "secondary" : "ghost"} onClick={() => setGroup(g.id)} data-testid={`group-${g.id || "all"}`}>
                {g.label}
              </Button>
            ))}
            <Input className="h-8 w-full sm:ml-auto sm:max-w-xs" placeholder="Name or hash" value={text} onChange={(e) => setText(e.target.value)} aria-label="Filter torrents" />
            <select className="h-8 rounded-md border border-border bg-transparent px-2 text-sm" value={sort} onChange={(e) => setSort(e.target.value as SortKey)} aria-label="Sort">
              {SORTS.map((s) => <option key={s.id} value={s.id}>{s.label}</option>)}
            </select>
          </div>
          <div className="mb-2 flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">{fmtNumber(rows.length)} shown · {fmtNumber(chosen.length)} selected ({fmtBytes(chosen.reduce((n, t) => n + t.bytes, 0))})</span>
            <Button size="sm" variant="destructive" disabled={chosen.length === 0 || actions.deletePreview.isPending} onClick={openDelete} data-testid="delete-selected">
              Delete selected
            </Button>
            {chosen.length > 0 ? <Button size="sm" variant="ghost" onClick={() => setSelected({})}>Clear selection</Button> : null}
          </div>
          {actions.deletePreview.error ? <p className="mb-2 text-sm text-[var(--bad-ink)]">{errText(actions.deletePreview.error)}</p> : null}
          {rows.length === 0 ? (
            <Card><Empty>Nothing matches.</Empty></Card>
          ) : (
            <Card className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-8"><input type="checkbox" aria-label="Select all shown" checked={allShown} onChange={toggleAll} /></TableHead>
                    {provider ? null : <TableHead className="w-px px-1 sm:px-3">Provider</TableHead>}
                    <TableHead>Name</TableHead>
                    <TableHead className="hidden text-right sm:table-cell">Size</TableHead>
                    <TableHead className="hidden sm:table-cell">Added</TableHead>
                    <TableHead className="hidden md:table-cell">Status</TableHead>
                    <TableHead>Usage</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((t) => (
                    <TorrentRow key={keyOf(t)} t={t} at={report.data?.generated_at ?? ""} checked={!!selected[keyOf(t)]} onToggle={() => toggle(t)} code={provider ? undefined : codes[t.provider] ?? t.provider} />
                  ))}
                </TableBody>
              </Table>
            </Card>
          )}
        </>
      ) : null}
      <JournalSection />
      {plan ? (
        <ConfirmDialog
          scope={deleteScope(plan)}
          subject="Delete selected torrents"
          onConfirm={confirmDelete}
          onClose={closeDelete}
          busy={actions.del.isPending || actions.deletePreview.isPending}
          error={actions.del.error ?? actions.deletePreview.error}
          done={done ? `Backup ${done.backup_id}; deleted ${done.deleted}, failed ${done.failed}.` : undefined}
          doneWarning={!!done && done.failed > 0}
          testid="torrent-delete"
        >
          <Choice checked={allowRecent} onChange={(v) => { setAllowRecent(v); preview(overrides, v); }} testid="allow-recent">
            Also delete chosen torrents added in the last 24 hours or still downloading
          </Choice>
          {plan.items.filter((it) => it.action === "refuse").map((it) => (
            <div key={`${it.provider}:${it.id}`} className="rounded-md border border-[var(--line)] p-2 text-[13px]" data-testid="refused-item">
              <div className="[overflow-wrap:anywhere]"><span className="font-medium" title={it.provider}>{providerLabel(codes[it.provider] ?? it.provider)}</span> {it.name || it.id}: {it.refusal}</div>
              {it.needs_override && it.expected_override_token ? (
                <Choice
                  checked={overrides[`${it.provider}:${it.id}`] === it.expected_override_token}
                  onChange={(v) => setOverride(`${it.provider}:${it.id}`, v ? it.expected_override_token : undefined)}
                  testid="override"
                >
                  Delete it anyway although it is {it.usage}: {it.usage === "used" ? refSummary(it.refs) : (it.reasons ?? []).join("; ")}
                  {(it.ref_list ?? []).length > 0 ? (
                    <ul className="mt-1 list-disc pl-5 text-xs text-muted-foreground">{(it.ref_list ?? []).map((x, i) => <li key={i} className="[overflow-wrap:anywhere]">{x.kind}: {x.detail}</li>)}</ul>
                  ) : null}
                </Choice>
              ) : null}
            </div>
          ))}
          {done ? (
            <ul className="list-disc pl-5 text-xs">
              {done.items.filter((it) => it.outcome && it.outcome !== "deleted").map((it) => (
                <li key={`${it.provider}:${it.id}`} className="[overflow-wrap:anywhere]">{it.name}: {it.outcome} {it.error}</li>
              ))}
            </ul>
          ) : null}
        </ConfirmDialog>
      ) : null}
    </div>
  );
}

/** One torrent; code is its account's cache code for the Provider column (All providers only). */
function TorrentRow({ t, at, checked, onToggle, code }: { t: Torrent; at: string; checked: boolean; onToggle: () => void; code?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <TableRow data-testid="torrent-row">
        <TableCell><input type="checkbox" aria-label={`Select ${t.name}`} checked={checked} onChange={onToggle} /></TableCell>
        {code === undefined ? null : (
          <TableCell className="px-1 sm:px-3" title={t.provider} data-testid="torrent-provider"><ProviderPills codes={[code]} /></TableCell>
        )}
        <TableCell className="max-w-[32rem] text-[13px]">
          <div className="[overflow-wrap:anywhere]">{t.name}</div>
          <div className="text-xs text-muted-foreground">
            {/* Below 640 px the size sits here, not in its column, so the table fits a phone. */}
            <span className="sm:hidden">{fmtBytes(t.bytes)} · </span>
            {t.parsed ? `${t.parsed}${t.quality ? ` · ${t.quality}` : ""} · ` : ""}<Link to={inventoryLink(t.hash)} className="font-mono text-[12.5px] text-primary hover:underline" title="See this torrent in Saved inventory" data-testid="torrent-hash">{t.hash.slice(0, 12)}</Link>
            {t.files >= 0 ? ` · ${t.files} file${t.files === 1 ? "" : "s"}` : ""}
            {t.added_by_theatarr ? " · added by Theatarr" : ""}
          </div>
        </TableCell>
        <TableCell className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">{fmtBytes(t.bytes)}</TableCell>
        <TableCell className="hidden whitespace-nowrap sm:table-cell" title={t.added ? fmtTime(t.added) : undefined}>{t.added ? fmtAgo(t.added) : "—"}</TableCell>
        <TableCell className="hidden md:table-cell">{t.status}{t.state !== "downloaded" && t.progress < 100 ? ` ${Math.round(t.progress)}%` : ""}</TableCell>
        <TableCell>
          <button type="button" onClick={() => setOpen((o) => !o)} title={usageDetail(t)} aria-expanded={open} className="text-left">
            <Badge tone={usageTone(t.usage)}>{t.usage}</Badge>
            {t.usage === "used" ? <div className="text-xs text-muted-foreground">{refSummary(t.refs)}</div> : null}
          </button>
          {t.usage === "used" && (t.titles ?? []).length > 0 ? <TitleLinks titles={t.titles ?? []} max={3} className="block text-xs [overflow-wrap:anywhere]" testid="torrent-row-titles" /> : null}
        </TableCell>
      </TableRow>
      {open ? (
        <TableRow>
          <TableCell />
          <TableCell colSpan={code === undefined ? 5 : 6} className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="torrent-refs">
            {t.usage === "used" ? <RefDetail t={t} /> : <span className="whitespace-pre-wrap">{usageDetail(t)}</span>}
            <TorrentCopies t={t} at={at} />
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

/** "Add to other providers" for one torrent, and how its copies ended where the row does not show them. */
function TorrentCopies({ t, at }: { t: Torrent; at: string }) {
  const m = useCopyTorrent(t.hash, at);
  const copies = useTorrentCopies(t.hash, at, t.copies).data?.copies;
  const status = useStatus();
  const paused = globalDownloadsOff((status.data?.versions ?? []).map((v) => v.grab_state));
  return (
    <div className="mt-2 space-y-1" data-testid="torrent-copies">
      {otherProvidersLine(copies) ? <div>Other providers: <OtherProviders copies={copies} /></div> : null}
      <AddToProviders m={m} vars={{ provider: t.provider, id: t.id }} paused={paused} current={copies} />
    </div>
  );
}

/** The expanded references of a used torrent: the count, the titles they belong to (all of them, not only the listed
 *  references) as links to the title page, and the listed references, each with its title. */
function RefDetail({ t }: { t: Torrent }) {
  const titles = t.titles ?? [];
  return (
    <div className="space-y-1">
      <div>{refSummary(t.refs)}</div>
      {titles.length > 0 ? (
        <div data-testid="torrent-titles">
          {titles.length === 1 ? "Title: " : "Titles: "}
          <TitleLinks titles={titles} />
        </div>
      ) : null}
      <ul className="space-y-0.5">
        {(t.ref_list ?? []).map((x, i) => (
          <li key={i}>
            {REF_LABEL[x.kind] ?? x.kind}: {x.detail}
            {x.title_id ? <> · <Link to={`/title/${x.title_id}`} className="text-primary hover:underline">{x.title || `title ${x.title_id}`}</Link></> : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The titles a used torrent belongs to, as links to their title pages; max cuts the list ("and 2 more"). */
function TitleLinks({ titles, max, testid, className }: { titles: readonly { id: number; name: string }[]; max?: number; testid?: string; className?: string }) {
  const shown = max ? titles.slice(0, max) : titles;
  return (
    <span className={className} data-testid={testid}>
      {shown.map((x, i) => (
        <span key={x.id}>{i > 0 ? ", " : ""}<Link to={`/title/${x.id}`} className="text-primary hover:underline">{x.name}</Link></span>
      ))}
      {titles.length > shown.length ? ` and ${titles.length - shown.length} more` : null}
    </span>
  );
}

function JournalSection() {
  const q = useTorrentJournal(50);
  return (
    <Section title="Journal">
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : (q.data?.entries ?? []).length === 0 ? <Card><Empty>Nothing deleted, restored or backed up yet.</Empty></Card> : (
        <Card className="p-0">
          <Table>
            <TableBody>
              {(q.data?.entries ?? []).map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="whitespace-nowrap">{fmtTime(e.at)}</TableCell>
                  <TableCell><Badge tone={e.outcome === "failed" ? "negative" : "neutral"}>{e.action} {e.outcome}</Badge></TableCell>
                  <TableCell className="text-[13px] [overflow-wrap:anywhere]">{e.provider ? <span className="mr-1 font-medium" title={e.provider}>{providerLabel(e.provider)}</span> : null}{e.name || e.hash || e.backup_id}{e.detail ? <div className="text-xs text-muted-foreground">{e.detail}</div> : null}</TableCell>
                  <TableCell className="hidden whitespace-nowrap md:table-cell">{e.by}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </Section>
  );
}
