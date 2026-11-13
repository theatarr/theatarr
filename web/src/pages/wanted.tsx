import { Columns3 } from "lucide-react";
import { useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router";
import { useStatus } from "@/api/hooks";
import { usePerms } from "@/api/library";
import { useClearWantedSearch, useWanted, useWantedSearch, type WantedRow, type WantedSearchBody, type WantedSearchResult } from "@/api/wanted";
import { Empty, ErrorState, Loading, PageHeader, VersionTag } from "@/components/page";
import { InteractiveSearchButton } from "@/components/title/interactive-search";
import { WantedTabs } from "@/components/wanted-tabs";
import { journalLink } from "@/lib/admin-links";
import { Card } from "@/components/ui/card";
import { ColumnPicker } from "@/components/ui/column-picker";
import { DataTable, type DataColumn } from "@/components/ui/data-table";
import { Pager } from "@/components/ui/pager";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { fmtAgo, fmtGoDuration, fmtTime } from "@/lib/format";
import { useColumnPrefs, type ColumnDef } from "@/lib/prefs";
import { settingHref } from "@/lib/settings-map";
import { globalDownloadsOff, willGrab, type VersionGrabState } from "@/lib/version-grab";

const waits = [
  ["", "All"], ["due", "Due"], ["scheduled", "Scheduled"], ["in_flight", "Downloading"],
  ["not_scheduled", "Not scheduled"], ["downloads_off", "Paused"],
] as const;

const PAGE = 50;

/** "unknown": Pause is on but /status (the lock state) has not answered, so no cause is named. */
type GlobalOff = ReturnType<typeof globalDownloadsOff> | "unknown";
const OFF_WORD = { locked: "Paused (locked)", off: "Paused", unknown: "Paused" } as const;

export function searchResult(run: WantedRow["last_run"]): string {
  if (!run) return "Never searched";
  if (run.state === "failed") return `Failed: ${run.message}`;
  if (run.state === "queued") return "Queued";
  if (run.state === "searching") return "Searching";
  if (run.downloaded) return "Grabbed";
  if (run.state === "routed") return "Sent to download";
  return run.results ? `${run.results} results, none accepted` : "Nothing found";
}

export function targetWords(targets: string[]) {
  const names = [...new Set(targets)].sort();
  const words: string[] = [];
  for (let i = 0; i < names.length; i++) {
    const start = names[i];
    const match = /^(s\d+)e(\d+)$/.exec(start);
    let end = match ? Number(match[2]) : 0;
    if (match) {
      while (i + 1 < names.length) {
        const next = /^(s\d+)e(\d+)$/.exec(names[i + 1]);
        if (!next || next[1] !== match[1] || Number(next[2]) !== end + 1) break;
        end++; i++;
      }
    }
    words.push(start === "movie" ? "Movie" : start.toUpperCase() + (match && end > Number(match[2]) ? `-E${String(end).padStart(2, "0")}` : ""));
  }
  return words.join(", ");
}

/** Why a scheduled row is early, in words: a retry of a new release (with the try it will be), or a failed download. */
function whyWords(row: WantedRow): string {
  if (row.why === "new") return `New release, try ${(row.tries ?? 0) + 1}`;
  if (row.why && row.why !== "asked") return "After a failed download";
  return "";
}

/** The "Next search" cell. A due row says "Now": the hourly limit decides when it starts, so no time is promised. A
 * row never shows a time while paused. The library's own closed gate (from GET /status, admins) is shown under
 * "Not scheduled" and in place of "Paused". */
function NextSearch({ row, state, off, admin }: { row: WantedRow; state?: VersionGrabState; off: GlobalOff; admin: boolean }) {
  const library = state ? <VersionStatus state={state} canManage={admin} settingsKey={row.version} /> : null;
  switch (row.waiting) {
    case "due": return <>Now{row.why === "asked" ? " · asked by Search now" : ""}</>;
    case "scheduled": {
      const why = whyWords(row);
      return <><span title={fmtTime(row.next_at)}>{fmtAgo(row.next_at)}</span>{why ? <span className="text-[var(--ink-3)]"> · {why}</span> : null}</>;
    }
    case "in_flight": return <>Downloading</>;
    case "not_scheduled": return <>Not scheduled{state && !willGrab(state) ? <div className="mt-1">{library}</div> : null}</>;
    case "downloads_off": return library ?? <>{OFF_WORD[off ?? "unknown"]}</>;
  }
}

/** Why a row cannot be searched now ("" when it can). */
function blocked(row: WantedRow): string {
  switch (row.search_blocked) {
    case "downloads_off": return "Paused";
    case "in_flight": return "A download for it is in progress";
    default: return "";
  }
}

const asked = (row: WantedRow) => row.waiting === "due" && row.why === "asked";
const rowKey = (row: WantedRow) => `${row.title_id}/${row.version}/${row.season ?? ""}`;
const rowItem = (row: WantedRow) => ({ title_id: row.title_id, version: row.version, ...(row.season === undefined ? {} : { season: row.season }) });
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "es"}`;

/** What a Search now request did, in words: "Started 6, queued 306", then what was skipped and why. */
export function searchNowWords(r: WantedSearchResult): string {
  const parts = [`Started ${r.started}, queued ${r.pending}`];
  const why = new Map<string, number>();
  for (const s of r.skipped) why.set(s.reason, (why.get(s.reason) ?? 0) + 1);
  for (const [reason, n] of why) parts.push(`Skipped ${n}: ${reason}`);
  return parts.join(". ") + ".";
}

type Column = "library" | "what" | "next" | "last";
const COLUMNS: ColumnDef<Column>[] = [
  { id: "library", label: "Library", hint: "The library the item is wanted in" },
  { id: "what", label: "What", hint: "The film, or the season's episodes" },
  { id: "next", label: "Next search", hint: "When the item is searched next" },
  { id: "last", label: "Last search", hint: "When it was last searched, and what that found" },
];
const ALL_COLUMNS = COLUMNS.map(c => c.id);

type Ask = { body: WantedSearchBody; count: number; title: string };

export function WantedPage() {
  const only = useLocation().pathname === "/wanted/cutoff" ? "cutoff" : "missing";
  const [sp, setSp] = useSearchParams();
  const version = sp.get("version") || undefined;
  const waiting = sp.get("waiting") || undefined;
  const offset = Math.max(0, Number(sp.get("offset")) || 0);
  const q = useWanted({ only, version, waiting, limit: PAGE, offset });
  const perms = usePerms();
  const status = useStatus(perms.admin); // /status is admin-only: managers word rows from the wanted list alone
  const states = new Map((status.data?.versions ?? []).map((v) => [v.key, v.grab_state]));
  const off: GlobalOff = states.size ? globalDownloadsOff([...states.values()]) : q.data && !q.data.downloads_enabled ? "unknown" : undefined;
  const change = (key: string, value: string) => {
    const next = new URLSearchParams(sp);
    next.delete("offset");
    if (value) next.set(key, value); else next.delete(key);
    setSp(next);
  };
  const page = (to: number) => {
    const next = new URLSearchParams(sp);
    if (to > 0) next.set("offset", String(to)); else next.delete("offset");
    setSp(next);
  };
  const label = (key: string) => q.data?.libraries[key] || key;
  const d = q.data;
  const search = useWantedSearch();
  const clear = useClearWantedSearch();
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [ask, setAsk] = useState<Ask>();
  const [picker, setPicker] = useState(false);
  const [prefs, savePrefs] = useColumnPrefs<Column>("wanted-columns", COLUMNS, ALL_COLUMNS);
  const items = d?.items ?? [];
  // Only rows Search now can search may be chosen (the table disables the others); a row that became blocked after it
  // was ticked is not sent.
  const selectable = items.filter(row => !blocked(row));
  const chosen = selectable.filter(row => picked.has(rowKey(row)));
  const left = d ? Math.max(0, d.per_hour - d.searched_last_hour) : 0;
  const send = (body: WantedSearchBody) => search.mutate(body, { onSuccess: () => { setPicked(new Set()); setAsk(undefined); } });
  // A request larger than what is left of the hour is confirmed first.
  const request = (a: Ask, confirm: boolean) => {
    search.reset();
    if (confirm) setAsk(a); else send(a.body);
  };
  const what = only === "cutoff" ? "cutoff unmet" : "missing";
  const tabQuery = version ? `?version=${encodeURIComponent(version)}` : "";
  const switchOff = d ? (only === "missing" ? !d.missing : d.cutoff_unmet === "off") : false;
  // The regular repeat runs only while a backlog switch is on.
  const backlogOn = d ? d.missing || d.cutoff_unmet !== "off" : false;
  const name = (row: WantedRow) => d?.titles[row.title_id] || `Title ${row.title_id}`;
  const titleLink = (row: WantedRow) => <Link className="font-semibold text-[var(--ink)] hover:text-[var(--accent-ink)] [overflow-wrap:anywhere]" to={`/title/${row.title_id}`}>{name(row)}</Link>;
  const cells: Record<Column, DataColumn<WantedRow>> = {
    library: { id: "library", label: "Library", cell: row => <div className="min-w-0">{label(row.version)}<div className="mt-0.5"><VersionTag k={row.version} /></div></div> },
    what: { id: "what", label: "What", cell: row => targetWords(row.targets) },
    next: { id: "next", label: "Next search", cell: row => <NextSearch row={row} state={states.get(row.version)} off={off} admin={perms.admin} /> },
    last: { id: "last", label: "Last search", cell: row => <>{row.last_run ? <span title={fmtTime(row.last_run.at)}>{fmtAgo(row.last_run.at)} · </span> : null}{searchResult(row.last_run)}</> },
  };
  const columns: DataColumn<WantedRow>[] = [
    { id: "title", label: "Title", cell: titleLink, card: "none", cellClassName: "min-w-[10rem]" },
    ...prefs.order.filter(id => prefs.shown.includes(id)).map(id => cells[id]),
  ];
  const actions = (row: WantedRow) => {
    const why = blocked(row);
    return <div className="flex flex-wrap items-baseline gap-2 md:flex-nowrap md:justify-end">
      <InteractiveSearchButton titleId={row.title_id} version={row.version} season={row.season} size="xs" variant="ghost" />
      {row.decision_id && perms.admin ? <Link className="text-primary hover:underline" to={journalLink(String(row.decision_id))}>Decision</Link> : null}
      <button type="button" className="text-primary hover:underline disabled:cursor-not-allowed disabled:text-[var(--ink-3)] disabled:no-underline" disabled={!!why || asked(row) || search.isPending} title={why || (asked(row) ? "Already asked for" : undefined)} onClick={() => request({ body: { only, items: [rowItem(row)] }, count: 1, title: "Search now" }, false)}>Search now</button>
      {why && row.waiting !== "downloads_off" && row.waiting !== "in_flight" ? <span className="text-[var(--ink-3)]" data-testid="search-blocked">{why}</span> : null}
    </div>;
  };
  return <div>
    <PageHeader title="Wanted" description="What is waiting for a search and when each item is searched next. Search now starts a search under the hourly limit." />
    <WantedTabs active={only} query={tabQuery} admin={perms.admin} />
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground" data-testid="wanted-pace">Up to {d.per_hour} searches an hour ({d.searched_last_hour} in the last hour). {backlogOn ? `Items are searched again every ${fmtGoDuration(d.again_after)}. ` : ""}A missing new release is tried again sooner: the wait starts at 1 hour and doubles each time, up to {fmtGoDuration(d.again_after)}, until a week after its release.</p>
      {switchOff ? <p role="status" data-testid="wanted-switch-off" className="rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)]">Theatarr is not searching for {only === "cutoff" ? "upgrades" : "missing items"} by itself. Items here are searched when you ask, or after a failed download.{perms.admin ? <> <Link className="text-primary hover:underline" to={settingHref("router", only === "cutoff" ? "wanted.cutoff_unmet" : "wanted.missing") ?? "/admin/settings/downloads"}>Automatic search settings</Link></> : null}</p> : null}
      <DownloadsOffNotice off={off} canManage={perms.admin} />
      <label className="flex items-center gap-2 text-sm text-[var(--ink-3)]">Library
        <Select aria-label="Library" className="w-auto max-w-full" value={version ?? ""} onChange={e => change("version", e.target.value)}>
          <option value="">All libraries</option>
          {Object.entries(d.libraries).map(([key, lib]) => <option key={key} value={key}>{lib} · {key}</option>)}
        </Select>
      </label>
      <div className="flex flex-wrap gap-2" aria-label="Waiting filters">{waits.map(([key, word]) => {
        const count = key ? d.counts[key] ?? 0 : Object.values(d.counts).reduce((a, b) => a + b, 0);
        const shown = key === "downloads_off" && off ? OFF_WORD[off] : word;
        return key && !count ? null : <Button key={key} variant={waiting === key || !waiting && !key ? "default" : "outline"} size="sm" aria-pressed={(waiting ?? "") === key} onClick={() => change("waiting", key)}>{shown} ({count})</Button>;
      })}</div>
      {d.search_now_pending > 0 ? <p role="status" data-testid="search-now-queue" className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)]">
        <span>{plural(d.search_now_pending, "search")} queued by Search now{!d.downloads_enabled ? " · waiting until Pause is off" : ""}</span>
        <Button variant="outline" size="sm" disabled={clear.isPending} onClick={() => clear.mutate()}>Cancel queued searches</Button>
      </p> : null}
      <div className="flex flex-wrap items-center gap-2" aria-label="Search now">
        <Button variant="outline" size="sm" disabled={!chosen.length || search.isPending} onClick={() => request({ body: { only, items: chosen.map(rowItem) }, count: chosen.length, title: `Search ${chosen.length} selected` }, chosen.length > left)}>Search selected ({chosen.length})</Button>
        <Button variant="outline" size="sm" disabled={!d.searchable || !d.downloads_enabled || search.isPending} title={!d.downloads_enabled ? "Paused" : !d.searchable ? "Nothing in this list can be searched now" : undefined} onClick={() => request({ body: { only, all: { ...(version ? { version } : {}), ...(waiting ? { waiting: waiting as NonNullable<WantedSearchBody["all"]>["waiting"] } : {}) } }, count: d.searchable, title: `Search all ${d.searchable} ${what}${version ? ` in ${label(version)}` : ""}` }, true)}>Search all {d.searchable > 0 ? `${d.searchable} ` : ""}{what}{version ? ` in ${label(version)}` : ""}</Button>
        <Button variant="outline" size="sm" className="ml-auto" aria-label="Columns" onClick={() => setPicker(true)}><Columns3 /><span className="hidden sm:inline">Columns</span></Button>
        {search.data && !ask ? <span role="status" className="basis-full text-[13px] text-[var(--ink-2)]" data-testid="search-now-result">{searchNowWords(search.data)}</span> : null}
        {(search.error && !ask) || clear.error ? <span role="alert" className="basis-full text-[13px] text-[var(--bad-ink)]">{errText(search.error ?? clear.error)}</span> : null}
      </div>
      {!items.length ? <Card><Empty>No items waiting in this selection.</Empty></Card> : <DataTable
        testid="wanted-table"
        rows={items}
        rowKey={rowKey}
        columns={columns}
        // While paused nothing can be searched, so nothing is offered for selection; a row with a download in flight
        // cannot be chosen.
        select={d.downloads_enabled ? {
          chosen: picked,
          toggle: (key, on) => setPicked(old => { const next = new Set(old); if (on) next.add(key); else next.delete(key); return next; }),
          all: on => setPicked(on ? new Set(selectable.map(rowKey)) : new Set()),
          selectable: key => selectable.some(row => rowKey(row) === key),
        } : undefined}
        selectAllLabel="Select all on this page"
        rowLabel={row => `${name(row)}, ${targetWords(row.targets)}, ${label(row.version)}`}
        actions={actions}
        cardHead={titleLink}
        rowProps={() => ({ "data-testid": "wanted-row" })}
      />}
      <Pager offset={offset} limit={PAGE} total={d.total} onPage={page} />
    </div> : null}
    {picker ? <ColumnPicker label="Wanted columns" note="Title and the row's links always stay. On a phone the columns show under each title." columns={COLUMNS} prefs={prefs} onChange={savePrefs} onClose={() => setPicker(false)} /> : null}
    {ask && d ? <ConfirmDialog danger={false} testid="search-now-confirm" busy={search.isPending} error={search.error} onClose={() => setAsk(undefined)} onConfirm={() => send(ask.body)}
      scope={{ title: ask.title, confirm: `Queue ${plural(ask.count, "search")}`, does: [`Queue ${plural(ask.count, "search")}; they start as the hourly limit allows`], keeps: ["The hourly limit: no search goes around it", "Searches that already started"] }}>
      <p data-testid="search-now-estimate">{plural(ask.count, "search")}. They start as the hourly limit allows: up to {d.per_hour} an hour ({d.searched_last_hour} used this hour). Raise <em>Searches per hour</em> in {perms.admin ? <Link className="text-primary hover:underline" to={settingHref("router", "wanted.per_hour") ?? "/admin/settings/downloads"}>Settings › Download clients</Link> : "Settings › Download clients"} to go faster.</p>
    </ConfirmDialog> : null}
  </div>;
}
