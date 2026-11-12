// The title page's copies (docs/design/ui.md, Title page): one compact card per version, the one place a copy is
// managed. Each card keeps apart the copy (the version's label: the lane), its current file in words ("4K WEB"), the
// preferred quality (the profile's cutoff, "4K Remux") and the upgrade status in the one vocabulary
// (lib/upgrade.ts, with Why?), then monitoring
// and search now. Details discloses the file, score, path, release history, journal and the raw version key. Only
// for callers with "see files": the title page shows no copy cards to anyone else (docs/design/users.md). A version an arr still imports shows the arr's monitoring read-only
// (docs/design/manage.md).
import { ArrowUpCircle, CheckCircle2, ChevronDown, CircleDashed, Download, FileX2, Loader2, PencilLine, Search, XCircle } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useSearchNow, useSetVersion, type SearchOutcome } from "@/api/add";
import { usePerms, type HistoryEvent, type Version } from "@/api/library";
import { useVersionJournal } from "@/api/manage";
import { DownloadsOffNotice, VersionStatus } from "@/components/version-status";
import { globalDownloadsOff } from "@/lib/version-grab";
import { KV, Mono } from "@/components/page";
import { InfoHash } from "@/components/title/info-hash";
import { journalLink } from "@/lib/admin-links";
import { JournalList } from "@/components/title/journal-list";
import { CopyActions } from "@/components/title/copy-actions";
import { ControlNote, MonitorSwitch } from "@/components/title/manage-bits";
import { MinimumAvailabilityControl } from "@/components/title/title-choices";
import { UpgradeStatus } from "@/components/title/upgrade-why";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fmtAgo, fmtBytes, fmtNumber, fmtTime } from "@/lib/format";
import { mediaLine } from "@/lib/media";
import { copyFields } from "@/lib/title-layout";
import { belowCount } from "@/lib/upgrade";
import { cn } from "@/lib/utils";

const EVENTS: Record<HistoryEvent["event"], { label: string; icon: typeof Download; cls: string }> = {
  searched: { label: "Searched", icon: Search, cls: "text-muted-foreground" },
  grabbed: { label: "Grabbed", icon: Download, cls: "text-[var(--accent-ink)]" },
  imported: { label: "Imported", icon: CheckCircle2, cls: "text-[var(--ok-ink)]" },
  upgraded: { label: "Upgraded", icon: ArrowUpCircle, cls: "text-[var(--ok-ink)]" },
  failed: { label: "Failed", icon: XCircle, cls: "text-[var(--bad-ink)]" },
  deleted: { label: "File removed", icon: FileX2, cls: "text-[var(--ink-3)]" },
  renamed: { label: "Renamed", icon: PencilLine, cls: "text-[var(--ink-3)]" },
  ignored: { label: "Ignored", icon: CircleDashed, cls: "text-[var(--ink-3)]" },
};

/** The releases view of a title's library, with the row of one release (by hash) picked out when given. */
export function releasesHref(titleId: number, version: string, hash?: string): string {
  const q = new URLSearchParams({ version });
  if (hash) q.set("hash", hash.toLowerCase());
  return `/title/${titleId}/releases?${q.toString()}`;
}

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s);

/** One line for a searched event: results, accepted, and what it grabbed or why nothing. */
function searchLine(h: HistoryEvent): string {
  const counts = h.results != null ? `${fmtNumber(h.results)} results, ${fmtNumber(h.accepted ?? 0)} accepted` : "";
  return [counts, h.summary].filter(Boolean).join(" · ");
}

/** A version's (or an episode's) release history, newest first. With titleId and version, a manager's grabs and
 *  failures link to their release's row in the releases view (managers only, as that view), and the newest search to
 *  its results: the releases view shows the newest search only. */
