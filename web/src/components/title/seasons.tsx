// The title page's Episodes tab: each season as a collapsible section and, inside it, its episodes
// (GET /titles/{id}/episodes). Managed the way Sonarr does it (docs/design/manage.md), laid out phone first
// (docs/design/ui.md, Title page / Episodes; the logic is lib/seasons.ts):
//
// - a season header with a compact completion summary ("8 of 10 available · 2 missing · 1 upgrade found"); per library
//   counts, size, the upgrade status with Why?, the monitor switch and "Search season" sit behind its Details;
// - read-only monitoring and disabled search said once above the seasons, not on every line;
// - All / Missing / Upgrade found filters per season, and a selection of episodes for bulk monitor, unmonitor and
//   search whose toolbar shows only while something is selected;
// - on desktop a table with a column per library (the two-copy matrix); on phones rows for one copy at a time with a
//   copy selector; every state in words (Available, Missing, Not aired, Upgrade found), never a bare icon;
// - accounts without "see files" get one availability column across copies and nothing about files or quality
//   beyond the resolution (the API sends nothing more);
// - episode rows open the episode overview (?episode=<id>, episode-drawer.tsx);
// - for managers, when a current library of the series searches live: Interactive search (interactive-search.tsx) on each
//   season header (season packs and that season's episodes) and an icon button at the end of each episode row, in the
//   default library; the window's tabs switch library.
//
// The section header and episode table follow Scryer (github.com/scryer-media/scryer)
// apps/scryer-web/components/views/series-overview/season-section.tsx @ 2ef7757c, GPL-3.0: the bordered season
// block, its clickable header row, the episode table columns, the row and season checkboxes, and the season's
// monitored state counted from its episodes (all, some, none).
import { Bookmark, ChevronDown, ChevronRight, ListOrdered, Loader2, Lock, Search, SlidersHorizontal, X } from "lucide-react";
import { useEffect, useState, type KeyboardEvent, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import type { SearchOutcome } from "@/api/add";
import { usePerms, useEpisodes, type Episode, type EpisodeList } from "@/api/library";
import { useMonitor, useScopedSearch, type ManageOutcome } from "@/api/manage";
import { ErrorState, Loading } from "@/components/page";
import { EpisodeOverview } from "@/components/title/episode-drawer";
import { InteractiveSearchButton, useLiveLibraries } from "@/components/title/interactive-search";
import { CopyDot, CopyTrack } from "@/components/library/copy-bits";
import { MonitorSwitch } from "@/components/title/manage-bits";
import { DownloadsOffNotice } from "@/components/version-status";
import { ProgressBar } from "@/components/activity-bits";
import { UpgradeStatus } from "@/components/title/upgrade-why";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtBytes, fmtNumber } from "@/lib/format";
import { seasonBar, stageCounts, stageTone, stageWords } from "@/lib/copies";
import { fmtDay } from "@/lib/library";
import { globalDownloadsOff } from "@/lib/version-grab";
import {
  aired,
  cellStatus,
  copiesInView,
  episodeLayout,
  episodeNumber,
  heldOf,
  matchesFilter,
  mergedStatus,
  plainAvailable,
  restrictions,
  seasonSummary,
  statusWords,
  type Cell,
  type EpisodeFilter,
  type Layout,
  type Status,
} from "@/lib/seasons";
import { cn } from "@/lib/utils";

type SeasonT = EpisodeList["seasons"][number];
type VersionT = EpisodeList["versions"][number];

/** Desktop from the md breakpoint: the copy matrix; below it, rows with a copy selector. */
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

