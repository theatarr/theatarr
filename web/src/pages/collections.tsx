// /collections: TMDb movie collections, as Radarr's Movies › Collections (docs/design/arrview.md, Collections). Add
// collection searches TMDb and adds one; a monitored collection adds its missing movies to its libraries when it is
// added, on "Add missing movies now" and at each list refresh. Search on add starts the usual add-time search, which
// the download router gates. Managers and admins (GET/POST/PUT /collections).
import { useState } from "react";
import { Link } from "react-router";
import { Library, Plus } from "lucide-react";
import { refreshWords, useCollections, useRefreshCollection, useSetCollection, type Collection, type CollectionUpdate } from "@/api/collections";
import { useAuth } from "@/api/auth";
import { permsOf } from "@/api/library";
import { AddCollectionDialog, AVAILABILITY, NO_LIBRARY } from "@/components/collections/add-collection";
import { CollectionsTabs } from "@/components/collections/collections-tabs";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { MonitorSwitch } from "@/components/title/manage-bits";
import { Chip } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Forbidden } from "./library";
import { cn } from "@/lib/utils";
import { wordMatcher } from "@/lib/words";
import { plural } from "@/lib/format";

function CollectionRow({ c, libraries, save, busy, refresh }: { c: Collection; libraries: Record<string, string>; save: (u: CollectionUpdate) => void; busy: boolean;
  /** Add missing movies now; absent when TMDb is not set up. */
  refresh?: () => void }) {
  const name = c.name || `Collection ${c.tmdb_id}`;
  const toggle = (key: string) => save({ versions: c.versions.includes(key) ? c.versions.filter(k => k !== key) : [...c.versions, key] });
  return <li className="flex min-w-0 gap-3 px-3 py-3" data-testid={`collection-${c.tmdb_id}`}>
    {c.poster ? <img src={c.poster} alt="" loading="lazy" className="h-[72px] w-12 shrink-0 rounded-[4px] object-cover ring-1 ring-[var(--line)]" /> : <div className="grid h-[72px] w-12 shrink-0 place-items-center rounded-[4px] bg-[var(--sunken)] text-[var(--ink-3)]"><Library className="size-5" aria-hidden /></div>}
    <div className="min-w-0 flex-1 space-y-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <MonitorSwitch on={c.monitored} disabled={busy || (!c.monitored && c.versions.length === 0)}
          reason={!c.monitored && c.versions.length === 0 ? "Choose a library first" : undefined} label={`Monitor ${name}`} onChange={monitored => save({ monitored })} />
        <h2 className="min-w-0 font-semibold text-[var(--ink)] [overflow-wrap:anywhere]">{name}</h2>
        {/* In the library: the movie is in a library; available: it has a file there. */}
        <Chip tone={c.total > 0 && c.available >= c.total ? "ok" : "ghost"}>{c.held} of {c.total} in the library · {c.available} available</Chip>
        {c.monitored && refresh ? <Button variant="outline" size="xs" disabled={busy} onClick={refresh}>Add missing movies now</Button> : null}
      </div>
      {!c.name ? <p className="text-xs text-muted-foreground">Name and movies arrive with the next list refresh.</p> : null}
      <fieldset className="flex flex-wrap items-center gap-1.5 text-sm" disabled={busy}>
        <legend className="sr-only">Libraries for {name}</legend>
        <span className="mr-1 text-muted-foreground">Libraries</span>
        {Object.entries(libraries).map(([key, label]) => {
          const on = c.versions.includes(key);
          // A monitored collection keeps at least one library.
          const last = on && c.monitored && c.versions.length === 1;
          return <button key={key} type="button" aria-pressed={on} disabled={last} onClick={() => toggle(key)} title={last ? `${key}: a monitored collection needs a library` : key}
            className={cn("rounded-[3px] border px-2 py-0.5 text-xs", on ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line-2)] text-[var(--ink-3)]")}>{label}</button>;
        })}
        {Object.keys(libraries).length === 0 ? <span className="text-xs text-muted-foreground">{NO_LIBRARY}</span>
          : c.versions.length === 0 ? <span className="text-xs text-muted-foreground">Choose a library to monitor this collection</span> : null}
      </fieldset>
      <div className="flex flex-wrap items-center gap-4 text-sm">
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
  </li>;
}

