// A table's column picker (docs/design/ui.md "Library views"): show and reorder the optional columns, kept per user in
// this browser (lib/prefs.ts). OptionsDialog is the small dialog it opens in, shared with the posters' options. Moved
// unchanged from components/library/view-options.tsx, which re-exports it.
import { ArrowDown, ArrowUp, X } from "lucide-react";
import { useRef, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Modal } from "@/components/ui/modal";
import { swapColumns } from "@/lib/prefs";

export function OptionsDialog({ label, note, onClose, children }: { label: string; note: string; onClose: () => void; children: ReactNode }) {
  const done = useRef<HTMLButtonElement>(null);
  return (
    <Modal
      label={label}
      onClose={onClose}
      initialFocus={done}
      testid="view-options"
      className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] sm:max-w-md sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <h2 className="text-lg font-semibold text-[var(--ink)]">{label}</h2>
        <p className="text-sm text-muted-foreground">{note}</p>
        <button type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4 text-sm">{children}</div>
      <div className="flex justify-end border-t border-[var(--line)] p-3">
        <Button ref={done} size="sm" onClick={onClose}>Done</Button>
      </div>
    </Modal>
  );
}

/** Show and reorder a table's optional columns: label names the dialog, note says what always stays; columns are
 *  the ones this account may choose (prefs.order may hold more, which stay where they are). */
export function ColumnPicker<T extends string, P extends { order: T[]; shown: T[] }>({ label, note, columns, prefs, onChange, onClose }: { label: string; note: string; columns: { id: T; label: string; hint: string }[]; prefs: P; onChange: (p: P) => void; onClose: () => void }) {
  const ids = prefs.order.filter((id) => columns.some((c) => c.id === id));
  const toggle = (id: T, on: boolean) => onChange({ ...prefs, shown: on ? [...prefs.shown, id] : prefs.shown.filter((x) => x !== id) });
  return (
    <OptionsDialog label={label} note={note} onClose={onClose}>
      <ul className="divide-y divide-[var(--line)] border-y border-[var(--line)]" data-testid="column-list">
        {ids.map((id, i) => {
          const c = columns.find((x) => x.id === id)!;
          return (
            <li key={id} className="flex items-center gap-2 px-1 py-1.5">
              <label className="flex min-w-0 flex-1 cursor-pointer items-start gap-2">
                <Checkbox className="mt-0.5" checked={prefs.shown.includes(id)} onChange={(e) => toggle(id, e.target.checked)} />
                <span className="min-w-0">
                  <span className="block">{c.label}</span>
                  {/* A hint that only repeats the name says nothing. */}
                  {c.hint && c.hint !== c.label ? <span className="block text-xs text-[var(--ink-3)]">{c.hint}</span> : null}
                </span>
              </label>
              <Button variant="ghost" size="icon-sm" aria-label={`Move ${c.label} up`} disabled={i === 0} onClick={() => onChange(swapColumns(prefs, id, ids[i - 1]))}>
                <ArrowUp />
              </Button>
              <Button variant="ghost" size="icon-sm" aria-label={`Move ${c.label} down`} disabled={i === ids.length - 1} onClick={() => onChange(swapColumns(prefs, id, ids[i + 1]))}>
                <ArrowDown />
              </Button>
            </li>
          );
        })}
      </ul>
    </OptionsDialog>
  );
}