export function History({ events, titleId, version }: { events: HistoryEvent[]; titleId?: number; version?: string }) {
  const perms = usePerms();
  const admin = perms.admin;
  const [all, setAll] = useState(false);
  if (events.length === 0) return <p className="text-sm text-muted-foreground">No history yet.</p>;
  const shown = all ? events : events.slice(0, 6);
  const target = perms.manage && titleId && version ? { titleId, version } : undefined;
  const rowLink = (h: HistoryEvent) => (target && h.info_hash ? releasesHref(target.titleId, target.version, h.info_hash) : undefined);
  return (
    <div>
      <ol className="relative space-y-3 border-l border-[var(--line)] pl-4">
        {shown.map((h, i) => {
          const e = EVENTS[h.event] ?? EVENTS.ignored;
          const release = h.event === "grabbed" || h.event === "failed" ? rowLink(h) : undefined;
          return (
            <li key={i} className="relative" data-testid="history-event">
              <e.icon className={cn("absolute -left-[25px] top-0.5 size-[17px] rounded-full bg-card", e.cls)} />
              <div className="flex flex-wrap items-baseline gap-x-2 text-sm">
                <span className="font-medium">{e.label}</span>
                {h.provider && (h.event === "grabbed" || h.event === "failed") ? <span className="text-muted-foreground">via {h.provider}</span> : null}
                {h.episode ? <Mono className="text-muted-foreground">{h.episode}</Mono> : null}
                {h.quality ? <span className="text-muted-foreground">{h.quality}</span> : null}
                {h.score !== undefined && h.score !== null ? <span className="text-xs text-muted-foreground">Score then {h.score}</span> : null}
                <span className="ml-auto whitespace-nowrap text-xs text-muted-foreground" title={fmtTime(h.date)}>{fmtAgo(h.date)}</span>
              </div>
              {h.release_title ? (
                release ? (
                  <Link to={release} className="mt-0.5 block break-all font-mono text-[11.5px] text-muted-foreground hover:text-primary hover:underline" data-testid="history-release">{h.release_title}</Link>
                ) : (
                  <div className="mt-0.5 break-all font-mono text-[11.5px] text-muted-foreground">{h.release_title}</div>
                )
              ) : null}
              {h.event === "failed" && h.reason ? <div className="text-xs text-[var(--bad-ink)]" data-testid="history-reason">{h.reason}</div> : null}
              {h.event === "searched" && searchLine(h) ? <div className="text-xs text-muted-foreground" data-testid="history-search">{searchLine(h)}</div> : null}
              {h.trigger || h.source ? <div className="text-[11.5px] text-[var(--ink-3)]" data-testid="history-trigger">{cap(h.trigger || h.source || "")}</div> : null}
              {h.event === "searched" && h.latest && target ? (
                <Link className="mr-3 text-xs text-primary hover:underline" to={releasesHref(target.titleId, target.version, h.info_hash)}>See this search's results</Link>
              ) : null}
              {admin && h.decision_id ? <Link className="text-[11.5px] text-[var(--ink-3)] hover:text-foreground hover:underline" to={journalLink(String(h.decision_id))}>Journal entry</Link> : null}
            </li>
          );
        })}
      </ol>
      {events.length > shown.length || all ? (
        <Button variant="ghost" size="xs" className="mt-2" onClick={() => setAll(!all)}>
          {all ? "Show fewer" : `Show all ${events.length}`}
        </Button>
      ) : null}
    </div>
  );
}

function VersionJournal({ v }: { v: Version }) {
  const [open, setOpen] = useState(false);
  const j = useVersionJournal(v.title_id ?? 0, v.key ?? "", open);
  return (
    <div>
      <button type="button" onClick={() => setOpen(!open)} aria-expanded={open} className="flex items-center gap-1 text-xs font-bold uppercase tracking-[0.05em] text-[var(--ink-3)] hover:text-foreground" data-testid={`version-journal-${v.key}`}>
        Journal
        <ChevronDown className={cn("size-3.5 transition-transform", !open && "-rotate-90")} />
      </button>
      {open ? (
        <div className="mt-2">
          {j.isPending ? <p className="text-sm text-muted-foreground">Loading…</p> : <JournalList items={j.data?.items ?? []} />}
        </div>
      ) : null}
    </div>
  );
}

