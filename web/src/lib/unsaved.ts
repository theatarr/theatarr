// The unsaved edits of the forms on screen (settings panels and the editor sheets), so leaving the page can ask first
// (components/settings/unsaved-bar.tsx LeaveGuard). Each form reports its change count while it is mounted.
import { useEffect } from "react";
import type { Scope } from "@/lib/ops";

export const changeCount = (n: number) => n === 1 ? "1 unsaved change" : `${n} unsaved changes`;

/** The question before unsaved edits are dropped (leaving the page, closing an editor). */
export const discardScope = (n: number, leaving: boolean): Scope => ({
  title: leaving ? "Leave without saving?" : "Close without saving?",
  does: [`Drop ${changeCount(n)}.`],
  keeps: ["The saved settings stay as they are."],
  confirm: leaving ? "Leave without saving" : "Close without saving",
});

const forms = new Map<symbol, number>();

/** The unsaved changes of every mounted form. */
export function unsavedTotal(): number {
  let n = 0;
  for (const c of forms.values()) n += c;
  return n;
}

/** Report a form's unsaved change count while it is mounted. */
export function useUnsavedChanges(changes: number) {
  useEffect(() => {
    if (changes <= 0) return;
    const id = Symbol("form");
    forms.set(id, changes);
    return () => {
      forms.delete(id);
    };
  }, [changes]);
}
