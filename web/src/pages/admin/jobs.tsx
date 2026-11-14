// Admin › Jobs (docs/design/ui.md): the counts as one summary strip, then failed jobs that need attention (each with
// its friendly name, raw kind, error, effect, Retry and Dismiss), then running and queued work, then the recurring
// tasks with how often they run and their last run; failures already resolved, retried or dismissed and finished jobs
// (identical ones as one row) are folded below. Counts update live from the event stream.
// A failed job needs attention until a later done job of its kind and payload resolves it, it is retried, or it is
// dismissed (lib/jobs.ts).
import { useSearchParams } from "react-router";
import { CheckCircle2, EyeOff, RotateCcw } from "lucide-react";
import { type JobSchedule, useJobActions, useJobCounts, useJobsFiltered, useJobsRecurring } from "@/api/jobs";
import { Empty, ErrorState, Fold, JobState, Loading, Mono, PageHeader, Section, Strip } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fmtAgo, fmtNumber, fmtTime } from "@/lib/format";
import { attentionCount, canDismiss, canRetry, collapseDone, everyWords, groupJobs, jobEffect, jobName, jobNotes, type Job } from "@/lib/jobs";

export function JobsPage() {
  const [params, setParams] = useSearchParams();
  const kind = params.get("kind") ?? "";
  const jobs = useJobsFiltered({ kind: kind || undefined, limit: 50 });
  const counts = useJobCounts(jobs.data);
  const failed = useJobsFiltered({ kind: kind || undefined, state: "failed", dismissed: false, limit: 50 });
  const { dismissResolved } = useJobActions();
  const open = attentionCount(counts);
  const header = <PageHeader crumb={[{ label: "System", to: "/admin" }]} title="Tasks" description="Background work: what failed and needs a look first, then what runs next." />;
  if (jobs.isPending) return <div>{header}<Loading /></div>;
  if (jobs.error) return <div>{header}<ErrorState error={jobs.error} /></div>;
  const g = groupJobs(failed.data?.items ?? [], jobs.data?.items ?? []);
  const anyResolved = g.settled.some((j) => j.resolved_by && !j.dismissed_at);
  return (
    <div>
      {header}
      {kind ? <p className="mb-3">Showing {jobName(kind)} tasks. <button className="text-primary underline" onClick={() => { const next = new URLSearchParams(params); next.delete("kind"); setParams(next); }}>Show all tasks</button></p> : null}
      <Strip
        items={[
          { label: "need attention", value: fmtNumber(open), tone: open > 0 ? "bad" : undefined, testid: "jobs-attention" },
          { label: "running", value: fmtNumber(counts.running ?? 0), testid: "jobs-running" },
          { label: "queued", value: fmtNumber(counts.queued ?? 0), testid: "jobs-queued" },
          { label: "failed, resolved ones included", value: fmtNumber(counts.failed ?? 0), testid: "jobs-failed" },
          { label: "done", value: fmtNumber(counts.done ?? 0), testid: "jobs-done" },
        ]}
      />
      <Section title="Needs attention" className="mt-5">
        {failed.error ? (
          <ErrorState error={failed.error} />
        ) : failed.isPending ? (
          <Loading />
        ) : g.attention.length === 0 ? (
          <Card><Empty icon={CheckCircle2}>No failed task needs attention.</Empty></Card>
        ) : (
          <JobList items={g.attention} showEffect testid="jobs-attention-list" />
        )}
      </Section>
      <Section title="Running and queued" aside="running first, then by run time">
        {g.active.length === 0 ? <Card><Empty>Nothing is running or queued.</Empty></Card> : <JobList items={g.active} testid="jobs-active-list" />}
      </Section>
      <Recurring />
      {g.settled.length > 0 ? (
        <Fold
          testid="jobs-settled"
          title={`Resolved, retried and dismissed failures (${g.settled.length})`}
          aside={
            anyResolved ? (
              <Button
                size="xs"
                variant="outline"
                data-testid="jobs-dismiss-resolved"
                disabled={dismissResolved.isPending}
                onClick={(e) => {
                  e.preventDefault();
                  dismissResolved.mutate();
                }}
              >
                <EyeOff /> Dismiss all resolved
              </Button>
            ) : null
          }
        >
          <JobList items={g.settled} flat />
        </Fold>
      ) : null}
      {g.done.length > 0 ? (
        <Fold testid="jobs-done-list" title={`Finished recently (${g.done.length})`}>
          <DoneList items={g.done} />
        </Fold>
      ) : null}
    </div>
  );
}

/** The recurring tasks with how often they run, their last run and, when one is queued, their next run. */
function Recurring() {
  const q = useJobsRecurring();
  return (
    <Section title="Recurring" aside="how often each runs, from the settings in force">
      {q.error ? <ErrorState error={q.error} /> : q.isPending ? <Loading /> : (
        <Card className="p-0">
          <ul className="divide-y divide-border" data-testid="jobs-recurring">
            {q.data.items.map((s) => <RecurringRow key={s.kind} s={s} />)}
          </ul>
        </Card>
      )}
    </Section>
  );
}

