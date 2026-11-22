import { Columns3, X } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router";
import { useBulkMonitor } from "@/api/copies";
import { useStatus } from "@/api/hooks";
import { useMonitor, useScopedSearch } from "@/api/manage";
import { useLibraryName } from "@/lib/library-names";
import { usePerms } from "@/api/library";
import { useClearWantedSearch, useWanted, useWantedSearch, type WantedRow, type WantedSearchBody, type WantedSearchResult, type WantedSort } from "@/api/wanted";
import { Empty, ErrorState, Loading, PageHeader, VersionTag } from "@/components/page";
import { InteractiveSearchButton } from "@/components/title/interactive-search";
import { failed, searched } from "@/components/title/seasons";
import { toast } from "@/components/toaster";
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
import { downloadHref } from "@/lib/activity";
import { fmtAgo, fmtDay, fmtGoDuration, fmtNumber, fmtTime, plural } from "@/lib/format";
import { nextSearchWhy } from "@/lib/library-rows";
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

/** A grabbed download still on its way, in Activity's stage words (lib/activity.ts stageLabel). */
const IN_FLIGHT: Record<string, string> = {
  queued: "Queued", adding: "Starting", added: "Waiting to start", downloading: "Downloading", ready: "Download complete", importing: "Linking",
};

/** The Last search cell's words. A grabbed run says what became of its download: Grabbed once imported, the stage
 *  while it is on its way, "Grabbed, not imported" when it failed (the cell links those two to Activity). */
export function searchResult(run: WantedRow["last_run"]): string {
  if (!run) return "Never searched";
  if (run.state === "failed") return `Failed: ${run.message}`;
  if (run.state === "queued") return "Queued";
  if (run.state === "searching") return "Searching";
  // Every candidate missed every provider's cache before any took it: nothing was grabbed (no Activity link).
  if (run.state === "missed") return run.message || "No release was cached";
  if (run.downloaded) {
    const stage = IN_FLIGHT[run.download_state ?? ""];
    return stage ? `Grabbed · ${stage}` : run.download_state === "failed" ? "Grabbed, not imported" : "Grabbed";
  }
  if (run.state === "routed") return "Sent to download";
  return run.results ? `${plural(run.results, "result")}, none accepted` : "Nothing found";
}

/** The What cell: the targets in words, with a whole season's episode count ("S20, 139 episodes"). */
export function whatWords(row: Pick<WantedRow, "targets" | "episodes">): string {
  const words = targetWords(row.targets);
  return row.episodes && row.targets.some((t) => /^s\d+$/.test(t)) ? `${words}, ${plural(row.episodes, "episode")}` : words;
}

/** The What cell; a series row opens to its episodes: on Missing each with its air date, on Cutoff unmet with the
 *  quality it holds, and the cutoff. Each episode has Search and Unmonitor in the row's library only. The lines are
 *  mounted only while open. */
function WhatCell({ row }: { row: WantedRow }) {
  const [open, setOpen] = useState(false);
  // Held here, always mounted, so an action's toast and refresh still run when the row is closed meanwhile.
  const search = useScopedSearch(row.title_id);
  const monitor = useMonitor(row.title_id);
  const eps = row.episode_list ?? [];
  if (!eps.length) return <>{whatWords(row)}</>;
  return <details data-testid="wanted-episodes" open={open} onToggle={(ev) => setOpen(ev.currentTarget.open)}>
    <summary className="cursor-pointer">{whatWords(row)}</summary>
    {open ? <EpisodeLines row={row} eps={eps} search={search} monitor={monitor} /> : null}
    {row.cutoff ? <p className="mt-1 text-[13px] text-[var(--ink-3)]">Cutoff: {row.cutoff}</p> : null}
  </details>;
}

type WantedEpisode = NonNullable<WantedRow["episode_list"]>[number];