export function CollectionsPage() {
  const me = useAuth();
  const perms = permsOf(me.data?.permissions);
  const q = useCollections(perms.manage);
  const set = useSetCollection();
  const refresh = useRefreshCollection();
  const [adding, setAdding] = useState(false);
  const [done, setDone] = useState("");
  const [filter, setFilter] = useState("");
  const [onlyMonitored, setOnlyMonitored] = useState(false);
  const [opened, setOpened] = useState<number | null>(null);
  if (me.isPending) return <Loading />;
  if (!perms.manage) return <Forbidden description="Collections are for managers and admins." />;
  const d = q.data;
  const match = wordMatcher(filter);
  // A collection found through a movie has no name or movies until the next list refresh: it waits off the list,
  // unless it is monitored (then its row and switch stay).
  // A row chosen from the add dialog's "Already added" result always shows.
  const waiting = (c: Collection) => !c.name && c.total === 0 && !c.monitored && c.tmdb_id !== opened;
  const listed = (d?.collections ?? []).filter(c => !waiting(c));
  const unnamed = (d?.collections.length ?? 0) - listed.length;
  const shown = listed.filter(c => (!onlyMonitored || c.monitored) && match(c.name || String(c.tmdb_id)));
  const monitored = d?.collections.filter(c => c.monitored).length ?? 0;
  // An already added collection chosen in the dialog: its row, whatever the filters hid.
  const show = (id: number) => {
    setFilter("");
    setOnlyMonitored(false);
    setOpened(id);
    window.setTimeout(() => document.querySelector(`[data-testid="collection-${id}"]`)?.scrollIntoView?.({ block: "center" }), 0);
  };
  return <div>
    <PageHeader title="Collections" description="Movie collections from TMDb. Add one by name, or they appear when a movie in the library belongs to one. A monitored collection adds its missing movies to the chosen libraries when it is added and at each list refresh; search on add then searches for them, through the download settings, so nothing downloads while downloads are off."
      actions={d ? <Button onClick={() => setAdding(true)} disabled={!d.can_add} title={d.can_add ? undefined : "TMDb is not set up, so collections cannot be searched"}><Plus aria-hidden />Add collection</Button> : null} />
    <CollectionsTabs active="collections" />
    {adding && d ? <AddCollectionDialog libraries={d.libraries} onClose={() => setAdding(false)} onExisting={show}
      onAdded={r => { setDone(`Added ${refreshWords(r)}`); show(r.collection.tmdb_id); }} /> : null}
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground">{plural(listed.length, "collection")}, {monitored} monitored.{unnamed ? ` ${plural(unnamed, "more collection")} ${unnamed === 1 ? "gets its" : "get their"} name with the next list refresh.` : ""} {perms.admin ? <Link className="text-primary hover:underline" to="/admin/settings/metadata#refresh">Refresh schedule</Link> : null}</p>
      {d.can_add ? null : <p className="text-sm text-muted-foreground">TMDb is not set up, so collections cannot be added or refreshed here.</p>}
      {done ? <p role="status" className="text-sm text-[var(--ink)]" data-testid="collections-done">{done}</p> : null}
      {set.error ? <ErrorState error={set.error} /> : null}
      {refresh.error ? <ErrorState error={refresh.error} /> : null}
      <div className="flex flex-wrap items-center gap-3">
        <Input aria-label="Filter collections" placeholder="Filter" className="max-w-xs" value={filter} onChange={e => setFilter(e.target.value)} />
        <label className="flex items-center gap-2 text-sm"><Checkbox checked={onlyMonitored} onChange={e => setOnlyMonitored(e.target.checked)} />Monitored only</label>
      </div>
      {shown.length === 0 ? <Card><Empty icon={Library}>{listed.length === 0 ? "No collections yet. Add one, or they appear when a movie in the library belongs to one." : "No collection matches."}</Empty></Card> :
        <Card className="p-0"><ul className="divide-y divide-[var(--line)]">{shown.map(c => <CollectionRow key={c.tmdb_id} c={c} libraries={d.libraries} busy={(set.isPending && set.variables?.id === c.tmdb_id) || (refresh.isPending && refresh.variables === c.tmdb_id)}
          refresh={d.can_add ? () => refresh.mutate(c.tmdb_id, { onSuccess: r => setDone(refreshWords(r)) }) : undefined}
          save={u => set.mutate({ id: c.tmdb_id, ...u })} />)}</ul></Card>}
    </div> : null}
  </div>;
}
