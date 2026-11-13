// The title page's Libraries table (docs/design/ui.md, Title page): one row per library the title is in (Live or Watch
// only), for a movie and a series alike, on the shared DataTable. Monitored and Library always show, then the columns
// the user chose (lib/library-rows.ts, kept per user in this browser), then the row's actions: Search now, Interactive
// search (a movie's Live library) and Details, which opens a panel with what has no column: the library key, path,
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
import { currentLibraries, DEFAULT_LIBRARY_COLUMNS, fileName, LIBRARY_COLUMNS, lastSearch, libraryColumns, libraryStatus, nextSearch, nextSearchOf, type LibraryColumn } from "@/lib/library-rows";
import { unlinkable } from "@/lib/ops";
import { useColumnPrefs } from "@/lib/prefs";
import { belowCount, nextLine } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { downloadReason, linksOff, MODE_WORDS, verdict } from "@/lib/version-grab";
import { mediaLine } from "@/lib/media";

type Kind = "movie" | "series";

const PREFS = "title-libraries";

const nameOf = (v: Version) => v.label || v.key || "";

/** The library cell: colour, name (the key as tooltip), the Live / Watch only tag, and a verdict only when it adds
 *  something ("Downloads off", "New files won't be linked"); Paused is said once by the page. */
function LibraryName({ v, wrap = false }: { v: Version; wrap?: boolean }) {
  const perms = usePerms();
  const c = v.control;
  const state = c?.grab_state;
  const mode = MODE_WORDS[state?.mode ?? c?.mode ?? ""];
  const off = state && verdict(state).word === "Downloads off";
  return (
    <span className={cn("flex min-w-0 items-center gap-x-2 gap-y-1", wrap ? "flex-wrap" : "whitespace-nowrap")}>
      <span className="flex min-w-0 items-center gap-1.5 font-medium text-[var(--ink)]" title={v.key} data-testid="library-name">
        <CopyDot vkey={v.key} className="size-2.5 rounded-[3px]" />
        <span className="truncate">{nameOf(v)}</span>
      </span>
      {mode ? <Tag title={mode.help} data-mode={state?.mode ?? c?.mode}>{mode.label}</Tag> : null}
      {off ? <VersionStatus state={state} why={false} settingsKey={v.key} canManage={perms.admin} /> : null}
      {!off && state && linksOff(state) ? <Badge tone="ghost" data-testid="links-off" title="This library writes no links: new files won't be linked">New files won't be linked</Badge> : null}
    </span>
  );
}

/** The Status cell: the stage linked to Activity, the upgrade badge with Why?, or the words; a second line under it. */
function StatusCell({ v, title, kind }: { v: Version; title: TitleDetail; kind: Kind }) {
  const s = libraryStatus(v, { ...title, kind });
  const titleId = v.title_id ?? 0;
  return (
    <span className="flex flex-col items-start gap-0.5" data-testid="library-status">
      {s.stage ? (
        <StageLink a={s.stage} />
      ) : s.upgrade ? (
        <UpgradeStatus u={v.upgrade} what={nameOf(v)} titleId={titleId} releases={releasesHref(titleId, v.key ?? "")} ceilingControl={kind === "movie"} />
      ) : (
        <Badge tone={s.tone}>{s.label}</Badge>
      )}
      {s.sub ? (
        kind === "movie" && v.has_file && v.acquisition ? <StageLink a={{ ...v.acquisition, upgrade: true }} className="text-[11.5px]" /> : <span className="text-[11.5px] text-[var(--ink-3)]">{s.sub}</span>
      ) : null}
    </span>
  );
}

const blank = null;

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
      // A score without its formats (files kept from a former Sonarr/Radarr, adopted links) stays blank, as Formats does.
      return held && c?.score != null && c.formats?.length ? (
        <span className="tabular-nums" title={c.formats.map((f) => `${f.name} ${f.score >= 0 ? "+" : ""}${fmtNumber(f.score)}`).join(", ")}>{fmtNumber(c.score)}</span>
      ) : blank;
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
      return held ? mediaLine(c?.media) || blank : blank;
    case "formats":
      return held && c?.formats?.length ? (
        <span className="flex flex-wrap gap-1">{c.formats.map((f) => <Tag key={f.name}>{f.name} {f.score >= 0 ? "+" : ""}{fmtNumber(f.score)}</Tag>)}</span>
      ) : blank;
    case "added":
      return held && c?.added_at ? <span className="whitespace-nowrap" title={fmtTime(c.added_at)}>{fmtAgo(c.added_at)}</span> : blank;
    case "last_search": {
      const at = lastSearch(v);
      return at ? <span className="whitespace-nowrap" title={fmtTime(at)}>{fmtAgo(at)}</span> : blank;
    }
    case "next_search": {
      const n = nextSearchOf(v);
      return n ? <span className="whitespace-nowrap" title={fmtTime(n.at)}>{nextSearch(n)}</span> : blank;
    }
    case "path":
      return held && c?.path ? <Mono className="break-all text-muted-foreground">{c.path}</Mono> : blank;
  }
}

