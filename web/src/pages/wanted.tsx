import { useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router";
import { useStatus } from "@/api/hooks";
import { useClearWantedSearch, useWanted, useWantedSearch, type WantedRow, type WantedSearchBody, type WantedSearchResult } from "@/api/wanted";
import { Empty, ErrorState, Loading, PageHeader, VersionTag } from "@/components/page";
import { WantedTabs } from "@/components/wanted-tabs";
import { journalLink } from "@/lib/admin-links";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { fmtAgo, fmtGoDuration, fmtTime } from "@/lib/format";
import { settingHref } from "@/lib/settings-map";
import { globalDownloadsOff, willGrab, type VersionGrabState } from "@/lib/version-grab";

const waits = [
  ["", "All"], ["due", "Due"], ["searched_recently", "Searched recently"], ["in_flight", "Downloading"],
  ["grab_off", "Downloads off"], ["loop_off", "Loop off"], ["downloads_off", "Paused globally"],
] as const;

/** "unknown": new downloads are off but /status (the lock state) has not answered, so no cause is named. */
type GlobalOff = ReturnType<typeof globalDownloadsOff> | "unknown";
const OFF_WORD = { locked: "Locked off", off: "Paused globally", unknown: "New downloads off" } as const;

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

/** Why a row waits. The library's own state comes from the server's grab state (GET /status), worded by the shared
 * verdict: "Locked off" under the environment lock, and a library whose download gate is closed says so even while
 * the search loop is off. Without that state (loading or failed) the row falls back to the wanted reason, worded
 * without a cause. */
function Waiting({ row, enabled, missing, state, off }: { row: WantedRow; enabled: boolean; missing: boolean; state?: VersionGrabState; off: GlobalOff }) {
  const library = state ? <VersionStatus state={state} canManage settingsKey={row.version} /> : null;
  if (row.search_queued) return <>Queued for search (#{row.search_queued})</>;
  switch (row.waiting) {
    case "due": return <>Next in line (#{row.position})</>;
    case "searched_recently": return <>Searched {fmtAgo(row.last_run?.at)}; again {fmtAgo(row.next_at)}</>;
    case "in_flight": return <>Downloading</>;
    case "grab_off": return library ?? <>Downloads off: Don't download for this library is ticked · <Link className="text-primary hover:underline" to={`/admin/settings/libraries?edit=${encodeURIComponent(row.version)}`}>Edit library</Link></>;
    case "loop_off": return <>{enabled && row.reason === "cutoff" ? "Cutoff-unmet searches are off" : enabled && !missing ? "Missing searches are off" : "Automatic searches are off"}{state && !willGrab(state) ? <div className="mt-1">{library}</div> : null}</>;
    case "downloads_off": return library ?? <>{OFF_WORD[off ?? "unknown"]}</>;
  }
}

/** Why a row cannot be searched now ("" when it can): its search could only be refused or dropped. The server names
 * it apart from the loop's waiting reason, which says "loop off" first. */
function blocked(row: WantedRow): string {
  switch (row.search_blocked) {
    case "grab_off": return "Downloads are off for this library";
    case "downloads_off": return "New downloads are off";
    case "in_flight": return "A download for it is in progress";
    default: return "";
  }
}

const rowKey = (row: WantedRow) => `${row.title_id}/${row.version}/${row.season ?? ""}`;
const rowItem = (row: WantedRow) => ({ title_id: row.title_id, version: row.version, ...(row.season === undefined ? {} : { season: row.season }) });
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "es"}`;
const clock = (iso: string | null | undefined) => iso ? new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";

/** What a Search now request did, in words: "Started 6, queued 306; next at 14:05", then what was skipped and why. */
export function searchNowWords(r: WantedSearchResult): string {
  const parts = [`Started ${r.started}, queued ${r.pending}${r.pending && r.next_at ? `; next at ${clock(r.next_at)}` : ""}`];
  const why = new Map<string, number>();
  for (const s of r.skipped) why.set(s.reason, (why.get(s.reason) ?? 0) + 1);
  for (const [reason, n] of why) parts.push(`Skipped ${n}: ${reason}`);
  return parts.join(". ") + ".";
}

type Ask = { body: WantedSearchBody; count: number; title: string };

export function WantedPage() {
  const only = useLocation().pathname === "/wanted/cutoff" ? "cutoff" : "missing";
  const [sp, setSp] = useSearchParams();
  const version = sp.get("version") || undefined;
  const waiting = sp.get("waiting") || undefined;
  const offset = Math.max(0, Number(sp.get("offset")) || 0);
  const q = useWanted({ only, version, waiting, limit: 50, offset });
  const status = useStatus();
  const states = new Map((status.data?.versions ?? []).map((v) => [v.key, v.grab_state]));
  const off: GlobalOff = states.size ? globalDownloadsOff([...states.values()]) : q.data && !q.data.downloads_enabled ? "unknown" : undefined;
  const change = (key: string, value: string) => {
    const next = new URLSearchParams(sp);
    next.delete("offset");
    if (value) next.set(key, value); else next.delete(key);
    setSp(next);
  };
  const label = (key: string) => q.data?.libraries[key] || key;
  const d = q.data;
  const search = useWantedSearch();
  const clear = useClearWantedSearch();
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [ask, setAsk] = useState<Ask>();
  const searchable = (d?.items ?? []).filter(row => !blocked(row));
  const chosen = searchable.filter(row => picked.has(rowKey(row)));
  const left = d ? Math.max(0, d.per_hour - d.searched_last_hour) : 0;
  const send = (body: WantedSearchBody) => search.mutate(body, { onSuccess: () => { setPicked(new Set()); setAsk(undefined); } });
  // A request the hour cannot start at once is confirmed first, with how long it will take.
  const request = (a: Ask, confirm: boolean) => {
    search.reset();
    if (confirm) setAsk(a); else send(a.body);
  };
  const toggle = (key: string, on: boolean) => setPicked(old => { const next = new Set(old); if (on) next.add(key); else next.delete(key); return next; });
  const what = only === "cutoff" ? "cutoff unmet" : "missing";
  const waitingHours = ask && d ? Math.ceil(Math.max(0, ask.count + d.search_now_pending - left) / d.per_hour) : 0;
  const tabQuery = version ? `?version=${encodeURIComponent(version)}` : "";
  return <div>
    <PageHeader title="Wanted" description="What automatic searches are waiting for. Viewing this page never starts a search; Search now queues one under the hourly limit." />
    <WantedTabs active={only} query={tabQuery} />
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground">Searches every {fmtGoDuration(d.interval)}, up to {d.per_hour} an hour, again after {fmtGoDuration(d.again_after)}. {plural(d.searched_last_hour, "search")} in the last hour. {d.add_time_pending > 0 ? `${plural(d.add_time_pending, "search")} for newly added titles ${d.add_time_pending === 1 ? "goes" : "go"} first.` : ""}</p>
      {!d.enabled || (only === "missing" ? !d.missing : d.cutoff_unmet === "off") ? <p role="status" className="rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)]">{!d.enabled ? "Automatic searches are off." : only === "cutoff" ? "Cutoff-unmet searches are off." : "Missing searches are off."} <Link className="text-primary hover:underline" to={settingHref("router", "wanted.enabled") ?? "/admin/settings/downloads"}>Automatic search settings</Link></p> : null}
      <DownloadsOffNotice off={off} canManage />
      <label className="flex items-center gap-2 text-sm text-[var(--ink-3)]">Library
        <Select aria-label="Library" className="w-auto max-w-full" value={version ?? ""} onChange={e => change("version", e.target.value)}>
          <option value="">All libraries</option>
          {Object.entries(d.libraries).map(([key, name]) => <option key={key} value={key}>{name} · {key}</option>)}
        </Select>
      </label>
      <div className="flex flex-wrap gap-2" aria-label="Waiting filters">{waits.map(([key, name]) => {
        const count = key ? d.counts[key] ?? 0 : Object.values(d.counts).reduce((a, b) => a + b, 0);
        const word = key === "downloads_off" && off ? OFF_WORD[off] : name;
        return key && !count ? null : <Button key={key} variant={waiting === key || !waiting && !key ? "default" : "outline"} size="sm" aria-pressed={(waiting ?? "") === key} onClick={() => change("waiting", key)}>{word} ({count})</Button>;
      })}</div>
      {d.search_now_pending > 0 ? <p role="status" data-testid="search-now-queue" className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)]">
        <span>{plural(d.search_now_pending, "search")} queued by Search now{d.search_now_next_at ? ` · next at ${clock(d.search_now_next_at)}` : ""}</span>
        <Button variant="outline" size="sm" disabled={clear.isPending} onClick={() => clear.mutate()}>Cancel queued searches</Button>
      </p> : null}
      <div className="flex flex-wrap items-center gap-2" aria-label="Search now">
        <Button variant="outline" size="sm" disabled={!chosen.length || search.isPending} onClick={() => request({ body: { only, items: chosen.map(rowItem) }, count: chosen.length, title: `Search ${chosen.length} selected` }, chosen.length > left)}>Search selected ({chosen.length})</Button>
        <Button variant="outline" size="sm" disabled={!d.searchable || !d.downloads_enabled || search.isPending} title={d.downloads_enabled ? undefined : "New downloads are off"} onClick={() => request({ body: { only, all: { ...(version ? { version } : {}), ...(waiting ? { waiting: waiting as NonNullable<WantedSearchBody["all"]>["waiting"] } : {}) } }, count: d.searchable, title: `Search all ${d.searchable} ${what}${version ? ` in ${label(version)}` : ""}` }, true)}>Search all {d.searchable} {what}{version ? ` in ${label(version)}` : ""}</Button>
        {search.data && !ask ? <span role="status" className="text-[13px] text-[var(--ink-2)]" data-testid="search-now-result">{searchNowWords(search.data)}</span> : null}
        {(search.error && !ask) || clear.error ? <span role="alert" className="text-[13px] text-[var(--bad-ink)]">{errText(search.error ?? clear.error)}</span> : null}
      </div>
      {!d.items.length ? <Card><Empty>No items waiting in this selection.</Empty></Card> : <Card className="overflow-hidden p-0">
        <div className="flex items-center gap-3 border-b border-[var(--line)] bg-[var(--sunken)] px-3 py-1.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">
          <Checkbox aria-label="Select all on this page" disabled={!searchable.length} checked={searchable.length > 0 && chosen.length === searchable.length} indeterminate={chosen.length > 0 && chosen.length < searchable.length} onChange={e => setPicked(e.target.checked ? new Set(searchable.map(rowKey)) : new Set())} />
          <div className="hidden flex-1 grid-cols-[2fr_1fr_1fr_2fr_2fr_1fr] gap-3 md:grid" aria-hidden>{["Title", "Library", "What", "Why waiting", "Last search", "Links"].map(h => <span key={h}>{h}</span>)}</div>
        </div>
        <ul className="divide-y divide-[var(--line)]">{d.items.map(row => { const name = d.titles[row.title_id] || `Title ${row.title_id}`; const why = blocked(row); return <li key={`${row.title_id}/${row.version}/${row.targets.join()}`} className="flex min-w-0 items-baseline gap-3 px-3 py-2 text-[13px]" data-testid="wanted-row">
          <Checkbox className="self-start md:self-baseline" aria-label={`Select ${name}, ${targetWords(row.targets)}, ${label(row.version)}`} disabled={!!why} title={why || undefined} checked={!why && picked.has(rowKey(row))} onChange={e => toggle(rowKey(row), e.target.checked)} />
          <div className="grid min-w-0 flex-1 gap-1.5 md:grid-cols-[2fr_1fr_1fr_2fr_2fr_1fr] md:items-baseline md:gap-3">
          <Link className="font-semibold text-[var(--ink)] hover:text-[var(--accent-ink)] [overflow-wrap:anywhere]" to={`/title/${row.title_id}`}>{name}</Link>
          <div className="min-w-0">{label(row.version)}<div className="mt-0.5"><VersionTag k={row.version} /></div></div>
          <div>{targetWords(row.targets)}</div>
          <div><Waiting row={row} enabled={d.enabled} missing={d.missing} state={states.get(row.version)} off={off} /></div>
          <div>{row.last_run ? <span title={fmtTime(row.last_run.at)}>{fmtAgo(row.last_run.at)} · </span> : null}{searchResult(row.last_run)}</div>
          <div className="flex flex-wrap gap-2"><Link className="text-primary hover:underline" to={`/title/${row.title_id}/releases?version=${encodeURIComponent(row.version)}`}>Releases</Link>{row.decision_id ? <Link className="text-primary hover:underline" to={journalLink(String(row.decision_id))}>Decision</Link> : null}<button type="button" className="text-primary hover:underline disabled:cursor-not-allowed disabled:text-[var(--ink-3)] disabled:no-underline" disabled={!!why || !!row.search_queued || search.isPending} title={why || (row.search_queued ? "Already queued" : undefined)} onClick={() => request({ body: { only, items: [rowItem(row)] }, count: 1, title: "Search now" }, false)}>Search now</button></div>
          </div>
        </li>; })}</ul>
      </Card>}
      <div className="flex items-center justify-between gap-3"><Button variant="outline" disabled={offset === 0 || q.isPlaceholderData} onClick={() => change("offset", String(Math.max(0, offset - 50)))}>Previous</Button><span className="text-sm text-muted-foreground">{d.total ? `${offset + 1}–${Math.min(offset + 50, d.total)} of ${d.total}` : "0 items"}</span><Button variant="outline" disabled={offset + 50 >= d.total || q.isPlaceholderData} onClick={() => change("offset", String(offset + 50))}>Next</Button></div>
    </div> : null}
    {ask && d ? <ConfirmDialog danger={false} testid="search-now-confirm" busy={search.isPending} error={search.error} onClose={() => setAsk(undefined)} onConfirm={() => send(ask.body)}
      scope={{ title: ask.title, confirm: `Queue ${plural(ask.count, "search")}`, does: [`Queue ${plural(ask.count, "search")}; they start oldest first as the hourly limit allows`], keeps: ["The hourly limit: no search goes around it", "Searches that already started"] }}>
      <p data-testid="search-now-estimate">{plural(ask.count, "search")}. Up to {d.per_hour} start each hour ({d.searched_last_hour} used this hour){waitingHours > 0 ? `, so this takes about ${waitingHours} hour${waitingHours === 1 ? "" : "s"}` : ", so they start now"}. While they wait, each automatic search round starts them before its own searches. Raise <em>Searches per hour</em> in <Link className="text-primary hover:underline" to={settingHref("router", "wanted.per_hour") ?? "/admin/settings/downloads"}>Settings › Download clients</Link> to go faster.</p>
    </ConfirmDialog> : null}
  </div>;
}
