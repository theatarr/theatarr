// Where keyboard focus goes when a record's sheet closes (docs/design/ui.md, accessibility). The sheet (components/ui/
// sheet.tsx) gives focus back to the control that opened it; after a delete that control is gone, and editorReturn
// (lib/settings.ts) picks a neighbour's Edit button, or Add. The functions take the few DOM members they use, so the
// node-only tests can pass stand-ins.
import type { EditorReturn } from "@/lib/settings";

type Focusable = { focus: (o?: FocusOptions) => void };

/** The selector of an editor's opener in the list: a record's Edit (or View) button, or the Add button. */
export function returnSelector(to: EditorReturn): string {
  if (to === "add") return '[data-testid="add"]';
  return `[data-record="${to.edit.replace(/["\\]/g, "\\$&")}"] [data-testid="edit"]`;
}

/** The element to focus after an editor closed: the control returnSelector names, or root itself (tabIndex -1) when
 * that control is missing or disabled (Add in yaml mode). */
export function returnTarget<T extends Focusable>(root: T & { querySelector: (s: string) => unknown }, to: EditorReturn): T | Focusable {
  const el = root.querySelector(returnSelector(to)) as (Focusable & { disabled?: boolean }) | null;
  return el && !el.disabled ? el : root;
}
