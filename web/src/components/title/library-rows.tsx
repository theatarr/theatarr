// The title page's Libraries table (docs/design/ui.md, Title page): one row per library the title is in, for a movie
// and a series alike, on the shared DataTable. Monitored and Library always show, then the columns
// the user chose (lib/library-rows.ts, kept per user in this browser), then the row's actions: Search now, Interactive
// search (a movie's library) and Details, which opens a panel with what has no column: the library key, path,
// hash, edition, upgrade explanation, minimum availability, the hand-offs to Wanted, the last search's results, each
// linked file with Unlink, and Remove from this library. Below 768 px each library is a stacked block. A library the
// title was removed from has no row; one that still has linked files gets one line under the table. Only for callers
// with "see files".
import { ChevronDown, Columns3, Loader2, Search, UserSearch } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useSearchNow, useSetVersion } from "@/api/add";
import { usePerms, type TitleDetail, type Version } from "@/api/library";
import { CopyDot, StageLink, useMinWidth } from "@/components/library/copy-bits";
import { KV, Mono } from "@/components/page";
import { ProviderPills } from "@/components/provider-pills";
import { AddToProviders } from "@/components/title/add-to-providers";
import { CopyActions, LeftoverLinks, RemoveDialog } from "@/components/title/copy-actions";
import { InfoHash, TorrentLink } from "@/components/title/info-hash";
import { InteractiveSearch } from "@/components/title/interactive-search";
import { ControlNote, MonitorSwitch } from "@/components/title/manage-bits";
import { MinimumAvailabilityControl } from "@/components/title/title-choices";
import { UpgradeStatus } from "@/components/title/upgrade-why";
import { releasesHref } from "@/components/title/versions";
import { VersionStatus } from "@/components/version-status";
import { Badge, Tag } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ColumnPicker } from "@/components/ui/column-picker";
import { DataTable, type DataColumn } from "@/components/ui/data-table";
import { fmtAgo, fmtBytes, fmtNumber, fmtTime } from "@/lib/format";
import { currentLibraries, DEFAULT_LIBRARY_COLUMNS, fileName, LIBRARY_COLUMNS, lastSearchAt, libraryColumns, libraryStatus, nextSearch, nextSearchOf, type LibraryColumn } from "@/lib/library-rows";
import { leftoverLinks, unlinkable } from "@/lib/ops";
import { useColumnPrefs } from "@/lib/prefs";
import { belowCount, nextLine } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { useLibraryName } from "@/lib/library-names";
import { namesList } from "@/lib/sync-groups";
import { toast } from "@/components/toaster";
import { downloadReason, linksOff, verdict } from "@/lib/version-grab";
import { mediaLine, subsList } from "@/lib/media";
import { coverageWords, latestEvent, missingLine } from "@/lib/title-facts";
import { upcomingRelease, undatedUpcoming } from "@/lib/library";

type Kind = "movie" | "series";

const PREFS = "title-libraries";

const nameOf = (v: Version) => v.label || v.key || "";

/** The library cell: colour, name (the key as tooltip), and a verdict only when it adds
 *  something ("Downloads off", "New files won't be linked"); Paused is said once by the page. */
