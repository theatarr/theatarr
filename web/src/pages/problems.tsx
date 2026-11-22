// Requests › Problems (/requests/problems; docs/design/users.md "Report a problem"). Everyone sees their own problem
// reports with the status and the manager's reply. Managers (manage_requests) see every open report: what is wrong,
// the note, who and when, and for those who see files the files the title's libraries hold for it now, with Replace
// file… (unlink, blocklist the release in each library that loses it, search again; offered only when that search can
// run, else the row says why and links to the title page) and Mark fixed with an optional reply; a choice above the
// list (Open, My reports) shows a manager their own reports, fixed ones too.
import { Check, CheckCircle2, Flag, Loader2, RefreshCw, Send } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { usePerms } from "@/api/library";
import { replaceScope, reportSubject, useFixReport, useReplaceReported, useReports, type ProblemReport, type ReportFile } from "@/api/reports";
import { Empty, ErrorState, Loading, Mono } from "@/components/page";
import { Poster } from "@/components/search/search-results";
import { Badge, Tag } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { fmtAgo, nameWithoutYear } from "@/lib/format";
import { cn } from "@/lib/utils";

export function ProblemsList() {
  const perms = usePerms();
  // Managers choose between every open report and their own reports (open and fixed, with the reply).
  const [mine, setMine] = useState(false);
  const manager = perms.manageRequests && !mine;
  const q = useReports(mine);
  const items = q.data?.items ?? [];
  return (
    <>
      {perms.manageRequests ? (
        <div className="mb-4 flex flex-wrap gap-1.5" role="group" aria-label="Which reports">
          {[{ id: false, label: "Open" }, { id: true, label: "My reports" }].map((c) => (
            <button
              key={c.label}
              type="button"
              aria-pressed={mine === c.id}
              onClick={() => setMine(c.id)}
              className={cn(
                "rounded-[4px] border border-[var(--line)] px-2.5 py-1 text-[13px] font-medium text-[var(--ink-3)] transition hover:border-[var(--line-2)]",
                mine === c.id && "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]",
              )}
            >
              {c.label}
            </button>
          ))}
        </div>
      ) : null}
      {q.isPending ? (
        <Loading />
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : items.length === 0 ? (
        <Card>
          <Empty icon={manager ? CheckCircle2 : Flag}>
            {manager ? "No open problem reports." : "No problem reports. Use Report a problem on a movie or an episode that does not play right."}
          </Empty>
        </Card>
      ) : (
        <ul className="space-y-3" data-testid="problem-list">
          {items.map((r) => (
            <li key={r.id}>
              <ProblemRow r={r} manager={manager} />
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

function titleLink(r: ProblemReport): string {
  return r.episode_id ? `/title/${r.title_id}?episode=${r.episode_id}` : `/title/${r.title_id}`;
}

function ProblemRow({ r, manager }: { r: ProblemReport; manager: boolean }) {
  const [fixing, setFixing] = useState(false);
  const who = r.user?.display_name || r.user?.username;
  return (
    <Card className="p-0" data-testid="problem-row" data-report-id={r.id} data-status={r.status}>
      <div className="flex gap-3 p-3 sm:gap-4 sm:p-4">
        <Poster src={r.poster} title={r.name} className="relative aspect-[2/3] w-14 shrink-0 self-start rounded-[4px] ring-1 ring-[var(--line)] sm:w-[68px]" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0">
              <div className="mb-1 flex flex-wrap items-center gap-1.5">
                <Tag className="uppercase tracking-wider">{r.kind_label}</Tag>
                <Badge tone={r.status === "open" ? "warning" : "positive"} data-testid="problem-status">{r.status === "open" ? "Open" : "Fixed"}</Badge>
              </div>
              <h3 className="text-[15px] font-semibold leading-snug text-[var(--ink)] [overflow-wrap:anywhere]">
                <Link to={titleLink(r)} className="hover:underline">
                  {nameWithoutYear(r.name, r.year)}
                  {r.year ? <span className="font-normal text-[var(--ink-3)]"> ({r.year})</span> : null}
                  {r.episode ? <span className="font-normal"> · {r.episode}{r.episode_name ? ` ${r.episode_name}` : ""}</span> : null}
                </Link>
              </h3>
            </div>
            {manager && r.status === "open" ? (
              <Button size="sm" variant="outline" onClick={() => setFixing(!fixing)} aria-expanded={fixing} data-testid="problem-fix">
                <Check /> Mark fixed
              </Button>
            ) : null}
          </div>
          <p className="mt-2 text-xs text-[var(--ink-3)]">
            {manager && who ? `${who} · ` : ""}reported {fmtAgo(r.created_at)}
            {r.status === "fixed" && r.resolved_at ? ` · fixed ${fmtAgo(r.resolved_at)}` : ""}
          </p>
          {r.note ? <p className="mt-1.5 text-sm text-[var(--ink)] [overflow-wrap:anywhere]">“{r.note}”</p> : null}
          {r.reply ? (
            <p className="mt-1.5 rounded-[5px] bg-[var(--ok-soft)] px-2.5 py-1.5 text-xs text-[var(--ok-ink)] [overflow-wrap:anywhere]" data-testid="problem-reply">
              {r.reply}
            </p>
          ) : null}
        </div>
      </div>
      {manager && r.files && r.files.length > 0 ? (
        <ul className="space-y-2 border-t border-[var(--line)] p-3 sm:p-4" data-testid="problem-files">
          {r.files.map((f) => (
            <FileRow key={f.link_id} r={r} f={f} />
          ))}
        </ul>
      ) : null}
      {fixing ? <FixForm r={r} onDone={() => setFixing(false)} /> : null}
    </Card>
  );
}

function FileRow({ r, f }: { r: ProblemReport; f: ReportFile }) {
  const perms = usePerms();
  const [confirm, setConfirm] = useState(false);
  const replace = useReplaceReported();
  const name = f.path.split("/").pop() ?? f.path;
  return (
    <li className="min-w-0 text-sm" data-testid="problem-file" data-link-id={f.link_id} data-replace={f.replace ? "1" : "0"}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <span title={f.path} className="block"><Mono className="text-[13px] [overflow-wrap:anywhere]">{name}</Mono></span>
          <p className="text-xs text-[var(--ink-3)] [overflow-wrap:anywhere]">
            {[f.libraries.join(", "), f.quality, f.episodes && f.episodes.length > 1 ? f.episodes.join(", ") : "", f.release_title].filter(Boolean).join(" · ")}
          </p>
        </div>
        {perms.manage && f.replace ? (
          <Button size="sm" variant="outline" onClick={() => setConfirm(true)} data-testid="problem-replace">
            <RefreshCw /> Replace file…
          </Button>
        ) : null}
      </div>
      {!f.replace && f.why ? (
        <p className="mt-1 text-xs text-[var(--warn-ink)]" data-testid="problem-why">
          {f.why}. <Link to={titleLink(r)} className="text-[var(--accent-ink)] hover:underline">Open the title</Link>
        </p>
      ) : null}
      {confirm ? (
        <ConfirmDialog
          testid="problem-replace-confirm"
          subject={`${reportSubject(r)} · ${name}`}
          scope={replaceScope(f)}
          busy={replace.isPending}
          error={replace.error}
          done={replace.data?.message}
          doneWarning={replace.data ? !replace.data.search.started : false}
          onClose={() => {
            setConfirm(false);
            replace.reset();
          }}
          onConfirm={() => replace.mutate({ id: r.id, link_id: f.link_id })}
        />
      ) : null}
    </li>
  );
}

function FixForm({ r, onDone }: { r: ProblemReport; onDone: () => void }) {
  const fix = useFixReport();
  const [reply, setReply] = useState("");
  return (
    <form
      className="flex flex-wrap items-center gap-2 border-t border-[var(--line)] p-3 sm:p-4"
      onSubmit={(e) => {
        e.preventDefault();
        fix.mutate({ id: r.id, reply: reply.trim() }, { onSuccess: onDone });
      }}
    >
      <Input value={reply} onChange={(e) => setReply(e.target.value)} placeholder="Reply (optional; the reporter sees it)" maxLength={500} className="min-w-0 flex-1 basis-56" aria-label="Reply" data-testid="fix-reply" autoFocus />
      <Button size="sm" type="submit" disabled={fix.isPending} data-testid="fix-confirm">
        {fix.isPending ? <Loader2 className="animate-spin" /> : <Send />} Mark fixed
      </Button>
      <Button size="sm" variant="ghost" type="button" onClick={onDone}>Back</Button>
      {fix.error ? <span className="text-xs text-[var(--bad-ink)]">{errText(fix.error)}</span> : null}
    </form>
  );
}
