// Admin › Torrents (docs/design/torrents.md): every debrid account's torrents with what uses them, like Debrid Media
// Manager's library view. Provider tabs, group filters, sorting, multi-select and "Delete selected" (a preview, then a
// confirmation bound to the exact set; a full backup is taken first) and the torrent journal.
// Torrent-list backups are listed on System › Backup (components/backups/torrent-backups.tsx). Other libraries' folders
// (torrents.scan_roots, which the usage check reads) and the backup schedule are settings: Settings › Download clients ›
// Torrents; a line beside the filters says what "Not used" checks and links there. The provider, filter, text and sort
// live in the URL (?provider=, ?group=, ?q=, ?sort=), so Back and a reload keep them.
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useTorrentActions, useTorrentJournal, useTorrentReport } from "@/api/torrents";
import { Empty, ErrorState, Loading, PageHeader, Section } from "@/components/page";
import { ProviderPills, ProviderTag, providerLabel } from "@/components/provider-pills";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Choice, ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { useSettingsSections } from "@/api/settings";
import { settingHref } from "@/lib/settings-map";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtBytes, fmtNumber, fmtTime, plural } from "@/lib/format";
import { TitleLinks, UsageBadge, UsageDetail } from "@/components/torrent-usage";
import { AddToProviders } from "@/components/title/add-to-providers";
import {
  BY_HASH,
  GROUPS,
  deleteScope,
  deleteSelection,
  filterTorrents,
  groupCounts,
  hashesTogether,
  keyOf,
  providerCodes,
  providerProblem,
  refSummary,
  servedByLine,
  sortTorrents,
  usageDetail,
  type DeletePlan,
  type GroupID,
  type SortKey,
  type Torrent,
  type TorrentReport,
} from "@/lib/torrents";
import { torrentJournalBy, torrentJournalWords, torrentStateWords } from "@/lib/torrent-words";

const SORTS: { id: SortKey; label: string }[] = [
  { id: "size", label: "Size" },
  { id: "age", label: "Age" },
  { id: "name", label: "Name" },
];

