// The episode overview (docs/design/manage.md, docs/design/ui.md Title page / Episodes), in the shared Modal: a dialog
// centred on desktop and a bottom sheet on phones, opened from an episode row (?episode=<id>). The episode's number
// (labelled "E01 · absolute 1" for anime), air date and runtime; then, for callers who see files, three tabs:
// Details (per copy its state against the cutoff with Why? and what Theatarr would take next, the download in hand,
// the monitor switch and Search now for managers, read-only with the reason while an arr manages the copy,
// Interactive search for a library that may search, and the file's facts with Cached on, shown openly), History (per
// copy its history and journal) and Last search (the stored decision, read-only). Accounts without "see files" get
// one availability line across copies and nothing else (the API sends no file, history or states for them).
// What it holds follows Scryer (github.com/scryer-media/scryer)
// apps/scryer-web/components/views/series-overview/episode-details-panel.tsx and episode-panel-content.tsx
// @ 2ef7757c, GPL-3.0 (episode header, the files on disk with their media facts, the history list); the look is ours.
import { Activity, Loader2, Search, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import type { SearchOutcome } from "@/api/add";
import type { Schemas } from "@/api/client";
import { usePerms } from "@/api/library";
import { useEpisode, useEpisodeReleases, useMonitor, useScopedSearch, type EpisodeDetail, type EpisodeVersionDetail, type ManageOutcome } from "@/api/manage";
import { VersionStatus } from "@/components/version-status";
import { Empty, ErrorState, KV, Mono, VersionTag } from "@/components/page";
import { JournalList } from "@/components/title/journal-list";
import { ControlNote, MonitorSwitch, UpgradeBlock } from "@/components/title/manage-bits";
import { InteractiveSearchButton } from "@/components/title/interactive-search";
import { ReleaseGrid } from "@/components/title/release-grid";
import { Summary } from "@/components/title/show-releases";
import { ChangeEpisodes } from "@/components/title/change-episodes";
import { ProgressBar } from "@/components/activity-bits";
import { History } from "@/components/title/versions";
import { ProviderPills } from "@/components/provider-pills";
import { TorrentLink } from "@/components/title/info-hash";
import { AddToProviders, OtherProviders } from "@/components/title/provider-copies";
import { useCopyFile } from "@/api/provider-copies";
import { useLibraryName } from "@/lib/library-names";
import { otherProvidersLine } from "@/lib/provider-copies";
import { globalDownloadsOff } from "@/lib/version-grab";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { fmtAgo, fmtBytes, fmtTime } from "@/lib/format";
import { fmtDay, fmtRuntime } from "@/lib/library";
import { mediaLine } from "@/lib/media";
import { episodeNumber, mergedStatus, plainAvailable, statusWords } from "@/lib/seasons";
import { UPGRADE_WORDS, upgradeBadge } from "@/lib/upgrade";
import { activityHref, stageTone, stageWords } from "@/lib/copies";
import { cn } from "@/lib/utils";

type Acquisition = Schemas["Acquisition"];

function code(season: number, number: number) {
  return `S${String(season).padStart(2, "0")}E${String(number).padStart(2, "0")}`;
}

/** A copy's state for a caller who sees files, in the upgrade vocabulary once it has a file. */
function stateBadge(d: EpisodeVersionDetail, aired: boolean): { label: string; tone: "positive" | "warning" | "negative" | "neutral" | "info" } {
  if (d.has_file) {
    const badge = upgradeBadge(d.upgrade); if (badge) return badge;
    return d.state === "filled" ? UPGRADE_WORDS.below_cutoff : UPGRADE_WORDS.cutoff_met;
  }
  if (!aired) return { label: "Not aired", tone: "neutral" };
  if (!d.monitored) return { label: "Not monitored", tone: "neutral" };
  return { label: "Missing", tone: "negative" };
}

/** The last search Theatarr decided for this episode in one library, read-only (Interactive search searches now). */
function Releases({ titleId, eid, d }: { titleId: number; eid: number; d: EpisodeVersionDetail }) {
  const q = useEpisodeReleases(titleId, eid, d.key, false, true);
  const r = q.data;
  return (
    <div className="space-y-3" data-testid="episode-releases">
      {d.control?.live_releases ? null : (
        <p className="text-[11.5px] text-[var(--ink-3)]">{d.control?.mode === "active" ? "The acquirer is not running" : "Watch only: no live indexer search; the last stored decision"}</p>
      )}
      {q.isPending ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Loading…</p>
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : r ? (
        <>
          <Summary r={r} />
          {(r.candidates ?? []).length === 0 ? <Empty>No releases stored for this episode.</Empty> : <ReleaseGrid items={r.candidates ?? []} kind="series" outcomes={r.outcomes} />}
        </>
      ) : null}
    </div>
  );
}

function fileRows(f: EpisodeVersionDetail["file"]): [React.ReactNode, React.ReactNode][] {
  const rows: [React.ReactNode, React.ReactNode][] = [];
  if (!f) return rows;
  if (f.release_title || f.scene_name) rows.push(["Release", <Mono key="r" className="break-all">{f.release_title || f.scene_name}</Mono>]);
  if (f.quality) rows.push(["Quality", f.quality]);
  if (f.score !== undefined && f.score !== null) rows.push(["Profile score", String(f.score)]);
  if (f.size_bytes) rows.push(["Size", fmtBytes(f.size_bytes)]);
  if (f.release_group) rows.push(["Group", f.release_group]);
  rows.push(["Cached on", <span key="c" className="flex flex-wrap items-center gap-x-2 gap-y-1"><ProviderPills codes={f.cached_on} /><TorrentLink hash={f.info_hash} /></span>]);
  if (otherProvidersLine(f.copies, f.cached_on)) rows.push(["Other providers", <OtherProviders key="o" copies={f.copies} shown={f.cached_on} />]);
  if (f.languages?.length) rows.push(["Languages", f.languages.join(", ")]);
  const media = mediaLine(f.media);
  if (media) rows.push(["Media", media]);
  if (f.episodes && f.episodes.length > 1) rows.push(["In the file", f.episodes.join(", ")]);
  if (f.added_at) rows.push(["Added", <span key="a" title={fmtTime(f.added_at)}>{fmtAgo(f.added_at)}</span>]);
  if (f.instance) rows.push(["From", f.instance]);
  if (f.path) rows.push(["Path", <Mono key="p" className="break-all text-muted-foreground">{f.path}</Mono>]);
  return rows;
}

/** What is in hand for this episode in one copy (from the season list): the stage, its words and progress, when it
 * last moved, and a link to the Activity page. The current file stays until the new one is linked. */
function AcquisitionPart({ a }: { a: Acquisition }) {
  const warn = stageTone(a.stage) === "warning";
  const facts = [
    a.quality,
    a.updated_at ? `updated ${fmtAgo(a.updated_at)}` : "",
    a.waiting_since ? `waiting since ${fmtAgo(a.waiting_since)}` : "",
    a.next_retry ? `next try ${fmtAgo(a.next_retry)}` : "",
  ].filter(Boolean);
  return (
    <div
      className={cn("space-y-1.5 rounded-[5px] border px-3 py-2", warn ? "border-[var(--th-warning-border)] bg-[var(--warn-soft)]" : "border-[var(--th-info-border)] bg-[var(--accent-soft)]")}
      data-testid="episode-acquisition"
      data-stage={a.stage}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className={cn("text-sm font-semibold", warn ? "text-[var(--warn-ink)]" : "text-[var(--accent-ink)]")}>{stageWords(a)}</span>
        <span className="flex-1" />
        <Button size="xs" variant="outline" asChild>
          <Link to={activityHref(a.stage)}>
            <Activity /> Activity
          </Link>
        </Button>
      </div>
      {a.stage === "downloading" && a.progress !== undefined ? <ProgressBar pct={a.progress} tone="active" /> : null}
      <p className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
        {a.words}
        {facts.length ? ` · ${facts.join(" · ")}` : ""}
      </p>
      {a.upgrade ? <p className="text-xs text-muted-foreground">The current file stays available until the new one is linked.</p> : null}
    </div>
  );
}

type Tab = "details" | "history" | "releases";

/** A copy's block inside a tab, headed by its library when the episode is in more than one. */
function CopyBlock({ d, single, children, testid }: { d: EpisodeVersionDetail; single: boolean; children: ReactNode; testid?: string }) {
  return (
    <section className={cn("space-y-3", !single && "rounded-xl border border-[var(--line)] p-3")} data-episode-version={d.key} data-testid={testid}>
      {single ? null : <VersionTag k={d.key} label={d.label} />}
      {children}
    </section>
  );
}

/** One copy's Details, for callers who see files: state, controls, the download in hand, the upgrade and the file. */
function DetailsPart({ titleId, eid, epCode, d, aired, manage, admin, single, acquisition, onReleases }: { titleId: number; eid: number; epCode: string; d: EpisodeVersionDetail; aired: boolean; manage: boolean; admin: boolean; single: boolean; acquisition?: Acquisition; onReleases: () => void }) {
  const monitor = useMonitor(titleId);
  const search = useScopedSearch(titleId);
  const nameOf = useLibraryName();
  const [msg, setMsg] = useState("");
  const c = d.control;
  const badge = stateBadge(d, aired);
  const rows = fileRows(d.file);
  if (rows.length === 0 && d.has_file && d.upgrade?.quality) rows.push(["Quality", d.upgrade.quality]);
  return (
    <section className={cn("space-y-3", !single && "rounded-xl border border-[var(--line)] p-3")} data-episode-version={d.key}>
      <div className="flex flex-wrap items-center gap-2">
        <VersionTag k={d.key} label={d.label} />
        {c?.grab_state ? <VersionStatus state={c.grab_state} showMode settingsKey={d.key} canManage={admin} /> : null}
        <Badge tone={badge.tone}>{badge.label}</Badge>
      </div>
      {acquisition ? <AcquisitionPart a={acquisition} /> : null}
      {manage && c ? (
        <div className="flex flex-wrap items-center gap-3">
          <label className="flex items-center gap-2 text-sm">
            <MonitorSwitch
              on={d.monitored}
              disabled={!c.can_edit || monitor.isPending}
              reason={c.edit_reason}
              label={`${d.monitored ? "Unmonitor" : "Monitor"} this episode in ${d.label || nameOf(d.key)}`}
              onChange={(m) => monitor.mutate({ episode_ids: [eid], versions: [d.key], monitored: m }, { onSuccess: (o: ManageOutcome) => setMsg(o.message), onError: (e) => setMsg((e as Error).message) })}
              testid={`episode-monitor-${d.key}`}
            />
            <span>Monitored</span>
          </label>
          <span className="flex-1" />
          <Button
            size="sm"
            variant="outline"
            disabled={!c.can_search || search.isPending}
            title={c.can_search ? undefined : c.search_reason}
            onClick={() => search.mutate({ episode_ids: [eid], versions: [d.key] }, { onSuccess: (o: SearchOutcome) => setMsg(o.message), onError: (e) => setMsg((e as Error).message) })}
            data-testid={`episode-search-${d.key}`}
          >
            {search.isPending ? <Loader2 className="animate-spin" /> : <Search />} Search now
          </Button>
          {c.live_releases ? <InteractiveSearchButton titleId={titleId} version={d.key} episode={{ id: eid, code: epCode }} /> : null}
        </div>
      ) : null}
      {manage ? <ControlNote c={c} kind="both" /> : null}
      {msg ? <p className="text-xs text-[var(--accent-ink)]">{msg}</p> : null}
      <UpgradeBlock u={d.upgrade} seeFiles releases={onReleases} titleId={titleId} what={`This episode · ${d.label || nameOf(d.key)}`} />
      {rows.length > 0 ? (
        <div className="rounded-[5px] border border-[var(--line)] p-3" data-testid="episode-file">
          <KV items={rows} />
          {manage && d.file ? <div className="mt-3"><EpisodeAddToProviders titleId={titleId} eid={eid} d={d} /></div> : null}
          {admin && d.file?.link_id ? (
            <div className="mt-3">
              <ChangeEpisodes titleId={titleId} linkId={d.file.link_id} path={d.file.path ?? ""} copy={d.label || nameOf(d.key)} held={d.file.episodes ?? []} />
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

/** "Add to other providers" for the episode's file in one library (managers). */
function EpisodeAddToProviders({ titleId, eid, d }: { titleId: number; eid: number; d: EpisodeVersionDetail }) {
  const m = useCopyFile(titleId);
  return <AddToProviders m={m} vars={{ version: d.key, episode_id: eid }} paused={globalDownloadsOff([d.control?.grab_state])} current={d.file?.copies} testid={`add-to-providers-${d.key}`} />;
}

/** One copy's history and journal. */
function HistoryPart({ titleId, d, single }: { titleId: number; d: EpisodeVersionDetail; single: boolean }) {
  const journal = d.journal ?? [];
  return (
    <CopyBlock d={d} single={single} testid="episode-history">
      <History events={d.history ?? []} titleId={titleId} version={d.key} />
      {journal.length > 0 ? (
        <div data-testid="episode-journal">
          <div className="mb-1.5 text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Journal</div>
          <JournalList items={journal} empty="No journal entries about this episode." />
        </div>
      ) : null}
    </CopyBlock>
  );
}

/** For callers who see files: Details, History and Last search as tabs, each listing the episode's copies. */
function CopyTabs({ titleId, d, aired, manage, admin, acquisitions }: { titleId: number; d: EpisodeDetail; aired: boolean; manage: boolean; admin: boolean; acquisitions?: Record<string, Acquisition | undefined> }) {
  const [tab, setTab] = useState<Tab>("details");
  const single = d.versions.length === 1;
  const epCode = code(d.episode.season, d.episode.number);
  const events = d.versions.reduce((n, v) => n + (v.history?.length ?? 0) + (v.journal?.length ?? 0), 0);
  return (
    <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)} className="flex min-h-0 flex-1 flex-col">
      <TabsList className="shrink-0 overflow-x-auto px-4 [scrollbar-width:none]" aria-label="Episode">
        <TabsTrigger value="details">Details</TabsTrigger>
        <TabsTrigger value="history">
          History
          {events ? <span className="tabular-nums text-[var(--ink-3)]">{events}</span> : null}
        </TabsTrigger>
        <TabsTrigger value="releases">Last search</TabsTrigger>
      </TabsList>
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <TabsContent value="details" className="mt-0 space-y-4">
          {d.versions.map((v) => (
            <DetailsPart key={v.key} titleId={titleId} eid={d.episode.id} epCode={epCode} d={v} aired={aired} manage={manage} admin={admin} single={single} acquisition={acquisitions?.[v.key]} onReleases={() => setTab("releases")} />
          ))}
        </TabsContent>
        <TabsContent value="history" className="mt-0 space-y-4">
          {d.versions.map((v) => (
            <HistoryPart key={v.key} titleId={titleId} d={v} single={single} />
          ))}
        </TabsContent>
        <TabsContent value="releases" className="mt-0 space-y-4">
          {d.versions.map((v) => (
            <CopyBlock key={v.key} d={v} single={single} testid={`episode-releases-${v.key}`}>
              <Releases titleId={titleId} eid={d.episode.id} d={v} />
            </CopyBlock>
          ))}
        </TabsContent>
      </div>
    </Tabs>
  );
}

/** For accounts without "see files": one availability line across copies (copies are a household detail). */
function PlainAvailability({ e }: { e: EpisodeDetail["episode"] }) {
  const keys = Object.keys(e.versions);
  const s = mergedStatus(e, keys, false);
  const w = statusWords(s, false);
  const label = s === "available" ? plainAvailable(e, keys) : w.label;
  return (
    <p className="flex items-center gap-2 text-sm" data-testid="episode-availability" data-status={s}>
      <Badge tone={w.tone}>{label}</Badge>
    </p>
  );
}

export function EpisodeOverview({
  titleId,
  eid,
  anime,
  onClose,
  acquisitions,
}: {
  titleId: number;
  eid: number;
  anime: boolean;
  onClose: () => void;
  /** Per copy, what is in hand for the episode (the season list's cells). */
  acquisitions?: Record<string, Acquisition | undefined>;
}) {
  const q = useEpisode(titleId, eid);
  const perms = usePerms();
  const d = q.data;
  const e = d?.episode;
  const aired = !!e?.aired;
  const runtime = fmtRuntime(e?.runtime_minutes);
  // Modal portals to <body>: the title page is its own stacking context (isolate), which would put the phone tab bar
  // above us.
  return (
    <Modal
      label={e ? `${code(e.season, e.number)} ${e.name || ""}`.trim() : "Episode"}
      onClose={onClose}
      testid="episode-drawer"
      panelTestid="episode-overview"
      className="z-[70] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="relative flex max-h-[90dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-2xl sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <div className="text-xs font-medium text-[var(--ink-3)]">{d?.title_name ?? ""}</div>
        <h2 className="mt-0.5 text-lg font-semibold leading-tight text-[var(--ink)] [overflow-wrap:anywhere]" data-testid="episode-title">
          {e ? (
            <>
              <Mono className="mr-2 text-base text-[var(--accent-ink)]">{code(e.season, e.number)}</Mono>
              {e.name || `Episode ${e.number}`}
            </>
          ) : (
            "Episode"
          )}
        </h2>
        {e ? (
          <p className="mt-1 text-xs text-muted-foreground" data-testid="episode-meta">
            {[e.air_date ? `${aired ? "Aired" : "Airs"} ${fmtDay(e.air_date)}` : "No air date", runtime, anime && e.absolute ? episodeNumber(e, true) : ""].filter(Boolean).join(" · ")}
          </p>
        ) : null}
        <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      {d && !q.error && d.versions.length > 0 && perms.seeFiles ? (
        <CopyTabs titleId={titleId} d={d} aired={aired} manage={perms.manage} admin={perms.admin} acquisitions={acquisitions} />
      ) : (
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
          {q.isPending ? (
            <p className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" /> Loading…</p>
          ) : q.error ? (
            <ErrorState error={q.error} />
          ) : d ? (
            d.versions.length === 0 ? <Empty>This series has no libraries.</Empty> : <PlainAvailability e={d.episode} />
          ) : null}
        </div>
      )}
    </Modal>
  );
}
