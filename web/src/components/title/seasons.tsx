// The title page's Episodes tab: each season as a collapsible section and, inside it, its episodes
// (GET /titles/{id}/episodes). Managed the way Sonarr does it (docs/design/manage.md), laid out phone first
// (docs/design/ui.md, Title page / Episodes; the logic is lib/seasons.ts):
//
// - a library selector with two or more libraries: All libraries (from the md width, the default there) or one
//   library (phones have no All and show the first). Every action here acts on the libraries in view, and in All on
//   every library, which its tooltip says;
// - a season header on one line: chevron, monitor switch, the season's name; with one library in view its badge
//   ("8 / 10", red when something is missing) and facts (missing, upgrades found, downloads, size, the upgrade status
//   with Why?, and the next search when the server sends it); in All a badge per library and the summary sentence;
//   at the right Search and Interactive search;
// - read-only monitoring and disabled search said once above the seasons, not on every line;
// - All / In progress / Missing / Upgrade found filters per season, and a selection of episodes for bulk monitor,
//   unmonitor and search whose bar shows only while something is selected and names the libraries it acts on; it acts
//   only on ticked episodes on screen (a tick hidden by a filter or a collapsed season is dropped, as is one from
//   another library view);
// - one library in view on desktop: Sonarr's table (checkbox, monitor switch, number, title, the chosen columns of
//   lib/episode-columns.ts, Search and Interactive search); All: a column per library (the matrix); phones: rows for
//   the library in view; every state in words (Available, Missing, Not aired, Upgrade found), never a bare icon;
// - accounts without "see files" get one availability column across copies and nothing about files or quality
//   beyond the resolution (the API sends nothing more);
// - outcomes of Search and monitoring come back as toasts;
// - episode rows open the episode overview (?episode=<id>, episode-drawer.tsx).
//
// The section header and episode table follow Scryer (github.com/scryer-media/scryer)
// apps/scryer-web/components/views/series-overview/season-section.tsx @ 2ef7757c, GPL-3.0: the bordered season
// block, its clickable header row, the episode table columns, the row and season checkboxes, and the season's
// monitored state counted from its episodes (all, some, none).
import { Bookmark, ChevronDown, ChevronRight, Columns3, ListOrdered, Loader2, Lock, Search, X } from "lucide-react";
import { useEffect, useState, type KeyboardEvent, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import type { SearchOutcome } from "@/api/add";
import { usePerms, useEpisodes, type Episode, type EpisodeList } from "@/api/library";
import { useMonitor, useScopedSearch, type ManageOutcome } from "@/api/manage";
import { ErrorState, Loading, Mono } from "@/components/page";
import { ProviderPills } from "@/components/provider-pills";
import { toast } from "@/components/toaster";
import { EpisodeOverview } from "@/components/title/episode-drawer";
import { InteractiveSearchButton } from "@/components/title/interactive-search";
import { MonitorSwitch } from "@/components/title/manage-bits";
import { ProgressBar } from "@/components/activity-bits";
import { UpgradeStatus } from "@/components/title/upgrade-why";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ColumnPicker } from "@/components/ui/column-picker";
import { errText } from "@/components/ui/confirm-dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { stageCounts, stageTone, stageWords } from "@/lib/copies";
import { DEFAULT_EPISODE_COLUMNS, EPISODE_COLUMNS, EPISODE_COLUMNS_PREF, type EpisodeColumnId } from "@/lib/episode-columns";
import { fmtBytes, fmtNumber } from "@/lib/format";
import { fmtDay, fmtRuntime } from "@/lib/library";
import { nextSearch } from "@/lib/library-rows";
import { useColumnPrefs } from "@/lib/prefs";
import {
  aired,
  cellStatus,
  copiesInView,
  episodeLayout,
  episodeNumber,
  matchesFilter,
  mergedStatus,
  plainAvailable,
  restrictions,
  seasonBadge,
  seasonSummary,
  statusWords,
  type Cell,
  type EpisodeFilter,
  type Layout,
  type Picked,
  type Status,
} from "@/lib/seasons";
import { cn } from "@/lib/utils";

type SeasonT = EpisodeList["seasons"][number];
type VersionT = EpisodeList["versions"][number];

/** Desktop from the md breakpoint: All libraries and tables; below it, rows for one library. */
const WIDE = "(min-width: 768px)";

