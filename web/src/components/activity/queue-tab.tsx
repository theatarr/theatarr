// Activity › Queue (pages/activity.tsx): every download in hand as a compact row (title, copy, stage, progress or
// wait, one Details/Inspect action that opens its steps and, for admins, the file detail), filtered by Needs
// attention, Downloading, Waiting or All (?show=, lib/activity.ts). Row layout after Scryer's activity queue
// (github.com/scryer-media/scryer apps/scryer-web/components/views/activity/queue-row-presentation.tsx @ 2ef7757c,
// GPL-3.0: the status pill, title and release lines, and the details panel's small uppercase labels).
import { ChevronDown, ChevronUp, Inbox, NotebookPen } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useActivityAttempts, useActivityNow, type ActivityDownload } from "@/api/activity";
import { usePerms } from "@/api/library";
import { DownloadActions } from "@/components/activity/download-actions";
import { Poster, TitleLine } from "@/components/activity/row-bits";
import { ProgressBar, StepTrack, toneOf } from "@/components/activity-bits";
import { Empty, ErrorState, Loading, Mono, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fmtAgo, fmtBytes, fmtTime } from "@/lib/format";
import { filterDownloads, needsAttention, showCounts, SHOWS, stageLabel, timing, type ActivityShow } from "@/lib/activity";
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { adminLink, journalLink } from "@/lib/admin-links";
import { FAILED_WORDS } from "@/lib/request-timeline";

const WORD_TONE = {
  active: "text-[var(--accent-ink)]",
  ok: "text-[var(--ok-ink)]",
  warn: "text-[var(--warn)]",
  bad: "text-[var(--bad-ink)]",
  idle: "text-muted-foreground",
} as const;

function Label({ children }: { children: React.ReactNode }) {
  return <p className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">{children}</p>;
}

function DownloadDetails({ d }: { d: ActivityDownload }) {
  const history = useActivityAttempts(d.id);
  const attempts = history.data?.attempts ?? d.attempts ?? [];
  const admin = usePerms().admin;
  const journal = d.journal ? adminLink(admin, journalLink(d.journal)) : undefined;
  return (
    <div className="mt-3 grid gap-4 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] p-3 md:grid-cols-2">
      <div className="min-w-0 space-y-3">
        <div>
          <Label>Release</Label>
          <p className="mt-1 break-all font-mono text-[12.5px] text-foreground">{d.release}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            {[d.quality, d.score !== undefined ? `score ${d.score}` : "", d.size ? fmtBytes(d.size) : "", d.indexer].filter(Boolean).join(" · ")}
          </p>
        </div>
        {d.info_hash ? (
          <div>
            <Label>Hash</Label>
            <Mono className="mt-1 block break-all text-muted-foreground">{d.info_hash}</Mono>
          </div>
        ) : null}
        {d.error ? (
          <div>
            <Label>Message</Label>
            <p className="mt-1 whitespace-pre-wrap break-words text-sm text-[var(--warn-ink)]">{d.error}</p>
          </div>
        ) : null}
      </div>
      <div className="min-w-0 space-y-3">
        <div>
          <Label>Provider attempts</Label>
          {history.isPending ? <p role="status" className="mt-1 text-sm text-muted-foreground">Loading full attempt history…</p> : null}
          {history.error ? <div className="mt-1 text-sm"><p role="alert">Could not load full attempt history.</p><Button size="xs" variant="outline" onClick={() => void history.refetch()}>Retry attempt history</Button></div> : null}
          {!history.data && d.attempts_more ? <p className="mt-1 text-xs text-muted-foreground">Showing the latest {attempts.length} {attempts.length === 1 ? "attempt" : "attempts"}.</p> : null}
          {attempts.length === 0 && !history.isPending && !history.error ? (
            <p className="mt-1 text-sm text-muted-foreground">None recorded.</p>
          ) : (
            <ul className="mt-1 space-y-1 text-sm">
              {attempts.map((a, i) => (
                <li key={i} className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-medium">{a.provider}</span>
                  <Badge tone={a.outcome === "accepted" ? "positive" : a.outcome === "deferred" ? "warning" : a.outcome === "skipped" ? "neutral" : "negative"}>
                    {a.outcome}
                    {a.code ? ` · ${a.code}` : ""}
                  </Badge>
                  <span className="text-xs text-muted-foreground">{fmtTime(a.at)}</span>
                  {a.message ? <span className="w-full break-words text-xs text-muted-foreground">{a.message}</span> : null}
                </li>
              ))}
            </ul>
          )}
        </div>
        <div>
          <Label>Cache evidence</Label>
          {(d.cache ?? []).length === 0 ? (
            <p className="mt-1 text-sm text-muted-foreground">None for this hash.</p>
          ) : (
            <div className="mt-1 flex flex-wrap gap-1.5">
              {(d.cache ?? []).map((c, i) => (
                <Badge key={i} tone={c.state === "cached" ? "positive" : c.state === "miss" || c.state === "uncached" ? "negative" : "neutral"}>
                  {c.provider} {c.state} <span className="opacity-70">({c.source})</span>
                </Badge>
              ))}
            </div>
          )}
        </div>
        {journal ? (
          <Link to={journal} className="inline-flex items-center gap-1.5 text-sm text-primary hover:underline">
            <NotebookPen className="size-4" /> Journal entry
          </Link>
        ) : null}
      </div>
    </div>
  );
}

