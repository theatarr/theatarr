// Report a problem (docs/design/users.md "Report a problem"): anyone who may request says a movie or an episode with
// a file is broken, picking what is wrong (picture, sound, subtitles, won't play, something else) with an optional
// note, and follows it under Requests › Problems. A dialog centred on desktop and a bottom sheet on phones, on the
// shared Modal; it opens above the episode window too.
import { CheckCircle2, Flag, Loader2, Send, X } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { usePerms } from "@/api/library";
import { REPORT_KINDS, useCreateReport, type CreateReport } from "@/api/reports";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { cn } from "@/lib/utils";

export type ReportTarget = { titleId: number; episodeId?: number; subject: string };

export function ReportSheet({ target, onClose }: { target: ReportTarget; onClose: () => void }) {
  const create = useCreateReport();
  const [kind, setKind] = useState<CreateReport["kind"] | null>(null);
  const [note, setNote] = useState("");
  const done = create.isSuccess;
  return (
    <Modal
      label={`Report a problem with ${target.subject}`}
      onClose={onClose}
      testid="report-sheet"
      className="z-[80] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] animate-in fade-in-0"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-md sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <div className="text-xs font-medium text-[var(--ink-3)] [overflow-wrap:anywhere]">{target.subject}</div>
        <h2 className="mt-0.5 text-lg font-semibold leading-tight text-[var(--ink)]">Report a problem</h2>
        <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      {done ? (
        <div className="space-y-4 p-4" data-testid="report-done">
          <div className="flex items-start gap-3 rounded-[5px] border border-[var(--th-success-border)] bg-[var(--ok-soft)] p-3 text-sm text-[var(--ok-ink)]">
            <CheckCircle2 className="mt-0.5 size-4 shrink-0" />
            <div>Thanks, it is reported. You can follow it under Requests › Problems.</div>
          </div>
          <div className="flex gap-2">
            <Button asChild>
              <Link to="/requests/problems" onClick={onClose}>View problems</Link>
            </Button>
            <Button variant="outline" onClick={onClose}>Close</Button>
          </div>
        </div>
      ) : (
        <>
          <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
            <section>
              <h3 className="mb-2 text-[13px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">What is wrong?</h3>
              <div className="grid grid-cols-2 gap-2" role="radiogroup" aria-label="What is wrong" data-testid="report-kinds">
                {REPORT_KINDS.map((k) => (
                  <button
                    key={k.id}
                    type="button"
                    role="radio"
                    aria-checked={kind === k.id}
                    data-kind={k.id}
                    onClick={() => setKind(k.id)}
                    className={cn(
                      "rounded-[5px] border border-[var(--line)] px-3 py-2 text-left text-sm font-medium text-[var(--ink-2)] transition hover:border-[var(--line-2)]",
                      kind === k.id && "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_10%,transparent)] text-[var(--accent-ink)]",
                    )}
                  >
                    {k.label}
                  </button>
                ))}
              </div>
            </section>
            <section>
              <label htmlFor="report-note" className="mb-2 block text-[13px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Note (optional)</label>
              <textarea
                id="report-note"
                value={note}
                maxLength={500}
                onChange={(e) => setNote(e.target.value)}
                rows={3}
                placeholder="Where it happens, on which device"
                className="w-full resize-none rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-2 text-sm outline-none placeholder:text-[var(--ink-3)] focus:border-[var(--th-baccent)]"
                data-testid="report-note"
              />
            </section>
          </div>
          <div className="flex items-center justify-between gap-3 border-t border-[var(--line)] p-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
            <span className="min-w-0 truncate text-xs text-[var(--bad-ink)]">{create.error ? (create.error instanceof ApiError ? create.error.message : "Could not report") : ""}</span>
            <div className="flex shrink-0 gap-2">
              <Button variant="ghost" onClick={onClose}>Cancel</Button>
              <Button
                disabled={!kind || create.isPending}
                onClick={() => kind && create.mutate({ title_id: target.titleId, episode_id: target.episodeId, kind, note: note.trim() || undefined })}
                data-testid="report-submit"
              >
                {create.isPending ? <Loader2 className="animate-spin" /> : <Send />}
                Report
              </Button>
            </div>
          </div>
        </>
      )}
    </Modal>
  );
}

/** "Report a problem": for accounts that may request, on a movie or an episode that has a file. */
export function ReportProblemButton({ target, className }: { target: ReportTarget; className?: string }) {
  const perms = usePerms();
  const [open, setOpen] = useState(false);
  if (!perms.request) return null;
  return (
    <>
      <Button variant="outline" size="sm" className={className} onClick={() => setOpen(true)} data-testid="report-problem">
        <Flag /> Report a problem
      </Button>
      {open ? <ReportSheet target={target} onClose={() => setOpen(false)} /> : null}
    </>
  );
}