function LibraryName({ v, wrap = false }: { v: Version; wrap?: boolean }) {
  const perms = usePerms();
  const c = v.control;
  const state = c?.grab_state;
  const off = state && verdict(state).word === "Downloads off";
  return (
    <span className={cn("flex min-w-0 items-center gap-x-2 gap-y-1", wrap ? "flex-wrap" : "whitespace-nowrap")}>
      <span className="flex min-w-0 items-center gap-1.5 font-medium text-[var(--ink)]" title={v.key} data-testid="library-name">
        <CopyDot vkey={v.key} className="size-2.5 rounded-[3px]" />
        <span className="truncate">{nameOf(v)}</span>
      </span>
      {off ? <VersionStatus state={state} why={false} settingsKey={v.key} canManage={perms.admin} /> : null}
      {!off && state && linksOff(state) ? <Badge tone="ghost" data-testid="links-off" title="This library writes no links: new files won't be linked">New files won't be linked</Badge> : null}
    </span>
  );
}

/** Why a library is missing and what comes next (a monitored library with nothing in hand that is missing a movie or
 *  aired episodes): the last targeted search or "Not searched yet", and whether Theatarr searches by itself. */
export function whyMissing(v: Version, title: TitleDetail, kind: Kind, canSearch = false): string {
  if (!v.monitored || v.acquisition || !v.control) return "";
  const missing = kind === "series" ? (v.episodes?.missing ?? 0) > 0 : !v.has_file && !upcomingRelease({ ...title, kind }) && !undatedUpcoming({ ...title, kind });
  if (!missing) return "";
  const n = nextSearchOf(v);
  const next = n && title.auto_search?.enabled ? ` Next search ${nextSearch(n)}.` : "";
  return missingLine(v, title.auto_search, undefined, canSearch && !!v.control.can_search) + next;
}

/** The Status cell: the stage linked to Activity, the upgrade badge with Why?, or the words; a second line under it,
 *  then a failed attempt (beside Upgrade found, never instead of it) and why a missing library is missing. */
function StatusCell({ v, title, kind }: { v: Version; title: TitleDetail; kind: Kind }) {
  const perms = usePerms();
  const s = libraryStatus(v, { ...title, kind });
  const titleId = v.title_id ?? 0;
  const last = latestEvent(v);
  const why = whyMissing(v, title, kind, perms.manage);
  return (
    <span className="flex flex-col items-start gap-0.5" data-testid="library-status">
      {s.stage ? (
        <StageLink a={s.stage} />
      ) : s.upgrade ? (
        <span className="inline-flex flex-wrap items-center gap-1.5">
          <Badge tone={s.tone}>{s.label}</Badge>
          <UpgradeStatus u={v.upgrade} what={nameOf(v)} releases={releasesHref(titleId, v.key ?? "")} history={v.history ?? []} />
        </span>
      ) : (
        <Badge tone={s.tone}>{s.label}</Badge>
      )}
      {s.sub ? (
        kind === "movie" && v.has_file && v.acquisition ? <StageLink a={{ ...v.acquisition, upgrade: true }} className="text-[11.5px]" /> : <span className="text-[11.5px] text-[var(--ink-3)]">{s.sub}</span>
      ) : null}
      {last?.event === "failed" && !v.acquisition ? <span className="max-w-[22rem] text-[11.5px] text-[var(--warn-ink)]" data-testid="library-failed">{last.words}</span> : null}
      {why ? <span className="max-w-[22rem] text-[11.5px] text-[var(--ink-3)]" data-testid="library-why">{why}</span> : null}
    </span>
  );
}

const blank = null;

/** A file whose score was never recorded (an adopted file Theatarr did not score). */
const NOT_SCORED = "Not scored";

/** One optional column's cell. Blank, never "0" or "None", for a fact the file does not have. */
function cellOf(id: LibraryColumn, v: Version, kind: Kind, title: TitleDetail): ReactNode {
  const c = v.current;
  const held = !!v.has_file;
  switch (id) {
    case "status":
      return <StatusCell v={v} title={title} kind={kind} />;
    case "quality":
      return held && c?.quality ? <span className="whitespace-nowrap">{c.quality}</span> : blank;
    case "score":
      // A score only with scoring evidence: the server leaves it out for a link Theatarr never scored ("—", as
      // History says it); a calculated 0 stays (internal/web linkScoreSQL).
      if (!held || !c) return blank;
      return c.score != null ? (
        <span className="tabular-nums" title={c.formats?.length ? c.formats.map((f) => `${f.name} ${f.score >= 0 ? "+" : ""}${fmtNumber(f.score)}`).join(", ") : undefined}>{fmtNumber(c.score)}</span>
      ) : <span className="text-[var(--ink-3)]" title={NOT_SCORED} data-testid="score-not-recorded">—</span>;
    case "size":
      return held && c?.size_bytes ? <span className="whitespace-nowrap tabular-nums">{fmtBytes(c.size_bytes)}</span> : blank;
    case "cached_on":
      return (
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <ProviderPills codes={held ? v.cached_on : undefined} testid="cached-on" />
          {held ? <TorrentLink hash={c?.info_hash} /> : null}
        </span>
      );
    case "file":
      return held && c?.path ? <span className="block max-w-[16rem] truncate font-mono text-[12px]" title={c.path} data-testid="library-file">{fileName(c.path)}</span> : blank;
    case "preferred": {
      const cut = v.upgrade?.cutoff;
      return cut?.quality ? <span className="whitespace-nowrap">{cut.quality}{cut.upgrades ? "" : " · upgrades off"}</span> : blank;
    }
    case "minimum_availability":
      return v.minimum_availability ? <MinimumAvailabilityControl v={v} label={nameOf(v)} /> : blank;
    case "release":
      return held && c?.release_title ? <span className="block max-w-[22rem] truncate font-mono text-[12px]" title={c.release_title}>{c.release_title}</span> : blank;
    case "release_group":
      return held && c?.release_group ? c.release_group : blank;
    case "languages":
      return held && c?.languages?.length ? c.languages.join(", ") : blank;
    case "media":
      return held && mediaLine(c?.media) ? <span title={c?.media?.subtitles ? `Subtitles: ${subsList(c.media.subtitles)}` : undefined}>{mediaLine(c?.media)}</span> : blank;
    case "formats":
      return held && c?.formats?.length ? (
        <span className="flex flex-wrap gap-1">{c.formats.map((f) => <Tag key={f.name}>{f.name} {f.score >= 0 ? "+" : ""}{fmtNumber(f.score)}</Tag>)}</span>
      ) : blank;
    case "added":
      return held && c?.added_at ? <span className="whitespace-nowrap" title={fmtTime(c.added_at)}>{fmtAgo(c.added_at)}</span> : blank;
    case "last_search": {
      const at = lastSearchAt(v);
      return at ? <span className="whitespace-nowrap" title={fmtTime(at)}>{fmtAgo(at)}</span> : blank;
    }
    case "next_search": {
      const n = nextSearchOf(v);
      return n ? <span className="whitespace-nowrap" title={fmtTime(n.at)}>{nextSearch(n)}</span> : blank;
    }
    case "path":
      return held && c?.path ? <Mono className="text-muted-foreground [overflow-wrap:anywhere]">{c.path}</Mono> : blank;
  }
}

/** What Details opens under a row: what has no column and is rarely needed. */
function RowDetails({ v, kind, minimumShown, statusShown, onRemove }: { v: Version; kind: Kind; minimumShown: boolean; statusShown: boolean; onRemove: () => void }) {
  const perms = usePerms();
  const c = v.current;
  const key = v.key ?? "";
  const titleId = v.title_id ?? 0;
  const rows: [ReactNode, ReactNode][] = [];
  // The path once: Library files lists each linked file's; a file with no link (an arr's) shows it here.
  if (c?.path && unlinkable(v.links).length === 0) rows.push(["Path", <Mono key="p" className="text-muted-foreground [overflow-wrap:anywhere]">{c.path}</Mono>]);
  if (c?.edition) rows.push(["Edition", c.edition]);
  if (kind === "series" && v.episodes) {
    const e = v.episodes;
    rows.push(["Episodes", coverageWords(e, !!v.monitored)]);
  }
  // One Upgrade line: the quality word with Why? when no Status cell offers it (a series row always, its status being
  // an episode count; a movie when the Status column is hidden), the episodes under the preferred quality, and the
  // better release found with what came of it (nextLine).
  const badge = v.has_file && (kind === "series" || !statusShown) && v.upgrade && v.upgrade.state !== "none";
  const below = belowCount(v.upgrade);
  const next = nextLine(v.upgrade, undefined, v.history ?? []);
  if (badge || below || next) {
    rows.push(["Upgrade", (
      <span key="u" className="flex flex-col items-start gap-0.5 [overflow-wrap:anywhere]" data-testid="row-upgrade">
        {badge ? <UpgradeStatus u={v.upgrade} what={nameOf(v)} releases={releasesHref(titleId, key)} history={v.history ?? []} /> : null}
        {below ? <span>{below}</span> : null}
        {next ? <span>{next}</span> : null}
      </span>
    )]);
  }
  if (v.minimum_availability && !minimumShown) rows.push(["Minimum availability", <MinimumAvailabilityControl key="m" v={v} label={nameOf(v)} />]);
  // Identifiers last: the library key (admins) and the torrent's hash, which for admins opens Torrents filtered to it
  // (InfoHash), so no separate Torrent link here.
  if (perms.admin) rows.push(["Library key", <Mono key="k">{key}</Mono>]);
  if (c?.info_hash) rows.push(["Hash", <InfoHash key="h" hash={c.info_hash} admin={perms.admin} />]);
  const active = perms.manage && !!v.control;
  // Wanted lists monitored libraries only; Cutoff unmet lists a file under the preferred quality, so not one whose
  // quality is met (score mode lists those too, but the mode is not on this page: no link then).
  const listed = active && v.monitored !== false;
  return (
    <div className="space-y-3" data-testid={`library-row-details-${key}`}>
      <KV items={rows} />
      {kind === "movie" && perms.manage && v.has_file ? <AddToProviders hash={c?.info_hash} addTo={v.add_to} /> : null}
      <ControlNote c={v.control} kind="both" globalSaid verdictSaid={!!v.control?.grab_state} />
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {listed && (v.state === "wanted" || !!v.episodes?.missing) ? <Link className="text-primary hover:underline" to={`/wanted?version=${encodeURIComponent(key)}${titleId ? `&title=${titleId}` : ""}`}>See in Wanted</Link> : null}
        {listed && v.upgrade?.quality_met !== true && (v.state === "filled" || !!v.upgrade?.below) ? <Link className="text-primary hover:underline" to={`/wanted/cutoff?version=${encodeURIComponent(key)}${titleId ? `&title=${titleId}` : ""}`}>See in Cutoff unmet</Link> : null}
        {perms.manage && titleId ? <Link className="text-primary hover:underline" to={releasesHref(titleId, key)}>Last search results</Link> : null}
      </div>
      {perms.manage ? <CopyActions v={v} label={nameOf(v)} onRemove={onRemove} movie={kind === "movie"} /> : null}
    </div>
  );
}


export function LibraryRows({ title, versions }: { title: TitleDetail; versions: Version[] }) {
  const perms = usePerms();
  const titleId = title.id ?? 0;
  const kind: Kind = title.kind === "series" ? "series" : "movie";
  const rows = currentLibraries(versions);
  const leftover = versions.filter((v) => v.removed && leftoverLinks(v.links).length > 0);
  const offered = libraryColumns(kind, rows);
  const [prefs, savePrefs] = useColumnPrefs(PREFS, LIBRARY_COLUMNS, DEFAULT_LIBRARY_COLUMNS);
  const shown = prefs.order.filter((id) => prefs.shown.includes(id) && offered.some((c) => c.id === id));
  const [picking, setPicking] = useState(false);
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const wide = useMinWidth(768);
  const setVersion = useSetVersion(titleId);
  const libName = useLibraryName();
  const searchNow = useSearchNow(titleId);
  const [interactive, setInteractive] = useState<string | null>(null);
  // The removal dialog lives here, not in the row: it stays open while the row goes.
  const [removing, setRemoving] = useState<string | null>(null);
  const toggle = (key: string) => setOpen((o) => {
    const n = new Set(o);
    if (n.has(key)) n.delete(key);
    else n.add(key);
    return n;
  });

  const monitor = (v: Version) => {
    const c = v.control;
    if (!c) return v.monitored ? <span className="text-xs text-[var(--ink-3)]">Monitored</span> : <span className="text-xs text-[var(--ink-3)]">Not monitored</span>;
    return (
      <MonitorSwitch on={!!v.monitored} disabled={!c.can_edit || setVersion.isPending} reason={c.edit_reason}
        label={`${v.monitored ? "Unmonitor" : "Monitor"} ${nameOf(v)}`}
        onChange={(m) => setVersion.mutate({ key: v.key ?? "", monitored: m }, {
          onSuccess: (r) => {
            if (r.synced?.length) toast({ tone: "ok", title: `Also added to ${namesList(r.synced.map((k) => libName(k)))} (in sync)` });
          },
        })} testid={`version-monitor-${v.key}`} />
    );
  };

  // The server's search for one library may also grab for the title's other libraries that want it.
  const searchTip = (v: Version) =>
    rows.some((o) => o.key !== v.key && o.control)
      ? `Search ${nameOf(v)} now. Other libraries that want this title may use the results too.`
      : `Search ${nameOf(v)} now`;

  const actions = (v: Version) => {
    const c = v.control;
    const key = v.key ?? "";
    const name = nameOf(v);
    const searchable = !!c?.can_search;
    const interactiveOk = perms.manage && kind === "movie" && !!c?.live_releases;
    const iconSize = wide ? "icon-sm" : "sm";
    const phone = wide ? "" : "h-9";
    return (
      <span className={cn("inline-flex items-center justify-end gap-1", !wide && "flex-wrap")}>
        {perms.manage && c ? (
          // A disabled button shows no tooltip: the wrapper carries the reason.
          <span title={searchable ? searchTip(v) : downloadReason(c)} className="inline-flex">
            <Button size={iconSize} variant="ghost" className={phone} disabled={!searchable || searchNow.isPending}
              aria-label={`Search ${name} now`} onClick={() => searchNow.mutate([key])} data-testid={`version-search-${key}`}>
              {searchNow.isPending && searchNow.variables?.[0] === key ? <Loader2 className="animate-spin" /> : <Search />}
              {wide ? null : "Search now"}
            </Button>
          </span>
        ) : null}
        {interactiveOk ? (
          <Button size={iconSize} variant="ghost" className={phone} aria-label={`Interactive search in ${name}`} title="Search now and choose the release yourself"
            onClick={() => setInteractive(key)} data-testid={`version-interactive-${key}`}>
            <UserSearch />
            {wide ? null : "Interactive search"}
          </Button>
        ) : null}
        <Button size={iconSize} variant="ghost" className={phone} aria-expanded={open.has(key)} aria-label={`Details of ${name}`} title="Details"
          onClick={() => toggle(key)} data-testid={`copy-details-${key}`}>
          <ChevronDown className={cn("transition-transform", !open.has(key) && "-rotate-90")} />
          {wide ? null : "Details"}
        </Button>
      </span>
    );
  };

  const columns: DataColumn<Version>[] = [
    { id: "monitored", label: "Monitored", cell: monitor, headClassName: "w-px whitespace-nowrap", card: "none" },
    { id: "library", label: "Library", cell: (v) => <LibraryName v={v} />, card: "none" },
    ...shown.map((id): DataColumn<Version> => ({
      id,
      label: offered.find((c) => c.id === id)?.label ?? id,
      cell: (v) => cellOf(id, v, kind, title),
      headClassName: "whitespace-nowrap",
      cellClassName: id === "file" ? "max-w-[16rem]" : id === "release" ? "max-w-[22rem]" : undefined,
      card: "none",
    })),
  ];

  // The phone block: switch, name, tag and status; quality · score · size · providers (when any); the file name.
  const cardHead = (v: Version) => {
    const c = v.current;
    const facts = [
      v.has_file ? c?.quality : "",
      v.has_file && c?.score != null ? `score ${fmtNumber(c.score)}` : "",
      v.has_file && c?.size_bytes ? fmtBytes(c.size_bytes) : "",
    ].filter(Boolean).join(" · ");
    return (
      <div className="space-y-1.5">
        {/* The status moves under the name when both do not fit, so the name is never cut to a letter. */}
        <div className="flex flex-wrap items-start gap-2">
          {monitor(v)}
          <div className="min-w-[8rem] flex-1"><LibraryName v={v} wrap /></div>
          <div className="max-w-full"><StatusCell v={v} title={title} kind={kind} /></div>
        </div>
        {v.has_file ? (
          <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--ink-2)]">
            {facts ? <span className="tabular-nums">{facts}</span> : null}
            {v.cached_on?.length ? <ProviderPills codes={v.cached_on} testid="cached-on" /> : null}
            <TorrentLink hash={c?.info_hash} />
          </div>
        ) : null}
        {v.has_file && c?.path ? <div className="truncate font-mono text-[12px] text-muted-foreground" title={c.path} data-testid="library-file">{fileName(c.path)}</div> : null}
      </div>
    );
  };

  const outcome = searchNow.data;
  return (
    <section className="space-y-2" data-testid="library-rows">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-lg font-semibold text-[var(--ink)]">Libraries</h2>
        {wide && rows.length ? (
          <Button size="sm" variant="ghost" onClick={() => setPicking(true)} data-testid="library-columns">
            <Columns3 /> Columns
          </Button>
        ) : null}
      </div>
      {rows.length === 0 ? (
        <Card><p className="text-sm text-muted-foreground">This title is in no library yet.</p></Card>
      ) : (
        <DataTable
          rows={rows}
          rowKey={(v) => v.key ?? String(v.id)}
          columns={columns}
          actions={actions}
          expanded={(v) => (open.has(v.key ?? "") ? (
            <RowDetails v={v} kind={kind} minimumShown={wide && shown.includes("minimum_availability")} statusShown={!wide || shown.includes("status")}
              onRemove={() => setRemoving(v.key ?? "")} />
          ) : null)}
          cardHead={cardHead}
          rowProps={(v) => ({ "data-testid": "library-row", "data-version": v.key })}
          minWidth={768}
        />
      )}
      {outcome ? <p className="text-xs text-[var(--accent-ink)]" data-testid="library-search-outcome">{outcome.message}</p> : null}
      {searchNow.error ? <p className="text-xs text-[var(--bad-ink)]" role="alert">Search failed: {(searchNow.error as Error).message}</p> : null}
      {setVersion.error ? <p className="text-xs text-[var(--bad-ink)]" role="alert">{(setVersion.error as Error).message}</p> : null}
      {leftover.length ? (
        <p className="text-xs text-[var(--ink-2)]" data-testid="library-leftover">
          Still linked from a library this title was removed from:{" "}
          {leftover.map((v, i) => (
            <span key={v.key}>
              {i > 0 ? "; " : ""}
              {nameOf(v)} ({leftoverLinks(v.links).length === 1 ? "1 file" : `${leftoverLinks(v.links).length} files`}).{" "}
              {perms.manage && v.key ? <LeftoverLinks titleId={titleId} versions={[v.key]} /> : null}
            </span>
          ))}
        </p>
      ) : null}
      {picking ? (
        <ColumnPicker label="Columns" note="Monitored, Library and the actions always stay. Kept for your account in this browser."
          columns={offered} prefs={prefs} onChange={savePrefs} onClose={() => setPicking(false)} />
      ) : null}
      {removing ? <RemoveDialog titleId={titleId} versions={[removing]} kind={kind} anime={title.anime} onClose={() => setRemoving(null)} /> : null}
      {interactive ? <InteractiveSearch titleId={titleId} version={interactive} onClose={() => setInteractive(null)} /> : null}
    </section>
  );
}
