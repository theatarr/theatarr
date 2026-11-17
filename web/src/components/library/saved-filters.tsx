// The filter sheet's Saved filters (docs/design/ui.md "Library views"): a list's filters and sort kept under a name,
// per list and per user in this browser (lib/prefs.ts, theatarr.library.saved-filters.<list>.<user>). Picking one
// applies it, and the menu names it while the list shows it; Update “name” overwrites the last one picked with the
// current filters and sort; Delete “name” forgets it; Save as… keeps the current ones under a new name (the same name
// replaces that one). A saved filter is the list's query string
// (lib/library savedQuery); a field it no longer recognises is ignored when it is applied.
import { Bookmark } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SELECT_CLASS } from "@/components/ui/select";
import { readPref, useListPref } from "@/lib/prefs";
import { cn } from "@/lib/utils";

export type SavedFilter = { name: string; query: string };

/** The stored list, with anything malformed left out. */
export function normalizeSaved(v: unknown): SavedFilter[] {
  if (!Array.isArray(v)) return [];
  return v.filter((x): x is SavedFilter => !!x && typeof x === "object" && typeof x.name === "string" && x.name.trim() !== "" && typeof x.query === "string");
}

export function SavedFilters({ list, uid, current, apply }: {
  /** The list (movies, series, anime): each keeps its own. */
  list: string;
  uid: number | undefined;
  /** The current filters and sort as a saved filter's query (lib/library savedQuery). */
  current: string;
  apply: (query: string) => void;
}) {
  const what = `saved-filters.${list}`;
  const [saved, store] = useListPref<SavedFilter[]>(what, uid, () => readPref(what, uid, normalizeSaved));
  // The saved filter Update and Delete act on: the last one picked or saved here, else the one the list showed when
  // the sheet opened (or when the user's saved filters loaded). It stays the target while the filters are edited.
  const [picked, setPicked] = useState<string | null>(null);
  const [naming, setNaming] = useState<string | null>(null);
  const opened = saved.find((s) => s.query === current)?.name ?? null;
  useEffect(() => {
    setPicked((p) => p ?? opened);
  }, [uid]); // eslint-disable-line react-hooks/exhaustive-deps
  const pick = saved.find((s) => s.name === picked) ?? saved.find((s) => s.query === current);
  // The menu names it only while the list shows it, so picking it again after a change applies it again.
  const shown = pick && pick.query === current ? pick.name : "";
  const saveAs = () => {
    const name = (naming ?? "").trim();
    if (!name) return;
    store([...saved.filter((s) => s.name !== name), { name, query: current }]);
    setPicked(name);
    setNaming(null);
  };
  return (
    <div className="mb-4 space-y-2 border-b border-[var(--line)] pb-4" data-testid="saved-filters">
      <div className="flex items-center gap-1.5 text-xs font-bold uppercase tracking-[0.05em] text-[var(--ink-3)] [&>svg]:size-3.5">
        <Bookmark />
        Saved filters
      </div>
      {saved.length ? (
        <select
          aria-label="Saved filters"
          className={cn(SELECT_CLASS, "w-full")}
          value={shown}
          onChange={(e) => {
            const s = saved.find((x) => x.name === e.target.value);
            if (!s) return;
            setPicked(s.name);
            apply(s.query);
          }}
        >
          <option value="">Choose a saved filter</option>
          {saved.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}
        </select>
      ) : (
        <p className="text-xs text-[var(--ink-3)]">No saved filters yet.</p>
      )}
      {naming === null ? (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => setNaming("")}>Save as…</Button>
          {pick ? (
            <>
              <Button variant="outline" size="sm" disabled={pick.query === current} title={`Keep the current filters and sort as ${pick.name}`}
                onClick={() => store(saved.map((s) => (s.name === pick.name ? { ...s, query: current } : s)))}>
                Update “{pick.name}”
              </Button>
              <Button variant="outline" size="sm" onClick={() => {
                store(saved.filter((s) => s.name !== pick.name));
                setPicked(null);
              }}>
                Delete “{pick.name}”
              </Button>
            </>
          ) : null}
        </div>
      ) : (
        <form className="flex gap-2" onSubmit={(e) => {
          e.preventDefault();
          saveAs();
        }}>
          <Input aria-label="Name" placeholder="Name" autoFocus value={naming} onChange={(e) => setNaming(e.target.value)} className="h-8 min-w-0 flex-1" />
          <Button type="submit" size="sm" disabled={!naming.trim()}>Save</Button>
          <Button type="button" variant="ghost" size="sm" onClick={() => setNaming(null)}>Cancel</Button>
        </form>
      )}
      {naming !== null && saved.some((s) => s.name === naming.trim()) ? <p className="text-xs text-[var(--ink-3)]">Replaces the saved filter of that name.</p> : null}
    </div>
  );
}