function Field({ label, children, testid }: { label: string; children: ReactNode; testid?: string }) {
  return (
    <div className="min-w-0" data-testid={testid}>
      <dt className="text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--ink-3)]">{label}</dt>
      <dd className="mt-0.5 text-sm text-[var(--ink)] [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

/** What Details discloses: the file, score, path, provider and the raw version key. */
function detailRows(v: Version, admin: boolean): [ReactNode, ReactNode][] {
  const c = v.current;
  const eps = v.episodes;
  const rows: [ReactNode, ReactNode][] = [["Library key", <Mono key="k">{v.key}</Mono>]];
  if (c?.quality) rows.push(["Quality", c.quality]);
  if (v.upgrade?.score != null) rows.push(["Profile score", fmtNumber(v.upgrade.score)]);
  if (eps) rows.push(["Episodes", `${fmtNumber(eps.have)} of ${fmtNumber(eps.total)}${eps.missing ? ` · ${fmtNumber(eps.missing)} aired and missing` : ""}`]);
  else if (c?.files && c.files > 1) rows.push(["Files", fmtNumber(c.files)]);
  const below = belowCount(v.upgrade);
  if (below) rows.push(["Upgrades", below]);
  if (c?.size_bytes) rows.push(["Size", fmtBytes(c.size_bytes)]);
  const media = mediaLine(c?.media);
  if (media) rows.push(["Media", media]);
  if (c?.languages?.length) rows.push(["Languages", c.languages.join(", ")]);
  if (c?.edition) rows.push(["Edition", c.edition]);
  if (c?.added_at) rows.push(["Added", <span key="a" title={fmtTime(c.added_at)}>{fmtAgo(c.added_at)}</span>]);
  if (v.providers?.length) rows.push(["Provider", v.providers.join(", ")]);
  if (c?.release_title) rows.push(["Release", <Mono key="r" className="break-all">{c.release_title}</Mono>]);
  if (c?.instance) rows.push(["From", c.instance]);
  if (c?.path) rows.push(["Path", <Mono key="p" className="break-all text-muted-foreground">{c.path}</Mono>]);
  if (c?.info_hash) rows.push(["Hash", <InfoHash key="h" hash={c.info_hash} admin={admin} />]);
  return rows;
}

/** One copy: copy, current file, preferred quality, upgrade status, monitoring; Details below. */
function CopyCard({ v }: { v: Version }) {
  const perms = usePerms();
  const f = copyFields(v);
  const titleId = v.title_id ?? 0;
  const setVersion = useSetVersion(titleId);
  const searchNow = useSearchNow(titleId);
  const [outcome, setOutcome] = useState<SearchOutcome | null>(null);
  const [open, setOpen] = useState(false);
  const c = v.control;
  const cutoff = v.upgrade?.cutoff;
  const releases = v.title_id ? `/title/${v.title_id}/releases?version=${encodeURIComponent(v.key ?? "")}` : undefined;
  const eps = v.episodes;
  return (
    <section className="flex min-w-0 flex-col gap-3 bg-card p-4" data-version={v.key} data-testid="copy-card">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h3 className="flex items-center gap-2 text-base font-semibold text-[var(--ink)]" data-testid="copy-label">
          <span className="size-2.5 shrink-0 rounded-[3px] bg-[var(--vc)]" data-vkey={v.key} aria-hidden />
          {v.label}
        </h3>
        {v.control?.managed_by ? <p className="text-xs text-muted-foreground">mirrors {v.control.managed_by}</p> : null}
        {v.control?.grab_state ? <VersionStatus state={v.control.grab_state} showMode settingsKey={v.key} canManage={perms.admin} /> : null}
        <span className="text-sm text-muted-foreground" data-testid="copy-availability">{f.availability}</span>
      </div>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2.5">
        <Field label="Current file" testid="copy-current">{f.current}{eps && v.has_file && f.current !== "None yet" ? <span className="text-muted-foreground"> (most episodes)</span> : null}</Field>
        <Field label="Preferred quality" testid="copy-preferred">{f.preferred || "—"}{cutoff && !cutoff.upgrades ? <span className="text-muted-foreground"> · upgrades off</span> : null}</Field>
        <div className="col-span-2">
          <Field label="Upgrade status" testid="copy-upgrade">
            {v.has_file && v.upgrade && v.upgrade.state !== "none" ? (
              <UpgradeStatus u={v.upgrade} what={`${f.copy} copy`} titleId={titleId} releases={releases} ceilingControl={!eps} />
            ) : (
              <span className="text-muted-foreground">{v.has_file ? "—" : "Waiting for a file"}</span>
            )}
          </Field>
        </div>
        {v.minimum_availability ? (
          <div className="col-span-2">
            <Field label="Minimum availability" testid="copy-minimum-availability"><MinimumAvailabilityControl v={v} label={f.copy} /></Field>
          </div>
        ) : null}
      </dl>
      {c ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-[var(--line)] pt-3" data-testid="version-controls">
          <label className="flex items-center gap-2 text-sm">
            <MonitorSwitch
              on={!!v.monitored}
              disabled={!c.can_edit || setVersion.isPending}
              reason={c.edit_reason}
              label={`${v.monitored ? "Unmonitor" : "Monitor"} ${f.copy}`}
              onChange={(m) => setVersion.mutate({ key: v.key ?? "", monitored: m })}
              testid={`version-monitor-${v.key}`}
            />
            <span>{v.monitored ? (cutoff && !cutoff.upgrades ? "Monitored · upgrades off" : "Monitored · auto upgrades on") : "Not monitored"}</span>
          </label>
          <span className="flex-1" />
          {c.can_search ? (
            <Button size="xs" variant="outline" disabled={searchNow.isPending} onClick={() => { setOutcome(null); searchNow.mutate([v.key ?? ""], { onSuccess: setOutcome }); }} data-testid={`version-search-${v.key}`}>
              {searchNow.isPending ? <Loader2 className="animate-spin" /> : <Search />} Search now
            </Button>
          ) : null}
          <Button size="xs" variant="ghost" onClick={() => setOpen(!open)} aria-expanded={open} data-testid={`copy-details-${v.key}`}>
            Details <ChevronDown className={cn("transition-transform", !open && "-rotate-90")} />
          </Button>
        </div>
      ) : (
        <Button size="xs" variant="ghost" className="self-start" onClick={() => setOpen(!open)} aria-expanded={open} data-testid={`copy-details-${v.key}`}>
          Details <ChevronDown className={cn("transition-transform", !open && "-rotate-90")} />
        </Button>
      )}
      <ControlNote c={c} kind="both" globalSaid verdictSaid={!!c?.grab_state} />
      {perms.admin && v.control?.mode === "active" ? <>
        {v.state === "wanted" || !!v.episodes?.missing ? <Link className="text-xs text-primary hover:underline" to={`/wanted?version=${encodeURIComponent(v.key ?? "")}`}>See in Wanted</Link> : null}
        {v.state === "filled" || !!v.upgrade?.below ? <Link className="text-xs text-primary hover:underline" to={`/wanted/cutoff?version=${encodeURIComponent(v.key ?? "")}`}>See in Cutoff unmet</Link> : null}
      </> : null}
      {outcome ? <p className="text-xs text-[var(--accent-ink)]">{outcome.message}</p> : null}
      {searchNow.error ? <p className="text-xs text-[var(--bad-ink)]" role="alert">Search failed: {(searchNow.error as Error).message}</p> : null}
      {setVersion.error ? <p className="text-xs text-[var(--bad-ink)]" role="alert">{(setVersion.error as Error).message}</p> : null}
      {open ? (
        <div className="space-y-4 border-t border-[var(--line)] pt-3" data-testid="copy-details">
          <KV items={detailRows(v, perms.admin)} />
          {releases ? (
            <Link to={releases} className="inline-block text-xs font-medium text-primary hover:underline">
              Show releases
            </Link>
          ) : null}
          <div>
            <div className="mb-2 text-xs font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Release history</div>
            <History events={v.history ?? []} titleId={v.title_id} version={v.key} />
          </div>
          {perms.admin ? <VersionJournal v={v} /> : null}
          {perms.manage ? <CopyActions v={v} label={f.copy} /> : null}
        </div>
      ) : null}
    </section>
  );
}

/** The copies, side by side on wide screens, stacked on phones. */
export function CopyCards({ versions }: { versions: Version[] }) {
  const perms = usePerms();
  if (versions.length === 0) {
    return <Card><p className="text-sm text-muted-foreground">This title is in no library yet.</p></Card>;
  }
  return (
    <>
      <DownloadsOffNotice off={globalDownloadsOff(versions.map((v) => v.control?.grab_state))} canManage={perms.admin} />
      {/* One ruled panel: the libraries side by side on wide screens, hairlines between them. */}
      <div className="grid grid-cols-1 gap-px overflow-hidden rounded-[8px] border border-[var(--line)] bg-[var(--line)] lg:grid-cols-2 lg:[&>*:last-child:nth-child(odd)]:col-span-2" data-testid="copy-cards">
        {versions.map((v) => <CopyCard key={v.id} v={v} />)}
      </div>
    </>
  );
}
