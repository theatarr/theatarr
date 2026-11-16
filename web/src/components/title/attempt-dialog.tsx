// One failed download of the title, opened from the hero's "Details" (docs/design/ui.md, Title page): when it failed,
// why in plain sentences (the server's reason and reason_detail) with the stored text folded under Technical details,
// then its provider attempts as Activity's panel lists them (GET activity/downloads/{id}/attempts). For callers who
// see files.
import { useActivityAttempts } from "@/api/activity";
import { ProviderTag } from "@/components/provider-pills";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { attemptCodeWords, attemptMessageWords, attemptOutcomeWords } from "@/lib/router-words";
import { fmtTime } from "@/lib/format";
import { Link } from "react-router";

export function AttemptDialog({ downloadId, library, words, onClose }: { downloadId: number; library: string; words: string; onClose: () => void }) {
  const q = useActivityAttempts(downloadId);
  const attempts = q.data?.attempts ?? [];
  const reason = q.data?.reason;
  // words is "Last upgrade attempt Nov 9: not imported: …"; with the reason below, the heading keeps when.
  const when = reason ? words.split(": ")[0] : words;
  return (
    <Modal label={`${library}: failed download`} onClose={onClose} testid="attempt-dialog" className="z-[70] flex items-end justify-center sm:items-start sm:p-6 sm:pt-[10vh]"
      backdropClassName="absolute inset-0 bg-[var(--scrim)]"
      panelClassName="relative flex max-h-[85dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] sm:max-w-lg sm:rounded-[8px]">
      <div className="border-b border-[var(--line)] p-4">
        <h2 className="text-lg font-semibold text-[var(--ink)]">{library}</h2>
        <p className="mt-0.5 text-sm text-[var(--warn-ink)]">{when}</p>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4 text-sm">
        {reason ? (
          <div className="mb-4" data-testid="attempt-reason">
            <p className="font-medium text-[var(--ink)]">{reason}</p>
            {q.data?.reason_detail ? <p className="mt-0.5 text-[var(--ink-2)]">{q.data.reason_detail}</p> : null}
            {q.data?.error ? (
              <details className="mt-1.5">
                <summary className="cursor-pointer text-xs text-primary">Technical details</summary>
                <p className="mt-1 whitespace-pre-wrap break-words font-mono text-xs text-muted-foreground">{q.data.error}</p>
              </details>
            ) : null}
          </div>
        ) : null}
        <p className="mb-2 text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--ink-3)]">Provider attempts</p>
        {q.isPending ? <p role="status" className="text-muted-foreground">Loading…</p> : q.error ? <p role="alert">Could not load the attempts.</p> : attempts.length === 0 ? (
          <p className="text-muted-foreground">None recorded.</p>
        ) : (
          <ul className="space-y-1.5" data-testid="attempt-list">
            {attempts.map((a, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-x-2">
                {a.provider ? <ProviderTag account={a.provider} className="font-medium" /> : null}
                <Badge tone={a.outcome === "accepted" ? "positive" : a.outcome === "deferred" ? "warning" : a.outcome === "skipped" ? "neutral" : "negative"} title={[a.outcome, a.code].filter(Boolean).join(" · ")}>
                  {attemptOutcomeWords(a.outcome)}
                  {a.code && attemptCodeWords(a.code).toLowerCase() !== attemptOutcomeWords(a.outcome).toLowerCase() ? ` · ${attemptCodeWords(a.code)}` : ""}
                </Badge>
                <span className="text-xs text-muted-foreground">{fmtTime(a.at)}</span>
                {a.message ? <span className="w-full break-words text-xs text-muted-foreground">{attemptMessageWords(a.message)}</span> : null}
              </li>
            ))}
          </ul>
        )}
      </div>
      <div className="flex justify-end gap-2 border-t border-[var(--line)] p-3">
        <Button size="sm" variant="ghost" asChild><Link to="/activity/history">Activity › History</Link></Button>
        <Button size="sm" onClick={onClose}>Close</Button>
      </div>
    </Modal>
  );
}