function RecurringRow({ s }: { s: JobSchedule }) {
  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-4 py-2.5" data-kind={s.kind}>
      <span className="min-w-0 font-medium text-[var(--ink)] sm:w-72">{jobName(s.kind)}</span>
      <span className="text-sm text-muted-foreground sm:w-32" data-testid="recurring-every">{everyWords(s.every_seconds, s.at)}</span>
      <span className="text-sm text-muted-foreground" data-testid="recurring-last">
        {s.last_run ? <>last run <span title={fmtTime(s.last_run)}>{fmtAgo(s.last_run)}</span>{s.last_state === "failed" ? <Badge tone="negative" className="ml-1.5">failed</Badge> : null}</> : "not run recently"}
        {s.next_run ? <> · next <span title={fmtTime(s.next_run)}>{fmtTime(s.next_run)}</span></> : null}
      </span>
    </li>
  );
}

/** Finished jobs, identical ones (same kind and state) as one row: "×41, last 8 s ago". */
function DoneList({ items }: { items: Job[] }) {
  const rows = collapseDone(items);
  return (
    <ul className="divide-y divide-border">
      {rows.map(({ job, count }) => count === 1 ? <JobRow key={job.id} j={job} /> : (
        <li key={job.id} data-job={job.id} className="flex flex-wrap items-center gap-x-2 gap-y-1 px-4 py-3" data-testid="jobs-done-group">
          <span className="font-medium text-[var(--ink)]" data-testid="job-name">{jobName(job.kind)}</span>
          <JobState state={job.state} />
          <span className="text-sm text-muted-foreground">×{fmtNumber(count)}, last {fmtAgo(job.finished_at)}</span>
        </li>
      ))}
    </ul>
  );
}

/** Jobs as rows that stack on a phone: friendly name, raw kind and number, state, timing, error, actions. */
export function JobList({ items, showEffect, flat, testid }: { items: Job[]; showEffect?: boolean; flat?: boolean; testid?: string }) {
  const list = (
    <ul className="divide-y divide-border" data-testid={testid}>
      {items.map((j) => <JobRow key={j.id} j={j} showEffect={showEffect} />)}
    </ul>
  );
  return flat ? list : <Card className="p-0">{list}</Card>;
}

function JobRow({ j, showEffect }: { j: Job; showEffect?: boolean }) {
  return (
    <li data-job={j.id} className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3 sm:flex-nowrap">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-medium text-[var(--ink)]" data-testid="job-name">{jobName(j.kind)}</span>
          <JobState state={j.state} />
          {jobNotes(j).map((n) => (
            <Badge key={n.key} tone={n.tone} className="opacity-80" data-note={n.key}>{n.label}</Badge>
          ))}
        </div>
        <div className="mt-0.5 flex flex-wrap gap-x-2 text-xs text-muted-foreground">
          <Mono className="text-[11.5px]">{j.kind}</Mono>
          <span>#{j.id}</span>
          <span>tries {j.attempts}/{j.max_attempts}</span>
          <span>{jobTiming(j)}</span>
        </div>
        {j.last_error ? <p className="mt-1 line-clamp-4 max-w-[48rem] break-words text-[12.5px] text-[var(--bad-ink)] sm:line-clamp-none" title={j.last_error}>{j.last_error}</p> : null}
        {showEffect && jobEffect(j.kind) ? <p className="mt-1 text-[12.5px] text-muted-foreground">{jobEffect(j.kind)}</p> : null}
      </div>
      <JobActions job={j} />
    </li>
  );
}

function jobTiming(j: Job): string {
  // started_at is when the run was claimed. A job claimed before it was recorded has none: updated_at, as before.
  if (j.state === "running") return `started ${fmtAgo(j.started_at ?? j.updated_at)}`;
  if (j.state === "queued") {
    const at = j.run_at ? new Date(j.run_at).getTime() : NaN;
    return !Number.isNaN(at) && at > Date.now() ? `next run ${fmtAgo(j.run_at)} (${fmtTime(j.run_at)})` : `waiting since ${fmtTime(j.run_at)}`;
  }
  return `finished ${fmtAgo(j.finished_at)}`;
}

/** Retry and Dismiss for a failed job; nothing for other jobs. */
export function JobActions({ job, newest = false }: { job: Job; newest?: boolean }) {
  const { retry, dismiss } = useJobActions();
  const id = job.id;
  if (id === undefined || (!canRetry(job) && !canDismiss(job))) return null;
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5">
      {canRetry(job) ? (
        <Button size="xs" variant="outline" data-testid={`job-retry-${job.id}`} disabled={retry.isPending} onClick={() => retry.mutate(id)} title="Queue a fresh run with the same kind and arguments">
          <RotateCcw /> {newest ? "Retry newest" : "Retry"}
        </Button>
      ) : null}
      {!newest && canDismiss(job) ? (
        <Button size="xs" variant="ghost" data-testid={`job-dismiss-${job.id}`} disabled={dismiss.isPending} onClick={() => dismiss.mutate(id)} title="Hide from Needs attention; the job stays in the history">
          <EyeOff /> Dismiss
        </Button>
      ) : null}
    </span>
  );
}
