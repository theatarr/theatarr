import { Link, useLocation, useSearchParams } from "react-router";
import { useStatus } from "@/api/hooks";
import { useWanted, type WantedRow } from "@/api/wanted";
import { Empty, ErrorState, Loading, PageHeader, VersionTag } from "@/components/page";
import { WantedTabs } from "@/components/wanted-tabs";
import { journalLink } from "@/lib/admin-links";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
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
  switch (row.waiting) {
    case "due": return <>Next in line (#{row.position})</>;
    case "searched_recently": return <>Searched {fmtAgo(row.last_run?.at)}; again {fmtAgo(row.next_at)}</>;
    case "in_flight": return <>Downloading</>;
    case "grab_off": return library ?? <>Downloads off: Allow downloads for this library is off · <Link className="text-primary hover:underline" to={`/admin/settings/libraries?edit=${encodeURIComponent(row.version)}`}>Edit library</Link></>;
    case "loop_off": return <>{enabled && row.reason === "cutoff" ? "Cutoff-unmet searches are off" : enabled && !missing ? "Missing searches are off" : "Automatic searches are off"}{state && !willGrab(state) ? <div className="mt-1">{library}</div> : null}</>;
    case "downloads_off": return library ?? <>{OFF_WORD[off ?? "unknown"]}</>;
  }
}

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
  const tabQuery = version ? `?version=${encodeURIComponent(version)}` : "";
  return <div>
    <PageHeader title="Wanted" description="What automatic searches are waiting for. Viewing this page never starts a search." />
    <WantedTabs active={only} query={tabQuery} />
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground">Searches every {fmtGoDuration(d.interval)}, up to {d.per_hour} an hour, again after {fmtGoDuration(d.again_after)}. {d.searched_last_hour} searches in the last hour. {d.add_time_pending > 0 ? `${d.add_time_pending} searches for newly added titles go first.` : ""}</p>
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
      {!d.items.length ? <Card><Empty>No items waiting in this selection.</Empty></Card> : <Card className="overflow-hidden p-0">
        <div className="hidden grid-cols-[2fr_1fr_1fr_2fr_2fr_1fr] gap-3 border-b border-[var(--line)] bg-[var(--sunken)] px-3 py-1.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)] md:grid" aria-hidden>{["Title", "Library", "What", "Why waiting", "Last search", "Links"].map(h => <span key={h}>{h}</span>)}</div>
        <ul className="divide-y divide-[var(--line)]">{d.items.map(row => <li key={`${row.title_id}/${row.version}/${row.targets.join()}`} className="grid min-w-0 gap-1.5 px-3 py-2 text-[13px] md:grid-cols-[2fr_1fr_1fr_2fr_2fr_1fr] md:items-baseline md:gap-3" data-testid="wanted-row">
          <Link className="font-semibold text-[var(--ink)] hover:text-[var(--accent-ink)] [overflow-wrap:anywhere]" to={`/title/${row.title_id}`}>{d.titles[row.title_id] || `Title ${row.title_id}`}</Link>
          <div className="min-w-0">{label(row.version)}<div className="mt-0.5"><VersionTag k={row.version} /></div></div>
          <div>{targetWords(row.targets)}</div>
          <div><Waiting row={row} enabled={d.enabled} missing={d.missing} state={states.get(row.version)} off={off} /></div>
          <div>{row.last_run ? <span title={fmtTime(row.last_run.at)}>{fmtAgo(row.last_run.at)} · </span> : null}{searchResult(row.last_run)}</div>
          <div className="flex flex-wrap gap-2"><Link className="text-primary hover:underline" to={`/title/${row.title_id}/releases?version=${encodeURIComponent(row.version)}`}>Releases</Link>{row.decision_id ? <Link className="text-primary hover:underline" to={journalLink(String(row.decision_id))}>Decision</Link> : null}</div>
        </li>)}</ul>
      </Card>}
      <div className="flex items-center justify-between gap-3"><Button variant="outline" disabled={offset === 0 || q.isPlaceholderData} onClick={() => change("offset", String(Math.max(0, offset - 50)))}>Previous</Button><span className="text-sm text-muted-foreground">{d.total ? `${offset + 1}–${Math.min(offset + 50, d.total)} of ${d.total}` : "0 items"}</span><Button variant="outline" disabled={offset + 50 >= d.total || q.isPlaceholderData} onClick={() => change("offset", String(offset + 50))}>Next</Button></div>
    </div> : null}
  </div>;
}