/** A Wanted row's episodes, each with Search (the existing episode search) and Unmonitor (the existing episode
 *  monitoring) for the row's library; after either the Wanted list is read again, so the row's count follows. */
function EpisodeLines({ row, eps, search, monitor }: { row: WantedRow; eps: WantedEpisode[]; search: ReturnType<typeof useScopedSearch>; monitor: ReturnType<typeof useMonitor> }) {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: ["wanted"] });
  const why = blocked(row);
  const link = "text-primary hover:underline disabled:cursor-not-allowed disabled:text-[var(--ink-3)] disabled:no-underline";
  return <ul className="mt-1 space-y-1 text-[13px] text-[var(--ink-2)]">
    {eps.map(e => {
      const target = { episode_ids: [e.episode_id], versions: [row.version] };
      const code = episodeCode(e);
      const detail = row.reason === "cutoff" ? e.quality || "Unknown quality" : e.air_date ? fmtDay(e.air_date) : "";
      return <li key={e.episode_id} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
        <span className="[overflow-wrap:anywhere]" data-testid="wanted-episode">{code}{e.name ? ` · ${e.name}` : ""}{detail ? ` · ${detail}` : ""}</span>
        <span className="inline-flex gap-3">
          <button type="button" className={link} aria-label={`Search ${code}`} disabled={!!why || search.isPending} title={why || undefined}
            onClick={() => search.mutate(target, { onSuccess: (o) => searched(o), onError: failed("Not searched"), onSettled: refresh })}>Search</button>
          <button type="button" className={link} aria-label={`Unmonitor ${code}`} disabled={monitor.isPending}
            onClick={() => monitor.mutate({ ...target, monitored: false }, { onSuccess: (o) => toast({ tone: "ok", title: "Not monitored", body: o.message }), onError: failed("Not changed"), onSettled: refresh })}>Unmonitor</button>
        </span>
      </li>;
    })}
  </ul>;
}

const episodeCode = (e: { season: number; number: number }) =>
  `S${String(e.season).padStart(2, "0")}E${String(e.number).padStart(2, "0")}`;

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


/** The "Next search" cell, in the title page's words (lib/library-rows.ts nextSearchWhy): "Now (new release)", "in 3 h
 * (after a failed download)", with the try a new release's retry will be. A due row says "Now": the hourly limit
 * decides when it starts, so no time is promised. A row never shows a time while paused. The library's own closed gate (from GET /status, admins) is shown under
 * "Not scheduled" and in place of "Paused". */
function NextSearch({ row, state, off, admin }: { row: WantedRow; state?: VersionGrabState; off: GlobalOff; admin: boolean }) {
  const why = nextSearchWhy(row.why);
  const library = state ? <VersionStatus state={state} canManage={admin} settingsKey={row.version} /> : null;
  switch (row.waiting) {
    case "due": return <>Now{why ? ` (${why})` : ""}</>;
    case "scheduled":
      return <><span title={fmtTime(row.next_at)}>{fmtAgo(row.next_at)}</span>{why ? ` (${why})` : null}{row.why === "new" ? <span className="text-[var(--ink-3)]"> · try {(row.tries ?? 0) + 1}</span> : null}</>;
    case "in_flight": return <>Downloading</>;
    case "not_scheduled": return <>Not scheduled{state && !willGrab(state) ? <div className="mt-1">{library}</div> : null}</>;
    case "downloads_off": return library ?? <>{OFF_WORD[off ?? "unknown"]}</>;
  }
}