function Stage({ d, admin, attention }: { d: ActivityDownload; admin: boolean; attention: boolean }) {
  const label = stageLabel(d);
  const tone = attention && toneOf(d) !== "bad" ? "warn" : toneOf(d);
  const words = admin && d.detail ? d.detail : d.words;
  return (
    <div className="min-w-0">
      <p className={cn("text-sm font-medium", WORD_TONE[tone])} data-testid="stage">{label}</p>
      {words && words !== label ? <p className="truncate text-xs text-muted-foreground" data-testid="words" title={words}>{words}</p> : null}
    </div>
  );
}

function Progress({ d, now }: { d: ActivityDownload; now: number }) {
  const tone = toneOf(d);
  const showBar = d.state === "downloading" || d.state === "added" || d.state === "ready" || d.state === "importing";
  const t = timing(d, now);
  const parts = [
    t.waited ? `waiting ${t.waited}` : null,
    t.updated ? `updated ${t.updated}` : null,
    t.retry ? `next try ${t.retry}` : null,
  ].filter(Boolean);
  return (
    <div className="min-w-0">
      {showBar ? (
        <div className="flex items-center gap-2">
          <ProgressBar pct={d.progress} tone={tone} />
          <span className="w-9 text-right text-xs font-semibold tabular-nums">{Math.round(d.progress)}%</span>
        </div>
      ) : null}
      {parts.length > 0 ? <p className={cn("text-xs text-muted-foreground", showBar && "mt-1")} data-testid="timing">{parts.join(" · ")}</p> : null}
    </div>
  );
}

