// The library views' options (docs/design/ui.md "Library views"): the posters' options (size and the details under each
// poster), kept per user in this browser (lib/prefs.ts). The column picker moved to components/ui/column-picker.tsx and
// is re-exported here.
import { Checkbox } from "@/components/ui/checkbox";
import { ColumnPicker, OptionsDialog } from "@/components/ui/column-picker";
import { POSTER_DETAILS, type PosterPrefs, type PosterSize } from "@/lib/poster-options";
import { kindLabel, type CatalogId } from "@/lib/library";
import { cn } from "@/lib/utils";

export { ColumnPicker, OptionsDialog };

const SIZES: PosterSize[] = ["small", "medium", "large"];

export function PosterOptions({ catalog, prefs, seeFiles, onChange, onClose }: { catalog?: CatalogId; prefs: PosterPrefs; seeFiles: boolean; onChange: (p: PosterPrefs) => void; onClose: () => void }) {
  return (
    <OptionsDialog label="Poster options" note="Choose the size and what shows under each poster. The status bar and the library lines always show." onClose={onClose}>
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
            {kindLabel(catalog, d.id, d.label)}
          </label>
        ))}
      </div>
    </OptionsDialog>
  );
}
