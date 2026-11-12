// The library views' options (docs/design/ui.md "Library views"): a table's column picker (show and reorder the
// extra columns; the library table's and the release grid's) and the posters' options (size and the details under each
// poster). Both are kept per user in this browser (lib/copies.ts prefs).
import { ArrowDown, ArrowUp, X } from "lucide-react";
import { useRef, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Modal } from "@/components/ui/modal";
import { POSTER_DETAILS, swapColumns, type PosterPrefs, type PosterSize } from "@/lib/copies";
import { cn } from "@/lib/utils";

function OptionsDialog({ label, note, onClose, children }: { label: string; note: string; onClose: () => void; children: ReactNode }) {
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
                  <span className="block text-xs text-[var(--ink-3)]">{c.hint}</span>
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

const SIZES: PosterSize[] = ["small", "medium", "large"];

export function PosterOptions({ prefs, seeFiles, onChange, onClose }: { prefs: PosterPrefs; seeFiles: boolean; onChange: (p: PosterPrefs) => void; onClose: () => void }) {
  return (
    <OptionsDialog label="Poster options" note="Choose the details under each poster. The library bars, the title and each library's ownership always stay." onClose={onClose}>
      <div>
        <div className="mb-1.5 text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Poster size</div>
        <div role="group" aria-label="Poster size" className="flex gap-2">
          {SIZES.map((s) => (
            <button
              key={s}
              type="button"
              aria-pressed={prefs.size === s}
              onClick={() => onChange({ ...prefs, size: s })}
              className={cn(
                "flex-1 rounded-lg border border-[var(--line)] px-3 py-1.5",
                prefs.size === s && "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_16%,transparent)] text-[var(--accent-ink)]",
              )}
            >
              {s.charAt(0).toUpperCase() + s.slice(1)}
            </button>
          ))}
        </div>
        <p className="mt-1 text-xs text-[var(--ink-3)]">On a phone, small fits three across; medium and large fit two.</p>
      </div>
      <div className="space-y-1.5">
        {POSTER_DETAILS.filter((d) => seeFiles || !d.seeFiles).map((d) => (
          <label key={d.id} className="flex cursor-pointer items-center gap-2">
            <Checkbox
              checked={prefs.details.includes(d.id)}
              onChange={(e) => onChange({ ...prefs, details: e.target.checked ? [...prefs.details, d.id] : prefs.details.filter((x) => x !== d.id) })}
            />
            {d.label}
          </label>
        ))}
      </div>
    </OptionsDialog>
  );
}