/** Each library's bar for a season, under its header: held of aired, in progress striped, and the downloads by stage. */
function SeasonCopies({ season, copies, seeFiles }: { season: SeasonT; copies: VersionT[]; seeFiles: boolean }) {
  if (copies.length === 0 || season.season === 0) return null;
  return (
    <div className="grid gap-x-4 gap-y-1.5 px-4 pb-2.5 sm:grid-cols-2" data-testid="season-copies">
      {copies.map((v) => {
        const c = season.versions[v.key];
        const bar = seasonBar(c, season);
        const stages = seeFiles ? stageCounts(c?.stages) : "";
        return (
          <div key={v.key} className="min-w-0" data-season-copy={v.key}>
            <div className="flex min-w-0 items-center gap-1.5 text-[11.5px]">
              <CopyDot vkey={v.key} />
              <span className="truncate font-medium">{v.label}</span>
              <span className="ml-auto shrink-0 tabular-nums text-muted-foreground">{bar.have} / {bar.total}</span>
            </div>
            <CopyTrack vkey={v.key} bar={bar} className="mt-1 flex" />
            {stages ? <div className="mt-0.5 truncate text-[11px] text-[var(--accent-ink)]" data-testid="season-stages">{stages}</div> : null}
          </div>
        );
      })}
    </div>
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
  /** Interactive search per episode row (a manager, and a current library of the series searches live). */
  live?: { titleId: number };
};

const epCode = (e: Episode) => `S${String(e.season).padStart(2, "0")}E${String(e.number).padStart(2, "0")}`;

function openOnKey(e: KeyboardEvent, id: number, onOpen: (id: number) => void) {
  if (e.target !== e.currentTarget) return;
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    onOpen(id);
  }
}

