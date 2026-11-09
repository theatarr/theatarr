// The unsaved-changes bar of a settings form (docs/design/ui.md "Settings forms"): the change count with Discard and
// Save. With nothing changed it is the form's plain button row; with changes it sticks to the bottom of the screen
// (above the phone tab bar) while its form is in view.
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export const changeCount = (n: number) => n === 1 ? "1 unsaved change" : `${n} unsaved changes`;

export function UnsavedBar({ changes, label, children }: { changes: number; label?: string; children: ReactNode }) {
  const dirty = changes > 0;
  return (
    <div
      role={dirty ? "region" : undefined}
      aria-label={dirty ? `Unsaved changes${label ? `: ${label}` : ""}` : undefined}
      data-testid="unsaved-bar"
      data-dirty={dirty || undefined}
      className={cn(
        "mt-4 flex flex-wrap items-center justify-end gap-2",
        dirty && "sticky bottom-[calc(4rem+env(safe-area-inset-bottom))] z-20 -mx-2 rounded-[6px] border border-[var(--line-2)] bg-[var(--surface)] px-3 py-2 shadow-[var(--shadow-2)] md:bottom-3",
      )}
    >
      {dirty ? <span className="mr-auto text-sm font-medium text-[var(--ink)]" data-testid="unsaved-count" aria-live="polite">{changeCount(changes)}</span> : null}
      {children}
    </div>
  );
}
