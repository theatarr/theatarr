// A collection's settings, behind Edit on its page: monitoring, the libraries it adds to, search on add and minimum
// availability. Each control saves at once (PUT /collections/{id}), as the rows of the Collections list did.
import { X } from "lucide-react";
import { useSetCollection, type Collection, type CollectionUpdate } from "@/api/collections";
import { AVAILABILITY, NO_LIBRARY } from "@/components/collections/add-collection";
import { ErrorState } from "@/components/page";
import { MonitorSwitch } from "@/components/title/manage-bits";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Modal } from "@/components/ui/modal";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export const collectionName = (c: Pick<Collection, "name" | "tmdb_id">) => c.name || `Collection ${c.tmdb_id}`;

/** "2 of 3 in the library (1 removed) · 2 available": in the library, removed from every library, with a file. */
export const collectionCounts = (c: Pick<Collection, "held" | "total" | "removed" | "available">) =>
  `${c.held} of ${c.total} in the library${c.removed ? ` (${c.removed} removed)` : ""} · ${c.available} available`;

export function CollectionSettings({ c, libraries, onClose }: { c: Collection; libraries: Record<string, string>; onClose: () => void }) {
  const set = useSetCollection();
  const busy = set.isPending;
  const save = (u: CollectionUpdate) => set.mutate({ id: c.tmdb_id, ...u });
  const name = collectionName(c);
  const toggle = (key: string) => save({ versions: c.versions.includes(key) ? c.versions.filter(k => k !== key) : [...c.versions, key] });
  return <Modal label={`Edit ${name}`} onClose={onClose} testid="collection-settings"
    className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
    backdropClassName="absolute inset-0 bg-[var(--scrim)] animate-in fade-in-0"
    panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-xl sm:rounded-[8px]">
    <div className="relative border-b border-[var(--line)] p-4 pr-12">
      <h2 className="font-heading text-[22px] font-medium leading-tight text-[var(--ink)] [overflow-wrap:anywhere]">Edit {name}</h2>
      <p className="mt-1 text-xs text-muted-foreground">Changes save at once.</p>
      <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" className="absolute right-3 top-3"><X className="size-4" /></Button>
    </div>
    <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 text-sm">
      {set.error ? <ErrorState error={set.error} /> : null}
      <div className="flex items-center gap-3">
        <MonitorSwitch on={c.monitored} disabled={busy || (!c.monitored && c.versions.length === 0)}
          reason={!c.monitored && c.versions.length === 0 ? "Choose a library first" : undefined} label={`Monitor ${name}`} onChange={monitored => save({ monitored })} />
        <span className="text-[var(--ink)]">Monitor: add its missing movies to the libraries below at each list refresh, or at once with Add missing movies now</span>
      </div>
      <fieldset className="flex flex-wrap items-center gap-1.5" disabled={busy}>
        <legend className="mb-1.5 font-medium text-[var(--ink)]">Libraries</legend>
        {Object.entries(libraries).map(([key, label]) => {
          const on = c.versions.includes(key);
          // A monitored collection keeps at least one library.
          const last = on && c.monitored && c.versions.length === 1;
          return <button key={key} type="button" aria-pressed={on} disabled={last} onClick={() => toggle(key)} title={last ? "A monitored collection needs a library" : key}
            className={cn("rounded-[3px] border px-2 py-0.5 text-xs", on ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line-2)] text-[var(--ink-3)]")}>{label}</button>;
        })}
        {Object.keys(libraries).length === 0 ? <span className="text-xs text-muted-foreground">{NO_LIBRARY}</span>
          : c.versions.length === 0 ? <span className="text-xs text-muted-foreground">Choose a library to monitor this collection</span> : null}
      </fieldset>
      <div className="flex flex-wrap items-center gap-4">
        <label className="flex items-center gap-2">
          <Checkbox checked={c.search_on_add} disabled={busy} onChange={e => save({ search_on_add: e.target.checked })} />
          Search on add
        </label>
        <label className="flex items-center gap-2">Minimum availability
          <Select className="w-auto" value={c.minimum_availability} disabled={busy} onChange={e => save({ minimum_availability: e.target.value as CollectionUpdate["minimum_availability"] })}>
            {AVAILABILITY.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            {c.minimum_availability === "tba" ? <option value="tba">TBA</option> : null}
          </Select>
        </label>
      </div>
    </div>
    <div className="flex justify-end border-t border-[var(--line)] p-3"><Button variant="outline" onClick={onClose}>Done</Button></div>
  </Modal>;
}
