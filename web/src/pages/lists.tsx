// /collections/lists: the movie import lists (docs/design/arrview.md, Lists), as Radarr's Settings › Import Lists.
// TMDb movie lists only. An enabled list adds its new movies to its libraries at each list refresh and on Sync now;
// search on add starts the usual add-time search, which the download router gates. A list an arr client made is
// shown with its view and stays that client's. Managers and admins (GET/POST /lists, PUT/DELETE /lists/{id}).
import { useState } from "react";
import { ListVideo, Loader2, Plus, X } from "lucide-react";
import { useAuth } from "@/api/auth";
import { useDebounced } from "@/api/add";
import { permsOf } from "@/api/library";
import { listIDFrom, useListLookup, useListMutations, useLists, type ListAdd, type MovieList } from "@/api/lists";
import { AVAILABILITY } from "@/components/collections/add-collection";
import { CollectionsTabs } from "@/components/collections/collections-tabs";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { Chip } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { fmtAgo } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Forbidden } from "./library";

const MONITOR = [["movieOnly", "Movies"], ["movieAndCollection", "Movies and their collections"], ["none", "Nothing (added unmonitored)"]] as const;
type Mutations = ReturnType<typeof useListMutations>;

/** Add a list (no list given) or edit one. */
function ListDialog({ list, libraries, canSync, m, onClose }: { list?: MovieList; libraries: Record<string, string>; canSync: boolean; m: Mutations; onClose: () => void }) {
  const [source, setSource] = useState("");
  const [name, setName] = useState(list?.name ?? "");
  const keys = list ? list.libraries : Object.keys(libraries);
  const [versions, setVersions] = useState<string[]>(list?.versions ?? (keys.length === 1 ? keys : []));
  const [monitor, setMonitor] = useState<NonNullable<ListAdd["monitor"]>>(list?.monitor ?? "movieOnly");
  const [searchOnAdd, setSearchOnAdd] = useState(list?.search_on_add ?? true);
  const [minimum, setMinimum] = useState<NonNullable<ListAdd["minimum_availability"]>>(list?.minimum_availability ?? "released");
  const [enabled, setEnabled] = useState(list?.enabled ?? true);
  const tmdbID = listIDFrom(useDebounced(source, 300));
  const lookup = useListLookup(list || !canSync ? 0 : tmdbID);
  const found = lookup.data;
  const title = name.trim() || found?.name || "";
  const mutation = list ? m.set : m.add;
  const ready = title !== "" && versions.length > 0 && (list ? true : listIDFrom(source) > 0 && !found?.added);
  const submit = () => {
    const body = { name: title, versions, monitor, search_on_add: searchOnAdd, minimum_availability: minimum, enabled };
    if (list) m.set.mutate({ id: list.id, ...body }, { onSuccess: onClose });
    else m.add.mutate({ tmdb_id: listIDFrom(source), ...body }, { onSuccess: onClose });
  };
  const heading = list ? `Edit ${list.name}` : "Add list";
  return <Modal label={heading} onClose={onClose} testid="list-dialog"
    className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
    backdropClassName="absolute inset-0 bg-[var(--scrim)] animate-in fade-in-0"
    panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in fade-in-0 slide-in-from-bottom-6 sm:max-w-xl sm:rounded-[8px]">
    <div className="relative border-b border-[var(--line)] p-4 pr-12">
      <h2 className="font-heading text-[22px] font-medium leading-tight text-[var(--ink)] [overflow-wrap:anywhere]">{heading}</h2>
      <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" className="absolute right-3 top-3"><X className="size-4" /></Button>
    </div>
    <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 text-sm">
      {list ? list.view ? <p className="text-xs text-muted-foreground" data-testid="list-view">Created through the {list.view} view: it stays that client's list, and only that view's libraries can be chosen. Automatic adding is {list.enable_auto ? "on" : "off"} there.</p> : null
        : <label className="block space-y-1.5"><span className="font-medium text-[var(--ink)]">TMDb list</span>
          <Input aria-label="TMDb list id or URL" placeholder="List id, or its themoviedb.org address" value={source} onChange={e => setSource(e.target.value)} />
          {!canSync ? <span className="block text-xs text-muted-foreground">TMDb is not set up, so the list cannot be checked or synced yet.</span>
            : source.trim() && tmdbID === 0 && listIDFrom(source) === 0 ? <span className="block text-xs text-muted-foreground">Enter the number of a TMDb list, or paste its address (themoviedb.org/list/...).</span>
            : lookup.isFetching ? <span className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="size-3.5 animate-spin" aria-hidden />Looking the list up on TMDb</span>
            : lookup.error ? <ErrorState error={lookup.error} />
            : found ? <span className="block text-xs text-muted-foreground" data-testid="list-found">{found.added ? `${found.name} is already added.` : `${found.name}: ${found.movies} ${found.movies === 1 ? "movie" : "movies"}.`}{found.description ? ` ${found.description}` : ""}</span> : null}
        </label>}
      <label className="block space-y-1.5"><span className="font-medium text-[var(--ink)]">Name</span>
        <Input aria-label="List name" placeholder={found?.name ?? ""} value={name} onChange={e => setName(e.target.value)} />
      </label>
      <fieldset className="flex flex-wrap items-center gap-1.5">
        <legend className="mb-1.5 font-medium text-[var(--ink)]">Libraries</legend>
        {keys.map(key => {
          const on = versions.includes(key);
          return <button key={key} type="button" aria-pressed={on} title={key} onClick={() => setVersions(on ? versions.filter(k => k !== key) : [...versions, key])}
            className={cn("rounded-[3px] border px-2 py-0.5 text-xs", on ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]" : "border-[var(--line-2)] text-[var(--ink-3)]")}>{libraries[key] ?? key}</button>;
        })}
        {keys.length === 0 ? <span className="text-xs text-muted-foreground">There is no movie library to add to.</span>
          : versions.length === 0 ? <span className="text-xs text-muted-foreground">Choose at least one. Each movie goes to the chosen libraries that take it.</span> : null}
      </fieldset>
      <div className="flex flex-wrap items-center gap-4">
        <label className="flex items-center gap-2">Monitor
          <Select className="w-auto" value={monitor} onChange={e => setMonitor(e.target.value as typeof monitor)}>
            {MONITOR.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </Select>
        </label>
        <label className="flex items-center gap-2">Minimum availability
          <Select className="w-auto" value={minimum} onChange={e => setMinimum(e.target.value as typeof minimum)}>
            {AVAILABILITY.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            {minimum === "tba" ? <option value="tba">TBA</option> : null}
          </Select>
        </label>
        <label className="flex items-center gap-2"><Checkbox checked={searchOnAdd} onChange={e => setSearchOnAdd(e.target.checked)} />Search on add</label>
        <label className="flex items-center gap-2"><Checkbox checked={enabled} onChange={e => setEnabled(e.target.checked)} />Enabled: sync at each list refresh</label>
      </div>
      {mutation.error ? <ErrorState error={mutation.error} /> : null}
    </div>
    <div className="flex justify-end gap-2 border-t border-[var(--line)] p-4">
      <Button variant="outline" onClick={onClose}>Cancel</Button>
      <Button onClick={submit} disabled={!ready || mutation.isPending}>{mutation.isPending ? <Loader2 className="animate-spin" aria-hidden /> : null}{list ? "Save" : "Add"}</Button>
    </div>
  </Modal>;
}

function ListRow({ l, libraries, canSync, m, edit, remove, synced }: { l: MovieList; libraries: Record<string, string>; canSync: boolean; m: Mutations; edit: () => void; remove: () => void; synced: (words: string) => void }) {
  const syncing = m.sync.isPending && m.sync.variables === l.id;
  const busy = syncing || (m.set.isPending && m.set.variables?.id === l.id);
  const sync = () => m.sync.mutate(l.id, { onSuccess: r => synced(`${r.list.name}: ${r.added} ${r.added === 1 ? "movie" : "movies"} added, ${r.skipped} skipped (already in the library, excluded or not a movie).${r.sync_error ? ` ${r.sync_error}` : ""}`) });
  return <li className="flex min-w-0 flex-wrap items-start gap-3 px-3 py-3" data-testid={`list-${l.id}`}>
    <Switch className="mt-0.5" checked={l.enabled} disabled={busy} aria-label={`Enable ${l.name}`} onCheckedChange={enabled => m.set.mutate({ id: l.id, enabled })} />
    <div className="min-w-0 flex-1 space-y-1.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 className="min-w-0 font-semibold text-[var(--ink)] [overflow-wrap:anywhere]">{l.name}</h2>
        <a className="text-xs text-primary hover:underline" href={`https://www.themoviedb.org/list/${l.tmdb_id}`} target="_blank" rel="noreferrer">TMDb list {l.tmdb_id}</a>
        {l.view ? <Chip tone="ghost">Created through the {l.view} view</Chip> : null}
      </div>
      <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
        <span>Adds to</span>
        {l.versions.map(k => <Chip key={k} tone="ghost">{libraries[k] ?? k}</Chip>)}
        <span>· Monitor: {MONITOR.find(([v]) => v === l.monitor)?.[1] ?? l.monitor}</span>
        <span>· Search on add: {l.search_on_add ? "on" : "off"}</span>
        <span>· Last sync: {l.last_sync_at ? fmtAgo(l.last_sync_at) : "never"}</span>
      </div>
    </div>
    <div className="flex flex-wrap items-center gap-2">
      <Button variant="outline" size="sm" disabled={!canSync || busy} title={canSync ? undefined : "TMDb is not set up"} onClick={sync}>{syncing ? <Loader2 className="animate-spin" aria-hidden /> : null}Sync now</Button>
      <Button variant="outline" size="sm" onClick={edit}>Edit</Button>
      <Button variant="outline" size="sm" onClick={remove}>Delete</Button>
    </div>
  </li>;
}

export function ListsPage() {
  const me = useAuth();
  const perms = permsOf(me.data?.permissions);
  const q = useLists(perms.manage);
  const m = useListMutations();
  const [dialog, setDialog] = useState<MovieList | "add" | null>(null);
  const [removing, setRemoving] = useState<MovieList | null>(null);
  const [done, setDone] = useState("");
  if (me.isPending) return <Loading />;
  if (!perms.manage) return <Forbidden description="Lists are for managers and admins." />;
  const d = q.data;
  const open = (v: MovieList | "add") => { m.add.reset(); m.set.reset(); setDialog(v); };
  return <div>
    <PageHeader title="Collections" description="Lists add movies by themselves: every movie on an enabled TMDb list is added to the list's libraries at each list refresh, or with Sync now. Search on add then searches for them, through the download settings, so nothing downloads while downloads are off."
      actions={d ? <Button onClick={() => open("add")}><Plus aria-hidden />Add list</Button> : null} />
    <CollectionsTabs active="lists" />
    {dialog && d ? <ListDialog key={dialog === "add" ? "add" : dialog.id} list={dialog === "add" ? undefined : dialog} libraries={d.libraries} canSync={d.can_sync} m={m} onClose={() => setDialog(null)} /> : null}
    {removing ? <ConfirmDialog testid="list-delete" subject={removing.name} busy={m.remove.isPending} error={m.remove.error} onClose={() => setRemoving(null)}
      onConfirm={() => m.remove.mutate(removing.id, { onSuccess: () => setRemoving(null) })}
      scope={{ title: "Delete this list?", does: ["Stop adding movies from this TMDb list"], confirm: "Delete list",
        keeps: ["The movies it already added, and their files", ...(removing.view ? [`Nothing in the ${removing.view} client itself: delete it there too if it should not come back`] : [])] }} /> : null}
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d ? <div className="space-y-4">
      <p className="text-sm text-muted-foreground">TMDb movie lists only: other list providers and series lists are not supported yet.{d.can_sync ? "" : " TMDb is not set up, so lists do not sync."}</p>
      {done ? <p role="status" className="text-sm text-[var(--ink)]" data-testid="lists-done">{done}</p> : null}
      {m.sync.error ? <ErrorState error={m.sync.error} /> : null}
      {m.set.error && !dialog ? <ErrorState error={m.set.error} /> : null}
      {d.lists.length === 0 ? <Card><Empty icon={ListVideo}>No lists yet. Add a TMDb list by its id or address.</Empty></Card> :
        <Card className="p-0"><ul className="divide-y divide-[var(--line)]">{d.lists.map(l => <ListRow key={l.id} l={l} libraries={d.libraries} canSync={d.can_sync} m={m}
          edit={() => open(l)} remove={() => { m.remove.reset(); setRemoving(l); }} synced={setDone} />)}</ul></Card>}
    </div> : null}
  </div>;
}