/** The journal entry of the row's last search (admins): the search run's entry ("run:<id>"), which shows the decision. */
function DecisionLink({ row }: { row: WantedRow }) {
  if (!row.last_run) return null;
  return <Link className="text-primary hover:underline" to={journalLink(`run:${row.last_run.id}`)}>Decision</Link>;
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

/** What a Search now request did, in words: "Started 6, queued 306", then what was skipped and why. */
export function searchNowWords(r: WantedSearchResult): string {
  const parts = [`Started ${r.started}, queued ${r.pending}`];
  const why = new Map<string, number>();
  for (const s of r.skipped) why.set(s.reason, (why.get(s.reason) ?? 0) + 1);
  for (const [reason, n] of why) parts.push(`Skipped ${n}: ${reason}`);
  return parts.join(". ") + ".";
}

type Column = "library" | "what" | "date" | "next" | "last";
const COLUMNS: ColumnDef<Column>[] = [
  { id: "library", label: "Library", hint: "The library the item is wanted in" },
  { id: "what", label: "What", hint: "The film, or the season's episodes" },
  { id: "date", label: "Released", hint: "The film's home release, or the newest air date of the season's episodes" },
  { id: "next", label: "Next search", hint: "When the item is searched next" },
  { id: "last", label: "Last search", hint: "When it was last searched, and what that found" },
];
const ALL_COLUMNS = COLUMNS.map(c => c.id);

type Ask = { body: WantedSearchBody; count: number; title: string };

/** The sorts, for the phone's sort picker (the table's headers sort too). */
const SORTS: [WantedSort | "", string][] = [
  ["", "Search order"], ["title", "Title A–Z"], ["-title", "Title Z–A"], ["-date", "Newest release first"],
  ["date", "Oldest release first"], ["-last", "Searched most recently"], ["last", "Searched longest ago"],
];
const isSort = (v: string | null): v is WantedSort => SORTS.some(([k]) => k && k === v);
/** A header's first click: titles A–Z, dates and searches newest first. */
const FIRST: Record<"title" | "date" | "last", WantedSort> = { title: "title", date: "-date", last: "-last" };

export function WantedPage() {
  const only = useLocation().pathname === "/wanted/cutoff" ? "cutoff" : "missing";
  const [sp, setSp] = useSearchParams();
  const version = sp.get("version") || undefined;
  const waiting = sp.get("waiting") || undefined;
  const offset = Math.max(0, Number(sp.get("offset")) || 0);
  const title = Math.max(0, Math.trunc(Number(sp.get("title")) || 0)) || undefined;
  const rawSort = sp.get("sort");
  const sort = isSort(rawSort) ? rawSort : undefined;
  const q = useWanted({ only, version, waiting, title, sort, limit: PAGE, offset });
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
  const nameOf = useLibraryName();
  const label = (key: string) => q.data?.libraries[key] || nameOf(key);
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
  const tabQuery = version || title ? `?${new URLSearchParams({ ...(version ? { version } : {}), ...(title ? { title: String(title) } : {}) })}` : "";
  const switchOff = d ? (only === "missing" ? !d.missing : d.cutoff_unmet === "off") : false;
  // The regular repeat runs only while a backlog switch is on.
  const backlogOn = d ? d.missing || d.cutoff_unmet !== "off" : false;
  const name = (row: WantedRow) => d?.titles[row.title_id] || `Title ${row.title_id}`;
  // A film row's Unmonitor turns off that one copy (the library's bulk monitor with one title and one library); a
  // season row has it per episode.
  const qc = useQueryClient();
  const unmonitor = useBulkMonitor();
  const unmonitorFilm = (row: WantedRow) => unmonitor.mutate({ title_ids: [row.title_id], versions: [row.version], monitored: false }, {
    onSuccess: (o) => toast({ tone: "ok", title: "Not monitored", body: `${name(row)} in ${label(row.version)}: ${o.message}.` }),
    onError: failed("Not changed"),
    onSettled: () => void qc.invalidateQueries({ queryKey: ["wanted"] }),
  });
  const titleName = title ? d?.titles[title] || `Title ${title}` : "";
  const sortBy = (column: string) => {
    const c = column as keyof typeof FIRST;
    change("sort", sort === FIRST[c] ? (FIRST[c].startsWith("-") ? c : `-${c}`) : FIRST[c]);
  };
  const titleLink = (row: WantedRow) => <Link className="font-semibold text-[var(--ink)] hover:text-[var(--accent-ink)] [overflow-wrap:anywhere]" to={`/title/${row.title_id}`}>{name(row)}</Link>;
  const cells: Record<Column, DataColumn<WantedRow>> = {
    library: { id: "library", label: "Library", cell: row => <VersionTag k={row.version} label={d?.libraries[row.version]} /> },
    // On a phone under the actions: an opened season's episode list would push the dates and buttons far down.
    what: { id: "what", label: "What", cellClassName: "max-w-[22rem]", card: "foot", cell: row => <WhatCell row={row} /> },
    // A film's date is its home release, named so: its About's Released is the first cinema release.
    date: { id: "date", label: "Released", sortable: true, cell: row => {
      if (!row.date) return "—";
      const day = row.date_estimate ? `~${fmtDay(row.date)} (estimate)` : fmtDay(row.date);
      return row.targets.includes("movie") ? `Home release ${day}` : day;
    } },
    next: { id: "next", label: "Next search", cell: row => <NextSearch row={row} state={states.get(row.version)} off={off} admin={perms.admin} /> },
    // The decision behind the last search sits with it, so the row's actions keep their place.
    last: { id: "last", label: "Last search", sortable: true, cell: row => {
      const result = searchResult(row.last_run);
      const grabbed = result !== "Grabbed" && result.startsWith("Grabbed") && row.last_run?.download_id;
      return <>{row.last_run ? <span title={fmtTime(row.last_run.at)}>{fmtAgo(row.last_run.at)} · </span> : null}{result}{grabbed ? <> · <Link className="text-primary hover:underline" to={downloadHref(row.last_run!.download_id!)}>See in Activity</Link></> : null}{row.decision_id && perms.admin ? <> · <DecisionLink row={row} /></> : null}</>;
    } },
  };
  const columns: DataColumn<WantedRow>[] = [
    { id: "title", label: "Title", sortable: true, cell: titleLink, card: "none", cellClassName: "min-w-[10rem]" },
    ...prefs.order.filter(id => prefs.shown.includes(id)).map(id => cells[id]),
  ];
  const actions = (row: WantedRow) => {
    const why = blocked(row);
    return <div className="flex flex-wrap items-baseline gap-2 md:flex-nowrap md:justify-end">
      <InteractiveSearchButton titleId={row.title_id} version={row.version} season={row.season} size="xs" variant="ghost" />
      {/* With Last search hidden, the decision link stays a row action. */}
      {row.decision_id && perms.admin && !prefs.shown.includes("last") ? <DecisionLink row={row} /> : null}
      <button type="button" className="text-primary hover:underline disabled:cursor-not-allowed disabled:text-[var(--ink-3)] disabled:no-underline" disabled={!!why || asked(row) || search.isPending} title={why || (asked(row) ? "Already asked for" : undefined)} onClick={() => request({ body: { only, items: [rowItem(row)] }, count: 1, title: "Search now" }, false)}>Search now</button>
      {row.targets.includes("movie") ? <button type="button" className="text-primary hover:underline disabled:cursor-not-allowed disabled:text-[var(--ink-3)] disabled:no-underline" aria-label={`Unmonitor ${name(row)}`} disabled={unmonitor.isPending} onClick={() => unmonitorFilm(row)}>Unmonitor</button> : null}
      {why && row.waiting !== "downloads_off" && row.waiting !== "in_flight" ? <span className="text-[var(--ink-3)]" data-testid="search-blocked">{why}</span> : null}
    </div>;
  };
  return <div>
    <PageHeader title="Wanted" description="What is waiting for a search and when each item is searched next. Search now starts a search under the hourly limit." />
    <WantedTabs active={only} query={tabQuery} admin={perms.admin} />
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground" data-testid="wanted-pace">Up to {fmtNumber(d.per_hour)} searches an hour ({fmtNumber(d.searched_last_hour)} in the last hour).{backlogOn ? ` Items are searched again every ${fmtGoDuration(d.again_after)}.` : ""}{only === "missing" && d.missing ? ` A missing new release is tried again sooner: the wait starts at 1 hour and doubles each time, up to ${fmtGoDuration(d.again_after)}, until a week after its release.` : ""}</p>
      {switchOff ? <p role="status" data-testid="wanted-switch-off" className="rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)]">Theatarr is not searching for {only === "cutoff" ? "upgrades" : "missing items"} by itself. Items here are searched when you ask, or after a failed download.{only === "missing" ? " New releases are still picked up from RSS feeds, and one that a search did not find is tried again sooner, until a week after its release." : " Better releases are still picked up from RSS feeds."}{perms.admin ? <> <Link className="text-primary hover:underline" to={settingHref("router", only === "cutoff" ? "wanted.cutoff_unmet" : "wanted.missing") ?? "/admin/settings/indexers/rss"}>Automatic search settings</Link></> : null}</p> : null}
      <DownloadsOffNotice off={off} canManage={perms.admin} />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label className="flex items-center gap-2 text-sm text-[var(--ink-3)]">Library
          <Select aria-label="Library" className="w-auto max-w-full" value={version ?? ""} onChange={e => change("version", e.target.value)}>
            <option value="">All libraries</option>
            {Object.entries(d.libraries).map(([key, lib]) => <option key={key} value={key}>{lib}</option>)}
          </Select>
        </label>
        <label className="flex items-center gap-2 text-sm text-[var(--ink-3)]">Sort
          <Select aria-label="Sort" className="w-auto max-w-full" value={sort ?? ""} onChange={e => change("sort", e.target.value)}>
            {SORTS.map(([key, word]) => <option key={key} value={key}>{word}</option>)}
          </Select>
        </label>
        {title ? <span data-testid="wanted-title-filter" className="inline-flex items-center gap-1 rounded-full border border-[var(--line)] bg-[var(--sunken)] py-0.5 pl-3 pr-1 text-sm">
          {titleName}
          <button type="button" aria-label={`Show every title, not only ${titleName}`} className="grid size-6 place-items-center rounded-full text-[var(--ink-3)] hover:bg-[var(--hover)]" onClick={() => change("title", "")}><X className="size-3.5" /></button>
        </span> : null}
      </div>
      <div className="flex flex-wrap gap-2" aria-label="Waiting filters">{waits.map(([key, word]) => {
        const count = key ? d.counts[key] ?? 0 : Object.values(d.counts).reduce((a, b) => a + b, 0);
        const shown = key === "downloads_off" && off ? OFF_WORD[off] : word;
        return key && !count ? null : <Button key={key} variant={waiting === key || !waiting && !key ? "default" : "outline"} size="sm" aria-pressed={(waiting ?? "") === key} onClick={() => change("waiting", key)}>{shown} ({fmtNumber(count)})</Button>;
      })}</div>
      {d.search_now_pending > 0 ? <p role="status" data-testid="search-now-queue" className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[5px] border border-[var(--line)] bg-[var(--sunken)] px-3 py-2 text-[13px] text-[var(--ink-2)]">
        <span>{plural(d.search_now_pending, "search", "searches")} queued by Search now{!d.downloads_enabled ? " · waiting until Pause is off" : ""}</span>
        <Button variant="outline" size="sm" disabled={clear.isPending} onClick={() => clear.mutate()}>Cancel queued searches</Button>
      </p> : null}
      <div className="flex flex-wrap items-center gap-2" aria-label="Search now">
        {/* While paused the checkboxes are gone, and Search selected with them. */}
        {d.downloads_enabled ? <Button variant="outline" size="sm" disabled={!chosen.length || search.isPending} onClick={() => request({ body: { only, items: chosen.map(rowItem) }, count: chosen.length, title: `Search ${fmtNumber(chosen.length)} selected` }, chosen.length > left)}>Search selected ({fmtNumber(chosen.length)})</Button> : null}
        <Button variant="outline" size="sm" disabled={!d.searchable || !d.downloads_enabled || search.isPending} title={!d.downloads_enabled ? "Paused" : !d.searchable ? "Nothing in this list can be searched now" : undefined} onClick={() => request({ body: { only, all: { ...(version ? { version } : {}), ...(waiting ? { waiting: waiting as NonNullable<WantedSearchBody["all"]>["waiting"] } : {}), ...(title ? { title_id: title } : {}) } }, count: d.searchable, title: `Search all ${fmtNumber(d.searchable)} ${what}${title ? ` of ${titleName}` : ""}${version ? ` in ${label(version)}` : ""}` }, true)}>Search all {d.searchable > 0 ? `${fmtNumber(d.searchable)} ` : ""}{what}{title ? ` of ${titleName}` : ""}{version ? ` in ${label(version)}` : ""}</Button>
        <Button variant="outline" size="sm" className="ml-auto" aria-label="Columns" onClick={() => setPicker(true)}><Columns3 /><span className="hidden sm:inline">Columns</span></Button>
        {search.data && !ask ? <span role="status" className="basis-full text-[13px] text-[var(--ink-2)]" data-testid="search-now-result">{searchNowWords(search.data)}</span> : null}
        {(search.error && !ask) || clear.error ? <span role="alert" className="basis-full text-[13px] text-[var(--bad-ink)]">{errText(search.error ?? clear.error)}</span> : null}
      </div>
      {/* The unit: a row is one search (a film, or a series' season, in one library), not an episode or a title. */}
      {d.total > 0 ? <p className="text-[13px] text-[var(--ink-3)]" data-testid="wanted-count">{plural(d.total, "item")} (one per film or season in each library, not per episode){only === "missing" ? <>; Home's <Link className="text-primary hover:underline" to="/discover/missing">Missing</Link> counts titles</> : null}.</p> : null}
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
        sort={sort ? { column: sort.replace("-", ""), dir: sort.startsWith("-") ? "desc" : "asc" } : undefined}
        onSort={sortBy}
        actions={actions}
        cardHead={titleLink}
        // Top-aligned: an opened season (What) grows the row; the title, library and actions stay at its top.
        rowProps={() => ({ "data-testid": "wanted-row", className: "[&>td]:align-top" })}
      />}
      <Pager offset={offset} limit={PAGE} total={d.total} onPage={page} />
    </div> : null}
    {picker ? <ColumnPicker label="Wanted columns" note="Title and the row's links always stay. On a phone the columns show under each title." columns={COLUMNS} prefs={prefs} onChange={savePrefs} onClose={() => setPicker(false)} /> : null}
    {ask && d ? <ConfirmDialog danger={false} testid="search-now-confirm" busy={search.isPending} error={search.error} onClose={() => setAsk(undefined)} onConfirm={() => send(ask.body)}
      scope={{ title: ask.title, confirm: `Queue ${plural(ask.count, "search", "searches")}`, does: [`Queue ${plural(ask.count, "search", "searches")}; they start as the hourly limit allows`], keeps: ["The hourly limit: no search goes around it", "Searches that already started"] }}>
      <p data-testid="search-now-estimate">{plural(ask.count, "search", "searches")}. They start as the hourly limit allows: up to {d.per_hour} an hour ({d.searched_last_hour} used this hour). Raise <em>Searches per hour</em> in {perms.admin ? <Link className="text-primary hover:underline" to={settingHref("router", "wanted.per_hour") ?? "/admin/settings/indexers/rss"}>Settings › Indexers › Search &amp; RSS</Link> : "Settings › Indexers › Search & RSS"} to go faster.</p>
    </ConfirmDialog> : null}
  </div>;
}