/** Desktop: a table with a column per library (or one availability column). */
function EpisodeTable({ episodes, copies, merged, anime, manage, seeFiles, selected, onSelect, onOpen, live }: ListProps) {
  const ids = episodes.map((e) => e.id);
  const nSel = ids.filter((i) => selected.has(i)).length;
  const keys = copies.map((v) => v.key);
  return (
    <Table density="dense" data-testid="episode-table">
      <TableHeader>
        <TableRow>
          {manage ? (
            <TableHead className="w-9 pr-0">
              <Check checked={nSel === ids.length && ids.length > 0} indeterminate={nSel > 0} onChange={(v) => onSelect(ids, v)} label="Select every episode shown" />
            </TableHead>
          ) : null}
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
          {live ? <TableHead className="w-10"><span className="sr-only">Interactive search</span></TableHead> : null}
        </TableRow>
      </TableHeader>
      <TableBody>
        {episodes.map((e) => (
          <TableRow
            key={e.id}
            className="cv-auto-row-sm cursor-pointer hover:bg-accent/60 data-[selected=true]:bg-[color-mix(in_srgb,var(--accent)_8%,transparent)]"
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
            {live ? (
              <TableCell className="pl-0 text-right">
                <InteractiveSearchButton titleId={live.titleId} episode={{ id: e.id, code: epCode(e) }} iconOnly size="xs" variant="ghost" />
              </TableCell>
            ) : null}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/** Phones: one row per episode for the copy in view (or across copies), with its state in words. */
function EpisodeRows({ episodes, copies, merged, anime, manage, seeFiles, selected, onSelect, onOpen, live }: ListProps) {
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
                <StatusText st={{ ...st, quality: undefined }} manage={false} />
                {st.quality ? <span className="max-w-[9rem] truncate text-[11px] text-muted-foreground">{st.quality}</span> : null}
                <CellStage cell={st.cell} className="max-w-[9rem] text-right" />
              </div>
              {live ? <InteractiveSearchButton titleId={live.titleId} episode={{ id: e.id, code: epCode(e) }} iconOnly size="xs" variant="ghost" /> : null}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** One copy's line in a season's Details: counts, size, the upgrade status with Why?, monitor switch and search season. */
function SeasonVersion({ titleId, season, v, manage, seeFiles }: { titleId: number; season: SeasonT; v: VersionT; manage: boolean; seeFiles: boolean }) {
  const c = season.versions[v.key] ?? { have: 0, missing: 0, total: 0, below_cutoff: 0, upgrade_seen: 0, monitored: 0 };
  const monitor = useMonitor(titleId);
  const search = useScopedSearch(titleId);
  const [msg, setMsg] = useState<string>("");
  const ctl = v.control;
  const all = c.monitored >= season.episodes && season.episodes > 0;
  const some = c.monitored > 0 && !all;
  const facts = [
    heldOf(c, season),
    c.missing ? `${fmtNumber(c.missing)} missing` : "",
    seeFiles && c.below_cutoff ? `${fmtNumber(c.below_cutoff)} under preferred quality` : "",
    seeFiles && c.upgrade_seen ? `${fmtNumber(c.upgrade_seen)} upgrade${c.upgrade_seen === 1 ? "" : "s"} found` : "",
    c.size_bytes ? fmtBytes(c.size_bytes) : "",
  ].filter(Boolean);
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-2 text-xs" data-season-version={v.key}>
      <span data-vkey={v.key} className="inline-flex items-center gap-1.5 rounded-md border border-border bg-muted px-2 py-0.5 font-medium">
        <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
        {v.label}
      </span>
      <span className="text-muted-foreground" data-testid="season-facts">{facts.join(" · ")}</span>
      {seeFiles && c.have > 0 && c.upgrade ? (
        <UpgradeStatus u={c.upgrade} what={`Season ${season.season} · ${v.label}`} titleId={titleId} season={season.season} score={c.score} cutoff={v.cutoff} />
      ) : null}
      <span className="flex-1" />
      {manage && ctl ? (
        <>
          <label className="flex items-center gap-1.5" title={ctl.can_edit ? undefined : ctl.edit_reason}>
            <MonitorSwitch
              size="sm"
              on={all || some}
              disabled={!ctl.can_edit || monitor.isPending}
              reason={ctl.edit_reason}
              label={`${all ? "Unmonitor" : "Monitor"} season ${season.season} in ${v.key}`}
              onChange={(m) => monitor.mutate({ season: season.season, versions: [v.key], monitored: m }, { onSuccess: (o: ManageOutcome) => setMsg(o.message), onError: (e) => setMsg((e as Error).message) })}
              testid={`season-monitor-${season.season}-${v.key}`}
            />
            <span className="text-muted-foreground">{some ? `${c.monitored}/${season.episodes} monitored` : all ? "Monitored" : "Not monitored"}</span>
          </label>
          <Button
            size="xs"
            variant="outline"
            disabled={!ctl.can_search || search.isPending}
            title={ctl.can_search ? `Search season ${season.season} for ${v.key}` : ctl.search_reason}
            onClick={() => search.mutate({ season: season.season, versions: [v.key] }, { onSuccess: (o: SearchOutcome) => setMsg(o.message), onError: (e) => setMsg((e as Error).message) })}
            data-testid={`season-search-${season.season}-${v.key}`}
          >
            {search.isPending ? <Loader2 className="animate-spin" /> : <Search />} Search season
          </Button>
        </>
      ) : null}
      {msg ? <p className="w-full text-[11px] text-[var(--accent-ink)]">{msg}</p> : null}
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
  /** Interactive search on the header and the episode rows. */
  live: boolean;
  seeFiles: boolean;
  selected: Set<number>;
  onSelect: (ids: number[], on: boolean) => void;
  onOpen: (id: number) => void;
}) {
  const [filter, setFilter] = useState<EpisodeFilter>("all");
  const [details, setDetails] = useState(false);
  const keys = copies.map((v) => v.key);
  const all = list.episodes.filter((e) => e.season === season.season);
  const merged = layout.columns === "merged";
  const sum = seasonSummary(all, copies, seeFiles, merged);
  const counts: Record<EpisodeFilter, number> = {
    all: all.length,
    active: all.filter((e) => matchesFilter(e, "active", keys, seeFiles, merged)).length,
    missing: all.filter((e) => matchesFilter(e, "missing", keys, seeFiles, merged)).length,
    upgrade: all.filter((e) => matchesFilter(e, "upgrade", keys, seeFiles, merged)).length,
  };
  const shown = all.filter((e) => matchesFilter(e, filter, keys, seeFiles, merged));
  const Chevron = open ? ChevronDown : ChevronRight;
  const props: ListProps = { episodes: shown, copies, merged, anime, manage, seeFiles, selected, onSelect, onOpen, live: live ? { titleId } : undefined };
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-background/40" data-season={season.season}>
      <div className="flex items-stretch bg-card/60">
        <button type="button" onClick={onToggle} aria-expanded={open} className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 px-4 py-2.5 text-left transition hover:bg-accent/80">
          <Chevron className="size-4 shrink-0 text-muted-foreground" />
          <span className="min-w-0">
            <span className="block text-sm font-semibold text-foreground">{season.season === 0 ? "Specials" : `Season ${season.season}`}</span>
            <span className="block text-xs text-muted-foreground" data-testid="season-summary">
              {sum.text}
            </span>
          </span>
        </button>
        {live ? (
          // The label is hidden on phones, as Details' is (the button keeps its name for screen readers).
          <span className="flex shrink-0 items-center px-1 max-sm:[&_button]:gap-0 max-sm:[&_button]:text-[0px]" data-testid="season-interactive">
            <InteractiveSearchButton titleId={titleId} season={season.season} size="xs" variant="ghost" />
          </span>
        ) : null}
        {seeFiles && list.versions.length > 0 ? (
          <button
            type="button"
            onClick={() => setDetails(!details)}
            aria-expanded={details}
            aria-label={`Season ${season.season} details`}
            className="flex shrink-0 items-center gap-1 px-3 text-xs font-medium text-muted-foreground transition hover:bg-accent/80 hover:text-foreground aria-expanded:text-foreground"
            data-testid="season-details-toggle"
          >
            <SlidersHorizontal className="size-3.5" />
            <span className="max-sm:sr-only">Details</span>
          </button>
        ) : null}
      </div>
      {/* Per-library bars are file detail: accounts without See files get the combined summary only. */}
      {seeFiles ? <SeasonCopies season={season} copies={list.versions} seeFiles={seeFiles} /> : null}
      {details ? (
        <div className="divide-y divide-[var(--line)] border-t border-border bg-card/30" data-testid="season-details">
          {list.versions.map((v) => (
            <SeasonVersion key={v.key} titleId={titleId} season={season} v={v} manage={manage} seeFiles={seeFiles} />
          ))}
        </div>
      ) : null}
      {open ? (
        <div className="border-t border-border bg-card/40">
          {all.length > 0 ? <FilterChips value={filter} onChange={setFilter} counts={counts} seeFiles={seeFiles} /> : null}
          {shown.length === 0 ? (
            <p className="px-4 pb-3 text-sm text-muted-foreground">{EMPTY[filter]}</p>
          ) : layout.mode === "table" ? (
            <EpisodeTable {...props} />
          ) : (
            <EpisodeRows {...props} />
          )}
        </div>
      ) : null}
    </div>
  );
}

/** The bar that acts on the selected episodes: monitor, unmonitor, search, clear. */
function BulkBar({ titleId, list, selected, onClear }: { titleId: number; list: EpisodeList; selected: number[]; onClear: () => void }) {
  const monitor = useMonitor(titleId);
  const search = useScopedSearch(titleId);
  const [msg, setMsg] = useState("");
  const editable = list.versions.filter((v) => v.control?.can_edit).map((v) => v.key);
  const searchable = list.versions.filter((v) => v.control?.can_search).map((v) => v.key);
  const editWhy = list.versions.find((v) => v.control && !v.control.can_edit)?.control?.edit_reason;
  const searchWhy = list.versions.find((v) => v.control && !v.control.can_search)?.control?.search_reason;
  const busy = monitor.isPending || search.isPending;
  const done = (o: ManageOutcome | SearchOutcome) => setMsg(o.message);
  const fail = (e: unknown) => setMsg((e as Error).message);
  return (
    <div className="sticky bottom-[calc(env(safe-area-inset-bottom)+4.5rem)] z-20 md:bottom-4" data-testid="bulk-bar">
      <div className="flex flex-wrap items-center gap-2 rounded-xl border border-[var(--th-baccent)] bg-[var(--surface)] px-3 py-2 shadow-[var(--shadow-2)]">
        <span className="text-sm font-semibold">{fmtNumber(selected.length)} selected</span>
        <span className="flex-1" />
        <Button size="xs" variant="outline" disabled={busy || editable.length === 0} title={editable.length ? undefined : editWhy} onClick={() => monitor.mutate({ episode_ids: selected, versions: editable, monitored: true }, { onSuccess: done, onError: fail })} data-testid="bulk-monitor">
          Monitor
        </Button>
        <Button size="xs" variant="outline" disabled={busy || editable.length === 0} title={editable.length ? undefined : editWhy} onClick={() => monitor.mutate({ episode_ids: selected, versions: editable, monitored: false }, { onSuccess: done, onError: fail })} data-testid="bulk-unmonitor">
          Unmonitor
        </Button>
        <Button size="xs" disabled={busy || searchable.length === 0} title={searchable.length ? undefined : searchWhy} onClick={() => search.mutate({ episode_ids: selected, versions: searchable }, { onSuccess: done, onError: fail })} data-testid="bulk-search">
          {search.isPending ? <Loader2 className="animate-spin" /> : <Search />} Search
        </Button>
        <Button size="icon-xs" variant="ghost" aria-label="Clear the selection" onClick={onClear}>
          <X />
        </Button>
        {msg ? <p className="w-full text-[11px] text-[var(--accent-ink)]">{msg}</p> : null}
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

export function Seasons({ id, anime, franchises = [] }: { id: string; anime: boolean; franchises?: { slug: string; name: string }[] }) {
  const list = useEpisodes(id);
  const perms = usePerms();
  const wide = useWide();
  const [params, setParams] = useSearchParams();
  const [open, setOpen] = useState<number[] | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [copy, setCopy] = useState<string | null>(null);
  const titleId = Number(id);
  const live = useLiveLibraries(titleId, perms.manage).libs.length > 0;
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
  const select = (ids: number[], on: boolean) => {
    const next = new Set(selected);
    for (const i of ids) {
      if (on) next.add(i);
      else next.delete(i);
    }
    setSelected(next);
  };
  const manage = perms.manage;
  const seeFiles = perms.seeFiles;
  const layout = episodeLayout({ wide, copies: l.versions.length, seeFiles });
  const copies = copiesInView(layout, l.versions, copy);
  const off = globalDownloadsOff(l.versions.map((v) => v.control?.grab_state));
  const limits = manage ? restrictions(l.versions, { globalSaid: true }) : [];
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
      <DownloadsOffNotice off={off} canManage={perms.admin} />
      {limits.length > 0 ? (
        <Note icon={<Lock className="size-3.5" />} testid="episode-restrictions">
          {limits.map((s) => (
            <p key={s}>{perms.admin && s.includes(" — Settings › ") ? <>{s.split(" — ")[0]} — <Link className="underline" to={s.endsWith("Download clients") ? "/admin/settings/downloads" : "/admin/settings/libraries"}>{s.split(" — ")[1]}</Link></> : s}</p>
          ))}
        </Note>
      ) : null}
      {layout.selector ? (
        <div className="flex items-center gap-2" data-testid="copy-selector">
          <span className="text-xs text-muted-foreground">Library</span>
          <div className="flex min-w-0 flex-1 gap-1 overflow-x-auto rounded-lg border border-border p-0.5 [scrollbar-width:none]" role="radiogroup" aria-label="Library">
            {l.versions.map((v) => {
              const on = copies[0]?.key === v.key;
              return (
                <button
                  key={v.key}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  data-vkey={v.key}
                  onClick={() => setCopy(v.key)}
                  className={cn("inline-flex flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1.5 text-xs font-medium transition", on ? "bg-[var(--hover)] text-foreground" : "text-muted-foreground hover:text-foreground")}
                >
                  <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
                  {v.label}
                </button>
              );
            })}
          </div>
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
          live={manage && live}
          seeFiles={seeFiles}
          selected={selected}
          onSelect={select}
          onOpen={openEpisode}
        />
      ))}
      <p className="pt-1 text-xs text-muted-foreground">
        {manage ? "Open an episode for its file, history and releases; tick episodes to act on several." : "Open an episode for its details."}
      </p>
      {manage && selected.size > 0 ? <BulkBar titleId={titleId} list={l} selected={[...selected]} onClear={() => setSelected(new Set())} /> : null}
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
