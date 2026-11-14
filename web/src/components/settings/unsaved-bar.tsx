// The unsaved-changes bar of a settings form (docs/design/ui.md "Settings forms"): the change count with Discard and
// Save. With nothing changed it is the form's plain button row; with changes it sticks to the bottom of the screen
// (above the phone tab bar) while its form is in view.
import { useContext, useEffect, useState, type ReactNode } from "react";
import { UNSAFE_DataRouterContext, useBlocker } from "react-router";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { changeCount, discardScope, unsavedTotal, useUnsavedChanges } from "@/lib/unsaved";
import { cn } from "@/lib/utils";

export { changeCount };

/** Asks before a page with unsaved settings edits is left: another page in the app (Back included) or a reload or
 * closed tab (the browser's own question). Tabs, ?edit= and anchors on the same page are not leaving it. Mounted once
 * per page; a no-op outside a data router (unit tests render pages in a plain MemoryRouter). */
export function LeaveGuard() {
  const data = useContext(UNSAFE_DataRouterContext);
  useEffect(() => {
    const onUnload = (e: BeforeUnloadEvent) => {
      if (unsavedTotal() > 0) e.preventDefault();
    };
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  }, []);
  return data ? <Blocker /> : null;
}

function Blocker() {
  const [count, setCount] = useState(0);
  const blocker = useBlocker(({ currentLocation, nextLocation }) => {
    const n = unsavedTotal();
    // Another page is leaving it; so is a switch to or from a raw editor (?raw=1), which replaces the form.
    const raw = (search: string) => new URLSearchParams(search).get("raw");
    if (n === 0 || (currentLocation.pathname === nextLocation.pathname && raw(currentLocation.search) === raw(nextLocation.search))) return false;
    setCount(n);
    return true;
  });
  if (blocker.state !== "blocked") return null;
  return <ConfirmDialog testid="leave-confirm" scope={discardScope(count, true)} onClose={() => blocker.reset()} onConfirm={() => blocker.proceed()} />;
}

export function UnsavedBar({ changes, label, children }: { changes: number; label?: string; children: ReactNode }) {
  const dirty = changes > 0;
  // Leaving the page with these changes asks first (LeaveGuard).
  useUnsavedChanges(changes);
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