export function TorrentsPage() {
  const report = useTorrentReport();
  // The tab, filter, text and sort are in the URL; ?q= also lets other pages link to one torrent (a title's
  // "Torrent" link: its hash's first 12).
  const [params, setParams] = useSearchParams();
  const setParam = (key: string, value: string) => setParams((p) => {
    const n = new URLSearchParams(p);
    if (value) n.set(key, value);
    else n.delete(key);
    return n;
  }, { replace: true });
  const provider = params.get("provider") ?? "";
  const setProvider = (v: string) => setParam("provider", v);
  const asked = params.get("group") ?? "";
  const group: GroupID = GROUPS.some((g) => g.id === asked) ? (asked as GroupID) : "";
  const setGroup = (v: GroupID) => setParam("group", v);
  const text = params.get("q") ?? "";
  const setText = (v: string) => setParam("q", v);
  const sortAsked = params.get("sort") ?? "";
  const sort: SortKey = SORTS.some((x) => x.id === sortAsked) ? (sortAsked as SortKey) : "size";
  const setSort = (v: SortKey) => setParam("sort", v === "size" ? "" : v);
  const [selected, setSelected] = useState<Record<string, Torrent>>({});
  const [plan, setPlan] = useState<DeletePlan | null>(null);
  const [overrides, setOverrides] = useState<Record<string, string>>({});
  const [allowRecent, setAllowRecent] = useState(false);
  const [done, setDone] = useState<DeletePlan | null>(null);
  const actions = useTorrentActions();

  const rows = useMemo(
    () => {
      if (!report.data) return [];
      const sorted = sortTorrents(filterTorrents(report.data, provider, group, text), sort);
      // A hash's copies sit together in the duplicate groups, so a pair is read (and ticked) side by side.
      return BY_HASH.includes(group) ? hashesTogether(sorted) : sorted;
    },
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
  const counts = useMemo(() => (r ? groupCounts(r, provider) : null), [r, provider]);
  return (
    <div>
      <PageHeader
        crumb={[{ label: "Admin", to: "/admin" }]}
        title="Torrents"
        description="Every debrid account's torrents and what uses them, as Theatarr last read them. Refresh reads the accounts again."
        actions={
          <Button size="sm" variant="outline" onClick={() => actions.refreshRecord.mutate()} disabled={actions.refreshRecord.isPending || report.isFetching} aria-busy={actions.refreshRecord.isPending} data-testid="refresh-record">
            {actions.refreshRecord.isPending ? "Reading the accounts…" : "Refresh"}
          </Button>
        }
      />
      {report.isPending ? (
        <Loading />
      ) : report.error ? (
        <ErrorState error={report.error} />
      ) : r ? (
        <>
          <RecordSummary r={r} codes={codes} />
          {actions.refreshRecord.error ? <p className="mb-2 text-sm text-[var(--bad-ink)]" data-testid="refresh-result">Refresh failed: {errText(actions.refreshRecord.error)}. The page shows the record as it was.</p> : null}
          {actions.refreshRecord.data ? <RefreshResult r={actions.refreshRecord.data} codes={codes} /> : null}
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
          <div className="mb-1 flex flex-wrap items-center gap-1">
            {GROUPS.map((g) => (
              <Button key={g.id || "all"} size="xs" variant={g.id === group ? "secondary" : "ghost"} onClick={() => setGroup(g.id)} data-testid={`group-${g.id || "all"}`}>
                {g.label}{counts ? <> <span className="tabular-nums text-muted-foreground" data-testid={`group-count-${g.id || "all"}`}>{fmtNumber(counts[g.id])}</span></> : null}
              </Button>
            ))}
            <Input className="h-8 w-full sm:ml-auto sm:max-w-xs" placeholder="Name or hash" value={text} onChange={(e) => setText(e.target.value)} aria-label="Filter torrents" />
            <select className="h-8 rounded-md border border-border bg-transparent px-2 text-sm" value={sort} onChange={(e) => setSort(e.target.value as SortKey)} aria-label="Sort">
              {SORTS.map((s) => <option key={s.id} value={s.id}>{s.label}</option>)}
            </select>
          </div>
          <UnusedSummary />
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
                    <TorrentRow key={keyOf(t)} t={t} checked={!!selected[keyOf(t)]} onToggle={() => toggle(t)} code={provider ? undefined : codes[t.provider] ?? t.provider} />
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
              <div className="[overflow-wrap:anywhere]"><span className="font-medium" title={it.provider}>{providerLabel(codes[it.provider] ?? it.provider)}</span> {it.name || selected[keyOf(it)]?.name || it.id}: {it.refusal}</div>
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

/** What "Not used by Theatarr" checks, beside the filters: Theatarr's libraries and the other libraries' folders
 *  listed in Settings, linked there. Other apps that share an account are not seen. */
function UnusedSummary() {
  const sections = useSettingsSections();
  const roots = sections.data?.sections.find((x) => x.key === "torrents")?.config?.scan_roots;
  const n = Array.isArray(roots) ? roots.length : 0;
  return (
    <p className="mb-3 text-xs text-muted-foreground" data-testid="unused-summary">
      Not used by Theatarr: no Theatarr library uses it, nor {sections.data && n ? `any of the ${plural(n, "folder")} of other libraries listed in settings` : sections.data ? "a folder of another library (none are listed)" : "the folders of other libraries listed in settings"}. Other apps on the same accounts are not seen.{" "}
      <Link to={settingHref("torrents", "scan_roots") ?? "/admin/settings"} className="text-primary hover:underline" data-testid="scan-roots-link">Other libraries’ folders</Link>
    </p>
  );
}

/** One torrent; code is its account's cache code for the Provider column (All providers only). */
function TorrentRow({ t, checked, onToggle, code }: { t: Torrent; checked: boolean; onToggle: () => void; code?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <TableRow data-testid="torrent-row">
        <TableCell><input type="checkbox" aria-label={`Select ${t.name}`} checked={checked} onChange={onToggle} /></TableCell>
        {code === undefined ? null : (
          <TableCell className="px-1 sm:px-3" title={t.provider} data-testid="torrent-provider"><ProviderPills codes={[code]} /></TableCell>
        )}
        <TableCell className="max-w-[32rem] text-[13px]">
          <div className="line-clamp-2 [overflow-wrap:anywhere]" title={t.raw_name ? `The provider's name: ${t.raw_name}` : t.name}>{t.name}</div>
          {/* Two lines at most: a parsed list of 291 episodes would make the row a page tall; the whole is the tooltip. */}
          <div className="line-clamp-2 text-xs text-muted-foreground [overflow-wrap:anywhere]" title={t.parsed || undefined} data-testid="torrent-second-line">
            {/* Below 640 px the size sits here, not in its column, so the table fits a phone. */}
            <span className="sm:hidden">{fmtBytes(t.bytes)} · </span>
            {t.parsed ? `${t.parsed}${t.quality ? ` · ${t.quality}` : ""} · ` : ""}<span className="font-mono text-[12.5px]" title={t.hash} data-testid="torrent-hash">{t.hash.slice(0, 12)}</span>
            {t.files >= 0 ? ` · ${t.files} file${t.files === 1 ? "" : "s"}` : ""}
            {t.added_by_theatarr ? " · added by Theatarr" : ""}
          </div>
        </TableCell>
        <TableCell className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">{fmtBytes(t.bytes)}</TableCell>
        <TableCell className="hidden whitespace-nowrap sm:table-cell" title={t.added ? fmtTime(t.added) : undefined}>{t.added ? fmtAgo(t.added) : "—"}</TableCell>
        <TableCell className="hidden md:table-cell">
          <div title={t.status ? `The provider says: ${t.status}` : undefined}>{torrentStateWords(t.state, t.progress)}</div>
          {servedByLine(t) ? <div className="max-w-[16rem] text-xs text-muted-foreground" data-testid="torrent-served-by">{servedByLine(t)}</div> : null}
        </TableCell>
        <TableCell>
          <button type="button" onClick={() => setOpen((o) => !o)} title={usageDetail(t)} aria-expanded={open} className="text-left">
            <UsageBadge u={t} />
            {t.usage === "used" ? <div className="text-xs text-muted-foreground">{refSummary(t.refs)}</div> : null}
            {t.usage === "unknown" && (t.reasons ?? []).length > 0 ? <div className="max-w-[16rem] text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="torrent-row-reason">{(t.reasons ?? [])[0]}</div> : null}
          </button>
          {t.usage === "used" && (t.titles ?? []).length > 0 ? <TitleLinks titles={t.titles ?? []} max={3} className="block text-xs [overflow-wrap:anywhere]" testid="torrent-row-titles" /> : null}
        </TableCell>
      </TableRow>
      {open ? (
        <TableRow>
          <TableCell />
          <TableCell colSpan={code === undefined ? 5 : 6} className="text-xs text-muted-foreground [overflow-wrap:anywhere]" data-testid="torrent-refs">
            {/* Below 768 px the Status column, and its served-by line, is hidden: the line opens the details instead. */}
            {servedByLine(t) ? <p className="mb-1 md:hidden">{servedByLine(t)}</p> : null}
            <UsageDetail u={t} />
            {/* The files Theatarr recorded for the hash (imported or probed), folded: a pack can hold hundreds. */}
            {(t.recorded_files ?? []).length > 0 ? (
              <details className="mt-1" data-testid="torrent-files">
                <summary className="cursor-pointer">{plural((t.recorded_files ?? []).length, "file")}</summary>
                <ul className="mt-1 space-y-0.5">{(t.recorded_files ?? []).map((f) => <li key={f.path} className="[overflow-wrap:anywhere]">{f.path} · {fmtBytes(f.size)}</li>)}</ul>
              </details>
            ) : null}
            <div className="mt-2 text-foreground">
              <AddToProviders hash={t.hash} addTo={t.add_to} pack={t.files > 1} testid="torrent-add-to-providers" />
            </div>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

/** Each account's part of the record: its counts with their units and the time it was read ("as of"), "Not read yet"
 *  for an account never read (not an empty one), and a failed read in words with the earlier record still shown. */
function RecordSummary({ r, codes }: { r: TorrentReport; codes: Record<string, string> }) {
  return (
    <Card className="mb-3 divide-y divide-[var(--line)] p-0" data-testid="record">
      {r.providers.map((p) => {
        const label = providerLabel(codes[p.provider] ?? p.provider);
        const problem = p.error ? providerProblem(p, label) : null;
        return (
          <div key={p.provider} className="px-4 py-2 text-[13px]">
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
              <span className="min-w-8 font-medium" title={p.provider}>{label}</span>
              {p.as_of ? (
                <>
                  <span className={p.near_limit ? "tabular-nums text-[var(--warn-ink)]" : "tabular-nums"} data-testid={`provider-count-${p.provider}`}>
                    {fmtNumber(p.account_torrents)}{p.limit ? ` of ${fmtNumber(p.limit)}` : ""} account {p.account_torrents === 1 && !p.limit ? "torrent" : "torrents"} · {plural(p.unique_hashes, "unique hash", "unique hashes")} · {fmtBytes(p.bytes)} · {fmtNumber(p.usage.unused)} unused ({fmtBytes(p.usage.unused_bytes)}){p.usage.unknown > 0 ? ` · ${fmtNumber(p.usage.unknown)} unknown` : ""}
                  </span>
                  <span className="text-muted-foreground" data-testid={`provider-as-of-${p.provider}`}>as of {fmtTime(p.as_of)}</span>
                </>
              ) : (
                <span className="text-[var(--warn-ink)]" data-testid={`provider-count-${p.provider}`}>Not read yet — Refresh reads it</span>
              )}
            </div>
            {p.near_limit ? (
              <p className="mt-0.5 text-[var(--warn-ink)]" data-testid="near-limit">
                {label} holds {fmtNumber(p.account_torrents)} of its {fmtNumber(p.limit ?? 0)} torrent limit (magnet_limit). Nothing is removed automatically.
              </p>
            ) : null}
            {problem ? (
              <div className="mt-0.5" data-testid={`provider-problem-${p.provider}`}>
                <p className="text-[var(--bad-ink)] [overflow-wrap:anywhere]">{problem.message}</p>
                <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">Details</summary><p className="font-mono [overflow-wrap:anywhere]">{problem.raw}</p></details>
              </div>
            ) : null}
          </div>
        );
      })}
      {r.providers.some((p) => p.as_of) ? (
        <p className="px-4 py-2 text-xs text-muted-foreground" data-testid="usage-as-of">
          Each time is when that account's torrent list was read. Used and unused are worked out again after each read or Refresh.
        </p>
      ) : null}
    </Card>
  );
}

/** What the last Refresh read, per account; refresh_error only when no account's own line already says why. */
function RefreshResult({ r, codes }: { r: TorrentReport; codes: Record<string, string> }) {
  const parts = r.providers.map((p) => {
    const label = providerLabel(codes[p.provider] ?? p.provider);
    if (p.error) return `${label} could not be read`;
    return p.as_of ? `${label} ${plural(p.account_torrents, "account torrent")}` : `${label} not read`;
  });
  return (
    <p className="mb-2 text-sm" data-testid="refresh-result">
      Read the accounts: {parts.join(" · ")}.{r.refresh_error && !r.providers.some((p) => p.error) ? ` Not every account was read: ${r.refresh_error}.` : ""}
    </p>
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
                  <TableCell><Badge tone={e.outcome === "failed" ? "negative" : "neutral"} title={`${e.action} ${e.outcome}`}>{torrentJournalWords(e.action, e.outcome)}</Badge></TableCell>
                  <TableCell className="text-[13px] [overflow-wrap:anywhere]">{e.provider ? <ProviderTag account={e.provider} className="mr-1 font-medium" /> : null}{e.name || e.hash || (e.backup_id ? <span title={e.backup_id}>Backup</span> : null)}{e.detail ? <div className="text-xs text-muted-foreground">{e.detail}</div> : null}</TableCell>
                  <TableCell className="hidden whitespace-nowrap md:table-cell">{torrentJournalBy(e.by)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </Section>
  );
}
