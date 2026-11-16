// /collections: TMDb movie collections, as Radarr's Movies › Collections (docs/design/arrview.md, Collections). A
// compact list (name, counts, monitored); a row opens the collection's page (/collections/{id}: its films, and its
// settings behind Edit). Add collection searches TMDb and adds one; a monitored collection adds its missing movies to its libraries when it is
// added, on "Add missing movies now" and at each list refresh. Search on add starts the usual add-time search, which
// the download router gates. Managers and admins (GET/POST/PUT /collections).
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Library, Plus } from "lucide-react";
import { refreshWords, useCollections, useRefreshCollection, type Collection } from "@/api/collections";
import { useAuth } from "@/api/auth";
import { permsOf } from "@/api/library";
import { AddCollectionDialog } from "@/components/collections/add-collection";
import { collectionCounts, collectionName } from "@/components/collections/collection-settings";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { Chip } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Forbidden } from "./library";
import { wordMatcher } from "@/lib/words";
import { plural } from "@/lib/format";

/** The add-missing button's words: a collection whose movies are all in the library has none missing, but the button
 *  still refreshes it and can add a held film to another target library, so it stays with words that say so. */
export const addMissingLabel = (c: Pick<Collection, "held" | "total">) =>
  c.total > 0 && c.held >= c.total ? "Refresh and add missing movies" : "Add missing movies now";

/** One collection on the list: its name opens its page (the films; settings behind Edit there). */
function CollectionRow({ c, busy, refresh }: { c: Collection; busy: boolean;
  /** Add missing movies now; absent when TMDb is not set up. */
  refresh?: () => void }) {
  const name = collectionName(c);
  return <li className="flex min-w-0 items-center gap-3 px-3 py-2" data-testid={`collection-${c.tmdb_id}`}>
    <Link to={`/collections/${c.tmdb_id}`} className="group flex min-w-0 flex-1 items-center gap-3 rounded-[4px] outline-none focus-visible:ring-2 focus-visible:ring-[var(--line-focus)]">
      {c.poster ? <img src={c.poster} alt="" loading="lazy" className="h-12 w-8 shrink-0 rounded-[3px] object-cover ring-1 ring-[var(--line)]" /> : <div className="grid h-12 w-8 shrink-0 place-items-center rounded-[3px] bg-[var(--sunken)] text-[var(--ink-3)]"><Library className="size-4" aria-hidden /></div>}
      <span className="min-w-0 flex-1">
        <span className="block font-semibold text-[var(--ink)] [overflow-wrap:anywhere] group-hover:underline group-hover:underline-offset-2">{name}</span>
        {/* In the library: the movie is in a library; available: it has a file there. */}
        <span className="block text-[12.5px] text-[var(--ink-3)]">{c.name ? collectionCounts(c) : "Name and movies arrive with the next list refresh."}</span>
      </span>
    </Link>
    <div className="flex shrink-0 flex-wrap items-center justify-end gap-2">
      {c.monitored && refresh ? <Button variant="outline" size="xs" disabled={busy} onClick={refresh}>{addMissingLabel(c)}</Button> : null}
      <Chip tone={c.monitored ? "ok" : "ghost"}>{c.monitored ? "Monitored" : "Not monitored"}</Chip>
    </div>
  </li>;
}

export function CollectionsPage() {
  const me = useAuth();
  const perms = permsOf(me.data?.permissions);
  const q = useCollections(perms.manage);
  const refresh = useRefreshCollection();
  const [adding, setAdding] = useState(false);
  const [done, setDone] = useState("");
  // The filter text and Monitored only live in the URL (?q=, ?monitored=1), so Back and a shared link keep them.
  const [params, setParams] = useSearchParams();
  const filter = params.get("q") ?? "";
  const onlyMonitored = params.get("monitored") === "1";
  const setParam = (key: string, value: string) => setParams(p => {
    const next = new URLSearchParams(p);
    if (value) next.set(key, value); else next.delete(key);
    return next;
  }, { replace: true });
  const setFilter = (v: string) => setParam("q", v);
  const setOnlyMonitored = (on: boolean) => setParam("monitored", on ? "1" : "");
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
    setParams(p => { const next = new URLSearchParams(p); next.delete("q"); next.delete("monitored"); return next; }, { replace: true });
    setOpened(id);
    window.setTimeout(() => document.querySelector(`[data-testid="collection-${id}"]`)?.scrollIntoView?.({ block: "center" }), 0);
  };
  return <div>
    <PageHeader title="Collections" description="Movie collections from TMDb. Add one by name, or they appear when a movie in the library belongs to one. A monitored collection adds its missing movies to the chosen libraries when it is added and at each list refresh; search on add then searches for them, through the download settings, so nothing downloads while downloads are off."
      actions={d ? <Button onClick={() => setAdding(true)} disabled={!d.can_add} title={d.can_add ? undefined : "TMDb is not set up, so collections cannot be searched"}><Plus aria-hidden />Add collection</Button> : null} />
    <p className="mb-4 text-sm text-muted-foreground" data-testid="import-lists-link">Import lists (TMDb lists, Radarr and Sonarr) are in <Link className="text-[var(--accent-ink)] underline-offset-2 hover:underline" to="/admin/settings/import-lists">Settings › Import lists</Link>.</p>
    {adding && d ? <AddCollectionDialog libraries={d.libraries} onClose={() => setAdding(false)} onExisting={show}
      onAdded={r => { setDone(`Added ${refreshWords(r)}`); show(r.collection.tmdb_id); }} /> : null}
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground">{plural(listed.length, "collection")}, {monitored} monitored.{unnamed ? ` ${plural(unnamed, "more collection")} ${unnamed === 1 ? "gets its" : "get their"} name with the next list refresh.` : ""} {perms.admin ? <Link className="text-primary hover:underline" to="/admin/settings/metadata#refresh">Refresh schedule</Link> : null}</p>
      {d.can_add ? null : <p className="text-sm text-muted-foreground">TMDb is not set up, so collections cannot be added or refreshed here.</p>}
      {done ? <p role="status" className="text-sm text-[var(--ink)]" data-testid="collections-done">{done}</p> : null}
      {refresh.error ? <ErrorState error={refresh.error} /> : null}
      <div className="flex flex-wrap items-center gap-3">
        <Input aria-label="Filter collections" placeholder="Filter" className="w-full sm:max-w-xs" value={filter} onChange={e => setFilter(e.target.value)} />
        <label className="flex items-center gap-2 text-sm"><Checkbox checked={onlyMonitored} onChange={e => setOnlyMonitored(e.target.checked)} />Monitored only</label>
      </div>
      {shown.length === 0 ? <Card><Empty icon={Library}>{listed.length === 0 ? "No collections yet. Add one, or they appear when a movie in the library belongs to one." : "No collection matches."}</Empty></Card> :
        <Card className="p-0"><ul className="divide-y divide-[var(--line)]">{shown.map(c => <CollectionRow key={c.tmdb_id} c={c} busy={refresh.isPending && refresh.variables === c.tmdb_id}
          refresh={d.can_add ? () => refresh.mutate(c.tmdb_id, { onSuccess: r => setDone(refreshWords(r)) }) : undefined} />)}</ul></Card>}
    </div> : null}
  </div>;
}