function DownloadRow({ d, admin, now }: { d: ActivityDownload; admin: boolean; now: number }) {
  const [open, setOpen] = useState(false);
  const attention = needsAttention(d, now);
  return (
    <li className={cn("px-3 py-2 sm:px-4", open && "bg-[var(--sunken)]")} data-testid="download" data-state={d.state} data-attention={attention || undefined}>
      <div className="flex gap-3 md:grid md:grid-cols-[1.75rem_minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1fr)_5.5rem] md:items-center md:gap-4">
        {d.title.id ? (
          <Link to={`/title/${d.title.id}`} className="shrink-0 self-start md:self-center" tabIndex={-1} aria-hidden>
            <Poster src={d.title.poster} className="w-7" />
          </Link>
        ) : <Poster src={d.title.poster} className="w-7 self-start md:self-center" />}
        <div className="min-w-0 flex-1">
          <TitleLine t={d.title} />
          <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            {admin ? <VersionTag k={d.version} className="text-[11px]" /> : null}
            {d.quality ? <Badge tone="outline">{admin ? d.quality : resolutionWords(d.quality) || d.quality}</Badge> : null}
            {d.episodes && d.episodes.length > 0 ? (
              <span>{d.episodes.length > 4 ? `${d.episodes.slice(0, 3).join(" ")} … ${d.episodes.length} episodes` : d.episodes.join(" ")}</span>
            ) : null}
          </div>
          <div className="mt-1.5 space-y-1 md:hidden">
            <Stage d={d} admin={admin} attention={attention} />
            <Progress d={d} now={now} />
          </div>
        </div>
        <div className="hidden md:block"><Stage d={d} admin={admin} attention={attention} /></div>
        <div className="hidden md:block"><Progress d={d} now={now} /></div>
        <div className="shrink-0 self-start md:self-center md:text-right">
          <Button
            size="xs"
            variant={attention ? "outline" : "ghost"}
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            data-testid="download-details"
          >
            {attention ? "Inspect" : "Details"} {open ? <ChevronUp /> : <ChevronDown />}
          </Button>
        </div>
      </div>
      {open ? (
        <div className="mt-2 border-t border-[var(--line)] pt-3 md:pl-[2.75rem]" data-testid="download-open">
          <div className="max-w-xl">
            <StepTrack steps={d.steps} />
          </div>
          {!admin && d.state === "failed" && !d.not_started ? <p className="mt-2 text-sm" data-testid="failed-words">{FAILED_WORDS}</p> : null}
          <p className="mt-2 text-xs text-muted-foreground">
            Added {fmtAgo(d.created_at)}
            {admin && d.provider_name ? <> · <Badge tone="info">{d.provider_name}</Badge></> : null}
            {admin && d.client ? <> · <Badge tone="info">client {d.client}</Badge></> : null}
            {admin && d.cached ? <> · <Badge tone="positive">cached</Badge></> : null}
            {admin && d.reused ? <> · <Badge tone="neutral">reused</Badge></> : null}
          </p>
          {admin ? <DownloadDetails d={d} /> : null}
          {admin ? <DownloadActions d={d} /> : null}
        </div>
      ) : null}
    </li>
  );
}

/** The clock the waiting times count from, ticking every 30 s. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(t);
  }, []);
  return now;
}

const EMPTY: Record<ActivityShow, string> = {
  attention: "Nothing needs attention.",
  downloading: "Nothing is downloading right now.",
  waiting: "Nothing is waiting.",
  all: "Nothing is downloading right now.",
};

export function QueueTab({ admin, as, show, onShow }: { admin: boolean; as?: "user"; show: ActivityShow; onShow: (s: ActivityShow) => void }) {
  const q = useActivityNow({ as });
  const now = useNow();
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const all = q.data.now ?? [];
  const counts = showCounts(all, now);
  const items = filterDownloads(all, show, now);
  return (
    <div>
      <div className="mb-4 flex flex-wrap gap-2" data-testid="counts" role="group" aria-label="Show">
        {SHOWS.map((s) => (
          <button
            key={s.id}
            type="button"
            onClick={() => onShow(s.id)}
            aria-pressed={show === s.id}
            data-show={s.id}
            data-count={counts[s.id]}
            className={cn(
              "rounded-[4px] border px-2.5 py-1 text-[13px] font-medium transition",
              show === s.id
                ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]"
                : "border-[var(--line)] text-muted-foreground hover:bg-[var(--hover)] hover:text-foreground",
              s.id === "attention" && counts.attention > 0 && show !== s.id && "border-[var(--warn)] text-[var(--warn)]",
            )}
          >
            {admin || !s.userLabel ? s.label : s.userLabel} <span className="tabular-nums opacity-80">{counts[s.id]}</span>
          </button>
        ))}
      </div>
      {items.length === 0 ? (
        <Card>
          <Empty icon={Inbox}>
            <span>{EMPTY[show]}</span>
            {show === "all" ? <span className="text-xs">New downloads show up here as they start, without a reload.</span> : null}
          </Empty>
        </Card>
      ) : (
        <Card className="p-0">
          <div className="hidden border-b border-[var(--line)] bg-[var(--sunken)] px-4 py-1.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)] md:grid md:grid-cols-[1.75rem_minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1fr)_5.5rem] md:gap-4">
            <span />
            <span>Title and library</span>
            <span>Stage</span>
            <span>Progress</span>
            <span className="sr-only">Action</span>
          </div>
          <ul className="divide-y divide-border">
            {items.map((d) => (
              <DownloadRow key={d.id} d={d} admin={admin} now={now} />
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