/** What Details opens under a row: what has no column and is rarely needed. */
function RowDetails({ v, kind, minimumShown, statusShown, onRemove }: { v: Version; kind: Kind; minimumShown: boolean; statusShown: boolean; onRemove: () => void }) {
  const perms = usePerms();
  const c = v.current;
  const key = v.key ?? "";
  const titleId = v.title_id ?? 0;
  const rows: [ReactNode, ReactNode][] = [["Library key", <Mono key="k">{key}</Mono>]];
  if (c?.path) rows.push(["Path", <Mono key="p" className="break-all text-muted-foreground">{c.path}</Mono>]);
  if (c?.info_hash) rows.push(["Hash", <span key="h" className="flex flex-wrap items-baseline gap-x-2"><InfoHash hash={c.info_hash} admin={perms.admin} /><TorrentLink hash={c.info_hash} /></span>]);
  if (c?.edition) rows.push(["Edition", c.edition]);
  if (kind === "series" && v.episodes) {
    const e = v.episodes;
    rows.push(["Episodes", `${fmtNumber(e.have)} of ${fmtNumber(e.total)}${e.missing ? ` · ${fmtNumber(e.missing)} aired and missing` : ""}`]);
  }
  // The upgrade explanation (Why?, and the source ceiling for a movie) when no Status cell offers it: a series row
  // always (its status is an episode count), a movie when the Status column is hidden.
  if (v.has_file && (kind === "series" || !statusShown) && v.upgrade && v.upgrade.state !== "none") {
    rows.push(["Upgrade status", <UpgradeStatus key="u" u={v.upgrade} what={nameOf(v)} titleId={titleId} releases={releasesHref(titleId, key)} ceilingControl={kind === "movie"} />]);
  }
  const below = belowCount(v.upgrade);
  if (below) rows.push(["Upgrades", below]);
  const next = nextLine(v.upgrade);
  if (next) rows.push(["Upgrade", <span key="n" className="[overflow-wrap:anywhere]">{next}</span>]);
  if (v.minimum_availability && !minimumShown) rows.push(["Minimum availability", <MinimumAvailabilityControl key="m" v={v} label={nameOf(v)} />]);
  const active = perms.manage && v.control?.mode === "active";
  return (
    <div className="space-y-3" data-testid={`library-row-details-${key}`}>
      <KV items={rows} />
      <ControlNote c={v.control} kind="both" globalSaid verdictSaid={!!v.control?.grab_state} />
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {active && (v.state === "wanted" || !!v.episodes?.missing) ? <Link className="text-primary hover:underline" to={`/wanted?version=${encodeURIComponent(key)}`}>See in Wanted</Link> : null}
        {active && (v.state === "filled" || !!v.upgrade?.below) ? <Link className="text-primary hover:underline" to={`/wanted/cutoff?version=${encodeURIComponent(key)}`}>See in Cutoff unmet</Link> : null}
        {perms.manage && titleId ? <Link className="text-primary hover:underline" to={releasesHref(titleId, key)}>Last search results</Link> : null}
      </div>
      {perms.manage ? <CopyActions v={v} label={nameOf(v)} onRemove={onRemove} /> : null}
    </div>
  );
}

export function LibraryRows({ title, versions }: { title: TitleDetail; versions: Version[] }) {
  const perms = usePerms();
  const titleId = title.id ?? 0;
  const kind: Kind = title.kind === "series" ? "series" : "movie";
  const rows = currentLibraries(versions);
  const leftover = versions.filter((v) => v.removed && unlinkable(v.links).length > 0);
  const offered = libraryColumns(kind, rows);
  const [prefs, savePrefs] = useColumnPrefs(PREFS, LIBRARY_COLUMNS, DEFAULT_LIBRARY_COLUMNS);
  const shown = prefs.order.filter((id) => prefs.shown.includes(id) && offered.some((c) => c.id === id));
  const [picking, setPicking] = useState(false);
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const wide = useMinWidth(768);
  const setVersion = useSetVersion(titleId);
  const searchNow = useSearchNow(titleId);
  const [interactive, setInteractive] = useState<string | null>(null);
  // The removal dialog lives here, not in the row: its result stays on screen after the row goes.
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
        onChange={(m) => setVersion.mutate({ key: v.key ?? "", monitored: m })} testid={`version-monitor-${v.key}`} />
    );
  };

  // The server's search for one library may also grab for the title's other Live libraries that want it.
  const searchTip = (v: Version) =>
    rows.some((o) => o.key !== v.key && o.control?.mode === "active")
      ? `Search ${nameOf(v)} now. Other Live libraries that want this title may use the results too.`
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
    { id: "monitored", label: "Monitored", header: <span className="sr-only">Monitored</span>, cell: monitor, headClassName: "w-px", card: "none" },
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

  // The phone block: switch, name, tag and status; quality · size · providers; the file name.
  const cardHead = (v: Version) => {
    const c = v.current;
    const facts = [v.has_file ? c?.quality : "", v.has_file && c?.size_bytes ? fmtBytes(c.size_bytes) : ""].filter(Boolean).join(" · ");
    return (
      <div className="space-y-1.5">
        <div className="flex items-start gap-2">
          {monitor(v)}
          <div className="min-w-0 flex-1"><LibraryName v={v} wrap /></div>
          <div className="shrink-0"><StatusCell v={v} title={title} kind={kind} /></div>
        </div>
        {v.has_file ? (
          <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--ink-2)]">
            {facts ? <span>{facts}</span> : null}
            <ProviderPills codes={v.cached_on} testid="cached-on" />
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
              {nameOf(v)} ({unlinkable(v.links).length === 1 ? "1 file" : `${unlinkable(v.links).length} files`}).{" "}
              {perms.manage && v.key ? <LeftoverLinks titleId={titleId} version={v.key} /> : null}
            </span>
          ))}
        </p>
      ) : null}
      {picking ? (
        <ColumnPicker label="Columns" note="Monitored, Library and the actions always stay. Kept for your account in this browser."
          columns={offered} prefs={prefs} onChange={savePrefs} onClose={() => setPicking(false)} />
      ) : null}
      {removing ? <RemoveDialog titleId={titleId} versions={[removing]} onClose={() => setRemoving(null)} /> : null}
      {interactive ? <InteractiveSearch titleId={titleId} version={interactive} onClose={() => setInteractive(null)} /> : null}
    </section>
  );
}
