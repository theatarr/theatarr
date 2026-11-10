// The Add collection dialog of /collections: search TMDb for a collection, choose its libraries, and add it (POST
// /collections). A monitored collection's missing movies are added in the same request. A result that is already on
// the page is not added again: choosing it closes the dialog and shows its row.
import { Library, Loader2, X } from "lucide-react";
import { useRef, useState } from "react";
import { useDebounced } from "@/api/add";
import { useAddCollection, useCollectionSearch, type CollectionAdd, type CollectionHit, type CollectionRefreshed } from "@/api/collections";
import { ErrorState } from "@/components/page";
import { Chip } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export const AVAILABILITY = [["announced", "Announced"], ["inCinemas", "In cinemas"], ["released", "Released"]] as const;

export const NO_LIBRARY = "None: only an active movie library that no other library follows can take a collection's movies";

export function AddCollectionDialog({ libraries, onClose, onAdded, onExisting }: {
  libraries: Record<string, string>;
  onClose: () => void;
  onAdded: (r: CollectionRefreshed) => void;
  /** An already added collection was chosen: show its row. */
  onExisting: (tmdbID: number) => void;
}) {
  const keys = Object.keys(libraries);
  const box = useRef<HTMLInputElement>(null);
  const [q, setQ] = useState("");
  const [picked, setPicked] = useState<CollectionHit | null>(null);
  const [versions, setVersions] = useState<string[]>(keys.length === 1 ? keys : []);
  const [monitored, setMonitored] = useState(keys.length > 0);
  const [searchOnAdd, setSearchOnAdd] = useState(true);
  const [minimum, setMinimum] = useState<NonNullable<CollectionAdd["minimum_availability"]>>("released");
  const settled = useDebounced(q, 300).trim();
  const search = useCollectionSearch(settled);
  const add = useAddCollection();
  const needsLibrary = monitored && versions.length === 0;
  const submit = () => {
    if (!picked || needsLibrary) return;
    add.mutate({ tmdb_id: picked.tmdb_id, versions, monitored, search_on_add: searchOnAdd, minimum_availability: minimum },
      { onSuccess: r => { onAdded(r); onClose(); } });
  };
  return <Modal label="Add collection" onClose={onClose} testid="add-collection" initialFocus={box}
    className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
    backdropClassName="absolute inset-0 bg-[var(--scrim)] animate-in fade-in-0"
    panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-xl sm:rounded-[8px]">
    <div className="relative border-b border-[var(--line)] p-4 pr-12">
      <h2 className="font-heading text-[22px] font-medium leading-tight text-[var(--ink)]">Add collection</h2>
      <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" className="absolute right-3 top-3"><X className="size-4" /></Button>
    </div>
    <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 text-sm">
      {picked ? <>
        <div className="flex items-center gap-3">
          <Poster src={picked.poster} />
          <div className="min-w-0 flex-1">
            <div className="font-semibold text-[var(--ink)] [overflow-wrap:anywhere]">{picked.name}</div>
            {picked.overview ? <p className="line-clamp-2 text-xs text-muted-foreground">{picked.overview}</p> : null}
          </div>
          <Button variant="outline" size="sm" onClick={() => setPicked(null)} disabled={add.isPending}>Change</Button>
        </div>
        <fieldset className="flex flex-wrap items-center gap-1.5">
          <legend className="mb-1.5 font-medium text-[var(--ink)]">Libraries</legend>
          {keys.map(key => {
            const on = versions.includes(key);
            return <button key={key} type="button" aria-pressed={on} title={key} onClick={() => setVersions(on ? versions.filter(k => k !== key) : [...versions, key])}
              className={cn("rounded-[3px] border px-2 py-0.5 text-xs", on ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line-2)] text-[var(--ink-3)]")}>{libraries[key]}</button>;
          })}
          {keys.length === 0 ? <span className="text-xs text-muted-foreground">{NO_LIBRARY}. The collection can still be added without monitoring.</span> : null}
        </fieldset>
        <div className="flex flex-wrap items-center gap-4">
          <label className="flex items-center gap-2">
            <Checkbox checked={monitored} disabled={keys.length === 0} onChange={e => setMonitored(e.target.checked)} />
            Monitor: add its missing movies now and as new ones are announced
          </label>
          <label className="flex items-center gap-2">
            <Checkbox checked={searchOnAdd} onChange={e => setSearchOnAdd(e.target.checked)} />
            Search on add
          </label>
          <label className="flex items-center gap-2">Minimum availability
            <Select className="w-auto" value={minimum} onChange={e => setMinimum(e.target.value as typeof minimum)}>
              {AVAILABILITY.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </Select>
          </label>
        </div>
        {needsLibrary ? <p className="text-xs text-muted-foreground">Choose a library to monitor this collection.</p> : null}
        {add.error ? <ErrorState error={add.error} /> : null}
      </> : <>
        <Input ref={box} aria-label="Search TMDb collections" placeholder="Search TMDb for a collection" value={q} onChange={e => setQ(e.target.value)} />
        {search.error ? <ErrorState error={search.error} /> : null}
        {settled.length < 2 ? <p className="text-xs text-muted-foreground">Type a collection's name, for example "Alien".</p>
          : search.isPending ? <p className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" aria-hidden />Searching TMDb</p>
          : search.data && search.data.results.length === 0 ? <p className="text-xs text-muted-foreground">TMDb has no collection by that name.</p> : null}
        {settled.length >= 2 && search.data ? <ul className="divide-y divide-[var(--line)]">{search.data.results.map(h =>
          <li key={h.tmdb_id}>
            <button type="button" data-testid={`collection-hit-${h.tmdb_id}`} className="flex w-full items-center gap-3 py-2 text-left hover:bg-[var(--sunken)]"
              onClick={() => { if (h.added) { onExisting(h.tmdb_id); onClose(); } else setPicked(h); }}>
              <Poster src={h.poster} />
              <span className="min-w-0 flex-1">
                <span className="block font-medium text-[var(--ink)] [overflow-wrap:anywhere]">{h.name}</span>
                {h.overview ? <span className="line-clamp-2 text-xs text-muted-foreground">{h.overview}</span> : null}
              </span>
              {h.added ? <Chip tone="ghost">Already added</Chip> : null}
            </button>
          </li>)}</ul> : null}
      </>}
    </div>
    <div className="flex justify-end gap-2 border-t border-[var(--line)] p-4">
      <Button variant="outline" onClick={onClose}>Cancel</Button>
      <Button onClick={submit} disabled={!picked || needsLibrary || add.isPending}>{add.isPending ? <Loader2 className="animate-spin" aria-hidden /> : null}Add</Button>
    </div>
  </Modal>;
}

function Poster({ src }: { src?: string }) {
  return src ? <img src={src} alt="" loading="lazy" className="h-[60px] w-10 shrink-0 rounded-[4px] object-cover ring-1 ring-[var(--line)]" />
    : <div className="grid h-[60px] w-10 shrink-0 place-items-center rounded-[4px] bg-[var(--sunken)] text-[var(--ink-3)]"><Library className="size-4" aria-hidden /></div>;
}