function useWide(): boolean {
  const mq = () => (typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(WIDE) : null);
  const [wide, setWide] = useState(() => mq()?.matches ?? true);
  useEffect(() => {
    const m = mq();
    if (!m) return;
    const on = () => setWide(m.matches);
    on();
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return wide;
}

const TONE: Record<Status, { text: string; dot: string }> = {
  available: { text: "text-foreground", dot: "bg-[var(--ok-ink)]" },
  upgrade: { text: "text-[var(--accent-ink)]", dot: "bg-[var(--accent-ink)]" },
  missing: { text: "text-[var(--bad-ink)]", dot: "bg-[var(--bad-ink)]" },
  not_aired: { text: "text-[var(--ink-3)]", dot: "bg-[var(--line)]" },
  unmonitored: { text: "text-[var(--ink-3)]", dot: "bg-[var(--line)]" },
  none: { text: "text-[var(--ink-3)]", dot: "bg-[var(--line)]" },
};

/** Icon buttons are 36 px below the md width (wave 1: they were 24 px). */
const PHONE_ICON = "max-md:size-9";
const PHONE_ICON_WRAP = "contents max-md:[&_button]:size-9";

/** What one place (a copy's cell, or the merged column) says about an episode: the word, and the quality for managers. */
function statusOf(e: Episode, keys: string[], merged: boolean, seeFiles: boolean): { s: Status; label: string; quality?: string; cell?: Cell } {
  if (merged) {
    const s = mergedStatus(e, keys, seeFiles);
    return { s, label: s === "available" && !seeFiles ? plainAvailable(e, keys) : statusWords(s, seeFiles).label };
  }
  const c = e.versions[keys[0]];
  const s = cellStatus(c, aired(e), seeFiles);
  return { s, label: statusWords(s, seeFiles).label, quality: seeFiles && c?.has_file ? c.quality : undefined, cell: c };
}

function cellTitle(c: Cell | undefined): string | undefined {
  if (!c) return undefined;
  const parts = [c.has_file && c.state === "filled" ? "Preferred quality not met" : undefined, c.release_group, c.size_bytes ? fmtBytes(c.size_bytes) : undefined, c.score != null ? `profile score ${c.score}` : undefined, c.monitored ? "monitored" : "not monitored"];
  return parts.filter(Boolean).join(" · ") || undefined;
}

function StatusText({ st, manage, className }: { st: ReturnType<typeof statusOf>; manage: boolean; className?: string }) {
  const t = TONE[st.s];
  return (
    <span className={cn("inline-flex max-w-full items-center gap-1.5 text-[12.5px]", className)} data-status={st.s}>
      {manage && st.cell ? (
        <Bookmark className={cn("size-3 shrink-0", st.cell.monitored ? "fill-current text-[var(--accent-ink)]" : "text-[var(--ink-3)]")} aria-label={st.cell.monitored ? "Monitored" : "Not monitored"} />
      ) : null}
      <span className={cn("size-2 shrink-0 rounded-full", t.dot)} aria-hidden />
      <span className={cn("whitespace-nowrap font-medium", t.text)}>{st.label}</span>
      {st.quality ? <span className="truncate text-[11.5px] text-muted-foreground">{st.quality}</span> : null}
    </span>
  );
}

/** An episode cell's acquisition: the stage in words and, while downloading, its progress. Details and the Activity
 * link are in the episode overview. */
function CellStage({ cell, className }: { cell: Cell | undefined; className?: string }) {
  const a = cell?.acquisition;
  if (!a) return null;
  return (
    <span className={cn("mt-0.5 block max-w-[12rem] text-[11px]", className)} data-stage={a.stage} title={a.words}>
      <span className={cn("block truncate font-medium", stageTone(a.stage) === "warning" ? "text-[var(--warn-ink)]" : "text-[var(--accent-ink)]")}>{stageWords(a)}</span>
      {a.stage === "downloading" && a.progress !== undefined ? <ProgressBar pct={a.progress} tone="active" className="mt-0.5 h-1" /> : null}
    </span>
  );
}

function Check({ checked, indeterminate, onChange, label }: { checked: boolean; indeterminate?: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <input
      type="checkbox"
      aria-label={label}
      checked={checked}
      ref={(el) => {
        if (el) el.indeterminate = !!indeterminate && !checked;
      }}
      onClick={(e) => e.stopPropagation()}
      onChange={(e) => onChange(e.target.checked)}
      className="size-4 cursor-pointer accent-[var(--accent)]"
    />
  );
}

// ---- Acting on the libraries in view ----

/**
 * The libraries an action reaches: one library (its key and name) or, in All, every library of the series (keys
 * undefined: the routes then act on all of them). edit / search: whether any library in view allows it, and why not.
 */
type Scope = { keys?: string[]; name: string; edit: boolean; editWhy?: string; search: boolean; searchWhy?: string };

function scopeOf(copies: VersionT[], all: boolean): Scope {
  const editWhy = copies.find((v) => v.control && !v.control.can_edit)?.control?.edit_reason;
  const searchWhy = copies.find((v) => v.control && !v.control.can_search)?.control?.search_reason;
  return {
    keys: all ? undefined : copies.map((v) => v.key),
    name: all ? "every library" : (copies[0]?.label ?? ""),
    edit: copies.some((v) => v.control?.can_edit),
    editWhy,
    search: copies.some((v) => v.control?.can_search),
    searchWhy,
  };
}

function searched(o: SearchOutcome) {
  toast(o.started ? { tone: "ok", title: "Search started", body: o.message } : { tone: "warn", title: "Not searched", body: o.message });
}

function monitored(on: boolean) {
  return (o: ManageOutcome) => toast({ tone: "ok", title: on ? "Monitored" : "Not monitored", body: o.message });
}

function failed(title: string) {
  return (e: unknown) => toast({ tone: "bad", title, body: errText(e) });
}

/** One season's actions on the libraries in view: monitor or search the season, or some of its episodes. */
function useSeasonActions(titleId: number, scope: Scope) {
  const monitor = useMonitor(titleId);
  const search = useScopedSearch(titleId);
  return {
    busy: monitor.isPending || search.isPending,
    searching: search.isPending,
    monitor: (t: { season?: number; episode_ids?: number[] }, on: boolean) => monitor.mutate({ ...t, versions: scope.keys, monitored: on }, { onSuccess: monitored(on), onError: failed("Not changed") }),
    search: (t: { season?: number; episode_ids?: number[] }) => search.mutate({ ...t, versions: scope.keys }, { onSuccess: searched, onError: failed("Not searched") }),
  };
}

type Actions = ReturnType<typeof useSeasonActions>;

// ---- Episode lists ----

type ListProps = {
  episodes: Episode[];
  copies: VersionT[];
  merged: boolean;
  anime: boolean;
  manage: boolean;
  seeFiles: boolean;
  selected: Set<number>;
  onSelect: (ids: number[], on: boolean) => void;
  onOpen: (id: number) => void;
  /** Interactive search per episode row (a manager, and a library in view searches live); version: the one in view. */
  live?: { titleId: number; version?: string };
  /** Search and monitoring per row (managers). */
  act?: { scope: Scope; actions: Actions };
  /** The chosen columns, in order (one library in view on desktop). */
  columns: EpisodeColumnId[];
};

const epCode = (e: Episode) => `S${String(e.season).padStart(2, "0")}E${String(e.number).padStart(2, "0")}`;

function openOnKey(e: KeyboardEvent, id: number, onOpen: (id: number) => void) {
  if (e.target !== e.currentTarget) return;
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    onOpen(id);
  }
}

/** The row's Search (the libraries in view) and Interactive search icons. They never open the episode overview. */
function RowIcons({ e, live, act }: { e: Episode; live: ListProps["live"]; act: ListProps["act"] }) {
  if (!act && !live) return null;
  const what = `Search ${epCode(e)} now in ${act?.scope.name}`;
  return (
    <span className="inline-flex items-center justify-end gap-0.5" onClick={(ev) => ev.stopPropagation()} onKeyDown={(ev) => ev.stopPropagation()}>
      {act ? (
        <Button
          size="icon-xs"
          variant="ghost"
          className={PHONE_ICON}
          aria-label={what}
          title={act.scope.search ? what : act.scope.searchWhy}
          disabled={!act.scope.search || act.actions.busy}
          onClick={() => act.actions.search({ episode_ids: [e.id] })}
          data-testid="episode-search"
        >
          <Search />
        </Button>
      ) : null}
      {live ? (
        <span className={PHONE_ICON_WRAP}>
          <InteractiveSearchButton titleId={live.titleId} version={live.version} episode={{ id: e.id, code: epCode(e) }} iconOnly size="xs" variant="ghost" />
        </span>
      ) : null}
    </span>
  );
}

const rowClass = "cv-auto-row-sm cursor-pointer hover:bg-accent/60 data-[selected=true]:bg-[color-mix(in_srgb,var(--accent)_8%,transparent)]";

function SelectAllHead({ ids, selected, onSelect }: { ids: number[]; selected: Set<number>; onSelect: ListProps["onSelect"] }) {
  const n = ids.filter((i) => selected.has(i)).length;
  return (
    <TableHead className="w-9 pr-0">
      <Check checked={n === ids.length && ids.length > 0} indeterminate={n > 0} onChange={(v) => onSelect(ids, v)} label="Select every episode shown" />
    </TableHead>
  );
}

const COLUMN_HEAD: Record<EpisodeColumnId, { label: string; className?: string }> = {
  air: { label: "Air date", className: "w-28" },
  status: { label: "Status", className: "w-40" },
  quality: { label: "Quality", className: "w-32" },
  size: { label: "Size", className: "w-20 text-right" },
  cached: { label: "Cached on", className: "w-24" },
  score: { label: "Score", className: "w-16 text-right" },
  group: { label: "Release group", className: "w-28" },
  release: { label: "Release" },
  file: { label: "File" },
  runtime: { label: "Runtime", className: "w-20" },
};

/** A chosen column's cell for the library in view. Blank, never "0" or "None", for a fact the file lacks. */
function ColumnCell({ id, e, c, seeFiles }: { id: EpisodeColumnId; e: Episode; c: Cell | undefined; seeFiles: boolean }) {
  const file = c?.has_file ? c : undefined;
  const long = (v: string | undefined) => (v ? <span className="block max-w-[22rem] truncate" title={v}><Mono className="text-[11.5px]">{v}</Mono></span> : null);
  switch (id) {
    case "air":
      return <TableCell className="whitespace-nowrap text-muted-foreground">{e.air_date ? fmtDay(e.air_date) : "—"}</TableCell>;
    case "status": {
      const s = cellStatus(c, aired(e), seeFiles);
      return (
        <TableCell title={cellTitle(c)} data-col="status">
          <StatusText st={{ s, label: statusWords(s, seeFiles).label }} manage={false} />
          <CellStage cell={c} />
        </TableCell>
      );
    }
    case "quality":
      return <TableCell className="whitespace-nowrap">{file?.quality ?? ""}</TableCell>;
    case "size":
      return <TableCell className="whitespace-nowrap text-right tabular-nums">{file?.size_bytes ? fmtBytes(file.size_bytes) : ""}</TableCell>;
    case "cached":
      return <TableCell data-col="cached">{file ? <ProviderPills codes={file.cached_on} /> : null}</TableCell>;
    case "score":
      return <TableCell className="text-right tabular-nums">{file?.score != null ? String(file.score) : ""}</TableCell>;
    case "group":
      return <TableCell className="whitespace-nowrap">{file?.release_group ?? ""}</TableCell>;
    case "release":
      return <TableCell>{long(file?.release_title)}</TableCell>;
    case "file":
      return <TableCell>{long(file?.path)}</TableCell>;
    case "runtime":
      return <TableCell className="whitespace-nowrap text-muted-foreground">{fmtRuntime(e.runtime_minutes) ?? ""}</TableCell>;
  }
}

/** Desktop, one library in view (Sonarr's layout): checkbox, monitor switch, number, title, the chosen columns, then
 *  Search and Interactive search. */
function LibraryTable({ episodes, copies, anime, manage, seeFiles, selected, onSelect, onOpen, live, act, columns }: ListProps) {
  const v = copies[0];
  const ids = episodes.map((e) => e.id);
  return (
    <Table density="dense" data-testid="episode-table" data-library={v?.key}>
      <TableHeader>
        <TableRow>
          {manage ? <SelectAllHead ids={ids} selected={selected} onSelect={onSelect} /> : null}
          {act ? <TableHead className="w-10"><span className="sr-only">Monitored</span></TableHead> : null}
          <TableHead className={anime ? "w-36" : "w-14"}>#</TableHead>
          <TableHead className="min-w-40">Title</TableHead>
          {columns.map((id) => (
            <TableHead key={id} className={cn("whitespace-nowrap", COLUMN_HEAD[id].className)} data-col={id}>
              {COLUMN_HEAD[id].label}
            </TableHead>
          ))}
          {act || live ? <TableHead className="w-16"><span className="sr-only">Search</span></TableHead> : null}
        </TableRow>
      </TableHeader>
      <TableBody>
        {episodes.map((e) => {
          const c = v ? e.versions[v.key] : undefined;
          const st = statusOf(e, v ? [v.key] : [], false, seeFiles);
          const on = !!c?.monitored;
          const what = `${on ? "Stop monitoring" : "Monitor"} ${epCode(e)} in ${act?.scope.name}`;
          return (
            <TableRow
              key={e.id}
              className={rowClass}
              data-episode={`${e.season}x${e.number}`}
              data-selected={selected.has(e.id)}
              data-state={st.s}
              tabIndex={0}
              role="button"
              aria-label={`Episode ${e.number}: ${e.name || ""}`}
              onClick={() => onOpen(e.id)}
              onKeyDown={(ev) => openOnKey(ev, e.id, onOpen)}
            >
              {manage ? (
                <TableCell className="pr-0">
                  <Check checked={selected.has(e.id)} onChange={(x) => onSelect([e.id], x)} label={`Select episode ${e.number}`} />
                </TableCell>
              ) : null}
              {act ? (
                <TableCell className="pr-0">
                  {c ? (
                    <MonitorSwitch
                      size="sm"
                      on={on}
                      disabled={!act.scope.edit || act.actions.busy}
                      reason={act.scope.edit ? undefined : act.scope.editWhy}
                      label={what}
                      onChange={(m) => act.actions.monitor({ episode_ids: [e.id] }, m)}
                      testid={`episode-monitor-${e.id}`}
                    />
                  ) : null}
                </TableCell>
              ) : null}
              <TableCell className="whitespace-nowrap tabular-nums text-muted-foreground" data-testid="episode-number">
                {episodeNumber(e, anime)}
              </TableCell>
              <TableCell>
                <div className={cn("font-medium", manage && !on && "text-muted-foreground")}>{e.name || `Episode ${e.number}`}</div>
              </TableCell>
              {columns.map((id) => (
                <ColumnCell key={id} id={id} e={e} c={c} seeFiles={seeFiles} />
              ))}
              {act || live ? (
                <TableCell className="pl-0 text-right">
                  <RowIcons e={e} live={live} act={act} />
                </TableCell>
              ) : null}
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

/** Desktop, All libraries: a column per library (or one availability column), then Search and Interactive search. */
function MatrixTable({ episodes, copies, merged, anime, manage, seeFiles, selected, onSelect, onOpen, live, act }: ListProps) {
  const ids = episodes.map((e) => e.id);
  const keys = copies.map((v) => v.key);
  return (
    <Table density="dense" data-testid="episode-table">
      <TableHeader>
        <TableRow>
          {manage ? <SelectAllHead ids={ids} selected={selected} onSelect={onSelect} /> : null}
          <TableHead className={anime ? "w-36" : "w-14"}>#</TableHead>
          <TableHead>Episode</TableHead>
          <TableHead className="w-32">Air date</TableHead>
          {merged ? (
            <TableHead className="w-44">Availability</TableHead>
          ) : (
            copies.map((v) => (
              <TableHead key={v.key} className="w-48 align-bottom">
                <span data-vkey={v.key} className="inline-flex items-center gap-1.5">
                  <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
                  {v.label}
                </span>
              </TableHead>
            ))
          )}
          {act || live ? <TableHead className="w-16"><span className="sr-only">Search</span></TableHead> : null}
        </TableRow>
      </TableHeader>
      <TableBody>
        {episodes.map((e) => (
          <TableRow
            key={e.id}
            className={rowClass}
            data-episode={`${e.season}x${e.number}`}
            data-selected={selected.has(e.id)}
            tabIndex={0}
            role="button"
            aria-label={`Episode ${e.number}: ${e.name || ""}`}
            onClick={() => onOpen(e.id)}
            onKeyDown={(ev) => openOnKey(ev, e.id, onOpen)}
          >
            {manage ? (
              <TableCell className="pr-0">
                <Check checked={selected.has(e.id)} onChange={(v) => onSelect([e.id], v)} label={`Select episode ${e.number}`} />
              </TableCell>
            ) : null}
            <TableCell className="whitespace-nowrap tabular-nums text-muted-foreground" data-testid="episode-number">
              {episodeNumber(e, anime)}
            </TableCell>
            <TableCell>
              <div className={cn("font-medium", manage && !e.monitored && "text-muted-foreground")}>{e.name || `Episode ${e.number}`}</div>
            </TableCell>
            <TableCell className="whitespace-nowrap text-muted-foreground">{e.air_date ? fmtDay(e.air_date) : "—"}</TableCell>
            {merged ? (
              <TableCell>
                <StatusText st={statusOf(e, keys, true, seeFiles)} manage={false} />
              </TableCell>
            ) : (
              copies.map((v) => {
                const st = statusOf(e, [v.key], false, seeFiles);
                return (
                  <TableCell key={v.key} title={cellTitle(st.cell)} data-state={st.s}>
                    <StatusText st={st} manage={manage} />
                    <CellStage cell={st.cell} />
                  </TableCell>
                );
              })
            )}
            {act || live ? (
              <TableCell className="pl-0 text-right">
                <RowIcons e={e} live={live} act={act} />
              </TableCell>
            ) : null}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/** Phones: one row per episode for the library in view (or across copies), with its state in words. */
function EpisodeRows({ episodes, copies, merged, anime, manage, seeFiles, selected, onSelect, onOpen, live, act }: ListProps) {
  const keys = copies.map((v) => v.key);
  const ids = episodes.map((e) => e.id);
  const nSel = ids.filter((i) => selected.has(i)).length;
  return (
    <div data-testid="episode-rows">
      {manage && ids.length > 0 ? (
        <label className="flex items-center gap-2.5 border-b border-[var(--line)] px-3 py-2 text-xs text-muted-foreground">
          <Check checked={nSel === ids.length} indeterminate={nSel > 0} onChange={(v) => onSelect(ids, v)} label="Select every episode shown" />
          Select all shown
        </label>
      ) : null}
      <ul className="divide-y divide-[var(--line)]">
        {episodes.map((e) => {
          const st = statusOf(e, keys, merged, seeFiles);
          return (
            <li
              key={e.id}
              className="flex cursor-pointer items-center gap-2.5 px-3 py-2.5 hover:bg-accent/60 data-[selected=true]:bg-[color-mix(in_srgb,var(--accent)_8%,transparent)]"
              data-episode={`${e.season}x${e.number}`}
              data-selected={selected.has(e.id)}
              data-state={st.s}
              tabIndex={0}
              role="button"
              aria-label={`Episode ${e.number}: ${e.name || ""}, ${st.label}`}
              onClick={() => onOpen(e.id)}
              onKeyDown={(ev) => openOnKey(ev, e.id, onOpen)}
            >
              {manage ? <Check checked={selected.has(e.id)} onChange={(v) => onSelect([e.id], v)} label={`Select episode ${e.number}`} /> : null}
              <div className="min-w-0 flex-1">
                <div className={cn("truncate text-sm font-medium", manage && !e.monitored && "text-muted-foreground")}>{e.name || `Episode ${e.number}`}</div>
                <div className="truncate text-xs text-muted-foreground">
                  <span className="tabular-nums" data-testid="episode-number">{episodeNumber(e, anime)}</span>
                  {" · "}
                  {e.air_date ? fmtDay(e.air_date) : "No air date"}
                </div>
              </div>
              <div className="flex shrink-0 flex-col items-end gap-0.5 text-right">
                <StatusText st={{ ...st, quality: undefined }} manage={manage} />
                {st.quality ? <span className="max-w-[9rem] truncate text-[11px] text-muted-foreground">{st.quality}</span> : null}
                <CellStage cell={st.cell} className="max-w-[9rem] text-right" />
              </div>
              <RowIcons e={e} live={live} act={act} />
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function FilterChips({ value, onChange, counts, seeFiles }: { value: EpisodeFilter; onChange: (f: EpisodeFilter) => void; counts: Record<EpisodeFilter, number>; seeFiles: boolean }) {
  const opts: { id: EpisodeFilter; label: string }[] = [
    { id: "all", label: "All" },
    ...(seeFiles ? [{ id: "active" as const, label: "In progress" }] : []),
    { id: "missing", label: "Missing" },
    ...(seeFiles ? [{ id: "upgrade" as const, label: "Upgrade found" }] : []),
  ];
  return (
    <div className="flex flex-wrap gap-1.5 px-3 py-2" role="group" aria-label="Show episodes" data-testid="season-filters">
      {opts.map((o) => (
        <button
          key={o.id}
          type="button"
          aria-pressed={value === o.id}
          data-filter={o.id}
          onClick={() => onChange(o.id)}
          className="inline-flex items-center gap-1 rounded-full border border-border px-2.5 py-1 text-xs font-medium text-muted-foreground transition hover:text-foreground aria-pressed:border-[var(--th-baccent)] aria-pressed:bg-[color-mix(in_srgb,var(--accent)_12%,transparent)] aria-pressed:text-foreground"
        >
          {o.label}
          <span className="tabular-nums text-[var(--ink-3)]">{fmtNumber(counts[o.id])}</span>
        </button>
      ))}
    </div>
  );
}

const EMPTY: Record<EpisodeFilter, string> = { all: "No episodes.", active: "Nothing in progress here.", missing: "Nothing missing here.", upgrade: "No upgrade found in this season." };

// ---- Season header ----

/** Phase E's next search for a season in one library, when the server sends it (a field that may not be there yet). */
/** Phase E's next search for a season in one library, in the Libraries table's words (lib/library-rows.ts). */
function nextSearchText(c: object | undefined): string {
  const n = (c ?? {}) as { next_search_at?: unknown; next_search_why?: unknown };
  if (typeof n.next_search_at !== "string" || !n.next_search_at) return "";
  const s = nextSearch({ at: n.next_search_at, why: typeof n.next_search_why === "string" && n.next_search_why ? n.next_search_why : undefined });
  return s ? `Next search: ${s}` : "";
}

/** A library's badge in All: its colour, name and "8 / 10" in the badge's tone. */
function LibraryBadge({ v, season }: { v: VersionT; season: SeasonT }) {
  const b = seasonBadge(season.versions[v.key], season);
  return (
    <span data-vkey={v.key} data-tone={b.tone} data-testid="season-badge" className="inline-flex h-[22px] items-center gap-1.5 whitespace-nowrap rounded-[3px] px-1.5 text-xs shadow-[inset_0_0_0_1px_var(--line-2)]">
      <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
      <span className="font-medium">{v.label}</span>
      <span className={cn("tabular-nums", b.tone === "bad" ? "text-[var(--bad-ink)]" : b.tone === "ok" ? "text-[var(--ok-ink)]" : "text-muted-foreground")}>{b.text}</span>
    </span>
  );
}

/** The facts beside the badge with one library in view: missing, upgrades found, downloads by stage, size; the
 *  upgrade status with Why?; the next search when sent. */
function OneLibraryFacts({ titleId, season, v, seeFiles }: { titleId: number; season: SeasonT; v: VersionT; seeFiles: boolean }) {
  const c = season.versions[v.key];
  const b = seasonBadge(c, season);
  const facts = [
    c?.missing ? `${fmtNumber(c.missing)} missing` : "",
    seeFiles && c?.upgrade_seen ? `${fmtNumber(c.upgrade_seen)} upgrade${c.upgrade_seen === 1 ? "" : "s"} found` : "",
    seeFiles ? stageCounts(c?.stages) : "",
    c?.size_bytes ? fmtBytes(c.size_bytes) : "",
    nextSearchText(c),
  ].filter(Boolean);
  return (
    <>
      <Badge tone={b.tone === "neutral" ? "neutral" : b.tone} data-testid="season-badge" className="tabular-nums">
        {b.text}
      </Badge>
      {facts.length ? <span className="min-w-0 truncate text-xs text-muted-foreground" data-testid="season-facts">{facts.join(" · ")}</span> : null}
      {seeFiles && c && c.have > 0 && c.upgrade ? (
        <span onClick={(e) => e.stopPropagation()} className="contents">
          <UpgradeStatus u={c.upgrade} what={`Season ${season.season} · ${v.label}`} titleId={titleId} season={season.season} score={c.score} cutoff={v.cutoff} />
        </span>
      ) : null}
    </>
  );
}

function SeasonBlock({
  titleId,
  season,
  list,
  layout,
  copies,
  anime,
  open,
  onToggle,
  manage,
  live,
  seeFiles,
  selected,
  onSelect,
  onOpen,
  scope,
  columns,
  filter,
  onFilter,
}: {
  titleId: number;
  season: SeasonT;
  list: EpisodeList;
  layout: Layout;
  copies: VersionT[];
  anime: boolean;
  open: boolean;
  onToggle: () => void;
  manage: boolean;
  /** Interactive search on the header and the episode rows; version: the library in view. */
  live?: { titleId: number; version?: string };
  seeFiles: boolean;
  selected: Set<number>;
  onSelect: (ids: number[], on: boolean) => void;
  onOpen: (id: number) => void;
  scope: Scope;
  columns: EpisodeColumnId[];
  /** The season's filter (kept by Seasons, which acts only on ticked episodes the filters show). */
  filter: EpisodeFilter;
  onFilter: (f: EpisodeFilter) => void;
}) {
  const actions = useSeasonActions(titleId, scope);
  const keys = copies.map((v) => v.key);
  const all = list.episodes.filter((e) => e.season === season.season);
  const merged = layout.columns === "merged";
  const one = layout.columns === "selected" ? copies[0] : undefined;
  const sum = seasonSummary(all, copies, seeFiles, merged);
  const counts: Record<EpisodeFilter, number> = {
    all: all.length,
    active: all.filter((e) => matchesFilter(e, "active", keys, seeFiles, merged)).length,
    missing: all.filter((e) => matchesFilter(e, "missing", keys, seeFiles, merged)).length,
    upgrade: all.filter((e) => matchesFilter(e, "upgrade", keys, seeFiles, merged)).length,
  };
  const shown = all.filter((e) => matchesFilter(e, filter, keys, seeFiles, merged));
  const Chevron = open ? ChevronDown : ChevronRight;
  const name = season.season === 0 ? "Specials" : `Season ${season.season}`;
  const act = manage && copies.length > 0 ? { scope, actions } : undefined;
  // The season is on when any library in view monitors any of its episodes; the switch then stops monitoring.
  const mon = copies.map((v) => season.versions[v.key]?.monitored ?? 0);
  const on = mon.some((n) => n > 0);
  const monitorWhat = `${on ? "Stop monitoring" : "Monitor"} ${name.toLowerCase()} in ${scope.name}`;
  const searchWhat = `Search ${name.toLowerCase()} now in ${scope.name}`;
  const props: ListProps = { episodes: shown, copies, merged, anime, manage, seeFiles, selected, onSelect, onOpen, live, act, columns };
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-background/40" data-season={season.season}>
      <div className="flex cursor-pointer flex-wrap items-center gap-x-2 gap-y-1 bg-card/60 px-2 py-1.5 transition hover:bg-accent/40 md:flex-nowrap" onClick={onToggle} data-testid="season-header">
        <button type="button" onClick={(e) => { e.stopPropagation(); onToggle(); }} aria-expanded={open} aria-label={name} className={cn("grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-accent", PHONE_ICON)}>
          <Chevron className="size-4" />
        </button>
        {act ? (
          <MonitorSwitch
            size="sm"
            on={on}
            disabled={!scope.edit || actions.busy}
            reason={scope.edit ? undefined : scope.editWhy}
            label={monitorWhat}
            onChange={(m) => actions.monitor({ season: season.season }, m)}
            testid={`season-monitor-${season.season}`}
          />
        ) : null}
        <span className="shrink-0 text-sm font-semibold text-foreground">{name}</span>
        <span className="flex min-w-0 items-center gap-2 max-md:order-last max-md:basis-full max-md:flex-wrap md:flex-1">
          {one && seeFiles ? (
            <OneLibraryFacts titleId={titleId} season={season} v={one} seeFiles={seeFiles} />
          ) : (
            <>
              {seeFiles && !merged ? copies.map((v) => <LibraryBadge key={v.key} v={v} season={season} />) : null}
              <span className="min-w-0 truncate text-xs text-muted-foreground" data-testid="season-summary">
                {sum.text}
              </span>
            </>
          )}
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-0.5" onClick={(e) => e.stopPropagation()}>
          {act ? (
            <Button
              size="icon-sm"
              variant="ghost"
              className={PHONE_ICON}
              aria-label={searchWhat}
              title={scope.search ? searchWhat : scope.searchWhy}
              disabled={!scope.search || actions.busy}
              onClick={() => actions.search({ season: season.season })}
              data-testid={`season-search-${season.season}`}
            >
              {actions.searching ? <Loader2 className="animate-spin" /> : <Search />}
            </Button>
          ) : null}
          {live ? (
            // An icon: the label is hidden but stays the button's name (iconOnly would name it "… for this title").
            <span className={cn(PHONE_ICON_WRAP, "[&_button]:gap-0 [&_button]:text-[0px]")} data-testid="season-interactive">
              <InteractiveSearchButton titleId={titleId} version={live.version} season={season.season} size="sm" variant="ghost" />
            </span>
          ) : null}
        </span>
      </div>
      {open ? (
        <div className="border-t border-border bg-card/40">
          {all.length > 0 ? <FilterChips value={filter} onChange={onFilter} counts={counts} seeFiles={seeFiles} /> : null}
          {shown.length === 0 ? (
            <p className="px-4 pb-3 text-sm text-muted-foreground">{EMPTY[filter]}</p>
          ) : layout.mode === "rows" ? (
            <EpisodeRows {...props} />
          ) : layout.columns === "selected" ? (
            <LibraryTable {...props} />
          ) : (
            <MatrixTable {...props} />
          )}
        </div>
      ) : null}
    </div>
  );
}

/** The bar that acts on the selected episodes in the libraries in view: monitor, unmonitor, search, clear. */
function BulkBar({ titleId, scope, selected, onClear }: { titleId: number; scope: Scope; selected: number[]; onClear: () => void }) {
  const actions = useSeasonActions(titleId, scope);
  const where = `in ${scope.name}`;
  return (
    <div className="sticky bottom-[calc(env(safe-area-inset-bottom)+4.5rem)] z-20 md:bottom-4" data-testid="bulk-bar">
      <div className="flex flex-wrap items-center gap-2 rounded-xl border border-[var(--th-baccent)] bg-[var(--surface)] px-3 py-2 shadow-[var(--shadow-2)]">
        <span className="min-w-0 truncate text-sm font-semibold" data-testid="bulk-scope">
          {fmtNumber(selected.length)} selected · {scope.name}
        </span>
        <span className="flex-1" />
        <Button size="xs" variant="outline" disabled={actions.busy || !scope.edit} title={scope.edit ? `Monitor ${where}` : scope.editWhy} onClick={() => actions.monitor({ episode_ids: selected }, true)} data-testid="bulk-monitor">
          Monitor
        </Button>
        <Button size="xs" variant="outline" disabled={actions.busy || !scope.edit} title={scope.edit ? `Stop monitoring ${where}` : scope.editWhy} onClick={() => actions.monitor({ episode_ids: selected }, false)} data-testid="bulk-unmonitor">
          Unmonitor
        </Button>
        <Button size="xs" disabled={actions.busy || !scope.search} title={scope.search ? `Search now ${where}` : scope.searchWhy} onClick={() => actions.search({ episode_ids: selected })} data-testid="bulk-search">
          {actions.searching ? <Loader2 className="animate-spin" /> : <Search />} Search
        </Button>
        <Button size="icon-xs" variant="ghost" className={PHONE_ICON} aria-label="Clear the selection" onClick={onClear}>
          <X />
        </Button>
      </div>
    </div>
  );
}

function Note({ icon, children, testid }: { icon: ReactNode; children: ReactNode; testid?: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-[var(--line)] bg-[var(--surface)] px-3 py-2 text-xs text-[var(--ink-3)]" data-testid={testid}>
      <span className="mt-0.5 shrink-0">{icon}</span>
      <div className="min-w-0 space-y-0.5 [overflow-wrap:anywhere]">{children}</div>
    </div>
  );
}

/** The library selector: All libraries (desktop) or one library. */
function LibrarySelector({ versions, all, view, wide, onPick }: { versions: VersionT[]; all: boolean; view?: string; wide: boolean; onPick: (p: Picked) => void }) {
  const opt = (key: string, on: boolean, body: ReactNode, vkey?: string) => (
    <button
      key={key}
      type="button"
      role="radio"
      aria-checked={on}
      data-vkey={vkey}
      onClick={() => onPick(key === "*" ? "all" : key)}
      className={cn("inline-flex flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1.5 text-xs font-medium transition", on ? "bg-[var(--hover)] text-foreground" : "text-muted-foreground hover:text-foreground")}
    >
      {body}
    </button>
  );
  return (
    <div className="flex min-w-0 flex-1 items-center gap-2" data-testid="copy-selector">
      <span className="text-xs text-muted-foreground">Library</span>
      <div className="flex min-w-0 flex-1 gap-1 overflow-x-auto rounded-lg border border-border p-0.5 [scrollbar-width:none] md:flex-none" role="radiogroup" aria-label="Library">
        {wide ? opt("*", all, "All libraries") : null}
        {versions.map((v) =>
          opt(
            v.key,
            !all && view === v.key,
            <>
              <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
              {v.label}
            </>,
            v.key,
          ),
        )}
      </div>
    </div>
  );
}

export function Seasons({ id, anime, franchises = [] }: { id: string; anime: boolean; franchises?: { slug: string; name: string }[] }) {
  const list = useEpisodes(id);
  const perms = usePerms();
  const wide = useWide();
  const [params, setParams] = useSearchParams();
  const [open, setOpen] = useState<number[] | null>(null);
  // The ticked episodes belong to the library view they were ticked in: another view starts with none.
  const [ticked, setTicked] = useState<{ view: string; ids: Set<number> }>({ view: "", ids: new Set() });
  const [filters, setFilters] = useState<Record<number, EpisodeFilter>>({});
  const [picked, setPicked] = useState<Picked>(null);
  const [picker, setPicker] = useState(false);
  const [colPrefs, setColPrefs] = useColumnPrefs(EPISODE_COLUMNS_PREF, EPISODE_COLUMNS, DEFAULT_EPISODE_COLUMNS);
  const titleId = Number(id);
  const episode = Number(params.get("episode")) || null;
  const openEpisode = (eid: number | null) => {
    const p = new URLSearchParams(params);
    if (eid) p.set("episode", String(eid));
    else p.delete("episode");
    setParams(p, { replace: !eid });
  };
  if (list.isPending) return <Loading />;
  if (list.error) return <ErrorState error={list.error} />;
  const l = list.data;
  if (l.seasons.length === 0) return <Card><p className="text-sm text-muted-foreground">No episodes are known for this series yet.</p></Card>;
  // Open the latest season that has aired episodes, until the user picks.
  const latest = [...l.seasons].filter((s) => s.season > 0 && s.aired > 0).pop()?.season ?? l.seasons[0].season;
  const opened = open ?? [latest];
  const toggle = (s: number) => setOpen(opened.includes(s) ? opened.filter((x) => x !== s) : [...opened, s]);
  const manage = perms.manage;
  const seeFiles = perms.seeFiles;
  const layout = episodeLayout({ wide, copies: l.versions.length, seeFiles, picked });
  const copies = copiesInView(layout, l.versions, picked);
  const all = layout.columns !== "selected";
  const scope = scopeOf(copies, all);
  const one = all ? undefined : copies[0];
  // The view in sight (a pick, All, or the switch between desktop and phone on resize).
  const view = one?.key ?? "*";
  // Another view clears the ticks for good (React's state-from-render pattern), so a round trip does not bring them back.
  // A ticked episode a season's filter hides is dropped the same way: a bulk action and its count only ever cover
  // episodes that are ticked and shown.
  const merged = layout.columns === "merged";
  const keys = copies.map((v) => v.key);
  // A collapsed season shows none of its episodes, so it counts as hidden too.
  const shown = (e: Episode) => opened.includes(e.season) && matchesFilter(e, filters[e.season] ?? "all", keys, seeFiles, merged);
  const hidden = new Set(l.episodes.filter((e) => !shown(e)).map((e) => e.id));
  if (ticked.view !== view) setTicked({ view, ids: new Set() });
  else if ([...ticked.ids].some((id) => hidden.has(id))) setTicked({ view, ids: new Set([...ticked.ids].filter((id) => !hidden.has(id))) });
  const selected = ticked.view === view ? new Set([...ticked.ids].filter((id) => !hidden.has(id))) : new Set<number>();
  const setSelected = (ids: Set<number>) => setTicked({ view, ids });
  const select = (ids: number[], on: boolean) => {
    const next = new Set(selected);
    for (const i of ids) {
      if (on) next.add(i);
      else next.delete(i);
    }
    setSelected(next);
  };
  // Interactive search: in one library only when that library searches live (it opens there); in All, the default.
  // The episodes route lists current libraries only, so a removed one is never offered.
  const liveOn = manage && (one ? !!one.control?.live_releases : l.versions.some((v) => v.control?.live_releases));
  const live = liveOn ? { titleId, version: one?.key } : undefined;
  const columns = colPrefs.order.filter((c) => colPrefs.shown.includes(c));
  const columnsButton = layout.mode === "table" && layout.columns === "selected";
  // About the libraries in view only: one picked library's notice never speaks of another.
  const limits = manage ? restrictions(one ? [one] : l.versions, { globalSaid: true }) : [];
  const watchOrder = anime ? franchises : [];
  return (
    <div className="space-y-2.5">
      {watchOrder.length > 0 ? (
        <Link
          to="?tab=order"
          className="flex items-center gap-2.5 rounded-lg border border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_8%,transparent)] px-3 py-2.5 text-sm hover:bg-[color-mix(in_srgb,var(--accent)_14%,transparent)]"
          data-testid="watch-order-link"
        >
          <ListOrdered className="size-4 shrink-0 text-primary" />
          <span className="min-w-0 flex-1">
            <span className="font-medium">Watch order</span>
            <span className="text-muted-foreground"> · part of {watchOrder.map((f) => f.name).join(", ")}</span>
          </span>
          <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
        </Link>
      ) : null}
      {l.versions.length === 0 ? <p className="text-sm text-muted-foreground">This series has no libraries.</p> : null}
      {limits.length > 0 ? (
        <Note icon={<Lock className="size-3.5" />} testid="episode-restrictions">
          {limits.map((s) => (
            <p key={s}>{perms.admin && s.includes(" — Settings › ") ? <>{s.split(" — ")[0]} — <Link className="underline" to={s.endsWith("Download clients") ? "/admin/settings/downloads" : "/admin/settings/libraries"}>{s.split(" — ")[1]}</Link></> : s}</p>
          ))}
        </Note>
      ) : null}
      {layout.selector || columnsButton ? (
        <div className="flex items-center gap-2">
          {layout.selector ? <LibrarySelector versions={l.versions} all={all} view={one?.key} wide={wide} onPick={setPicked} /> : <span className="flex-1" />}
          {columnsButton ? (
            <Button size="xs" variant="outline" onClick={() => setPicker(true)} data-testid="episode-columns">
              <Columns3 /> Columns
            </Button>
          ) : null}
        </div>
      ) : null}
      {l.seasons.map((s) => (
        <SeasonBlock
          key={s.season}
          titleId={titleId}
          season={s}
          list={l}
          layout={layout}
          copies={copies}
          anime={anime}
          open={opened.includes(s.season)}
          onToggle={() => toggle(s.season)}
          manage={manage}
          live={live}
          seeFiles={seeFiles}
          selected={selected}
          onSelect={select}
          onOpen={openEpisode}
          scope={scope}
          columns={columns}
          filter={filters[s.season] ?? "all"}
          onFilter={(f) => setFilters({ ...filters, [s.season]: f })}
        />
      ))}
      <p className="pt-1 text-xs text-muted-foreground">
        {manage ? "Open an episode for its file, history and releases; tick episodes to act on several." : "Open an episode for its details."}
      </p>
      {manage && selected.size > 0 && copies.length > 0 ? <BulkBar titleId={titleId} scope={scope} selected={[...selected]} onClear={() => setSelected(new Set())} /> : null}
      {picker ? (
        <ColumnPicker
          label="Episode columns"
          note="The number, title, monitor switch and search buttons always show."
          columns={EPISODE_COLUMNS}
          prefs={colPrefs}
          onChange={setColPrefs}
          onClose={() => setPicker(false)}
        />
      ) : null}
      {episode ? (
        <EpisodeOverview
          titleId={titleId}
          eid={episode}
          anime={anime}
          onClose={() => openEpisode(null)}
          acquisitions={Object.fromEntries(Object.entries(l.episodes.find((e) => e.id === episode)?.versions ?? {}).map(([k, c]) => [k, c.acquisition]))}
        />
      ) : null}
    </div>
  );
}
