// The confirmation every recovery action opens (docs/design/ui.md "Recovery actions"): the action's scope spelled out,
// what it will do and what it leaves alone (lib/ops.ts), optional choices, then Cancel and the confirm button. A
// centred dialog on desktop and a bottom sheet on phones, on the shared Modal (no Radix dialog: CSP).
import { Check, Loader2, ShieldCheck, X } from "lucide-react";
import { useRef, type ReactNode } from "react";
import { Link } from "react-router";
import { ApiError } from "@/api/client";
import { usePerms } from "@/api/library";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Modal } from "@/components/ui/modal";
import type { Scope } from "@/lib/ops";

export function errText(e: unknown): string {
  return e instanceof ApiError ? e.message : String((e as Error)?.message ?? e);
}

/** The scope's cleanup line: its page as a link, or as plain words for an account that cannot open an admin page. */
function Cleanup({ cleanup }: { cleanup: NonNullable<Scope["cleanup"]> }) {
  const perms = usePerms();
  return (
    <p className="mt-2 text-xs leading-5 text-[var(--ink-3)]" data-testid="confirm-cleanup">
      {cleanup.text}{" "}
      {cleanup.admin && !perms.admin ? cleanup.label : <Link className="text-primary hover:underline" to={cleanup.to}>{cleanup.label}</Link>}
      .
    </p>
  );
}

export function ConfirmDialog({
  scope,
  subject,
  onConfirm,
  onClose,
  busy,
  error,
  done,
  doneWarning = false,
  danger = true,
  children,
  testid,
  does,
}: {
  scope: Scope;
  /** Replaces the "This will" list (the settings confirmation groups the server's effects by library). */
  does?: ReactNode;
  /** What the action is about (a title, a release), shown above the heading. */
  subject?: ReactNode;
  onConfirm: () => void;
  onClose: () => void;
  busy?: boolean;
  error?: unknown;
  /** The server's answer once done: shown in place of the buttons. */
  done?: string;
  /** The action ran but did not do what was asked (an import now that imported nothing): shown as a warning. */
  doneWarning?: boolean;
  danger?: boolean;
  /** Choices (checkboxes) between the scope and the buttons. */
  children?: ReactNode;
  testid?: string;
}) {
  const cancel = useRef<HTMLButtonElement>(null);
  return (
    <Modal
      label={scope.title}
      onClose={onClose}
      testid={testid ?? "confirm-dialog"}
      initialFocus={cancel}
      className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] animate-in fade-in-0"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-lg sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        {subject ? <div className="text-xs font-medium text-[var(--ink-3)] [overflow-wrap:anywhere]">{subject}</div> : null}
        <h2 className="mt-0.5 font-heading text-[22px] font-medium leading-tight text-[var(--ink)]">{scope.title}</h2>
        <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" className="absolute right-3 top-3">
          <X className="size-4" />
        </Button>
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 text-sm">
        <div>
          <div className="mb-1.5 text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">This will</div>
          {does ?? (
            <ul className="space-y-1.5" data-testid="confirm-does">
              {scope.does.map((l, i) => (
                <li key={i} className="flex gap-2 text-[var(--ink)] [overflow-wrap:anywhere]">
                  <Check className="mt-0.5 size-4 shrink-0 text-[var(--accent-ink)]" aria-hidden />
                  {l}
                </li>
              ))}
            </ul>
          )}
        </div>
        <div>
          <div className="mb-1.5 text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Stays as it is</div>
          <ul className="space-y-1.5" data-testid="confirm-keeps">
            {scope.keeps.map((l, i) => (
              <li key={i} className="flex gap-2 text-muted-foreground [overflow-wrap:anywhere]">
                <ShieldCheck className="mt-0.5 size-4 shrink-0 text-[var(--ok-ink)]" aria-hidden />
                {l}
              </li>
            ))}
          </ul>
          {scope.cleanup ? <Cleanup cleanup={scope.cleanup} /> : null}
        </div>
        {children ? <div className="space-y-2 border-t border-[var(--line)] pt-3">{children}</div> : null}
        {scope.blocked ? (
          <p className="rounded-md border border-[var(--th-warning-border)] bg-[var(--warn-soft)] px-3 py-2 text-[var(--warn-ink)]" data-testid="confirm-blocked">
            {scope.blocked}
          </p>
        ) : null}
        {error ? (
          <div className="rounded-md border border-[var(--th-danger-border)] bg-[var(--bad-soft)] px-3 py-2 text-[var(--bad-ink)] [overflow-wrap:anywhere]" data-testid="confirm-error">
            {errText(error)}
            {error instanceof ApiError && error.detail?.length ? (
              <ul className="mt-1 list-disc pl-5 text-xs">{error.detail.map((d, i) => <li key={i}>{d}</li>)}</ul>
            ) : null}
          </div>
        ) : null}
        {done ? (
          <p
            className={
              doneWarning
                ? "rounded-md border border-[var(--th-warning-border)] bg-[var(--warn-soft)] px-3 py-2 text-[var(--warn-ink)] [overflow-wrap:anywhere]"
                : "rounded-md border border-[var(--th-success-border)] bg-[var(--ok-soft)] px-3 py-2 text-[var(--ok-ink)] [overflow-wrap:anywhere]"
            }
            data-testid="confirm-done"
            data-warning={doneWarning || undefined}
          >
            {done}
          </p>
        ) : null}
      </div>
      <div className="flex justify-end gap-2 border-t sm:rounded-b-[8px] border-[var(--line)] bg-[var(--bg)] p-3">
        <Button ref={cancel} variant="ghost" size="sm" onClick={onClose} data-testid="confirm-cancel">
          {done ? "Close" : "Cancel"}
        </Button>
        {done ? null : (
          <Button
            variant={danger ? "destructive" : "default"}
            size="sm"
            disabled={busy || !!scope.blocked}
            onClick={onConfirm}
            data-testid="confirm-ok"
          >
            {busy ? <Loader2 className="animate-spin" /> : null}
            {scope.confirm}
          </Button>
        )}
      </div>
    </Modal>
  );
}

/** A labelled checkbox for a dialog's choices. */
export function Choice({ checked, onChange, children, testid, disabled }: { checked: boolean; onChange: (v: boolean) => void; children: ReactNode; testid?: string; disabled?: boolean }) {
  return (
    <label className="flex cursor-pointer items-start gap-2 text-sm has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-70">
      <Checkbox className="mt-0.5" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} data-testid={testid} />
      <span>{children}</span>
    </label>
  );
}
